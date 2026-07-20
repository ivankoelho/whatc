package handlers_test

import (
	"encoding/json"
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

func TestApp_UpdateContactStatus(t *testing.T) {
	t.Parallel()

	t.Run("resolves a conversation", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "resolved"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.UpdateContactStatus(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
		assert.Equal(t, models.ContactStatusResolved, stored.ContactStatus)
	})

	t.Run("reopens a resolved conversation", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("contact_status", models.ContactStatusResolved).Error)

		req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "in_progress"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.UpdateContactStatus(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
		assert.Equal(t, models.ContactStatusInProgress, stored.ContactStatus)
	})

	t.Run("rejects an invalid status", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "done"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.UpdateContactStatus(req))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	})

	t.Run("denies a user without write permission", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		user := testutil.CreateTestUser(t, app.DB, org.ID) // no role
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "resolved"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.UpdateContactStatus(req))
		assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	})

	t.Run("does not reach a contact from another org", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		otherOrg := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		foreign := testutil.CreateTestContact(t, app.DB, otherOrg.ID)

		req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "resolved"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", foreign.ID.String())

		require.NoError(t, app.UpdateContactStatus(req))
		assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, "id = ?", foreign.ID).Error)
		assert.Equal(t, models.ContactStatusNew, stored.ContactStatus)
	})
}

func TestApp_ListContacts_StatusFilter(t *testing.T) {
	t.Parallel()

	// setup creates one contact in each status and returns the app plus ids.
	setup := func(t *testing.T) (*handlers.App, uuid.UUID, uuid.UUID) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))

		testutil.CreateTestContact(t, app.DB, org.ID) // stays 'new'
		inProgress := testutil.CreateTestContact(t, app.DB, org.ID)
		resolved := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(inProgress).Update("contact_status", models.ContactStatusInProgress).Error)
		require.NoError(t, app.DB.Model(resolved).Update("contact_status", models.ContactStatusResolved).Error)

		return app, org.ID, user.ID
	}

	t.Run("filters by status and reports a matching total", func(t *testing.T) {
		app, orgID, userID := setup(t)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, orgID, userID)
		testutil.SetQueryParam(req, "status", "in_progress")

		require.NoError(t, app.ListContacts(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var resp struct {
			Data struct {
				Contacts []handlers.ContactResponse `json:"contacts"`
				Total    int64                      `json:"total"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		assert.Equal(t, int64(1), resp.Data.Total)
		require.Len(t, resp.Data.Contacts, 1)
		assert.Equal(t, models.ContactStatusInProgress, resp.Data.Contacts[0].ContactStatus)
	})

	t.Run("returns every contact when no status is given", func(t *testing.T) {
		app, orgID, userID := setup(t)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, orgID, userID)

		require.NoError(t, app.ListContacts(req))

		var resp struct {
			Data struct {
				Total int64 `json:"total"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		assert.Equal(t, int64(3), resp.Data.Total)
	})

	t.Run("rejects an invalid status", func(t *testing.T) {
		app, orgID, userID := setup(t)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, orgID, userID)
		testutil.SetQueryParam(req, "status", "bogus")

		require.NoError(t, app.ListContacts(req))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	})

	t.Run("keeps the legacy status field untouched", func(t *testing.T) {
		app, orgID, userID := setup(t)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, orgID, userID)

		require.NoError(t, app.ListContacts(req))

		var resp struct {
			Data struct {
				Contacts []handlers.ContactResponse `json:"contacts"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		require.NotEmpty(t, resp.Data.Contacts)
		assert.Equal(t, "active", resp.Data.Contacts[0].Status,
			"the legacy status field must keep returning 'active'")
	})

	t.Run("counts only new conversations", func(t *testing.T) {
		app, orgID, userID := setup(t)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, orgID, userID)

		require.NoError(t, app.GetContactStatusCounts(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var resp struct {
			Data struct {
				New int64 `json:"new"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		assert.Equal(t, int64(1), resp.Data.New)
	})
}
