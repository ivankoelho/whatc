package handlers

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closedAllWeek is a business-hours configuration with every day disabled: always outside hours.
func closedAllWeek() models.JSONBArray {
	var h models.JSONBArray
	for d := 0; d < 7; d++ {
		h = append(h, map[string]any{"day": float64(d), "enabled": false, "start_time": "08:00", "end_time": "18:00"})
	}
	return h
}

// openAllWeek is open every day, all day.
func openAllWeek() models.JSONBArray {
	var h models.JSONBArray
	for d := 0; d < 7; d++ {
		h = append(h, map[string]any{"day": float64(d), "enabled": true, "start_time": "00:00", "end_time": "23:59"})
	}
	return h
}

func withHours(t *testing.T, app *App, orgID uuid.UUID, account string, hours models.JSONBArray, oohMessage string) {
	t.Helper()
	s := models.ChatbotSettings{OrganizationID: orgID, WhatsAppAccount: account, IsEnabled: true}
	s.BusinessHours = models.BusinessHoursConfig{Enabled: true, Hours: hours, OutOfHoursMessage: oohMessage}
	require.NoError(t, app.DB.Create(&s).Error)
}

func transfersOf(t *testing.T, app *App, contactID uuid.UUID) []models.AgentTransfer {
	t.Helper()
	var rows []models.AgentTransfer
	require.NoError(t, app.DB.Where("contact_id = ?", contactID).Find(&rows).Error)
	return rows
}

func outgoingTexts(t *testing.T, app *App, contactID uuid.UUID) []string {
	t.Helper()
	var msgs []models.Message
	require.NoError(t, app.DB.Where("contact_id = ? AND direction = ?", contactID, models.DirectionOutgoing).Find(&msgs).Error)
	var out []string
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func TestTransferToQueue_CreatesTheTransferAndSendsNothing(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	res, err := app.TransferToQueue(context.Background(), account, contact, models.TransferSourceAIConfirmed, "Solicitado pela IA.")
	require.NoError(t, err)
	assert.Equal(t, TransferCreated, res.Outcome)
	assert.NotEqual(t, uuid.Nil, res.TransferID)

	rows := transfersOf(t, app, contact.ID)
	require.Len(t, rows, 1)
	assert.Equal(t, res.TransferID, rows[0].ID)
	assert.Equal(t, models.TransferSourceAIConfirmed, rows[0].Source)
	assert.Equal(t, "Solicitado pela IA.", rows[0].Notes)
	assert.Equal(t, models.TransferStatusActive, rows[0].Status)
	assert.Nil(t, rows[0].TransferredByUserID, "nobody on the staff did it")
	assert.Nil(t, rows[0].AgentID, "it goes to the queue")
	assert.Nil(t, rows[0].TeamID)
	assert.Empty(t, outgoingTexts(t, app, contact.ID), "the service never messages the customer")
}

func TestTransferToQueue_AlreadyActiveIsAnOutcomeNotAnErrorAndIsIdempotent(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	first, err := app.TransferToQueue(context.Background(), account, contact, models.TransferSourceFlow, "")
	require.NoError(t, err)
	require.Equal(t, TransferCreated, first.Outcome)

	again, err := app.TransferToQueue(context.Background(), account, contact, models.TransferSourceAIConfirmed, "")
	require.NoError(t, err)
	assert.Equal(t, TransferAlreadyActive, again.Outcome)
	assert.Equal(t, uuid.Nil, again.TransferID)
	assert.Len(t, transfersOf(t, app, contact.ID), 1, "no second transfer")
}

func TestTransferToQueue_ConcurrentCallsCreateExactlyOneTransfer(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	var wg sync.WaitGroup
	results := make([]TransferResult, 6)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := app.TransferToQueue(context.Background(), account, contact, models.TransferSourceAIConfirmed, "")
			assert.NoError(t, err)
			results[i] = r
		}(i)
	}
	wg.Wait()

	created := 0
	for _, r := range results {
		if r.Outcome == TransferCreated {
			created++
		} else {
			assert.Equal(t, TransferAlreadyActive, r.Outcome)
		}
	}
	assert.Equal(t, 1, created)
	assert.Len(t, transfersOf(t, app, contact.ID), 1)
}

func TestTransferToQueue_OutsideBusinessHoursCreatesNothingAndSendsNothing(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	withHours(t, app, org.ID, account.Name, closedAllWeek(), "Estamos fechados.")

	res, err := app.TransferToQueue(context.Background(), account, contact, models.TransferSourceAIConfirmed, "")
	require.NoError(t, err)
	assert.Equal(t, TransferOutsideHours, res.Outcome)
	assert.Empty(t, transfersOf(t, app, contact.ID))
	assert.Empty(t, outgoingTexts(t, app, contact.ID), "the caller decides what to say, not the service")
}

// The legacy entry point keeps behaving exactly as before.
func TestCreateTransferToQueue_WrapperKeepsTheLegacyBehaviour(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)

	// outside hours: the out-of-hours message is sent and nothing is created
	closed := testutil.CreateTestContact(t, app.DB, org.ID)
	withHours(t, app, org.ID, account.Name, closedAllWeek(), "Estamos fechados.")
	app.createTransferToQueue(account, closed, models.TransferSourceChatbotDisabled)
	assert.Empty(t, transfersOf(t, app, closed.ID))
	assert.Equal(t, []string{"Estamos fechados."}, outgoingTexts(t, app, closed.ID))
}

func TestCreateTransferToQueue_WrapperCreatesInHoursAndIsSilent(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	withHours(t, app, org.ID, account.Name, openAllWeek(), "Estamos fechados.")
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	app.createTransferToQueue(account, contact, models.TransferSourceChatbotDisabled)
	rows := transfersOf(t, app, contact.ID)
	require.Len(t, rows, 1)
	assert.Equal(t, models.TransferSourceChatbotDisabled, rows[0].Source)
	assert.Empty(t, rows[0].Notes)
	assert.Empty(t, outgoingTexts(t, app, contact.ID))

	// a second call is a no-op, as before
	app.createTransferToQueue(account, contact, models.TransferSourceChatbotDisabled)
	assert.Len(t, transfersOf(t, app, contact.ID), 1)
}

func TestCreateTransferToQueue_WrapperWithoutBusinessHoursJustCreates(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	app.createTransferToQueue(account, contact, models.TransferSourceKeyword)
	assert.Len(t, transfersOf(t, app, contact.ID), 1)
}
