package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Legacy rows (no general_state) are classified by the stage flags, and a
// re-run never overwrites a classification an admin already changed.
func TestMigration_GeneralStateBackfillIsAdditiveAndIdempotent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opener := testutil.CreateTestUser(t, app.DB, org.ID)

	mk := func(name string, init, closing bool) models.OccurrenceStage {
		s := models.OccurrenceStage{OrganizationID: org.ID, Name: name, IsInitial: init, IsClosing: closing}
		require.NoError(t, app.DB.Create(&s).Error)
		return s
	}
	open := mk("Aberto", true, false)
	mid := mk("Em análise", false, false)
	wait := mk("Aguardando cliente", false, false)
	done := mk("Resolvido", false, true)

	occ := models.Occurrence{OrganizationID: org.ID, ContactID: contact.ID, Title: "x",
		StageID: wait.ID, OpenedByUserID: opener.ID, ProtocolNumber: "T-" + org.ID.String()[:8]}
	require.NoError(t, app.DB.Create(&occ).Error)

	require.NoError(t, database.CreateIndexes(app.DB))
	state := func(id any) string {
		var s string
		require.NoError(t, app.DB.Raw("SELECT general_state FROM occurrence_stages WHERE id = ?", id).Scan(&s).Error)
		return s
	}
	assert.Equal(t, models.GeneralStateNew, state(open.ID))
	assert.Equal(t, models.GeneralStateInProgress, state(mid.ID))
	assert.Equal(t, models.GeneralStateWaitingCustomer, state(wait.ID))
	assert.Equal(t, models.GeneralStateResolved, state(done.ID))

	var got models.Occurrence
	require.NoError(t, app.DB.First(&got, occ.ID).Error)
	assert.Equal(t, models.GeneralStateWaitingCustomer, got.GeneralState)

	// An admin reclassifies; a second run must keep it.
	require.NoError(t, app.DB.Model(&models.OccurrenceStage{}).Where("id = ?", mid.ID).
		Update("general_state", models.GeneralStateWaitingCustomer).Error)
	require.NoError(t, database.CreateIndexes(app.DB))
	assert.Equal(t, models.GeneralStateWaitingCustomer, state(mid.ID))
}
