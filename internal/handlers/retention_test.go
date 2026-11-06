package handlers_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// File retention: policies per organization, eligibility, the read-only simulation. Nothing here (or anywhere in
// this release) deletes a file.

type retWorld struct {
	*chatWorld
	admin  *models.User // settings.general read+write
	viewer *models.User // settings.general read only
}

func newRetWorld(t *testing.T) *retWorld {
	t.Helper()
	w := &retWorld{chatWorld: newFilesWorld(t)}
	w.run()
	org := w.logPorto.OrganizationID
	w.admin = chatUser(t, w.app, org, append([]string{"settings.general:read", "settings.general:write"}, chatUserKeys...))
	w.viewer = chatUser(t, w.app, org, append([]string{"settings.general:read"}, chatUserKeys...))
	return w
}

type simResp struct {
	Data struct {
		Categories []handlers.RetentionCategoryReport `json:"categories"`
	} `json:"data"`
}

func (w *retWorld) simulate(t *testing.T, u *models.User, body map[string]any) (int, map[string]handlers.RetentionCategoryReport) {
	t.Helper()
	code, raw := chatDo(t, w.app.SimulateRetention, u.OrganizationID, u.ID, body, nil, nil)
	var r simResp
	_ = json.Unmarshal(raw, &r)
	out := map[string]handlers.RetentionCategoryReport{}
	for _, c := range r.Data.Categories {
		out[c.Category] = c
	}
	return code, out
}

func (w *retWorld) setPolicy(t *testing.T, u *models.User, category string, months int, enabled bool) int {
	t.Helper()
	code, _ := chatDo(t, w.app.PutRetentionPolicy, u.OrganizationID, u.ID, map[string]any{"keep_months": months, "enabled": enabled},
		map[string]string{"category": category}, nil)
	return code
}

// closeCase marks a case closed `months` months ago.
func (w *retWorld) closeCase(t *testing.T, occ uuid.UUID, months int) {
	t.Helper()
	require.NoError(t, w.app.DB.Model(&models.Occurrence{}).Where("id = ?", occ).Update("closed_at", time.Now().AddDate(0, -months, 0)).Error)
}

