package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateOfPhone(t *testing.T) {
	cases := []struct {
		phone, want string
	}{
		{"5511987654321", "SP"},       // mobile, 9 digits
		{"+55 (21) 98765-4321", "RJ"}, // formatting is ignored
		{"551133334444", "SP"},        // landline
		{"5561988887777", "DF"},
		{"5592988887777", "AM"},
		{"5551988887777", "RS"},
		{"5598988887777", "MA"},
		{"14155550123", regionOther},   // US
		{"5500987654321", regionOther}, // DDD 00 does not exist
		{"5520987654321", regionOther}, // DDD 20 is not in use
		{"551198765432", "SP"},         // 8 digits starting 6-9: mobile from before the ninth digit
		{"55119876543", regionOther},   // too short
		{"", regionOther},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, stateOfPhone(c.phone), c.phone)
	}
}

// Every DDD in the table maps to one of the 27 federative units, and the 27 are
// all reachable: a typo in the table would otherwise silently lose a state.
func TestBrazilStateByDDD_CoversAll27States(t *testing.T) {
	seen := map[string]bool{}
	for ddd, uf := range brazilStateByDDD {
		assert.Len(t, ddd, 2, ddd)
		assert.Len(t, uf, 2, ddd)
		seen[uf] = true
	}
	assert.Len(t, seen, 27)
}

func TestGetContactsByRegion_GroupsByStateFromThePhone(t *testing.T) {
	app := newProcessorTestApp(t)
	org, _ := createProcessorTestOrg(t, app)
	other, _ := createProcessorTestOrg(t, app)
	now := time.Now()
	start, end := now.Add(-24*time.Hour), now.Add(time.Hour)

	add := func(orgID uuid.UUID, phone string, lastMsg time.Time) *models.Contact {
		t.Helper()
		c := testutil.CreateTestContactWith(t, app.DB, orgID, testutil.WithPhoneNumber(phone))
		require.NoError(t, app.DB.Model(&models.Contact{}).Where("id = ?", c.ID).Update("last_message_at", lastMsg).Error)
		return c
	}
	add(org.ID, "5511987654321", now)
	add(org.ID, "5511912345678", now)
	add(org.ID, "5521987654321", now)
	add(org.ID, "14155550123", now)                      // another country
	add(org.ID, "5511955554444", now.Add(-72*time.Hour)) // outside the period
	gone := add(org.ID, "5511966667777", now)            // soft-deleted
	require.NoError(t, app.DB.Delete(gone).Error)
	add(other.ID, "5511988889999", now) // another organization

	got := app.getContactsByRegion(org.ID, nil, start, end)

	counts := map[string]float64{}
	for _, p := range got {
		counts[p.Label] = p.Value
	}
	assert.Equal(t, map[string]float64{"SP": 2, "RJ": 1, regionOther: 1}, counts)
	require.NotEmpty(t, got)
	assert.Equal(t, "SP", got[0].Label, "ordered by volume, largest first")
}

// The grouped widget path (what the dashboard calls) routes "region" to the
// phone-derived query instead of a nonexistent column.
func TestGetGroupedData_RegionForContacts(t *testing.T) {
	app := newProcessorTestApp(t)
	org, _ := createProcessorTestOrg(t, app)
	now := time.Now()
	c := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithPhoneNumber("5585988887777"))
	require.NoError(t, app.DB.Model(&models.Contact{}).Where("id = ?", c.ID).Update("last_message_at", now).Error)

	w := models.Widget{DataSource: "contacts", GroupByField: "region", DisplayType: "chart", ChartType: "map"}
	got := app.getGroupedData(org.ID, uuid.Nil, w, nil, now.Add(-time.Hour), now.Add(time.Hour))
	require.Len(t, got, 1)
	assert.Equal(t, "CE", got[0].Label)
	assert.EqualValues(t, 1, got[0].Value)

	assert.Contains(t, widgetDataSources["contacts"], "region")
}
