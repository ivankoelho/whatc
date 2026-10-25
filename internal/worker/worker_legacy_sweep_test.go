package worker

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M4: clear-text codes of authentication campaigns (from before sealing) go once
// the retention window has passed; nothing else does.
func TestLegacySweep_DropsOldClearCodesOfAuthCampaignsOnly(t *testing.T) {
	now := time.Now()
	w := testWorker(t)
	w.Config = &config.Config{App: config.AppConfig{EncryptionKey: encKey}}
	_, authCamp, _ := authCampaign(t, w, now)

	// A normal campaign and a campaign whose template category is unknown.
	_, _, tplN, normalCamp, _ := createTestCampaignData(t, w)
	_, _, tplU, unknownCamp, _ := createTestCampaignData(t, w)
	require.NoError(t, w.DB.Model(tplU).Update("category", "").Error)
	_ = tplN

	mk := func(camp *models.BulkMessageCampaign, age time.Duration, params models.JSONB) *models.BulkMessageRecipient {
		r := &models.BulkMessageRecipient{CampaignID: camp.ID, PhoneNumber: "9", Status: models.MessageStatusPending}
		require.NoError(t, w.DB.Create(r).Error)
		require.NoError(t, w.DB.Model(r).UpdateColumn("template_params", params).Error)
		require.NoError(t, w.DB.Exec("UPDATE bulk_message_recipients SET created_at = ? WHERE id = ?", now.Add(-age), r.ID).Error)
		return r
	}
	oldClear := mk(authCamp, 25*time.Hour, models.JSONB{"1": "424242"})
	oldClearDeleted := mk(authCamp, 30*time.Hour, models.JSONB{"1": "515151"})
	require.NoError(t, w.DB.Delete(&models.BulkMessageRecipient{}, oldClearDeleted.ID).Error)
	freshClear := mk(authCamp, 2*time.Hour, models.JSONB{"1": "626262"})
	oldNormal := mk(normalCamp, 48*time.Hour, models.JSONB{"1": "John"})
	oldUnknown := mk(unknownCamp, 48*time.Hour, models.JSONB{"1": "ambiguous"})
	sealed, err := templateutil.SealParams(map[string]any{"1": "737373"}, encKey, now)
	require.NoError(t, err)
	freshSealed := mk(authCamp, 2*time.Hour, models.JSONB(sealed))

	n, err := ScrubExpiredSecrets(w.DB, now, templateutil.OTPPolicy{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(2))

	params := func(id string) string { return rawRow(t, w, "bulk_message_recipients", "template_params", id) }
	assert.Equal(t, "{}", params(oldClear.ID.String()), "old clear code of an authentication campaign is dropped")
	assert.Equal(t, "{}", params(oldClearDeleted.ID.String()), "soft-deleted rows too")
	assert.Contains(t, params(freshClear.ID.String()), "626262", "still inside the retention window")
	assert.Contains(t, params(oldNormal.ID.String()), "John", "ordinary campaigns are never touched")
	assert.Contains(t, params(oldUnknown.ID.String()), "ambiguous", "an unresolvable category is left alone, not guessed")
	assert.Contains(t, params(freshSealed.ID.String()), templateutil.SealedKey, "a fresh sealed code is kept")

	// Idempotent: a second run has nothing left to do for the legacy rows.
	var before models.BulkMessageRecipient
	require.NoError(t, w.DB.Unscoped().First(&before, oldClear.ID).Error)
	n2, err := ScrubExpiredSecrets(w.DB, now, templateutil.OTPPolicy{})
	require.NoError(t, err)
	assert.Zero(t, n2)
	var after models.BulkMessageRecipient
	require.NoError(t, w.DB.Unscoped().First(&after, oldClear.ID).Error)
	assert.Equal(t, before.UpdatedAt.Unix(), after.UpdatedAt.Unix(), "no audit-relevant timestamps are rewritten")
}
