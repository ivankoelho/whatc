package handlers

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Retention of files: which stored files are old enough to leave, and which are protected.
//
// Everything in this file only READS. The same classification feeds the simulation and, later, the purge
// engine, so what an administrator sees is what the engine would act on.
//
// A file is in exactly one bucket (first match wins):
//
//	held        a legal hold covers its protocol or contact
//	quarantine  the antimalware scan found it infected: kept as security evidence
//	shared      the same stored file is referenced by another record (never removed while shared)
//	open        its protocol is still open: the term has not started
//	not_due     inside the retention term
//	eligible    past the term and not protected

const (
	bucketHeld       = "held"
	bucketQuarantine = "quarantine"
	bucketShared     = "shared"
	bucketOpen       = "open"
	bucketNotDue     = "not_due"
	bucketEligible   = "eligible"
)

// retentionCategory ties a category to its stored files. cand selects, for ONE organization (@org), one
// row per file with: id, org, path, size, ref_at (when the term starts), occurrence_id, contact_id, name,
// related (the protocol number, when there is one).
type retentionCategory struct {
	category string
	source   string // file_scans source (quarantine lookup)
	sized    bool   // the database knows the size; otherwise it is measured on disk
	cand     string
}

var retentionCategories = map[string]retentionCategory{
	models.RetentionOccurrenceFiles: {
		models.RetentionOccurrenceFiles, models.ScanSourceOccurrenceFile, true,
		`SELECT t.id, t.organization_id AS org, t.storage_path AS path, t.size_bytes AS size,
			CASE WHEN o.id IS NULL THEN t.created_at ELSE o.closed_at END AS ref_at,
			t.occurrence_id, o.contact_id, t.name, COALESCE(o.protocol_number,'') AS related
		 FROM occurrence_files t LEFT JOIN occurrences o ON o.id = t.occurrence_id AND o.deleted_at IS NULL
		 WHERE t.deleted_at IS NULL AND t.organization_id = @org`},
	models.RetentionOccurrenceInvoices: {
		models.RetentionOccurrenceInvoices, models.ScanSourceOccurrenceDocument, false,
		`SELECT t.id, t.organization_id AS org, t.attachment_path AS path, 0::bigint AS size,
			CASE WHEN o.id IS NULL THEN t.created_at ELSE o.closed_at END AS ref_at,
			t.occurrence_id, o.contact_id, COALESCE(t.attachment_name,'') AS name, COALESCE(o.protocol_number,'') AS related
		 FROM occurrence_documents t LEFT JOIN occurrences o ON o.id = t.occurrence_id AND o.deleted_at IS NULL
		 WHERE t.deleted_at IS NULL AND COALESCE(t.attachment_path,'') <> '' AND t.organization_id = @org`},
	models.RetentionChatAttachments: {
		models.RetentionChatAttachments, models.ScanSourceChatAttachment, true,
		`SELECT t.id, t.organization_id AS org, t.storage_path AS path, t.size_bytes AS size,
			CASE WHEN ch.occurrence_id IS NOT NULL THEN o.closed_at ELSE t.created_at END AS ref_at,
			ch.occurrence_id, o.contact_id, t.name, COALESCE(o.protocol_number,'') AS related
		 FROM chat_attachments t
		 JOIN chat_channels ch ON ch.id = t.channel_id
		 LEFT JOIN occurrences o ON o.id = ch.occurrence_id AND o.deleted_at IS NULL
		 WHERE t.purged_at IS NULL AND t.message_id IS NOT NULL AND t.organization_id = @org`},
	models.RetentionWhatsAppMedia: {
		models.RetentionWhatsAppMedia, models.ScanSourceMessageMedia, false,
		`SELECT t.id, t.organization_id AS org, t.media_url AS path, 0::bigint AS size, t.created_at AS ref_at,
			NULL::uuid AS occurrence_id, t.contact_id, COALESCE(t.media_filename,'') AS name, '' AS related
		 FROM messages t
		 WHERE t.deleted_at IS NULL AND COALESCE(t.media_url,'') <> '' AND t.organization_id = @org`},
	models.RetentionCampaignMedia: {
		models.RetentionCampaignMedia, models.ScanSourceCampaignMedia, false,
		`SELECT t.id, t.organization_id AS org, t.header_media_local_path AS path, 0::bigint AS size, t.created_at AS ref_at,
			NULL::uuid AS occurrence_id, NULL::uuid AS contact_id, COALESCE(t.header_media_filename,'') AS name, t.name AS related
		 FROM bulk_message_campaigns t
		 WHERE t.deleted_at IS NULL AND COALESCE(t.header_media_local_path,'') <> '' AND t.organization_id = @org`},
}

