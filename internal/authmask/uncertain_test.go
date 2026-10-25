package authmask_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/authmask"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const releaseKey = "release-test-key-longer-than-32-characters!"

type releaseEnv struct {
	db       *gorm.DB
	org      *models.Organization
	campaign *models.BulkMessageCampaign
	admin    *models.User
}

func newReleaseEnv(t *testing.T) *releaseEnv {
	t.Helper()
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateAdminRole(t, db, org.ID)
	admin := testutil.CreateTestUser(t, db, org.ID, testutil.WithRoleID(&role.ID), testutil.WithEmail(testutil.UniqueEmail("rel-admin")))
	acct := testutil.CreateTestWhatsAppAccountWith(t, db, org.ID)
	tpl := testutil.CreateTestTemplate(t, db, org.ID, acct.Name)
	require.NoError(t, db.Model(tpl).Update("category", "AUTHENTICATION").Error)
	camp := &models.BulkMessageCampaign{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "release",
		WhatsAppAccount: acct.Name, TemplateID: tpl.ID, Status: models.CampaignStatusCompleted, CreatedBy: admin.ID}
	require.NoError(t, db.Create(camp).Error)
	return &releaseEnv{db, org, camp, admin}
}

// uncertain creates a failed recipient holding a claim (and a code sealed until `until`).
func (e *releaseEnv) uncertain(t *testing.T, until time.Time, status models.MessageStatus, msgAge time.Duration) *models.BulkMessageRecipient {
	t.Helper()
	contact := testutil.CreateTestContactWith(t, e.db, e.org.ID, testutil.WithPhoneNumber("5511"+uuid.New().String()[:8]))
	msg := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: e.org.ID, ContactID: contact.ID,
		Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, Status: models.MessageStatusPending,
		Content: "Seu código é ••••••", Metadata: models.JSONB{"campaign_id": e.campaign.ID.String()}}
	require.NoError(t, e.db.Create(msg).Error)
	require.NoError(t, e.db.Exec("UPDATE messages SET created_at = ? WHERE id = ?", time.Now().Add(-msgAge), msg.ID).Error)
	r := &models.BulkMessageRecipient{BaseModel: models.BaseModel{ID: uuid.New()}, CampaignID: e.campaign.ID, PhoneNumber: "+5511999990000", Status: status}
	require.NoError(t, e.db.Create(r).Error)
	sealed, err := templateutil.SealParamsUntil(map[string]any{"1": "424242"}, releaseKey, time.Now(), &until)
	require.NoError(t, err)
	require.NoError(t, e.db.Model(r).UpdateColumns(map[string]any{
		"message_id": msg.ID, "error_message": "delivery_uncertain: timeout", "template_params": models.JSONB(sealed),
	}).Error)
	return r
}

func (e *releaseEnv) release(t *testing.T, r *models.BulkMessageRecipient, email, reason string, dry bool) error {
	t.Helper()
	return authmask.ReleaseUncertain(e.db, authmask.ReleaseRequest{RecipientID: r.ID, OperatorEmail: email, Reason: reason},
		templateutil.OTPPolicy{}, time.Now(), dry)
}

func (e *releaseEnv) audits(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(&models.AuditLog{}).Where("organization_id = ? AND resource_id = ?", e.org.ID, e.campaign.ID).Count(&n).Error)
	return n
}

func (e *releaseEnv) claimHeld(t *testing.T, r *models.BulkMessageRecipient) bool {
	t.Helper()
	var got models.BulkMessageRecipient
	require.NoError(t, e.db.First(&got, r.ID).Error)
	return got.MessageID != nil
}

