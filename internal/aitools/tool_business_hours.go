package aitools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/businesshours"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// BusinessHoursToolName is the name of the organization's business-hours tool.
const BusinessHoursToolName = "get_business_hours"

var dayNames = [7]string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

type businessHoursDay struct {
	Day   string `json:"day"`
	Open  bool   `json:"open"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

type businessHoursResult struct {
	Configured bool               `json:"configured"`
	OpenNow    *bool              `json:"open_now,omitempty"`
	ServerTime string             `json:"server_time,omitempty"`
	Days       []businessHoursDay `json:"days,omitempty"`
}

// NewBusinessHoursSpec builds the get_business_hours tool. now is the clock (the server's, as for
// the chatbot's own business-hours rule); tests inject a fixed one.
//
// It reads the organization's default chatbot settings only: no argument, no contact data, no
// free text (the out-of-hours message is deliberately not included).
func NewBusinessHoursSpec(now func() time.Time) ToolSpec {
	return ToolSpec{
		Name: BusinessHoursToolName,
		Description: "Returns the organization's business hours: for each weekday whether it is open and the opening and " +
			"closing time (HH:MM), whether it is open right now, and the current server time. Times use the server's clock; " +
			"the organization has no separate time zone. Takes no arguments. Returns {\"configured\":false} when no business " +
			"hours are set up. Use it when the customer asks when the company is open or whether it is open now.",
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		Risk:       RiskRead,
		Factory: func(sc Scope, d Deps) ai.Tool {
			return businessHoursTool{scope: sc, read: d.Read, now: now}
		},
	}
}

type businessHoursTool struct {
	scope Scope
	read  ReadDB
	now   func() time.Time
}

func (t businessHoursTool) Execute(ctx context.Context, call ai.ToolCall) (ai.ToolResult, error) {
	var args struct{}
	if err := DecodeArgs(call, &args); err != nil {
		return InvalidArgsResult(), nil
	}

	var row struct {
		Enabled bool              `gorm:"column:business_hours_enabled"`
		Hours   models.JSONBArray `gorm:"column:business_hours"`
	}
	var found int64
	err := t.read.View(ctx, 0, func(tx *gorm.DB) error {
		res := tx.Table("chatbot_settings").
			Select("business_hours_enabled", "business_hours").
			Where("organization_id = ? AND whats_app_account = '' AND deleted_at IS NULL", t.scope.OrganizationID).
			Limit(1).Scan(&row)
		found = res.RowsAffected
		return res.Error
	})
	if err != nil {
		return ai.ToolResult{}, err
	}

	out := businessHoursResult{}
	// "configured" is the same condition the chatbot applies its own hours under
	if found > 0 && row.Enabled && len(row.Hours) > 0 {
		now := t.now()
		open := businesshours.Within(row.Hours, now)
		out = businessHoursResult{Configured: true, OpenNow: &open, ServerTime: now.Format("15:04")}
		for _, d := range businesshours.Schedule(row.Hours) {
			day := businessHoursDay{Day: dayNames[d.Day], Open: d.Open}
			if d.Open {
				day.Start, day.End = d.Start, d.End
			}
			out.Days = append(out.Days, day)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ai.ToolResult{}, err
	}
	return ai.ToolResult{Content: strings.TrimSpace(string(b))}, nil
}