// classifiedSQL wraps a category's candidates with the protections and the bucket.
func classifiedSQL(c retentionCategory) string {
	return `WITH refs AS (
		SELECT p FROM (
			SELECT storage_path AS p FROM occurrence_files WHERE deleted_at IS NULL
			UNION ALL SELECT attachment_path FROM occurrence_documents WHERE deleted_at IS NULL AND COALESCE(attachment_path,'') <> ''
			UNION ALL SELECT storage_path FROM chat_attachments WHERE purged_at IS NULL
			UNION ALL SELECT media_url FROM messages WHERE deleted_at IS NULL AND COALESCE(media_url,'') <> ''
			UNION ALL SELECT header_media_local_path FROM bulk_message_campaigns WHERE deleted_at IS NULL AND COALESCE(header_media_local_path,'') <> ''
		) u WHERE p <> '' GROUP BY p HAVING count(*) > 1
	), cand AS (` + c.cand + `
	), classified AS (
		SELECT c.*,
			EXISTS (SELECT 1 FROM retention_holds h WHERE h.organization_id = c.org AND h.revoked_at IS NULL AND (
				(h.scope_type = 'occurrence' AND h.scope_id = c.occurrence_id) OR (h.scope_type = 'contact' AND h.scope_id = c.contact_id))) AS held,
			EXISTS (SELECT 1 FROM file_scans f WHERE f.source = @src AND f.resource_id = c.id AND f.organization_id = c.org AND f.status = 'infected') AS quarantined,
			(c.path IN (SELECT p FROM refs)) AS shared
		FROM cand c
	)
	SELECT *, CASE
		WHEN held THEN 'held' WHEN quarantined THEN 'quarantine' WHEN shared THEN 'shared'
		WHEN ref_at IS NULL THEN 'open' WHEN ref_at >= @cutoff THEN 'not_due' ELSE 'eligible' END AS bucket
	FROM classified`
}

// RetentionBucketTotals is a count and a volume.
type RetentionBucketTotals struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// RetentionSample is one file in a report: enough to recognise it, nothing about its content.
type RetentionSample struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	Bytes       int64      `json:"bytes"`
	ReferenceAt *time.Time `json:"reference_at,omitempty"`
	Bucket      string     `json:"bucket"`
	Reason      string     `json:"reason"`
	Related     string     `json:"related,omitempty"` // protocol number (or campaign name)
}

// RetentionCategoryReport is the outcome of evaluating one category.
type RetentionCategoryReport struct {
	Category      string                           `json:"category"`
	Applied       bool                             `json:"applied"` // false: no term to apply (see Reason)
	Reason        string                           `json:"reason,omitempty"`
	Months        int                              `json:"months"`
	Assumed       bool                             `json:"assumed"` // the term was supplied for the simulation, not saved
	Cutoff        *time.Time                       `json:"cutoff,omitempty"`
	Buckets       map[string]RetentionBucketTotals `json:"buckets"`
	BytesPartial  bool                             `json:"bytes_partial"` // the volume of some files could not be measured
	Samples       []RetentionSample                `json:"samples"`       // eligible files, oldest first
	ProtectedSamp []RetentionSample                `json:"protected_samples"`
}

// retentionTerm decides which term applies to a category: the saved policy when it is enabled, or the
// months supplied for a what-if. No policy and no override means no term: nothing is ever eligible.
func (a *App) retentionTerm(orgID uuid.UUID, category string, override int) (months int, assumed bool, reason string) {
	if override > 0 {
		return override, true, ""
	}
	var p models.RetentionPolicy
	if err := a.DB.Where("organization_id = ? AND category = ?", orgID, category).First(&p).Error; err != nil {
		return 0, false, "no_policy"
	}
	if !p.Enabled {
		return 0, false, "policy_disabled"
	}
	if p.KeepMonths <= 0 {
		return 0, false, "no_term"
	}
	return p.KeepMonths, false, ""
}

