// Package templateaudit compares local template names with the ones Meta
// approved. It only reads; fixing a divergence is always a manual decision.
package templateaudit

import (
	"regexp"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
)

// Kinds of divergence.
const (
	NameDiffers  = "name_differs"   // same Meta template id, different name
	NotOnMeta    = "not_on_meta"    // neither id nor (name, language) found on Meta
	InvalidName  = "invalid_name"   // local name is not a valid Meta name ([a-z0-9_])
	StatusDiffer = "status_differs" // informational: local status differs from Meta's
)

var validName = regexp.MustCompile(`^[a-z0-9_]+$`)

// Divergence is one finding for a local template.
type Divergence struct {
	TemplateID string
	Kind       string
	Local      string // local name (or status)
	Meta       string // Meta name (or status); empty when not on Meta
}

// Compare checks the local templates of ONE account against what Meta returned for it.
func Compare(local []models.Template, meta []whatsapp.MetaTemplate) []Divergence {
	byID := make(map[string]whatsapp.MetaTemplate, len(meta))
	byNameLang := make(map[string]whatsapp.MetaTemplate, len(meta))
	for _, m := range meta {
		byID[m.ID] = m
		byNameLang[m.Name+"\x00"+m.Language] = m
	}
	var out []Divergence
	for _, t := range local {
		id := t.ID.String()
		if !validName.MatchString(t.Name) {
			out = append(out, Divergence{id, InvalidName, t.Name, ""})
		}
		var m whatsapp.MetaTemplate
		var ok bool
		if t.MetaTemplateID != "" {
			m, ok = byID[t.MetaTemplateID]
		}
		if !ok {
			m, ok = byNameLang[t.Name+"\x00"+t.Language]
		}
		if !ok {
			if t.MetaTemplateID != "" || t.Status == "APPROVED" {
				out = append(out, Divergence{id, NotOnMeta, t.Name, ""})
			}
			continue
		}
		if m.Name != t.Name {
			out = append(out, Divergence{id, NameDiffers, t.Name, m.Name})
		}
		if m.Status != "" && m.Status != t.Status {
			out = append(out, Divergence{id, StatusDiffer, t.Status, m.Status})
		}
	}
	return out
}