func TestRelease_RequiresARealAdministratorOfTheCampaignsOrganization(t *testing.T) {
	e := newReleaseEnv(t)
	r := e.uncertain(t, time.Now().Add(5*time.Minute), models.MessageStatusFailed, time.Hour)
	const why = "no delivery in the Meta logs"

	otherOrg := testutil.CreateTestOrganization(t, e.db)
	otherRole := testutil.CreateAdminRole(t, e.db, otherOrg.ID)
	foreign := testutil.CreateTestUser(t, e.db, otherOrg.ID, testutil.WithRoleID(&otherRole.ID), testutil.WithEmail(testutil.UniqueEmail("rel-foreign")))
	plain := testutil.CreateTestUser(t, e.db, e.org.ID, testutil.WithEmail(testutil.UniqueEmail("rel-plain")))
	inactiveRole := testutil.CreateAdminRole(t, e.db, e.org.ID)
	inactive := testutil.CreateTestUser(t, e.db, e.org.ID, testutil.WithRoleID(&inactiveRole.ID), testutil.WithEmail(testutil.UniqueEmail("rel-inactive")))
	require.NoError(t, e.db.Model(inactive).Update("is_active", false).Error)

	for name, email := range map[string]string{
		"unknown e-mail":                    "nobody@example.com",
		"empty":                             "",
		"admin of another organization":     foreign.Email,
		"user without campaign permissions": plain.Email,
		"inactive administrator":            inactive.Email,
	} {
		assert.ErrorIs(t, e.release(t, r, email, why, false), authmask.ErrOperatorNotAllowed, name)
	}
	assert.True(t, e.claimHeld(t, r), "nothing changed")
	assert.Zero(t, e.audits(t), "and nothing was audited")

	require.NoError(t, e.release(t, r, e.admin.Email, why, false))
	assert.False(t, e.claimHeld(t, r))
}

func TestRelease_RefusesACodeThatCanNoLongerBeSent(t *testing.T) {
	e := newReleaseEnv(t)

	expired := e.uncertain(t, time.Now().Add(-time.Minute), models.MessageStatusFailed, time.Hour)
	assert.ErrorIs(t, e.release(t, expired, e.admin.Email, "checked the logs", false), authmask.ErrCodeNotValid)
	assert.True(t, e.claimHeld(t, expired))

	scrubbed := e.uncertain(t, time.Now().Add(5*time.Minute), models.MessageStatusFailed, time.Hour)
	require.NoError(t, e.db.Model(scrubbed).UpdateColumn("template_params", models.JSONB{}).Error)
	assert.ErrorIs(t, e.release(t, scrubbed, e.admin.Email, "checked the logs", false), authmask.ErrNoCodeStored)

	assert.Zero(t, e.audits(t))
}

func TestRelease_ReasonIsRequiredAndNeverACode(t *testing.T) {
	e := newReleaseEnv(t)
	r := e.uncertain(t, time.Now().Add(5*time.Minute), models.MessageStatusFailed, time.Hour)

	for name, reason := range map[string]string{
		"empty":         "",
		"blank":         "   ",
		"too long":      string(make([]byte, 501)),
		"contains code": "the customer said the code 424242 never arrived",
	} {
		assert.ErrorIs(t, e.release(t, r, e.admin.Email, reason, false), authmask.ErrInvalidReason, name)
	}
	assert.True(t, e.claimHeld(t, r))
}

func TestRelease_DryRunChecksEverythingAndWritesNothing(t *testing.T) {
	e := newReleaseEnv(t)
	r := e.uncertain(t, time.Now().Add(5*time.Minute), models.MessageStatusFailed, time.Hour)

	require.NoError(t, e.release(t, r, e.admin.Email, "no delivery in the Meta logs", true))
	assert.True(t, e.claimHeld(t, r), "dry run keeps the claim")
	assert.Zero(t, e.audits(t), "and writes no audit entry")

	// A dry run still refuses what a real run would refuse.
	assert.ErrorIs(t, e.release(t, r, "nobody@example.com", "no delivery", true), authmask.ErrOperatorNotAllowed)
}

