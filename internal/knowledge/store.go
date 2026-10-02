package knowledge

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Hash identifies the content of a document (title + normalized body).
func Hash(title, body string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + body))
	return hex.EncodeToString(sum[:])
}

// Save creates or updates a document and rebuilds its chunks in ONE
// transaction, so a document never exists half-indexed. Scope/status are copied
// onto every chunk. doc.ID zero (or a row not found) means create.
func Save(db *gorm.DB, doc *models.KnowledgeDocument, sections []Section) error {
	doc.Body = NormalizeText(doc.Body)
	doc.Visibility = VisibilityFor(doc.UnitID, doc.DepartmentID)
	doc.ContentHash = Hash(doc.Title, doc.Body)
	if doc.Status == "" {
		doc.Status = models.KnowledgeStatusActive
	}
	pieces := Chunk(sections)

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(doc).Error; err != nil {
			return err
		}
		if err := tx.Where("document_id = ?", doc.ID).Delete(&models.KnowledgeChunk{}).Error; err != nil {
			return err
		}
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
				SearchHeading: FoldForSearch(p.Heading), SearchText: FoldForSearch(p.Content),
				Metadata: models.JSONB{"source_type": doc.SourceType, "origin": doc.Origin},
			}
		}
		return tx.CreateInBatches(rows, 100).Error
	})
}

// SetStatus archives/reactivates a document; its chunks follow in the same
// transaction (archived chunks are never retrieved).
func SetStatus(db *gorm.DB, doc *models.KnowledgeDocument, status string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(doc).Update("status", status).Error; err != nil {
			return err
		}
		return tx.Model(&models.KnowledgeChunk{}).Where("document_id = ?", doc.ID).Update("status", status).Error
	})
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
