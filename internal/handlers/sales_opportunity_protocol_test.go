package handlers_test

import (
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSalesOpportunityProtocol_Format(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	day := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	require.NoError(t, app.DB.Create(&models.SalesOpportunityCounter{
		OrganizationID: org.ID, Day: "20260917", LastSeq: 122,
	}).Error)

	got, err := app.NextOpportunityNumberForTest(org.ID, day)
	require.NoError(t, err)
	assert.Equal(t, "OPP-20260917-000123", got)
}

func TestSalesOpportunityProtocol_ResetsOnNewDay(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.DB.Create(&models.SalesOpportunityCounter{
		OrganizationID: org.ID, Day: "20260916", LastSeq: 457,
	}).Error)

	got, err := app.NextOpportunityNumberForTest(org.ID, time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, "OPP-20260917-000001", got)
}

func TestSalesOpportunityProtocol_TwoOrgsCanShareTheSameNumberSameDay(t *testing.T) {
	app := newTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	day := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)

	gotA, err := app.NextOpportunityNumberForTest(orgA.ID, day)
	require.NoError(t, err)
	gotB, err := app.NextOpportunityNumberForTest(orgB.ID, day)
	require.NoError(t, err)
	assert.Equal(t, gotA, gotB)
	assert.Equal(t, "OPP-20260917-000001", gotA)
}

// GATE. Same reasoning as TestOccurrenceProtocol_UniqueUnderConcurrency — a
// COUNT(*)+1 implementation passes a serial test and collides under load.
func TestSalesOpportunityProtocol_UniqueUnderConcurrency(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	day := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)

	const n = 30
	var wg sync.WaitGroup
	numbers := make([]string, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			numbers[idx], errs[idx] = app.NextOpportunityNumberForTest(org.ID, day)
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		require.NoError(t, err, "chamada %d falhou", i)
		assert.False(t, seen[numbers[i]], "número duplicado: %s", numbers[i])
		seen[numbers[i]] = true
	}
}
