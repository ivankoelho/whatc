package knowledge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// ImportResult counts what an import did (or, with dryRun, would do).
type ImportResult struct {
	Created     int `json:"created"`
	Updated     int `json:"updated"`
	Unchanged   int `json:"unchanged"`
	Reactivated int `json:"reactivated"`
	Archived    int `json:"archived"`
	// ArchivedOrigins lists the documents archived because their section left the HTML.
	ArchivedOrigins []string `json:"archived_origins,omitempty"`
	// Failed lists files that could not be read or produced no section; nothing was
	// archived for them.
	Failed []string `json:"failed,omitempty"`
}

// ValidateImportTarget refuses an import whose organization, unit or department
// does not exist or does not belong to the organization (the same rule the API applies).
func ValidateImportTarget(db *gorm.DB, orgID uuid.UUID, unit, dept *uuid.UUID) error {
	var n int64
	if err := db.Model(&models.Organization{}).Where("id = ?", orgID).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("organization %s does not exist", orgID)
	}
	if unit != nil {
		if err := db.Model(&models.Unit{}).Where("id = ? AND organization_id = ?", *unit, orgID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("unit %s does not exist in organization %s", *unit, orgID)
		}
	}
	if dept != nil {
		if err := db.Model(&models.Department{}).Where("id = ? AND organization_id = ?", *dept, orgID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("department %s does not exist in organization %s", *dept, orgID)
		}
	}
	return nil
}

// ImportManuals imports every *.html of dir as manual_html documents of one
// organization, one per h2/h3 section, with origin "manual/<file>#<anchor>".
//
// The HTML is the single source of truth of the CONTENT. Per file:
//   - a section is created, or its content updated when the hash changed;
//   - scope and status of an EXISTING document are never changed (unit/department
//     only apply to documents created now; whoever archived or re-scoped a document
//     through the API keeps that decision);
//   - documents of this file whose section is no longer in the HTML are ARCHIVED
//     (archived_by = import), never deleted; if the section comes back, it is
//     reactivated only when the import was the one that archived it;
//   - a file that cannot be read or produces no section is reported in Failed and
//     archives nothing.
//
// dryRun reads and reports without writing. Files missing from dir are not touched.
func ImportManuals(db *gorm.DB, orgID uuid.UUID, unit, dept *uuid.UUID, dir string, dryRun bool) (ImportResult, error) {
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
		name := filepath.Base(path)
		docs, err := parseManualFile(path)
		if err != nil || len(docs) == 0 {
			res.Failed = append(res.Failed, name)
			continue
		}
		seen := map[string]bool{}
		for _, d := range docs {
			origin := "manual/" + name + "#" + d.Anchor
			seen[origin] = true
			act, err := upsertByOrigin(db, orgID, unit, dept, origin, d, dryRun)
			if err != nil {
				return res, fmt.Errorf("%s: %w", origin, err)
			}
			switch act {
			case "created":
				res.Created++
			case "updated":
				res.Updated++
			case "reactivated":
				res.Reactivated++
			default:
				res.Unchanged++
			}
		}
		archived, err := archiveOrphans(db, orgID, name, seen, dryRun)
		if err != nil {
			return res, fmt.Errorf("%s: %w", name, err)
		}
		res.Archived += len(archived)
		res.ArchivedOrigins = append(res.ArchivedOrigins, archived...)
	}
	if len(res.Failed) > 0 {
		return res, fmt.Errorf("%d file(s) could not be imported and were left untouched: %v", len(res.Failed), res.Failed)
	}
	return res, nil
}

func parseManualFile(path string) ([]ManualDoc, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseManualHTML(f)
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// upsertByOrigin retries ONCE when a concurrent import created the same document
// between our read and our insert (unique violation on organization+origin).
func upsertByOrigin(db *gorm.DB, orgID uuid.UUID, unit, dept *uuid.UUID, origin string, d ManualDoc, dryRun bool) (string, error) {
	act, err := upsertOnce(db, orgID, unit, dept, origin, d, dryRun)
	if err != nil && isUniqueViolation(err) {
		return upsertOnce(db, orgID, unit, dept, origin, d, dryRun)
	}
	return act, err
}

func upsertOnce(db *gorm.DB, orgID uuid.UUID, unit, dept *uuid.UUID, origin string, d ManualDoc, dryRun bool) (string, error) {
	body := NormalizeText(d.Text)
	hash := Hash(d.Title, body)
	act := "unchanged"

	err := db.Transaction(func(tx *gorm.DB) error {
		var doc models.KnowledgeDocument
		err := tx.Clauses(lockUpdate).Where("organization_id = ? AND origin = ?", orgID, origin).First(&doc).Error
		switch {
		case err == nil:
			changed := doc.ContentHash != hash
			reactivate := doc.Status == models.KnowledgeStatusArchived && doc.ArchivedBy == models.KnowledgeArchivedByImport
			switch {
			case reactivate:
				act = "reactivated"
			case changed:
				act = "updated"
			}
			if dryRun || act == "unchanged" {
				return nil
			}
			if reactivate {
				SetArchivedState(&doc, models.KnowledgeStatusActive, models.KnowledgeArchivedByImport)
			}
			if changed {
				doc.Title, doc.Body = d.Title, body
				return Save(tx, &doc, SectionsFor(models.KnowledgeSourceManualHTML, d.Title, body))
			}
			return ApplyScope(tx, &doc)
		case errors.Is(err, gorm.ErrRecordNotFound):
			act = "created"
			if dryRun {
				return nil
			}
			doc = models.KnowledgeDocument{
				OrganizationID: orgID, UnitID: unit, DepartmentID: dept,
				SourceType: models.KnowledgeSourceManualHTML, Title: d.Title, Origin: origin, Body: body,
			}
			return Save(tx, &doc, SectionsFor(models.KnowledgeSourceManualHTML, d.Title, body))
		}
		return err
	})
	return act, err
}

// archiveOrphans archives the active manual_html documents of one file whose
// origin is not in seen. It is only called for a file that parsed with sections.
func archiveOrphans(db *gorm.DB, orgID uuid.UUID, file string, seen map[string]bool, dryRun bool) ([]string, error) {
	prefix := "manual/" + file + "#"
	var docs []models.KnowledgeDocument
	err := db.Where("organization_id = ? AND source_type = ? AND status = ? AND strpos(origin, ?) = 1",
		orgID, models.KnowledgeSourceManualHTML, models.KnowledgeStatusActive, prefix).Find(&docs).Error
	if err != nil {
		return nil, err
	}
	var archived []string
	for _, d := range docs {
		if seen[d.Origin] {
			continue
		}
		if !dryRun {
			err := db.Transaction(func(tx *gorm.DB) error {
				doc, err := Lock(tx, orgID, d.ID)
				if err != nil {
					return err
				}
				if doc.Status != models.KnowledgeStatusActive {
					return nil // someone archived it meanwhile
				}
				SetArchivedState(doc, models.KnowledgeStatusArchived, models.KnowledgeArchivedByImport)
				return ApplyScope(tx, doc)
			})
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return archived, err
			}
		}
		archived = append(archived, d.Origin)
	}
	return archived, nil
}
