package database

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/shridarpatil/whatomate/internal/models"
)

// ScanReportLine is one kind of stored file in the migration report.
type ScanReportLine struct {
	Source       string           `json:"source"`
	Total        int64            `json:"total"`        // files that exist now
	Registered   int64            `json:"registered"`   // have a verdict row
	Unregistered int64            `json:"unregistered"` // no row at all (closed; the backfill registers them)
	ByStatus     map[string]int64 `json:"by_status"`    // of the registered ones
	Bytes        int64            `json:"bytes"`        // size of the files that exist now
	NoVerdict    int64            `json:"no_verdict"`   // total minus clean: closed until scanned
	NoVerdictB   int64            `json:"no_verdict_bytes"`
}

// ScanReport is the read-only diagnostic for the initial scan of existing files.
type ScanReport struct {
	Lines       []ScanReportLine `json:"lines"`
	TotalFiles  int64            `json:"total_files"`
	TotalBytes  int64            `json:"total_bytes"`
	NoVerdict   int64            `json:"no_verdict"`
	NoVerdictB  int64            `json:"no_verdict_bytes"`
	AvgScanMs   float64          `json:"avg_scan_ms"`  // from past scans (0: none yet)
	AvgBytesPer float64          `json:"avg_bytes_ms"` // bytes per ms of past scans (0: none yet)
	// CampaignsUnverified: campaigns whose Meta media id was not uploaded by this server (older ones).
	CampaignsUnverified int64 `json:"campaigns_unverified_media"`
}

// reportSources: how to list the files of each kind, and where their size is known in the table.
var reportSources = []struct {
	source, table, pathCol, where, sizeCol string
}{
	{models.ScanSourceOccurrenceFile, "occurrence_files", "storage_path", "t.deleted_at IS NULL", "size_bytes"},
	{models.ScanSourceChatAttachment, "chat_attachments", "storage_path", "t.purged_at IS NULL", "size_bytes"},
	{models.ScanSourceOccurrenceDocument, "occurrence_documents", "attachment_path", "t.deleted_at IS NULL AND COALESCE(t.attachment_path,'') <> ''", ""},
	{models.ScanSourceMessageMedia, "messages", "media_url", "t.deleted_at IS NULL AND COALESCE(t.media_url,'') <> ''", ""},
	{models.ScanSourceCampaignMedia, "bulk_message_campaigns", "header_media_local_path", "t.deleted_at IS NULL AND COALESCE(t.header_media_local_path,'') <> ''", ""},
}

