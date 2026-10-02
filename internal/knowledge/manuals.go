package knowledge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// ImportResult counts what an import did.
type ImportResult struct {
	Created, Updated, Unchanged int
}

// ImportManuals imports every *.html of dir as manual_html documents of one
// organization, one per h2/h3 section, with origin "manual/<file>#<anchor>".
// Re-running is idempotent: same content and scope = unchanged; changed content
// replaces the chunks; documents of sections that vanished from the manual are
// left alone. The default scope is the whole organization.
func ImportManuals(db *gorm.DB, orgID uuid.UUID, unit, dept *uuid.UUID, dir string) (ImportResult, error) {
	var res ImportResult
	files, err := filepath.Glob(filepath.Join(dir, "*.html"))
	if err != nil {
		return res, err
	}
	if len(files) == 0 {
		return res, fmt.Errorf("no .html files in %s", dir)
	}
	sort.Strings(files)

	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return res, err
		}
		docs, err := ParseManualHTML(f)
		_ = f.Close()
		if err != nil {
			return res, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		for _, d := range docs {
			origin := "manual/" + filepath.Base(path) + "#" + d.Anchor
			status, err := upsertByOrigin(db, orgID, unit, dept, origin, d)
			if err != nil {
				return res, fmt.Errorf("%s: %w", origin, err)
			}
			switch status {
			case "created":
				res.Created++
			case "updated":
				res.Updated++
			default:
				res.Unchanged++
			}
		}
	}
	return res, nil
}

func upsertByOrigin(db *gorm.DB, orgID uuid.UUID, unit, dept *uuid.UUID, origin string, d ManualDoc) (string, error) {
	body := NormalizeText(d.Text)
	hash := Hash(d.Title, body)

	var doc models.KnowledgeDocument
	err := db.Where("organization_id = ? AND origin = ?", orgID, origin).First(&doc).Error
	switch {
	case err == nil:
		sameScope := sameID(doc.UnitID, unit) && sameID(doc.DepartmentID, dept)
		if doc.ContentHash == hash && sameScope && doc.Status == models.KnowledgeStatusActive {
			return "unchanged", nil
		}
		doc.Title, doc.Body, doc.UnitID, doc.DepartmentID = d.Title, body, unit, dept
		doc.Status = models.KnowledgeStatusActive
		return "updated", Save(db, &doc, SectionsFor(models.KnowledgeSourceManualHTML, d.Title, body))
	case errors.Is(err, gorm.ErrRecordNotFound):
		doc = models.KnowledgeDocument{
			OrganizationID: orgID, UnitID: unit, DepartmentID: dept,
			SourceType: models.KnowledgeSourceManualHTML, Title: d.Title, Origin: origin, Body: body,
		}
		return "created", Save(db, &doc, SectionsFor(models.KnowledgeSourceManualHTML, d.Title, body))
	}
	return "", err
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
