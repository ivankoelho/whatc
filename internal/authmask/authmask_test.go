package authmask_test

import (
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/authmask"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const body = "Seu código é *{{1}}*. Não compartilhe."

func TestRun_DryRunThenApplyIsIdempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	contact := testutil.CreateTestContactWith(t, db, org.ID)

	auth := &models.Template{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "codigo_acesso", Category: "AUTHENTICATION", BodyContent: body, Language: "pt_BR"}
	util := &models.Template{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "pedido", Category: "UTILITY", BodyContent: "Pedido {{1}}", Language: "pt_BR"}
	require.NoError(t, db.Create(auth).Error)
	require.NoError(t, db.Create(util).Error)

	mk := func(tpl *models.Template, content string, withID bool) *models.Message {
		md := models.JSONB{"template_name": tpl.Name}
		if withID {
			md["template_id"] = tpl.ID.String()
		}
		m := &models.Message{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
			Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, Status: models.MessageStatusSent,
			TemplateName: tpl.Name, Content: content, Metadata: md,
			InteractiveData: models.JSONB{"type": "button", "buttons": []any{map[string]any{"type": "OTP"}}},
		}
		require.NoError(t, db.Create(m).Error)
		return m
	}
	byID := mk(auth, "Seu código é *123456*. Não compartilhe.", true)
	byName := mk(auth, "Seu código é *654321*. Não compartilhe.", false)
	edited := mk(auth, "texto que não bate com o corpo 999999", true)
	normal := mk(util, "Pedido 77", true)

	rep, err := authmask.Run(db, false)
	require.NoError(t, err)
	assert.Equal(t, 2, rep.Masked)
	assert.Equal(t, 1, rep.Review)
	assert.Equal(t, []string{edited.ID.String()}, rep.ReviewIDs)
	var untouched models.Message
	require.NoError(t, db.First(&untouched, byID.ID).Error)
	assert.Contains(t, untouched.Content, "123456", "dry run must not write")

	_, err = authmask.Run(db, true)
	require.NoError(t, err)
	for _, id := range []uuid.UUID{byID.ID, byName.ID} {
		var m models.Message
		require.NoError(t, db.First(&m, id).Error)
		assert.Equal(t, "Seu código é *••••••*. Não compartilhe.", m.Content)
		assert.Equal(t, true, m.Metadata["auth_code_masked"])
		assert.Equal(t, "auth_code", m.InteractiveData["type"])
		assert.Nil(t, m.InteractiveData["buttons"])
		assert.Equal(t, models.MessageStatusSent, m.Status)
	}
	var n, e models.Message
	require.NoError(t, db.First(&n, normal.ID).Error)
	require.NoError(t, db.First(&e, edited.ID).Error)
	assert.Equal(t, "Pedido 77", n.Content)
	assert.Contains(t, e.Content, "999999", "unrecognised text is left for manual review")

	// Second run: nothing left to mask.
	rep2, err := authmask.Run(db, true)
	require.NoError(t, err)
	assert.Zero(t, rep2.Masked)
	assert.Equal(t, 2, rep2.AlreadyMasked)
	assert.Equal(t, 1, rep2.Review)
}

func TestRecipients_ScrubsDoneSealsPendingAndIsIdempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID)
	acct := testutil.CreateTestWhatsAppAccountWith(t, db, org.ID)
	tpl := testutil.CreateTestTemplate(t, db, org.ID, acct.Name)
	require.NoError(t, db.Model(tpl).Update("category", "AUTHENTICATION").Error)

	mkCampaign := func(status models.CampaignStatus) *models.BulkMessageCampaign {
		c := &models.BulkMessageCampaign{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "c",
			WhatsAppAccount: acct.Name, TemplateID: tpl.ID, Status: status, CreatedBy: user.ID}
		require.NoError(t, db.Create(c).Error)
		return c
	}
	mkRec := func(c *models.BulkMessageCampaign, st models.MessageStatus) *models.BulkMessageRecipient {
		r := &models.BulkMessageRecipient{BaseModel: models.BaseModel{ID: uuid.New()}, CampaignID: c.ID, PhoneNumber: "1", Status: st}
		require.NoError(t, db.Create(r).Error)
		require.NoError(t, db.Model(r).UpdateColumn("template_params", models.JSONB{"1": "424242"}).Error)
		return r
	}
	done := mkRec(mkCampaign(models.CampaignStatusCompleted), models.MessageStatusSent)
	cancelled := mkRec(mkCampaign(models.CampaignStatusCancelled), models.MessageStatusPending)
	pending := mkRec(mkCampaign(models.CampaignStatusDraft), models.MessageStatusPending)
	retryable := mkRec(mkCampaign(models.CampaignStatusCompleted), models.MessageStatusFailed)
	const key = "migration-test-key-longer-than-32-characters"

	// Dry run writes nothing.
	rep, err := authmask.Recipients(db, key, false, true)
	require.NoError(t, err)
	assert.Equal(t, 2, rep.Scrub)
	assert.Equal(t, 2, rep.SealPending)
	var r models.BulkMessageRecipient
	require.NoError(t, db.First(&r, done.ID).Error)
	assert.Equal(t, "424242", r.TemplateParams["1"])

	// Apply without -seal-pending: done/cancelled dropped, pending ones untouched.
	_, err = authmask.Recipients(db, key, true, false)
	require.NoError(t, err)
	for _, id := range []uuid.UUID{done.ID, cancelled.ID} {
		var rr models.BulkMessageRecipient
		require.NoError(t, db.First(&rr, id).Error)
		assert.Empty(t, rr.TemplateParams)
	}
	var pr models.BulkMessageRecipient
	require.NoError(t, db.First(&pr, pending.ID).Error)
	assert.Equal(t, "424242", pr.TemplateParams["1"], "pending campaigns are never altered without -seal-pending")

	// Apply with -seal-pending: sealed in place and still readable by the worker.
	_, err = authmask.Recipients(db, key, true, true)
	require.NoError(t, err)
	for _, id := range []uuid.UUID{pending.ID, retryable.ID} {
		var sr models.BulkMessageRecipient
		require.NoError(t, db.First(&sr, id).Error)
		assert.True(t, templateutil.IsSealed(sr.TemplateParams))
		opened, err := templateutil.OpenParams(sr.TemplateParams, key, time.Now())
		require.NoError(t, err)
		assert.Equal(t, "424242", opened["1"])
	}

	// Second run: nothing left.
	rep2, err := authmask.Recipients(db, key, true, true)
	require.NoError(t, err)
	assert.Zero(t, rep2.Scrub)
	assert.Zero(t, rep2.SealPending)
	assert.Equal(t, 2, rep2.AlreadySealed)
}

