package handlers_test

import (
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

type nextTaskEnv struct {
	app   *handlers.App
	orgID uuid.UUID
	user  uuid.UUID
	opp   *models.SalesOpportunity
}

func newNextTaskEnv(t *testing.T) nextTaskEnv {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	return nextTaskEnv{app, org.ID, agent.ID, newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)}
}

func (e nextTaskEnv) details(t *testing.T, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, e.orgID, e.user)
	req.RequestCtx.SetUserValue("id", e.opp.ID.String())
	require.NoError(t, e.app.UpdateSalesOpportunityDetails(req))
	return testutil.GetResponseStatusCode(req)
}

func (e nextTaskEnv) reload(t *testing.T) models.SalesOpportunity {
	var got models.SalesOpportunity
	require.NoError(t, e.app.DB.First(&got, "id = ?", e.opp.ID).Error)
	return got
}

func TestNextTask_SetEditAndClear(t *testing.T) {
	e := newNextTaskEnv(t)
	at := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

	assert.Equal(t, fasthttp.StatusOK, e.details(t, map[string]any{"next_task_at": at.Format(time.RFC3339), "next_task_note": "  Ligar com o orçamento  "}))
	got := e.reload(t)
	require.NotNil(t, got.NextTaskAt)
	assert.True(t, got.NextTaskAt.Equal(at))
	assert.Equal(t, "Ligar com o orçamento", got.NextTaskNote)

	// Editing only the note keeps the date.
	assert.Equal(t, fasthttp.StatusOK, e.details(t, map[string]any{"next_task_note": "Enviar fotos"}))
	got = e.reload(t)
	require.NotNil(t, got.NextTaskAt)
	assert.Equal(t, "Enviar fotos", got.NextTaskNote)

	// An empty date clears the task and its note.
	assert.Equal(t, fasthttp.StatusOK, e.details(t, map[string]any{"next_task_at": ""}))
	got = e.reload(t)
	assert.Nil(t, got.NextTaskAt)
	assert.Equal(t, "", got.NextTaskNote)
}

func TestNextTask_RejectsBadInput(t *testing.T) {
	e := newNextTaskEnv(t)
	assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, map[string]any{"next_task_at": "amanhã"}))
	long := make([]rune, 201)
	for i := range long {
		long[i] = 'a'
	}
	assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, map[string]any{"next_task_at": time.Now().Add(time.Hour).Format(time.RFC3339), "next_task_note": string(long)}))

	// a note with no task to belong to is ignored, not stored
	assert.Equal(t, fasthttp.StatusOK, e.details(t, map[string]any{"next_task_note": "solta"}))
	assert.Equal(t, "", e.reload(t).NextTaskNote)
}

func TestStage_DirecionamentoNotRequiredWhenTheOrganizationTurnedItOff(t *testing.T) {
	e := newNextTaskEnv(t)
	require.NoError(t, e.app.DB.Model(e.opp).Update("stage", models.SalesOpportunityStageAbrirOrcamento).Error)
	move := func() int {
		req := testutil.NewJSONRequest(t, map[string]any{"stage": "direcionada"})
		testutil.SetAuthContext(req, e.orgID, e.user)
		req.RequestCtx.SetUserValue("id", e.opp.ID.String())
		require.NoError(t, e.app.ChangeSalesOpportunityStage(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusBadRequest, move(), "on by default: the retail rule still holds")

	require.NoError(t, e.app.DB.Model(&models.Organization{}).Where("id = ?", e.orgID).
		Update("settings", models.JSONB{"sales_direcionamento_enabled": false}).Error)
	assert.Equal(t, fasthttp.StatusOK, move())
}

// The board replaces a card with what a write returns: the meeting and ad chips must survive that.
func TestDetailsResponseKeepsTheBoardContext(t *testing.T) {
	e := newNextTaskEnv(t)
	start := time.Now().Add(72 * time.Hour)
	var opp models.SalesOpportunity
	require.NoError(t, e.app.DB.First(&opp, "id = ?", e.opp.ID).Error)
	require.NoError(t, e.app.DB.Create(&models.AIAppointmentOffer{
		OrganizationID: e.orgID, ContactID: opp.ContactID, Status: models.AIAppointmentBooked, BookedStart: &start,
		Token: uuid.NewString(), SessionID: uuid.New(), WhatsAppAccount: "x", ExpiresAt: time.Now().Add(time.Hour),
	}).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"next_task_note": "x", "next_task_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
	testutil.SetAuthContext(req, e.orgID, e.user)
	req.RequestCtx.SetUserValue("id", e.opp.ID.String())
	require.NoError(t, e.app.UpdateSalesOpportunityDetails(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Contains(t, string(testutil.GetResponseBody(req)), `"meeting"`)
}