// evaluateRetention classifies one category for one organization. Read-only.
func (a *App) evaluateRetention(orgID uuid.UUID, category string, override int, now time.Time, sampleLimit int) (RetentionCategoryReport, error) {
	rep := RetentionCategoryReport{Category: category, Buckets: map[string]RetentionBucketTotals{}, Samples: []RetentionSample{}, ProtectedSamp: []RetentionSample{}}
	def, ok := retentionCategories[category]
	if !ok {
		return rep, fmt.Errorf("unknown retention category %q", category)
	}
	months, assumed, why := a.retentionTerm(orgID, category, override)
	if why != "" {
		rep.Reason = why
		return rep, nil
	}
	cutoff := now.AddDate(0, -months, 0)
	rep.Applied, rep.Months, rep.Assumed, rep.Cutoff = true, months, assumed, &cutoff
	args := []any{sql.Named("org", orgID), sql.Named("src", def.source), sql.Named("cutoff", cutoff)}
	base := classifiedSQL(def)

	type agg struct {
		Bucket string
		N      int64
		Bytes  int64
	}
	var rows []agg
	if err := a.DB.Raw("SELECT bucket, count(*) AS n, COALESCE(sum(size),0) AS bytes FROM ("+base+") x GROUP BY bucket", args...).Scan(&rows).Error; err != nil {
		return rep, err
	}
	for _, r := range rows {
		rep.Buckets[r.Bucket] = RetentionBucketTotals{Count: r.N, Bytes: r.Bytes}
	}
	for _, b := range []string{bucketEligible, bucketNotDue, bucketOpen, bucketHeld, bucketShared, bucketQuarantine} {
		if _, ok := rep.Buckets[b]; !ok {
			rep.Buckets[b] = RetentionBucketTotals{}
		}
	}

	type row struct {
		ID      uuid.UUID
		Path    string
		Size    int64
		RefAt   *time.Time
		Name    string
		Related string
		Bucket  string
	}
	reasonOf := func(r row) string {
		switch r.Bucket {
		case bucketEligible:
			if r.Related != "" && (category == models.RetentionOccurrenceFiles || category == models.RetentionOccurrenceInvoices) {
				return fmt.Sprintf("protocol closed more than %d months ago", months)
			}
			return fmt.Sprintf("older than %d months", months)
		case bucketHeld:
			return "legal hold"
		case bucketShared:
			return "same stored file referenced by another record"
		case bucketQuarantine:
			return "infected: kept as security evidence"
		}
		return r.Bucket
	}
	toSample := func(r row) RetentionSample {
		return RetentionSample{ID: r.ID, Name: r.Name, Bytes: r.Size, ReferenceAt: r.RefAt, Bucket: r.Bucket, Reason: reasonOf(r), Related: r.Related}
	}

	var elig []row
	if err := a.DB.Raw("SELECT id, path, size, ref_at, name, related, bucket FROM ("+base+") x WHERE bucket = 'eligible' ORDER BY ref_at, id LIMIT "+strconv.Itoa(sampleLimit), args...).Scan(&elig).Error; err != nil {
		return rep, err
	}
	for _, r := range elig {
		rep.Samples = append(rep.Samples, toSample(r))
	}
	var prot []row
	if err := a.DB.Raw("SELECT id, path, size, ref_at, name, related, bucket FROM ("+base+") x WHERE bucket IN ('held','shared','quarantine') ORDER BY bucket, ref_at, id LIMIT "+strconv.Itoa(sampleLimit), args...).Scan(&prot).Error; err != nil {
		return rep, err
	}
	for _, r := range prot {
		rep.ProtectedSamp = append(rep.ProtectedSamp, toSample(r))
	}

	// Sizes the database does not hold are measured on disk (read-only), for the eligible files, up to a cap.
	if !def.sized {
		const capStat = 20000
		var paths []string
		if err := a.DB.Raw("SELECT path FROM ("+base+") x WHERE bucket = 'eligible' LIMIT "+strconv.Itoa(capStat), args...).Scan(&paths).Error; err != nil {
			return rep, err
		}
		var total int64
		for _, p := range paths {
			if st, err := os.Stat(filepath.Join(a.getMediaStoragePath(), filepath.Clean(p))); err == nil && st.Mode().IsRegular() && !strings.HasPrefix(filepath.Clean(p), "..") {
				total += st.Size()
			}
		}
		t := rep.Buckets[bucketEligible]
		t.Bytes = total
		rep.Buckets[bucketEligible] = t
		rep.BytesPartial = int64(len(paths)) < t.Count
	}
	return rep, nil
}
