package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestApp_NotifyTyping(t *testing.T) {
	t.Parallel()

	t.Run("accepts a typing notification", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.NotifyTyping(req))
		assert.Equal(t, fasthttp.StatusNoContent, testutil.GetResponseStatusCode(req))
	})

	t.Run("does not reach a contact from another org", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		otherOrg := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		foreign := testutil.CreateTestContact(t, app.DB, otherOrg.ID)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", foreign.ID.String())

		require.NoError(t, app.NotifyTyping(req))
		assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
	})

	t.Run("rejects an unauthenticated request", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.NotifyTyping(req))
		assert.Equal(t, fasthttp.StatusUnauthorized, testutil.GetResponseStatusCode(req))
	})

	t.Run("succeeds without a websocket hub", func(t *testing.T) {
		// newTestApp leaves WSHub nil; the handler must not panic.
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.NotifyTyping(req))
		assert.Equal(t, fasthttp.StatusNoContent, testutil.GetResponseStatusCode(req))
	})
}

func TestApp_GetMessages_IncludesSenderName(t *testing.T) {
	t.Parallel()

	t.Run("outgoing message carries the agent name", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID,
			testutil.WithRoleID(&adminRole.ID), testutil.WithFullName("Ana Ribeiro"))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		require.NoError(t, app.DB.Create(&models.Message{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  org.ID,
			WhatsAppAccount: "acct",
			ContactID:       contact.ID,
			Direction:       models.DirectionOutgoing,
			MessageType:     models.MessageTypeText,
			Content:         "resposta do agente",
			SentByUserID:    &user.ID,
		}).Error)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.GetMessages(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var resp struct {
			Data struct {
				Messages []handlers.MessageResponse `json:"messages"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		require.Len(t, resp.Data.Messages, 1)
		assert.Equal(t, "Ana Ribeiro", resp.Data.Messages[0].SentByUserName)
	})

	t.Run("message without an agent carries an empty name", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		require.NoError(t, app.DB.Create(&models.Message{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  org.ID,
			WhatsAppAccount: "acct",
			ContactID:       contact.ID,
			Direction:       models.DirectionOutgoing,
			MessageType:     models.MessageTypeText,
			Content:         "resposta do chatbot",
		}).Error)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.GetMessages(req))

		var resp struct {
			Data struct {
				Messages []handlers.MessageResponse `json:"messages"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		require.Len(t, resp.Data.Messages, 1)
		assert.Empty(t, resp.Data.Messages[0].SentByUserName)
	})
}
