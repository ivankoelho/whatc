// Package authmask masks one-time codes already stored in message history.
//
// New AUTHENTICATION messages are masked at write time (handlers.createOutgoingMessage);
// this backfills the rows written before that. It is idempotent: a row is only
// touched while it lacks the auth_code_masked metadata flag.
package authmask

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"gorm.io/gorm"
)

const (
	metadataKey     = "auth_code_masked"
	interactiveType = "auth_code"
	batchSize       = 500
	maxReviewListed = 50
)

// Report summarises a run. In dry-run mode Masked is what WOULD be changed.
type Report struct {
	Candidates    int      // unmasked template messages inspected
	Masked        int      // rows masked (or that would be)
	AlreadyMasked int      // rows already carrying the flag (skipped)
	Review        int      // rows that could not be reliably identified/masked
	ReviewIDs     []string // first maxReviewListed ids, for manual review
}

type tmplKey struct {
	org  string
	name string
}

// Run masks stored codes. With apply=false nothing is written.
func Run(db *gorm.DB, apply bool) (*Report, error) {
	rep := &Report{}

	var already int64
	if err := db.Unscoped().Model(&models.Message{}).
		Where("message_type = ? AND metadata->>? = 'true'", models.MessageTypeTemplate, metadataKey).
		Count(&already).Error; err != nil {
		return nil, err
	}
	rep.AlreadyMasked = int(already)

	var templates []models.Template
	if err := db.Unscoped().Find(&templates).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]*models.Template, len(templates))
	byName := make(map[tmplKey][]*models.Template)
	for i := range templates {
		t := &templates[i]
		byID[t.ID.String()] = t
		k := tmplKey{t.OrganizationID.String(), t.Name}
		byName[k] = append(byName[k], t)
	}

	var batch []models.Message
	res := db.Unscoped().Where("message_type = ? AND (metadata IS NULL OR metadata->>? IS NULL)", models.MessageTypeTemplate, metadataKey).
		FindInBatches(&batch, batchSize, func(tx *gorm.DB, _ int) error {
			for i := range batch {
				m := &batch[i]
				rep.Candidates++
				tpl, ok, reliable := resolve(m, byID, byName)
				if !ok {
					continue // not an authentication template
				}
				if !reliable || !bodyMatches(tpl.BodyContent, m.Content) {
					rep.Review++
					if len(rep.ReviewIDs) < maxReviewListed {
						rep.ReviewIDs = append(rep.ReviewIDs, m.ID.String())
					}
					continue
				}
				rep.Masked++
				if !apply {
					continue
				}
				md := models.JSONB{}
				for k, v := range m.Metadata {
					md[k] = v
				}
				md[metadataKey] = true
				// UpdateColumns: keep updated_at and skip hooks; status and
				// timestamps of the message stay as they were.
				if err := db.Model(&models.Message{}).Where("id = ?", m.ID).UpdateColumns(map[string]any{
					"content":          templateutil.MaskAllParams(tpl.BodyContent),
					"metadata":         md,
					"interactive_data": models.JSONB{"type": interactiveType},
					"template_params":  nil,
				}).Error; err != nil {
					return fmt.Errorf("message %s: %w", m.ID, err)
				}
			}
			return nil
		})
	if res.Error != nil {
		return nil, res.Error
	}
	return rep, nil
}

// resolve finds the message's template. ok=false means "not authentication".
// reliable=false means we could not tell for sure (needs manual review).
func resolve(m *models.Message, byID map[string]*models.Template, byName map[tmplKey][]*models.Template) (tpl *models.Template, ok, reliable bool) {
	if id, _ := m.Metadata["template_id"].(string); id != "" {
		if t, found := byID[id]; found {
			return t, templateutil.IsAuthCategory(t.Category), true
		}
	}
	if m.TemplateName == "" {
		return nil, false, true
	}
	cands := byName[tmplKey{m.OrganizationID.String(), m.TemplateName}]
	auth, other := 0, 0
	for _, t := range cands {
		if templateutil.IsAuthCategory(t.Category) {
			auth++
			tpl = t
		} else {
			other++
		}
	}
	switch {
	case auth == 0:
		return nil, false, true
	case other > 0:
		return tpl, true, false // same name used with different categories
	default:
		return tpl, true, true
	}
}

// bodyMatches checks that content is the template body with its placeholders
// filled in (or already masked), so we never overwrite text we do not understand.
func bodyMatches(body, content string) bool {
	if body == "" {
		return false
	}
	if content == templateutil.MaskAllParams(body) {
		return true
	}
	parts := templateutil.ParameterPattern.Split(body, -1)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re, err := regexp.Compile(`(?s)^` + strings.Join(parts, `.+?`) + `$`)
	return err == nil && re.MatchString(content)
}