// BuildScanReport reads (only SELECTs and os.Stat) how many files exist, how many have a verdict and
// how big they are. mediaRoot is the storage root, used to stat the kinds whose size is not in the
// database; with stat=false that is skipped (those sizes stay 0).
func BuildScanReport(db *gorm.DB, mediaRoot string, stat bool) (ScanReport, error) {
	var rep ScanReport
	for _, s := range reportSources {
		line := ScanReportLine{Source: s.source, ByStatus: map[string]int64{}}
		if err := db.Table(s.table + " t").Where(s.where).Count(&line.Total).Error; err != nil {
			return rep, err
		}
		type sc struct {
			Status string
			N      int64
		}
		var rows []sc
		if err := db.Raw(`SELECT f.status AS status, count(*) AS n FROM file_scans f
			JOIN `+s.table+` t ON t.id = f.resource_id WHERE f.source = ? AND `+s.where+` GROUP BY f.status`, s.source).Scan(&rows).Error; err != nil {
			return rep, err
		}
		for _, r := range rows {
			line.ByStatus[r.Status] = r.N
			line.Registered += r.N
		}
		line.Unregistered = line.Total - line.Registered
		line.NoVerdict = line.Total - line.ByStatus[models.ScanClean]

		unclean := ` AND NOT EXISTS (SELECT 1 FROM file_scans f WHERE f.source = '` + s.source + `' AND f.resource_id = t.id AND f.status = 'clean')`
		sizeOf := func(onlyUnclean bool) int64 {
			if s.sizeCol != "" {
				q := `SELECT COALESCE(SUM(t.` + s.sizeCol + `),0) FROM ` + s.table + ` t WHERE ` + s.where
				if onlyUnclean {
					q += unclean
				}
				var n int64
				db.Raw(q).Scan(&n)
				return n
			}
			if !stat {
				return 0
			}
			q := `SELECT t.` + s.pathCol + ` FROM ` + s.table + ` t WHERE ` + s.where
			if onlyUnclean {
				q += unclean
			}
			var paths []string
			db.Raw(q).Scan(&paths)
			var n int64
			for _, p := range paths {
				clean := filepath.Clean(p)
				if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
					continue
				}
				if st, err := os.Stat(filepath.Join(mediaRoot, clean)); err == nil && st.Mode().IsRegular() {
					n += st.Size()
				}
			}
			return n
		}
		line.Bytes, line.NoVerdictB = sizeOf(false), sizeOf(true)
		rep.Lines = append(rep.Lines, line)
		rep.TotalFiles += line.Total
		rep.TotalBytes += line.Bytes
		rep.NoVerdict += line.NoVerdict
		rep.NoVerdictB += line.NoVerdictB
	}

	var perf struct{ AvgMs, BytesPerMs float64 }
	db.Raw(`SELECT COALESCE(avg(scan_ms),0) AS avg_ms,
		COALESCE(sum(size_bytes)::float8 / NULLIF(sum(scan_ms),0), 0) AS bytes_per_ms
		FROM file_scans WHERE status IN ('clean','infected') AND scan_ms > 0 AND scanned_at > ?`, time.Now().Add(-30*24*time.Hour)).Scan(&perf)
	rep.AvgScanMs, rep.AvgBytesPer = perf.AvgMs, perf.BytesPerMs
	db.Raw(`SELECT count(*) FROM bulk_message_campaigns c WHERE c.deleted_at IS NULL AND COALESCE(c.header_media_id,'') <> '' AND NOT EXISTS (SELECT 1 FROM meta_media_uploads m WHERE m.organization_id = c.organization_id AND m.media_id = c.header_media_id)`).Scan(&rep.CampaignsUnverified)
	return rep, nil
}

// ScanEstimate turns a report into times. The worker's pacing (batch per interval) bounds the number
// of files per hour; the engine's own speed bounds the bytes per second. The slower one decides.
type ScanEstimate struct {
	WorkerFilesPerHour float64       `json:"worker_files_per_hour"`
	EngineBytesPerSec  float64       `json:"engine_bytes_per_sec"`
	EngineAssumed      bool          `json:"engine_assumed"`
	ByWorker           time.Duration `json:"by_worker"`
	ByEngine           time.Duration `json:"by_engine"`
	Total              time.Duration `json:"total"`
}

// AssumedEngineBytesPerSec is used when no scan has been timed yet: deliberately cautious, because a
// first estimate that is too short is worse than one that is too long.
const AssumedEngineBytesPerSec = 10 << 20

// EstimateScan estimates the initial scan of what has no clean verdict yet.
func EstimateScan(rep ScanReport, batch int, interval time.Duration) ScanEstimate {
	e := ScanEstimate{WorkerFilesPerHour: float64(batch) * float64(time.Hour) / float64(interval)}
	e.EngineBytesPerSec = rep.AvgBytesPer * 1000
	if e.EngineBytesPerSec <= 0 {
		e.EngineBytesPerSec, e.EngineAssumed = AssumedEngineBytesPerSec, true
	}
	if e.WorkerFilesPerHour > 0 {
		e.ByWorker = time.Duration(float64(rep.NoVerdict) / e.WorkerFilesPerHour * float64(time.Hour))
	}
	e.ByEngine = time.Duration(float64(rep.NoVerdictB) / e.EngineBytesPerSec * float64(time.Second))
	e.Total = e.ByWorker
	if e.ByEngine > e.Total {
		e.Total = e.ByEngine
	}
	return e
}
