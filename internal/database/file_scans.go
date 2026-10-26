package database

import (
	"github.com/zerodha/logf"
	"gorm.io/gorm"

	"github.com/shridarpatil/whatomate/internal/models"
)

// unscannedSources: for each kind of stored file, the SELECT that lists the files that have no
// file_scans row yet. A file with no row is closed to everyone (the delivery paths only open on a
// clean row), so registering one as pending is what lets the scan worker reach it.
var unscannedSources = []struct{ source, selectSQL string }{
	{models.ScanSourceOccurrenceFile, `SELECT t.id, t.organization_id, t.storage_path, COALESCE(t.sha256,''), t.size_bytes
		FROM occurrence_files t WHERE t.deleted_at IS NULL`},
	{models.ScanSourceChatAttachment, `SELECT t.id, t.organization_id, t.storage_path, COALESCE(t.sha256,''), t.size_bytes
		FROM chat_attachments t WHERE t.purged_at IS NULL`},
	{models.ScanSourceOccurrenceDocument, `SELECT t.id, t.organization_id, t.attachment_path, '', 0
		FROM occurrence_documents t WHERE t.deleted_at IS NULL AND COALESCE(t.attachment_path,'') <> ''`},
	{models.ScanSourceMessageMedia, `SELECT t.id, t.organization_id, t.media_url, '', 0
		FROM messages t WHERE t.deleted_at IS NULL AND COALESCE(t.media_url,'') <> ''`},
}

// RegisterUnscannedFiles gives every stored file that lacks one a pending scan row, up to limit per
// source. It is how files that existed before the antimalware policy (and any path that forgot to
// register) enter the queue; the worker then scans them gradually. Idempotent.
func RegisterUnscannedFiles(db *gorm.DB, limit int) (int64, error) {
	var total int64
	for _, s := range unscannedSources {
		res := db.Exec(`INSERT INTO file_scans
			(id, organization_id, source, resource_id, storage_path, sha256, size_bytes, status, attempts, next_attempt_at, created_at, updated_at)
			SELECT gen_random_uuid(), x.organization_id, ?, x.id, x.path, x.sha, x.size, 'pending', 0, NOW(), NOW(), NOW()
			FROM (`+s.selectSQL+`
			  AND NOT EXISTS (SELECT 1 FROM file_scans f WHERE f.source = ? AND f.resource_id = t.id)
			  LIMIT ?) AS x(id, organization_id, path, sha, size)
			ON CONFLICT (source, resource_id) DO NOTHING`, s.source, s.source, limit)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
	}
	return total, nil
}

// BackfillFileScans runs at migration time: it registers every existing file as pending, in batches,
// so no legacy file is left outside the policy. Nothing is deleted and nothing is released: the
// scan worker opens a file only after a clean verdict.
func BackfillFileScans(db *gorm.DB, lo logf.Logger) error {
	var total int64
	for {
		n, err := RegisterUnscannedFiles(db, 5000)
		if err != nil {
			return err
		}
		total += n
		if n == 0 {
			break
		}
	}
	if total > 0 {
		lo.Info("Registered existing files for antimalware scanning", "files", total)
	}
	return nil
}
