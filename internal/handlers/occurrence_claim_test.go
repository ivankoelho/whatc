package handlers_test

import (
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Two agents claiming the same unassigned occurrence at once: exactly one wins.
func TestOccurrences_ClaimIsExclusiveUnderConcurrency(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAgentRole(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	opener := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	team := models.Team{OrganizationID: org.ID, Name: "log-" + uuid.NewString()[:6], IsActive: true}
	require.NoError(t, app.DB.Create(&team).Error)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Atraso", StageID: stage.ID,
		OpenedByUserID: opener.ID, TeamID: &team.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	const n = 6
	codes := make([]int, n)
	users := make([]*models.User, n)
	for i := range users {
		users[i] = testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
		require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: users[i].ID}).Error)
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := testutil.NewJSONRequest(t, map[string]any{})
			testutil.SetAuthContext(req, org.ID, users[i].ID)
			testutil.SetPathParam(req, "id", occ.ID.String())
			_ = app.ClaimOccurrence(req)
			codes[i] = testutil.GetResponseStatusCode(req)
		}(i)
	}
	wg.Wait()

	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case fasthttp.StatusOK:
			ok++
		case fasthttp.StatusConflict:
			conflict++
		}
	}
	assert.Equal(t, 1, ok, "exactly one claim wins: %v", codes)
	assert.Equal(t, n-1, conflict, "the rest get 409: %v", codes)
}
