package aitools

import (
	"context"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// EnabledSource tells which tools an organization enabled.
type EnabledSource interface {
	EnabledTools(ctx context.Context, orgID uuid.UUID) (map[string]bool, error)
}

// SettingsStore reads and writes ai_tool_settings. Opt-in: a tool with no row is disabled.
type SettingsStore struct{ DB *gorm.DB }

var _ EnabledSource = SettingsStore{}

// EnabledTools returns the tools enabled for the organization (only that organization's rows).
func (s SettingsStore) EnabledTools(ctx context.Context, orgID uuid.UUID) (map[string]bool, error) {
	var rows []models.AIToolSetting
	if err := s.DB.WithContext(ctx).Where("organization_id = ? AND enabled = ?", orgID, true).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.ToolName] = true
	}
	return out, nil
}
