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

// awayEnv is an organization with one agent who owns an active attendance.
type awayEnv struct {
	app      *handlers.App
	org      *models.Organization
	agent    *models.User
	contact  *models.Contact
	team     *models.Team
	transfer *models.AgentTransfer
	reaper   *handlers.PresenceReaper
}

// newAwayEnv creates the organization. tolerance < 0 leaves it without a
// settings row (an organization that never configured anything); tolerance 0
// has a row with the feature off; > 0 turns it on with that many minutes.
func newAwayEnv(t *testing.T, tolerance int) awayEnv {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	team := createTestTeam(t, app, org.ID, agent.ID)

	if tolerance >= 0 {
		upsertChatbotSettings(t, app, org.ID, true)
		setAwayTolerance(t, app, org.ID, tolerance > 0, tolerance)
	}

	tr := createTestTransfer(t, app, org.ID, contact.ID, account.Name, models.TransferStatusActive, &agent.ID)
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).Where("id = ?", tr.ID).Update("team_id", team.ID).Error)

	return awayEnv{app: app, org: org, agent: agent, contact: contact, team: team, transfer: tr,
		reaper: handlers.NewPresenceReaper(app, handlers.DefaultPresenceGrace, time.Hour)}
}

func setAwayTolerance(t *testing.T, app *handlers.App, orgID uuid.UUID, enabled bool, mins int) {
	t.Helper()
	updates := map[string]any{"away_reassign_enabled": enabled}
	if mins > 0 {
		updates["away_reassign_after_mins"] = mins
	}
	require.NoError(t, app.DB.Model(&models.ChatbotSettings{}).
		Where("organization_id = ? AND whats_app_account = ?", orgID, "").Updates(updates).Error)
	app.InvalidateChatbotSettingsCache(orgID)
}

