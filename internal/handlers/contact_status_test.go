package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransitionContactStatus(t *testing.T) {
	t.Parallel()

	t.Run("transitions when current status matches from", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("contact_status", models.ContactStatusResolved).Error)
		contact.ContactStatus = models.ContactStatusResolved

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusInProgress,
			[]models.ContactStatus{models.ContactStatusResolved},
			nil)

		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, models.ContactStatusInProgress, contact.ContactStatus)

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
		assert.Equal(t, models.ContactStatusInProgress, stored.ContactStatus)
	})

	t.Run("no-op when current status is outside from", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID) // 'new'

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusInProgress,
			[]models.ContactStatus{models.ContactStatusResolved},
			nil)

		require.NoError(t, err)
		assert.False(t, changed)

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
		assert.Equal(t, models.ContactStatusNew, stored.ContactStatus)
	})

	t.Run("empty from allows any origin", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusResolved, nil, nil)

		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, models.ContactStatusResolved, contact.ContactStatus)
	})

	t.Run("no-op when already at the target status", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID) // 'new'

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusNew, nil, nil)

		require.NoError(t, err)
		assert.False(t, changed)
	})

	t.Run("manual transition writes an audit log", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		user := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusResolved, nil, &user.ID)
		require.NoError(t, err)
		require.True(t, changed)

		// audit.LogAudit writes asynchronously
		require.Eventually(t, func() bool {
			var count int64
			app.DB.Model(&models.AuditLog{}).
				Where("resource_type = ? AND resource_id = ? AND user_id = ?", "contact", contact.ID, user.ID).
				Count(&count)
			return count == 1
		}, 3*time.Second, 50*time.Millisecond)
	})

	t.Run("automatic transition writes no audit log", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusResolved, nil, nil)
		require.NoError(t, err)
		require.True(t, changed)

		time.Sleep(300 * time.Millisecond) // give any stray goroutine a chance
		var count int64
		app.DB.Model(&models.AuditLog{}).
			Where("resource_type = ? AND resource_id = ?", "contact", contact.ID).
			Count(&count)
		assert.Equal(t, int64(0), count,
			"automatic transitions have no actor and AuditLog.UserID is NOT NULL")
	})
}