func TestRetention_PoliciesPerOrganization_PermissionsAndDefaults(t *testing.T) {
	w := newRetWorld(t)
	org := w.logPorto.OrganizationID

	// a fresh (or migrated) organization: nothing configured, everything off, the engine cannot delete
	code, raw := chatDo(t, w.app.GetRetention, org, w.viewer.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	var g struct {
		Data struct {
			Policies []struct {
				Category   string `json:"category"`
				Enabled    bool   `json:"enabled"`
				KeepMonths int    `json:"keep_months"`
			} `json:"policies"`
			NotCovered []string `json:"not_covered"`
			Engine     struct {
				ExecutionEnabled bool `json:"execution_enabled"`
			} `json:"engine"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &g))
	require.Len(t, g.Data.Policies, len(models.RetentionCategories))
	for _, p := range g.Data.Policies {
		assert.False(t, p.Enabled, "disabled by default: %s", p.Category)
		assert.Zero(t, p.KeepMonths, "no universal term: %s", p.Category)
	}
	assert.False(t, g.Data.Engine.ExecutionEnabled)
	assert.Contains(t, g.Data.NotCovered, "meta_hosted_media")
	assert.Contains(t, g.Data.NotCovered, "nextcloud_originals")
	assert.Contains(t, g.Data.NotCovered, "audit_logs")

	// permissions: read-only cannot change, an agent cannot even read
	assert.Equal(t, fasthttp.StatusForbidden, w.setPolicy(t, w.viewer, models.RetentionOccurrenceFiles, 24, true))
	code, _ = chatDo(t, w.app.GetRetention, org, w.sac.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)

	// validation: no enabling without a term; no absurd terms; unknown category
	assert.Equal(t, fasthttp.StatusBadRequest, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 0, true))
	assert.Equal(t, fasthttp.StatusBadRequest, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 601, false))
	assert.Equal(t, fasthttp.StatusBadRequest, w.setPolicy(t, w.admin, "audit_logs", 12, true), "audit logs are not a file category")
	assert.Equal(t, fasthttp.StatusBadRequest, w.setPolicy(t, w.admin, "meta_hosted_media", 12, true))

	// the administrator configures one category; it is audited and survives a re-read
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 24, true))
	var p models.RetentionPolicy
	require.NoError(t, w.app.DB.Where("organization_id = ? AND category = ?", org, models.RetentionOccurrenceFiles).First(&p).Error)
	assert.True(t, p.Enabled)
	assert.Equal(t, 24, p.KeepMonths)
	require.Eventually(t, func() bool {
		var n int64
		w.app.DB.Model(&models.AuditLog{}).Where("organization_id = ? AND resource_type = ?", org, "retention_policy").Count(&n)
		return n >= 1
	}, 3*time.Second, 50*time.Millisecond, "a policy change is audited")

	// another organization sees its own (empty) rules, and its change never touches ours
	other := newIsoWorld(t)
	oadmin := chatUser(t, w.app, other.org.ID, append([]string{"settings.general:read", "settings.general:write"}, chatUserKeys...))
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, oadmin, models.RetentionOccurrenceFiles, 6, false))
	_, raw = chatDo(t, w.app.GetRetention, other.org.ID, oadmin.ID, nil, nil, nil)
	assert.Contains(t, string(raw), `"keep_months":6`)
	require.NoError(t, w.app.DB.Where("organization_id = ? AND category = ?", org, models.RetentionOccurrenceFiles).First(&p).Error)
	assert.Equal(t, 24, p.KeepMonths)
}

func TestRetention_Eligibility_TermsClosedProtocolsAndDisabledPolicy(t *testing.T) {
	w := newRetWorld(t)
	old := w.occID
	young := w.forward(t, nil)
	fOld := w.upl(t, w.logPorto, "antigo.pdf", pdfBytes())
	code, raw := w.fileUpload(t, w.logPorto, young, "recente.pdf", append(pdfBytes(), 'y'))
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	open := w.forward(t, nil)
	code, raw = w.fileUpload(t, w.logPorto, open, "aberto.pdf", append(pdfBytes(), 'o'))
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	w.closeCase(t, old, 30)  // closed 30 months ago
	w.closeCase(t, young, 3) // closed 3 months ago
	// `open` stays open

	// no policy at all: nothing can be eligible
	_, sim := w.simulate(t, w.admin, map[string]any{"categories": []string{models.RetentionOccurrenceFiles}})
	rep := sim[models.RetentionOccurrenceFiles]
	assert.False(t, rep.Applied)
	assert.Equal(t, "no_policy", rep.Reason)
	assert.Zero(t, rep.Buckets["eligible"].Count)

	// a saved but DISABLED policy applies nothing either
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 24, false))
	_, sim = w.simulate(t, w.admin, map[string]any{"categories": []string{models.RetentionOccurrenceFiles}})
	assert.Equal(t, "policy_disabled", sim[models.RetentionOccurrenceFiles].Reason)
	assert.Zero(t, sim[models.RetentionOccurrenceFiles].Buckets["eligible"].Count)

	// enabled with 24 months: the file of the case closed 30 months ago is eligible, the others are not
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 24, true))
	_, sim = w.simulate(t, w.admin, map[string]any{"categories": []string{models.RetentionOccurrenceFiles}})
	rep = sim[models.RetentionOccurrenceFiles]
	require.True(t, rep.Applied)
	assert.Equal(t, 24, rep.Months)
	assert.EqualValues(t, 1, rep.Buckets["eligible"].Count, "the vencido file")
	assert.EqualValues(t, 1, rep.Buckets["not_due"].Count, "still inside the term")
	assert.EqualValues(t, 1, rep.Buckets["open"].Count, "the protocol is still open: the term has not started")
	require.Len(t, rep.Samples, 1)
	assert.Equal(t, fOld.ID, rep.Samples[0].ID)
	assert.Equal(t, "antigo.pdf", rep.Samples[0].Name)
	assert.Positive(t, rep.Buckets["eligible"].Bytes, "the volume is estimated")
	assert.Contains(t, rep.Samples[0].Reason, "24 months")
	assert.NotEmpty(t, rep.Samples[0].Related, "the related protocol is shown")

	// a what-if term is tried without saving it
	_, sim = w.simulate(t, w.admin, map[string]any{"categories": []string{models.RetentionOccurrenceFiles}, "months": map[string]int{models.RetentionOccurrenceFiles: 2}})
	rep = sim[models.RetentionOccurrenceFiles]
	assert.True(t, rep.Assumed)
	assert.EqualValues(t, 2, rep.Buckets["eligible"].Count, "with 2 months the 3-month-old case is eligible too")
	var p models.RetentionPolicy
	require.NoError(t, w.app.DB.Where("organization_id = ? AND category = ?", w.logPorto.OrganizationID, models.RetentionOccurrenceFiles).First(&p).Error)
	assert.Equal(t, 24, p.KeepMonths, "the what-if did not change the saved policy")

	// the simulation is for the caller's organization only
	other := newIsoWorld(t)
	oadmin := chatUser(t, w.app, other.org.ID, append([]string{"settings.general:read", "settings.general:write"}, chatUserKeys...))
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, oadmin, models.RetentionOccurrenceFiles, 1, true))
	_, sim = w.simulate(t, oadmin, map[string]any{"categories": []string{models.RetentionOccurrenceFiles}})
	assert.Zero(t, sim[models.RetentionOccurrenceFiles].Buckets["eligible"].Count+sim[models.RetentionOccurrenceFiles].Buckets["not_due"].Count+sim[models.RetentionOccurrenceFiles].Buckets["open"].Count,
		"another organization's files never appear")
}

func TestRetention_SharedReferencesAndQuarantineAreProtected(t *testing.T) {
	w := newRetWorld(t)
	org := w.logPorto.OrganizationID
	f := w.upl(t, w.logPorto, "compartilhado.pdf", pdfBytes())
	infected := w.upl(t, w.logPorto, "infectado.pdf", append(pdfBytes(), 'i'))
	w.closeCase(t, w.occID, 40)
	var stored models.OccurrenceFile
	require.NoError(t, w.app.DB.First(&stored, "id = ?", f.ID).Error)

	// the same stored file is also the media of a WhatsApp message: removing it for one record would break the other
	contact := testutil.CreateTestContact(t, w.app.DB, org)
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, ContactID: contact.ID,
		Direction: models.DirectionIncoming, MessageType: models.MessageTypeDocument, MediaURL: stored.StoragePath, Status: models.MessageStatusDelivered}).Error)
	// and an infected file stays as security evidence
	require.NoError(t, w.app.DB.Model(&models.FileScan{}).Where("resource_id = ?", infected.ID).Update("status", models.ScanInfected).Error)

	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 12, true))
	_, sim := w.simulate(t, w.admin, map[string]any{"categories": []string{models.RetentionOccurrenceFiles}})
	rep := sim[models.RetentionOccurrenceFiles]
	assert.EqualValues(t, 0, rep.Buckets["eligible"].Count)
	assert.EqualValues(t, 1, rep.Buckets["shared"].Count)
	assert.EqualValues(t, 1, rep.Buckets["quarantine"].Count)
	reasons := map[string]string{}
	for _, s := range rep.ProtectedSamp {
		reasons[s.Name] = s.Bucket
	}
	assert.Equal(t, "shared", reasons["compartilhado.pdf"])
	assert.Equal(t, "quarantine", reasons["infectado.pdf"])
}

// Every category is evaluated; files hosted outside WhatC are simply not candidates.
func TestRetention_OtherCategoriesAndExternalContent(t *testing.T) {
	w := newRetWorld(t)
	org := w.logPorto.OrganizationID
	root := w.app.Config.Storage.LocalPath
	longAgo := time.Now().AddDate(0, -30, 0)

	// WhatsApp media: its size is only known on disk
	require.NoError(t, os.MkdirAll(filepath.Join(root, "images"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "images", "m.jpg"), []byte("\xFF\xD8\xFF\xE0twelve"), 0o644))
	contact := testutil.CreateTestContact(t, w.app.DB, org)
	m := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, ContactID: contact.ID, Direction: models.DirectionIncoming,
		MessageType: models.MessageTypeImage, MediaURL: filepath.Join("images", "m.jpg"), Status: models.MessageStatusDelivered}
	require.NoError(t, w.app.DB.Create(m).Error)
	require.NoError(t, w.app.DB.Model(m).Update("created_at", longAgo).Error)

	// a campaign: one with a local copy (a candidate) and one whose media lives only on Meta (never a candidate)
	acct := testutil.CreateTestWhatsAppAccount(t, w.app.DB, org)
	tmpl := testutil.CreateTestTemplate(t, w.app.DB, org, acct.Name)
	mk := func(name, local string) {
		c := &models.BulkMessageCampaign{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, WhatsAppAccount: acct.Name, Name: name,
			TemplateID: tmpl.ID, Status: models.CampaignStatusDraft, CreatedBy: w.admin.ID, HeaderMediaID: "meta-hosted", HeaderMediaLocalPath: local}
		require.NoError(t, w.app.DB.Create(c).Error)
		require.NoError(t, w.app.DB.Model(c).Update("created_at", longAgo).Error)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "campaigns"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "campaigns", "c.jpg"), []byte("\xFF\xD8\xFF\xE0campaign"), 0o644))
	mk("com-copia", filepath.Join("campaigns", "c.jpg"))
	mk("so-meta", "")

	for _, c := range []string{models.RetentionWhatsAppMedia, models.RetentionCampaignMedia} {
		require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, c, 12, true))
	}
	_, sim := w.simulate(t, w.admin, nil)
	wa := sim[models.RetentionWhatsAppMedia]
	assert.EqualValues(t, 1, wa.Buckets["eligible"].Count)
	assert.Positive(t, wa.Buckets["eligible"].Bytes, "measured on disk, read-only")
	cm := sim[models.RetentionCampaignMedia]
	assert.EqualValues(t, 1, cm.Buckets["eligible"].Count, "only the campaign with a local copy; media hosted by Meta is not ours to delete")
	assert.Equal(t, "com-copia", cm.Samples[0].Related)
	// categories without a policy report why
	assert.Equal(t, "no_policy", sim[models.RetentionChatAttachments].Reason)
	assert.Equal(t, "no_policy", sim[models.RetentionOccurrenceInvoices].Reason)
}

// The simulation changes nothing: no rows, no files, no log entries.
func TestRetention_SimulationHasNoSideEffects(t *testing.T) {
	w := newRetWorld(t)
	f := w.upl(t, w.logPorto, "antigo.pdf", pdfBytes())
	w.closeCase(t, w.occID, 60)
	for _, c := range models.RetentionCategories {
		require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, c, 6, true))
	}
	var stored models.OccurrenceFile
	require.NoError(t, w.app.DB.First(&stored, "id = ?", f.ID).Error)

	tables := []any{&models.OccurrenceFile{}, &models.FileScan{}, &models.RetentionPolicy{}, &models.RetentionHold{}, &models.RetentionHoldEvent{},
		&models.RetentionRun{}, &models.RetentionItem{}, &models.AuditLog{}, &models.OccurrenceEvent{}, &models.ChatAttachment{}, &models.Message{}}
	count := func() map[string]int64 {
		out := map[string]int64{}
		for _, m := range tables {
			var n int64
			w.app.DB.Unscoped().Model(m).Count(&n)
			out[fmt.Sprintf("%T", m)] = n
		}
		return out
	}
	time.Sleep(200 * time.Millisecond) // let any asynchronous audit write of the setup land first
	before := count()
	for i := 0; i < 3; i++ {
		code, sim := w.simulate(t, w.admin, map[string]any{"months": map[string]int{models.RetentionOccurrenceFiles: 1}})
		require.Equal(t, fasthttp.StatusOK, code)
		assert.EqualValues(t, 1, sim[models.RetentionOccurrenceFiles].Buckets["eligible"].Count)
	}
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, before, count(), "no row of any table changed")
	_, err := os.Stat(filepath.Join(w.app.Config.Storage.LocalPath, stored.StoragePath))
	assert.NoError(t, err, "the file is still on disk")
	assert.Equal(t, fasthttp.StatusOK, w.dl(t, w.logPorto, f.ID, ""), "and still downloadable")
}
