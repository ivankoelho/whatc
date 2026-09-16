package models

import "github.com/google/uuid"

// BrandingSettingsSingletonID is the fixed, well-known primary key of the
// one and only branding_settings row. There is no per-request get-or-create:
// the row is seeded once at migrate time (see database.EnsureBrandingSettingsRow),
// so every read/write always addresses this exact ID — never "find or create".
var BrandingSettingsSingletonID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// BrandingSettings is a system-wide (not per-organization) singleton — there
// is no organization selector on the login page, so this cannot be scoped to
// a tenant the way ChatbotSettings is. Both fields are empty when no custom
// background is configured, and the login page falls back to its default
// gradient.
type BrandingSettings struct {
	BaseModel
	LoginBackgroundPath        string `gorm:"size:500" json:"login_background_path,omitempty"`
	LoginBackgroundContentType string `gorm:"size:100" json:"login_background_content_type,omitempty"`

	// Login page footer: copyright/rights notice and the system version
	// string, both free text set by a super admin — this codebase has no
	// build-time version stamping, so the version is whatever they type.
	FooterText    string `gorm:"size:255" json:"footer_text,omitempty"`
	FooterVersion string `gorm:"size:50" json:"footer_version,omitempty"`
}

func (BrandingSettings) TableName() string { return "branding_settings" }