// goAway marks the agent away through the real endpoint and returns how many
// attendances the endpoint reported returning to the queue.
func (e awayEnv) goAway(t *testing.T) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"is_available": false})
	testutil.SetAuthContext(req, e.org.ID, e.agent.ID)
	require.NoError(t, e.app.UpdateAvailability(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var resp struct {
		Data struct {
			Returned int `json:"transfers_to_queue"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data.Returned
}

// backdateAway pretends the agent marked away `ago` ago.
func (e awayEnv) backdateAway(t *testing.T, ago time.Duration) {
	t.Helper()
	res := e.app.DB.Model(&models.UserAvailabilityLog{}).
		Where("user_id = ? AND is_available = false AND ended_at IS NULL", e.agent.ID).
		Update("started_at", time.Now().Add(-ago))
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected, "the agent has an open away interval")
}

func (e awayEnv) owner(t *testing.T) *uuid.UUID {
	t.Helper()
	var tr models.AgentTransfer
	require.NoError(t, e.app.DB.First(&tr, "id = ?", e.transfer.ID).Error)
	return tr.AgentID
}

// customerWrites makes the customer the one waiting: their message is the latest.
func (e awayEnv) customerWrites(t *testing.T) {
	t.Helper()
	createTestMessage(t, e.app, e.org.ID, e.contact.ID, models.DirectionIncoming, time.Now())
}

func (e awayEnv) pinCarteira(t *testing.T) {
	t.Helper()
	require.NoError(t, e.app.DB.Model(&models.Contact{}).Where("id = ?", e.contact.ID).
		Update("assigned_user_id", e.agent.ID).Error)
}

// Without the opt-in (no settings row, or a row that never turned it on) going
// away returns the attendances to the queue at once, exactly as it always did.
func TestAwayTolerance_OffKeepsTheOriginalBehaviour(t *testing.T) {
	for name, tolerance := range map[string]int{"no settings row": -1, "row with the feature off": 0} {
		t.Run(name, func(t *testing.T) {
			e := newAwayEnv(t, tolerance)
			e.pinCarteira(t)

			returned := e.goAway(t)

			assert.Equal(t, 1, returned)
			assert.Nil(t, e.owner(t), "back in the queue immediately")
			assert.Nil(t, readContactAssignedUser(t, e.app, e.contact.ID), "the original behaviour also released the carteira")
		})
	}
}

// With the tolerance on, going away takes nothing from the agent.
func TestAwayTolerance_OnKeepsAttendancesWhileAway(t *testing.T) {
	e := newAwayEnv(t, 10)
	e.pinCarteira(t)

	returned := e.goAway(t)

	assert.Equal(t, 0, returned)
	require.NotNil(t, e.owner(t))
	assert.Equal(t, e.agent.ID, *e.owner(t))
	assert.Equal(t, 0, e.reaper.SweepAwayOrganization(e.org.ID, time.Now()), "nothing is due right after going away")
	require.NotNil(t, e.owner(t))
}

// The deadline is the organization's: a minute short keeps the attendance, a
// minute past releases it to the same team, with its own audit reason, and the
// release happens only once.
func TestAwayTolerance_SweepReleasesOnlyAfterTheDeadline(t *testing.T) {
	e := newAwayEnv(t, 10)
	e.goAway(t)
	e.customerWrites(t)

	e.backdateAway(t, 9*time.Minute)
	assert.Equal(t, 0, e.reaper.SweepAwayOrganization(e.org.ID, time.Now()), "9 of 10 minutes: not yet")
	require.NotNil(t, e.owner(t))

	e.backdateAway(t, 11*time.Minute)
	assert.Equal(t, 1, e.reaper.SweepAwayOrganization(e.org.ID, time.Now()), "11 of 10 minutes: released")
	assert.Nil(t, e.owner(t))

	var tr models.AgentTransfer
	require.NoError(t, e.app.DB.First(&tr, "id = ?", e.transfer.ID).Error)
	require.NotNil(t, tr.TeamID)
	assert.Equal(t, e.team.ID, *tr.TeamID, "it waits in the same team's queue")
	assert.Equal(t, models.TransferStatusActive, tr.Status)

	entry := distributionAudit(t, e.app, e.transfer.ID, "returned_away_timeout")
	assert.Equal(t, e.agent.ID.String(), entry["_old_agent"], "the audit keeps who it was taken from")

	assert.Equal(t, 0, e.reaper.SweepAwayOrganization(e.org.ID, time.Now()), "a second sweep releases nothing more")
}

// The relationship (carteira) survives the timeout: the conversation is being
// redistributed, not taken from the agent for good.
func TestAwayTolerance_TimeoutKeepsTheCarteira(t *testing.T) {
	e := newAwayEnv(t, 10)
	e.pinCarteira(t)
	e.goAway(t)
	e.customerWrites(t)
	e.backdateAway(t, 15*time.Minute)

	require.Equal(t, 1, e.reaper.SweepAwayOrganization(e.org.ID, time.Now()))

	got := readContactAssignedUser(t, e.app, e.contact.ID)
	require.NotNil(t, got)
	assert.Equal(t, e.agent.ID, *got)
}

// An agent who comes back before the deadline loses nothing, however long the
// sweep runs afterwards.
func TestAwayTolerance_ReturningBeforeTheDeadlineKeepsEverything(t *testing.T) {
	e := newAwayEnv(t, 10)
	e.goAway(t)
	e.backdateAway(t, 9*time.Minute)

	setAway(t, e.app, e.org.ID, e.agent.ID, false) // back

	assert.Equal(t, 0, e.reaper.SweepAwayOrganization(e.org.ID, time.Now().Add(time.Hour)))
	require.NotNil(t, e.owner(t))
	assert.Equal(t, e.agent.ID, *e.owner(t))
}

// Only organizations that opted in are swept, and only the away agent's own
// attendances are released.
func TestAwayTolerance_OnlyOptedInOrganizationsAndTheAwayAgent(t *testing.T) {
	on := newAwayEnv(t, 10)
	off := newAwayEnv(t, 0)
	// A second agent of the same organization who stays available.
	other := createTestAgent(t, on.app, on.org.ID)
	otherContact := testutil.CreateTestContact(t, on.app.DB, on.org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, on.app.DB, on.org.ID)
	otherTransfer := createTestTransfer(t, on.app, on.org.ID, otherContact.ID, account.Name, models.TransferStatusActive, &other.ID)

	on.goAway(t)
	on.customerWrites(t)
	on.backdateAway(t, time.Hour)
	// The agent of the organization that did not opt in is away for a long time too
	// (the original behaviour already returned their attendance when they left).
	require.NoError(t, off.app.DB.Create(&models.UserAvailabilityLog{
		UserID: off.agent.ID, OrganizationID: off.org.ID, IsAvailable: false, StartedAt: time.Now().Add(-time.Hour),
	}).Error)
	require.NoError(t, off.app.DB.Model(&models.User{}).Where("id = ?", off.agent.ID).Update("is_available", false).Error)

	assert.Equal(t, 0, off.reaper.SweepAwayOrganization(off.org.ID, time.Now()), "an organization that did not opt in is never swept")
	on.reaper.SweepAway(time.Now()) // the global sweep walks the opted-in organizations

	require.Nil(t, on.owner(t), "the opted-in organization's away agent is released")
	var kept models.AgentTransfer
	require.NoError(t, on.app.DB.First(&kept, "id = ?", otherTransfer.ID).Error)
	require.NotNil(t, kept.AgentID, "the agent who stayed available keeps theirs")
	assert.Equal(t, other.ID, *kept.AgentID)
	var offTr models.AgentTransfer
	require.NoError(t, off.app.DB.First(&offTr, "id = ?", off.transfer.ID).Error)
	require.NotNil(t, offTr.AgentID, "an organization that did not opt in is never swept")
}

// Turning the tolerance on counts from when the agent actually marked away, so
// someone who has been away for a long time is released on the first sweep.
func TestAwayTolerance_CountsFromWhenTheAgentMarkedAway(t *testing.T) {
	e := newAwayEnv(t, 0) // feature off while the agent goes away
	require.Equal(t, 1, e.goAway(t))
	// The attendance was returned at once; give it back to the agent to model one
	// that was assigned afterwards, then turn the feature on.
	require.NoError(t, e.app.DB.Model(&models.AgentTransfer{}).Where("id = ?", e.transfer.ID).
		Update("agent_id", e.agent.ID).Error)
	e.customerWrites(t)
	e.backdateAway(t, 30*time.Minute)
	setAwayTolerance(t, e.app, e.org.ID, true, 10)

	assert.Equal(t, 1, e.reaper.SweepAwayOrganization(e.org.ID, time.Now()))
}

func getAwaySettings(t *testing.T, app *handlers.App, orgID, userID uuid.UUID) handlers.ChatbotSettingsResponse {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.GetChatbotSettings(req))
	var resp struct {
		Data struct {
			Settings handlers.ChatbotSettingsResponse `json:"settings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data.Settings
}

func putAwaySettings(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.UpdateChatbotSettings(req))
	return testutil.GetResponseStatusCode(req)
}

// The configuration: off and ten minutes by default, validated, persisted, and
// untouched by an unrelated update.
func TestAwayTolerance_SettingsAPI(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)

	def := getAwaySettings(t, app, org.ID, user.ID)
	assert.False(t, def.AwayReassignEnabled, "off by default")
	assert.Equal(t, 10, def.AwayReassignAfterMins)

	for _, bad := range []int{0, -5, 1441} {
		assert.Equal(t, fasthttp.StatusBadRequest,
			putAwaySettings(t, app, org.ID, user.ID, map[string]any{"away_reassign_after_mins": bad}), "minutes=%d", bad)
	}
	assert.False(t, getAwaySettings(t, app, org.ID, user.ID).AwayReassignEnabled, "a rejected request changes nothing")

	require.Equal(t, fasthttp.StatusOK, putAwaySettings(t, app, org.ID, user.ID,
		map[string]any{"away_reassign_enabled": true, "away_reassign_after_mins": 25}))
	got := getAwaySettings(t, app, org.ID, user.ID)
	assert.True(t, got.AwayReassignEnabled)
	assert.Equal(t, 25, got.AwayReassignAfterMins)

	require.Equal(t, fasthttp.StatusOK, putAwaySettings(t, app, org.ID, user.ID, map[string]any{"greeting_message": "Oi"}))
	got = getAwaySettings(t, app, org.ID, user.ID)
	assert.True(t, got.AwayReassignEnabled, "an unrelated update keeps the tolerance")
	assert.Equal(t, 25, got.AwayReassignAfterMins)

	require.Equal(t, fasthttp.StatusOK, putAwaySettings(t, app, org.ID, user.ID, map[string]any{"away_reassign_enabled": false}))
	assert.False(t, getAwaySettings(t, app, org.ID, user.ID).AwayReassignEnabled)
}

// A settings row created without the new columns (as every existing
// organization's is after the migration) comes out off, with ten minutes.
func TestAwayTolerance_MigratedRowsStartOff(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{OrganizationID: org.ID}).Error)

	var row models.ChatbotSettings
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&row).Error)
	assert.False(t, row.AgentAssignment.AwayReassignEnabled)
	assert.Equal(t, 10, row.AgentAssignment.AwayReassignAfterMins)
}
