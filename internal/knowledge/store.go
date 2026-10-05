package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Hash identifies the content of a document (title + normalized body).
func Hash(title, body string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + body))
	return hex.EncodeToString(sum[:])
}

var lockUpdate = clause.Locking{Strength: "UPDATE"}

// Lock reads a live document of the organization WITH a row lock (SELECT ... FOR
// UPDATE). Call it inside a transaction, before deciding anything from the row:
// every operation on one document (edit, archive, delete, reindex, import) then
// runs one after the other instead of racing on its chunks.
func Lock(tx *gorm.DB, orgID, id uuid.UUID) (*models.KnowledgeDocument, error) {
	var doc models.KnowledgeDocument
	err := tx.Clauses(lockUpdate).
		Where("id = ? AND organization_id = ?", id, orgID).First(&doc).Error
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// Save creates or updates a document and rebuilds its chunks in ONE transaction
// (a savepoint when db is already a transaction), so a document never exists
// half-indexed. Scope/status are copied onto every chunk. doc.ID zero means create.
func Save(db *gorm.DB, doc *models.KnowledgeDocument, sections []Section) error {
	doc.Body = NormalizeText(doc.Body)
	doc.Visibility = VisibilityFor(doc.UnitID, doc.DepartmentID)
	doc.ContentHash = Hash(doc.Title, doc.Body)
	if doc.Status == "" {
		doc.Status = models.KnowledgeStatusActive
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(doc).Error; err != nil {
			return err
		}
		return rebuildChunks(tx, doc, sections)
	})
}

// rebuildChunks replaces ALL chunks of a document with ones built from sections,
// at the current IndexVersion, copying the document's scope and status.
func rebuildChunks(tx *gorm.DB, doc *models.KnowledgeDocument, sections []Section) error {
	if err := tx.Where("document_id = ?", doc.ID).Delete(&models.KnowledgeChunk{}).Error; err != nil {
		return err
	}
	pieces := Chunk(sections)
	if len(pieces) == 0 {
		return nil
	}
	rows := make([]models.KnowledgeChunk, len(pieces))
	for i, p := range pieces {
		rows[i] = models.KnowledgeChunk{
			DocumentID: doc.ID, OrganizationID: doc.OrganizationID,
			UnitID: doc.UnitID, DepartmentID: doc.DepartmentID,
			Visibility: doc.Visibility, Status: doc.Status,
			ChunkIndex: p.Index, Heading: p.Heading, Content: p.Content,
			SearchHeading: SearchForm(p.Heading), SearchText: SearchForm(p.Content),
			IndexVersion: IndexVersion,
			Metadata:     models.JSONB{"source_type": doc.SourceType, "origin": doc.Origin},
		}
	}
	return tx.CreateInBatches(rows, 100).Error
}

// Reindex rebuilds the chunks of one document from its stored title/body at the
// current IndexVersion. The document row (updated_at, hash, scope) is not touched.
// Returns how many chunks the document now has.
func Reindex(tx *gorm.DB, doc *models.KnowledgeDocument) (int, error) {
	if err := rebuildChunks(tx, doc, SectionsFor(doc.SourceType, doc.Title, doc.Body)); err != nil {
		return 0, err
	}
	var n int64
	err := tx.Model(&models.KnowledgeChunk{}).Where("document_id = ?", doc.ID).Count(&n).Error
	return int(n), err
}

// ApplyScope saves the document and copies its scope and status onto its chunks
// WITHOUT rebuilding them (used when only scope/status changed, e.g. a manual_html
// document whose content belongs to the importer).
func ApplyScope(tx *gorm.DB, doc *models.KnowledgeDocument) error {
	doc.Visibility = VisibilityFor(doc.UnitID, doc.DepartmentID)
	if err := tx.Save(doc).Error; err != nil {
		return err
	}
	return tx.Model(&models.KnowledgeChunk{}).Where("document_id = ?", doc.ID).Updates(map[string]any{
		"unit_id": doc.UnitID, "department_id": doc.DepartmentID,
		"visibility": doc.Visibility, "status": doc.Status,
	}).Error
}

