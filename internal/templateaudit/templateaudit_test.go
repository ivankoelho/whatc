package templateaudit_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateaudit"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
)

func tpl(name, metaID, status string) models.Template {
	return models.Template{BaseModel: models.BaseModel{ID: uuid.New()}, Name: name, MetaTemplateID: metaID, Language: "pt_BR", Status: status}
}

func TestCompare(t *testing.T) {
	meta := []whatsapp.MetaTemplate{
		{ID: "1", Name: "codigo_acesso", Language: "pt_BR", Status: "APPROVED"},
		{ID: "2", Name: "ok", Language: "pt_BR", Status: "APPROVED"},
	}
	local := []models.Template{
		tpl("cdigo_acesso", "1", "APPROVED"), // the accent bug: same Meta id, name lost the "ó"
		tpl("ok", "2", "APPROVED"),           // clean
		tpl("gone", "9", "APPROVED"),         // not on Meta
		tpl("draft_only", "", "DRAFT"),       // local draft never submitted: not a divergence
		tpl("Bad Name", "", "DRAFT"),         // invalid local name
	}
	got := templateaudit.Compare(local, meta)

	kinds := map[string]string{}
	for _, d := range got {
		kinds[d.Kind+":"+d.Local] = d.Meta
	}
	assert.Equal(t, "codigo_acesso", kinds[templateaudit.NameDiffers+":cdigo_acesso"])
	assert.Contains(t, kinds, templateaudit.NotOnMeta+":gone")
	assert.Contains(t, kinds, templateaudit.InvalidName+":Bad Name")
	assert.NotContains(t, kinds, templateaudit.NotOnMeta+":draft_only")
	assert.Len(t, got, 3) // name_differs, not_on_meta, invalid_name
}