// A run interrupted half way leaves some rows masked and some not; the next run
// finishes exactly the remaining ones.
func TestRun_ResumesAfterPartialExecution(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	contact := testutil.CreateTestContactWith(t, db, org.ID)
	auth := &models.Template{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "otp_partial", Category: "AUTHENTICATION", BodyContent: body, Language: "pt_BR"}
	require.NoError(t, db.Create(auth).Error)
	mk := func(content string, md models.JSONB) *models.Message {
		md["template_id"] = auth.ID.String()
		m := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
			Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, Status: models.MessageStatusSent,
			TemplateName: auth.Name, Content: content, Metadata: md}
		require.NoError(t, db.Create(m).Error)
		return m
	}
	doneAlready := mk("Seu código é *••••••*. Não compartilhe.", models.JSONB{"auth_code_masked": true})
	remaining := mk("Seu código é *777777*. Não compartilhe.", models.JSONB{})

	rep, err := authmask.Run(db, true)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, rep.Masked, 1) // the test DB is shared: other tests leave rows behind
	assert.GreaterOrEqual(t, rep.AlreadyMasked, 1)

	var a, b models.Message
	require.NoError(t, db.First(&a, doneAlready.ID).Error)
	require.NoError(t, db.First(&b, remaining.ID).Error)
	assert.Equal(t, "Seu código é *••••••*. Não compartilhe.", a.Content)
	assert.Equal(t, "Seu código é *••••••*. Não compartilhe.", b.Content)
	assert.Equal(t, remaining.CreatedAt.Unix(), b.CreatedAt.Unix(), "timestamps untouched")
	assert.Equal(t, models.MessageStatusSent, b.Status, "status untouched")
}

func TestValidityReport_CountsOnlyActionableRecipientsAndWritesNothing(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID)
	acct := testutil.CreateTestWhatsAppAccountWith(t, db, org.ID)
	tpl := testutil.CreateTestTemplate(t, db, org.ID, acct.Name)
	require.NoError(t, db.Model(tpl).Update("category", "AUTHENTICATION").Error)
	camp := &models.BulkMessageCampaign{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "report",
		WhatsAppAccount: acct.Name, TemplateID: tpl.ID, Status: models.CampaignStatusScheduled, CreatedBy: user.ID}
	require.NoError(t, db.Create(camp).Error)
	const key = "report-test-key-longer-than-32-characters!"
	now := time.Now()
	mk := func(st models.MessageStatus, params models.JSONB) {
		r := &models.BulkMessageRecipient{BaseModel: models.BaseModel{ID: uuid.New()}, CampaignID: camp.ID, PhoneNumber: "1", Status: st}
		require.NoError(t, db.Create(r).Error)
		require.NoError(t, db.Model(r).UpdateColumn("template_params", params).Error)
	}
	until := now.Add(time.Minute)
	past := now.Add(-time.Minute)
	withValidity, _ := templateutil.SealParamsUntil(map[string]any{"1": "1"}, key, now, &until)
	expired, _ := templateutil.SealParamsUntil(map[string]any{"1": "1"}, key, now.Add(-time.Hour), &past)
	noValidity, _ := templateutil.SealParams(map[string]any{"1": "1"}, key, now)
	mk(models.MessageStatusPending, withValidity)
	mk(models.MessageStatusPending, expired)
	mk(models.MessageStatusPending, noValidity)
	mk(models.MessageStatusFailed, models.JSONB{"1": "clear"}) // legacy plaintext, retryable
	mk(models.MessageStatusSent, models.JSONB{"1": "done"})    // not actionable
	mk(models.MessageStatusPending, models.JSONB{})            // nothing stored

	impacts, err := authmask.ValidityReport(db, now, templateutil.OTPPolicy{})
	require.NoError(t, err)
	var im *authmask.CampaignImpact
	for i := range impacts {
		if impacts[i].CampaignID == camp.ID {
			im = &impacts[i]
		}
	}
	require.NotNil(t, im)
	assert.Equal(t, 1, im.WithValidity)
	assert.Equal(t, 1, im.Expired)
	assert.Equal(t, 2, im.NoValidity)
	assert.Equal(t, 1, im.Plaintext)

	var n int64
	require.NoError(t, db.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ? AND template_params::text LIKE '%clear%'", camp.ID).Count(&n).Error)
	assert.Equal(t, int64(1), n, "the report is read-only")
}
