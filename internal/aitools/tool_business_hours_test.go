package aitools_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Monday 2026-10-05 at hhmm.
func monday(hhmm string) func() time.Time {
	t, _ := time.Parse("2006-01-02 15:04", "2026-10-05 "+hhmm)
	return func() time.Time { return t }
}

func hoursDay(d int, enabled bool, start, end string) map[string]any {
	return map[string]any{"day": float64(d), "enabled": enabled, "start_time": start, "end_time": end}
}

func putSettings(t *testing.T, db *gorm.DB, org uuid.UUID, account string, enabled bool, hours models.JSONBArray, msg string) {
	t.Helper()
	s := models.ChatbotSettings{OrganizationID: org, WhatsAppAccount: account}
	s.BusinessHours = models.BusinessHoursConfig{Enabled: enabled, Hours: hours, OutOfHoursMessage: msg}
	require.NoError(t, db.Create(&s).Error)
}

func runHours(t *testing.T, db *gorm.DB, org uuid.UUID, now func() time.Time, args string) ai.ToolResult {
	t.Helper()
	contact := uuid.New()
	spec := aitools.NewBusinessHoursSpec(now)
	tool := spec.Factory(aitools.Scope{OrganizationID: org, ContactID: &contact}, aitools.Deps{Read: aitools.NewReadDB(db)})
	res, err := tool.Execute(context.Background(), ai.ToolCall{ID: "c", Name: "get_business_hours", Arguments: json.RawMessage(args)})
	require.NoError(t, err)
	return res
}

func TestBusinessHoursSpec_IsAReadToolWithNoArguments(t *testing.T) {
	spec := aitools.NewBusinessHoursSpec(time.Now)
	assert.Equal(t, "get_business_hours", spec.Name)
	assert.Equal(t, aitools.RiskRead, spec.Risk)
	require.NoError(t, spec.Definition().Validate())
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(spec.Parameters))
	_, err := aitools.NewCatalog(spec)
	assert.NoError(t, err, "the schema stays inside what every provider adapter accepts")
}

func TestBusinessHours_ReturnsTheScheduleAndOpenNow(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	putSettings(t, db, org.ID, "", true, models.JSONBArray{hoursDay(1, true, "08:00", "18:00"), hoursDay(2, false, "08:00", "18:00")},
		"We are closed, call SENTINEL-OOH-MESSAGE")

	res := runHours(t, db, org.ID, monday("12:00"), `{}`)
	assert.False(t, res.IsError)
	assert.JSONEq(t, `{"configured":true,"open_now":true,"server_time":"12:00","days":[
		{"day":"sunday","open":false},{"day":"monday","open":true,"start":"08:00","end":"18:00"},{"day":"tuesday","open":false},
		{"day":"wednesday","open":false},{"day":"thursday","open":false},{"day":"friday","open":false},{"day":"saturday","open":false}]}`, res.Content)
	assert.NotContains(t, res.Content, "SENTINEL-OOH-MESSAGE", "no free text from the organization's settings")

	res = runHours(t, db, org.ID, monday("19:30"), ``)
	var out struct {
		OpenNow    bool   `json:"open_now"`
		ServerTime string `json:"server_time"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content), &out))
	assert.False(t, out.OpenNow)
	assert.Equal(t, "19:30", out.ServerTime, "the server clock is reported so the model does not guess")
}

func TestBusinessHours_NotConfiguredWhenOffOrEmptyOrMissing(t *testing.T) {
	db := testutil.SetupTestDB(t)
	missing := testutil.CreateTestOrganization(t, db)
	off := testutil.CreateTestOrganization(t, db)
	empty := testutil.CreateTestOrganization(t, db)
	putSettings(t, db, off.ID, "", false, models.JSONBArray{hoursDay(1, true, "08:00", "18:00")}, "")
	putSettings(t, db, empty.ID, "", true, models.JSONBArray{}, "")

	for name, org := range map[string]uuid.UUID{"no settings": missing.ID, "disabled": off.ID, "no hours": empty.ID} {
		res := runHours(t, db, org, monday("12:00"), `{}`)
		assert.False(t, res.IsError, name)
		assert.JSONEq(t, `{"configured":false}`, res.Content, name)
	}
}

func TestBusinessHours_OnlyTheOrganizationsDefaultSettingsOfTheCallersOrganization(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a, b := testutil.CreateTestOrganization(t, db), testutil.CreateTestOrganization(t, db)
	putSettings(t, db, a.ID, "", true, models.JSONBArray{hoursDay(1, true, "08:00", "18:00")}, "")
	putSettings(t, db, a.ID, "some-account", true, models.JSONBArray{hoursDay(1, true, "00:00", "23:59")}, "") // a per-account row
	putSettings(t, db, b.ID, "", true, models.JSONBArray{hoursDay(1, true, "10:00", "11:00")}, "")

	resA := runHours(t, db, a.ID, monday("12:00"), `{}`)
	assert.Contains(t, resA.Content, `"start":"08:00","end":"18:00"`, "A sees its own default row, not the per-account one")
	assert.Contains(t, resA.Content, `"open_now":true`)
	resB := runHours(t, db, b.ID, monday("12:00"), `{}`)
	assert.Contains(t, resB.Content, `"start":"10:00","end":"11:00"`)
	assert.Contains(t, resB.Content, `"open_now":false`, "B's hours are B's")
}

func TestBusinessHours_ArgumentsAreRefused(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	putSettings(t, db, org.ID, "", true, models.JSONBArray{hoursDay(1, true, "08:00", "18:00")}, "")
	for _, args := range []string{`{"organization_id":"` + uuid.NewString() + `"}`, `{"unit":"x"}`, `[1]`} {
		res := runHours(t, db, org.ID, monday("12:00"), args)
		assert.True(t, res.IsError, args)
		assert.Equal(t, "error: invalid arguments", res.Content, args)
	}
}