// SetArchivedState applies a status to a document and keeps archived_by
// deterministic:
//
//	active   -> archived : archived_by = by
//	archived -> active   : archived_by = ""
//	same status          : unchanged (an import-archived document edited by a person stays "import")
//
// The caller persists (Save / ApplyScope).
func SetArchivedState(doc *models.KnowledgeDocument, status, by string) {
	switch {
	case status == models.KnowledgeStatusArchived && doc.Status != models.KnowledgeStatusArchived:
		doc.ArchivedBy = by
	case status == models.KnowledgeStatusActive && doc.Status == models.KnowledgeStatusArchived:
		doc.ArchivedBy = models.KnowledgeArchivedByNone
	}
	if status != "" {
		doc.Status = status
	}
}

// Remove soft-deletes the document and physically drops its chunks.
func Remove(db *gorm.DB, doc *models.KnowledgeDocument) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("document_id = ?", doc.ID).Delete(&models.KnowledgeChunk{}).Error; err != nil {
			return err
		}
		return tx.Delete(doc).Error
	})
}

// SectionsFor builds the sections a document is chunked from, by source type.
// Markdown keeps its heading trail; everything else is one section titled with
// the document title (so a FAQ question weighs as a heading).
func SectionsFor(sourceType, title, body string) []Section {
	if sourceType == models.KnowledgeSourceMarkdown {
		return ParseMarkdown(body, title)
	}
	return []Section{{Heading: title, Text: body}}
}

// IndexStatus counts what exists and what is built with an older strategy.
type IndexStatus struct {
	Documents    int64 `json:"documents"`
	Chunks       int64 `json:"chunks"`
	StaleChunks  int64 `json:"stale_chunks"`
	IndexVersion int   `json:"index_version"`
}

// Status reports the index state of one organization.
func Status(db *gorm.DB, orgID uuid.UUID) (IndexStatus, error) {
	s := IndexStatus{IndexVersion: IndexVersion}
	if err := db.Model(&models.KnowledgeDocument{}).Where("organization_id = ?", orgID).Count(&s.Documents).Error; err != nil {
		return s, err
	}
	if err := db.Model(&models.KnowledgeChunk{}).Where("organization_id = ?", orgID).Count(&s.Chunks).Error; err != nil {
		return s, err
	}
	err := db.Model(&models.KnowledgeChunk{}).Where("organization_id = ? AND index_version < ?", orgID, IndexVersion).Count(&s.StaleChunks).Error
	return s, err
}

// ReindexResult is the outcome of an organization-wide reindex.
type ReindexResult struct {
	Documents      int   `json:"documents"`
	Chunks         int   `json:"chunks"`
	Skipped        int   `json:"skipped"`
	StaleRemaining int64 `json:"stale_remaining"`
}

// ReindexOrg reindexes the organization's documents ONE TRANSACTION EACH (commit
// per document: no giant transaction, no long lock). onlyStale skips documents
// whose chunks are all at the current version. Synchronous by design: the base is
// small; move it to a queue if it grows.
func ReindexOrg(db *gorm.DB, orgID uuid.UUID, onlyStale bool) (ReindexResult, error) {
	var res ReindexResult
	q := db.Model(&models.KnowledgeDocument{}).Where("organization_id = ?", orgID)
	if onlyStale {
		q = q.Where("id IN (?)", db.Model(&models.KnowledgeChunk{}).Select("document_id").
			Where("organization_id = ? AND index_version < ?", orgID, IndexVersion))
	}
	var ids []uuid.UUID
	if err := q.Order("created_at, id").Pluck("id", &ids).Error; err != nil {
		return res, err
	}
	for _, id := range ids {
		err := db.Transaction(func(tx *gorm.DB) error {
			doc, err := Lock(tx, orgID, id)
			if err != nil {
				return err
			}
			if onlyStale { // it may have been rebuilt while we waited for the lock
				var stale int64
				if err := tx.Model(&models.KnowledgeChunk{}).Where("document_id = ? AND index_version < ?", id, IndexVersion).Count(&stale).Error; err != nil {
					return err
				}
				if stale == 0 {
					res.Skipped++
					return nil
				}
			}
			n, err := Reindex(tx, doc)
			if err != nil {
				return err
			}
			res.Documents++
			res.Chunks += n
			return nil
		})
		if errors.Is(err, gorm.ErrRecordNotFound) { // deleted in the meantime
			res.Skipped++
			continue
		}
		if err != nil {
			return res, err
		}
	}
	st, err := Status(db, orgID)
	res.StaleRemaining = st.StaleChunks
	return res, err
}
