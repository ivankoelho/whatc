package calling

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/assignment"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// directAgentFixture wires a Manager to the real DB and an Assigner whose
// presence source is the returned map (agents are offline unless set true).
func directAgentFixture(t *testing.T) (*Manager, *gorm.DB, *models.Organization, map[uuid.UUID]bool) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set, skipping")
	}
	org := testutil.CreateTestOrganization(t, db)
	online := map[uuid.UUID]bool{}
	asg := assignment.New(db, rdb, testutil.NopLogger())
	asg.SetPresence(func(_, userID uuid.UUID) bool { return online[userID] })
	return &Manager{db: db, assigner: asg, log: testutil.NopLogger()}, db, org, online
}

// sessionFor builds the session shape pickDirectAgent reads: a caller whose
// contact has assignedTo as relationship manager.
func sessionFor(t *testing.T, db *gorm.DB, org *models.Organization, assignedTo uuid.UUID) *CallSession {
	t.Helper()
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(contact).Update("assigned_user_id", assignedTo).Error)
	return &CallSession{ID: "wacid-" + uuid.NewString()[:8], OrganizationID: org.ID, ContactID: contact.ID}
}

func TestPickDirectAgent_AssignedAgentOfflineIsNotRungDirectly(t *testing.T) {
	m, db, org, _ := directAgentFixture(t)
	agent := testutil.CreateTestUser(t, db, org.ID) // active, is_available=true, but not connected
	session := sessionFor(t, db, org, agent.ID)

	assert.Nil(t, m.pickDirectAgent(session),
		"an offline agent must never be rung directly: the call falls back to team rotation / broadcast")
}

func TestPickDirectAgent_AssignedAgentOnlineAndAvailableIsRung(t *testing.T) {
	m, db, org, online := directAgentFixture(t)
	agent := testutil.CreateTestUser(t, db, org.ID)
	online[agent.ID] = true
	session := sessionFor(t, db, org, agent.ID)

	got := m.pickDirectAgent(session)
	require.NotNil(t, got, "the direct path keeps working for an eligible agent")
	assert.Equal(t, agent.ID, *got)
}

func TestPickDirectAgent_AssignedAgentAwayOrInactiveIsNotRung(t *testing.T) {
	m, db, org, online := directAgentFixture(t)

	away := testutil.CreateTestUser(t, db, org.ID)
	online[away.ID] = true
	require.NoError(t, db.Model(away).Update("is_available", false).Error)
	assert.Nil(t, m.pickDirectAgent(sessionFor(t, db, org, away.ID)), "connected but away")

	inactive := testutil.CreateTestUser(t, db, org.ID)
	online[inactive.ID] = true
	require.NoError(t, db.Model(inactive).Update("is_active", false).Error)
	assert.Nil(t, m.pickDirectAgent(sessionFor(t, db, org, inactive.ID)), "inactive user")
}

func TestPickDirectAgent_AssignedAgentOnAnotherCallIsStillSkipped(t *testing.T) {
	m, db, org, online := directAgentFixture(t)
	agent := testutil.CreateTestUser(t, db, org.ID)
	online[agent.ID] = true
	// Eligible by the shared rule, but busy on a connected call transfer: the
	// call-specific condition still applies on top of eligibility.
	busyContact := testutil.CreateTestContact(t, db, org.ID)
	callLog := &models.CallLog{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: busyContact.ID,
		Status: models.CallStatusAnswered, CallerPhone: busyContact.PhoneNumber}
	require.NoError(t, db.Create(callLog).Error)
	require.NoError(t, db.Create(&models.CallTransfer{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, CallLogID: callLog.ID, ContactID: busyContact.ID,
		WhatsAppCallID: "busy-" + uuid.NewString()[:6], CallerPhone: "1", WhatsAppAccount: "acc",
		Status: models.CallTransferStatusConnected, AgentID: &agent.ID,
	}).Error)

	assert.Nil(t, m.pickDirectAgent(sessionFor(t, db, org, agent.ID)))
}

func TestPickDirectAgent_NoAssignedAgentOrNoAssignerFallsBack(t *testing.T) {
	m, db, org, online := directAgentFixture(t)
	contact := testutil.CreateTestContact(t, db, org.ID) // no relationship manager
	assert.Nil(t, m.pickDirectAgent(&CallSession{ID: "x", OrganizationID: org.ID, ContactID: contact.ID}))

	agent := testutil.CreateTestUser(t, db, org.ID)
	online[agent.ID] = true
	session := sessionFor(t, db, org, agent.ID)
	m.assigner = nil // no Assigner wired: nobody counts as eligible, the call falls back
	assert.Nil(t, m.pickDirectAgent(session))
}

func TestPickDirectAgent_StickyAgentStillTakesPrecedenceOverAssignedAgent(t *testing.T) {
	m, db, org, online := directAgentFixture(t)
	sticky := testutil.CreateTestUser(t, db, org.ID)
	online[sticky.ID] = true
	assigned := testutil.CreateTestUser(t, db, org.ID) // offline
	session := sessionFor(t, db, org, assigned.ID)
	session.StickyAgentID = &sticky.ID // validated by the webhook with the same Assigner rule

	got := m.pickDirectAgent(session)
	require.NotNil(t, got)
	assert.Equal(t, sticky.ID, *got)
}