func TestRelease_IsCompareAndSetAndAuditedInTheSameTransaction(t *testing.T) {
	e := newReleaseEnv(t)
	r := e.uncertain(t, time.Now().Add(5*time.Minute), models.MessageStatusFailed, time.Hour)

	require.NoError(t, e.release(t, r, e.admin.Email, "no delivery in the Meta logs", false))
	assert.EqualValues(t, 1, e.audits(t))
	assert.ErrorIs(t, e.release(t, r, e.admin.Email, "again", false), authmask.ErrNotUncertain, "a second release does nothing")
	assert.EqualValues(t, 1, e.audits(t), "and writes no second audit entry")

	var a models.AuditLog
	require.NoError(t, e.db.Where("organization_id = ? AND resource_id = ?", e.org.ID, e.campaign.ID).First(&a).Error)
	assert.Equal(t, e.admin.ID, a.UserID, "a real user, never a placeholder")
	assert.Equal(t, e.admin.FullName, a.UserName)
	assert.Equal(t, models.AuditActionUpdated, a.Action)
	assert.WithinDuration(t, time.Now(), a.CreatedAt, time.Minute)
	raw, _ := json.Marshal(a.Changes)
	for _, want := range []string{r.ID.String(), "release", "no delivery in the Meta logs", "recipient_delivery"} {
		assert.Contains(t, string(raw), want)
	}
	assert.NotContains(t, string(raw), "424242", "the code is not in the audit trail")

	// A recipient that is not uncertain (never claimed, or still pending) is not touched.
	pending := e.uncertain(t, time.Now().Add(5*time.Minute), models.MessageStatusPending, time.Hour)
	assert.ErrorIs(t, e.release(t, pending, e.admin.Email, "no delivery", false), authmask.ErrNotUncertain)
}

func TestRelease_RecipientsOfOtherOrganizationsCannotBeReleasedByForeignAdmins(t *testing.T) {
	e := newReleaseEnv(t)
	r := e.uncertain(t, time.Now().Add(5*time.Minute), models.MessageStatusFailed, time.Hour)
	other := newReleaseEnv(t) // another organization with its own administrator

	assert.ErrorIs(t, e.release(t, r, other.admin.Email, "no delivery", false), authmask.ErrOperatorNotAllowed)
	assert.True(t, e.claimHeld(t, r))
}

func TestListUncertain_ShowsStuckClaimsStraightFromPostgres(t *testing.T) {
	e := newReleaseEnv(t)
	until := time.Now().Add(5 * time.Minute)
	uncertain := e.uncertain(t, until, models.MessageStatusFailed, time.Hour)
	stuck := e.uncertain(t, until, models.MessageStatusPending, 10*time.Minute) // claimed long ago, never resolved
	inFlight := e.uncertain(t, until, models.MessageStatusPending, 5*time.Second)

	list, err := authmask.ListUncertain(e.db, time.Now(), 2*time.Minute)
	require.NoError(t, err)
	states := map[uuid.UUID]string{}
	for _, u := range list {
		states[u.RecipientID] = u.State
	}
	assert.Equal(t, authmask.StateUncertain, states[uncertain.ID])
	assert.Equal(t, authmask.StateStuck, states[stuck.ID], "found without any job coming back from Redis")
	assert.NotContains(t, states, inFlight.ID, "a claim that may still be running is not listed")
}

// If the audit entry cannot be written, the release does not happen: both are one transaction.
func TestRelease_AuditFailureRollsTheReleaseBack(t *testing.T) {
	e := newReleaseEnv(t)
	r := e.uncertain(t, time.Now().Add(5*time.Minute), models.MessageStatusFailed, time.Hour)

	fn := "fail_audit_" + e.campaign.ID.String()[:8]
	fn = "fn_" + fn[len("fail_audit_"):]
	require.NoError(t, e.db.Exec(`CREATE FUNCTION `+fn+`() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'audit store is down'; END $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, e.db.Exec(`CREATE TRIGGER `+fn+` BEFORE INSERT ON audit_logs FOR EACH ROW WHEN (NEW.resource_id = '`+e.campaign.ID.String()+`') EXECUTE FUNCTION `+fn+`()`).Error)
	defer func() {
		_ = e.db.Exec(`DROP TRIGGER IF EXISTS ` + fn + ` ON audit_logs`).Error
		_ = e.db.Exec(`DROP FUNCTION IF EXISTS ` + fn + `()`).Error
	}()

	require.Error(t, e.release(t, r, e.admin.Email, "no delivery in the Meta logs", false))

	assert.True(t, e.claimHeld(t, r), "no audit, no release")
	var got models.BulkMessageRecipient
	require.NoError(t, e.db.First(&got, r.ID).Error)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, "delivery_uncertain", "the error text was not rewritten either")
}
