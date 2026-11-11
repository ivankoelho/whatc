package database

import (
	"fmt"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// BackfillChatTriagePermission gives chat:triage (take a "new" conversation into attendance by status) to the
// roles that are meant to do the first triage: the system roles admin and manager and the preset "SAC".
// SeedSystemRolesForOrg skips an organization that already has system roles, and the team presets are created
// once and never overwritten, so a new permission never reaches an existing installation any other way. No other
// role receives it (agent, Vendas, Logística, Jurídico, the supervisors and custom roles keep their permissions
// exactly as they were: the supervisors already change any status through contacts:write).
//
// EXACT behavior (same shape as the other backfills: the guard is the data, there is no stored marker):
//   - Purely additive: it never revokes anything.
//   - An organization is skipped when its admin or manager SYSTEM role already holds the permission. That is
//     the state of a new organization (seeded with it) and of one this backfill already served.
//   - So it is idempotent, and a second run changes nothing.
//   - Otherwise it grants the three roles above (those that exist and are not deleted). This also covers a SAC
//     preset that the seed recreated in the same boot (and so already carries the permission).
//   - What an administrator does afterwards is respected only while admin or manager keep it. Removing it from the
//     SAC role alone, or from a custom role, is never undone. If it is removed from BOTH admin and manager, the
//     next -migrate grants it again (the data cannot tell "never granted" from "removed on purpose").
//   - Two boots running at once cannot fail on a duplicate link (ON CONFLICT DO NOTHING).
func BackfillChatTriagePermission(db *gorm.DB, lo logf.Logger) error {
	var seeded int64
	if err := db.Model(&models.Permission{}).Where("resource = ? AND action = ?", models.ResourceChat, models.ActionTriage).Count(&seeded).Error; err != nil {
		return fmt.Errorf("failed to look up chat:triage: %w", err)
	}
	if seeded == 0 {
		return fmt.Errorf("the permission chat:triage is not seeded: run the permission seed before this backfill")
	}
	res := db.Exec(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT r.id, target.id
		FROM custom_roles r
		JOIN permissions target ON target.resource = 'chat' AND target.action = 'triage'
		WHERE r.deleted_at IS NULL
		  AND ((r.is_system = TRUE AND r.name IN ('admin', 'manager')) OR r.name = 'SAC')
		  AND NOT EXISTS (
			SELECT 1 FROM custom_roles r2
			JOIN role_permissions rp2 ON rp2.custom_role_id = r2.id
			JOIN permissions p2 ON p2.id = rp2.permission_id
			WHERE r2.organization_id = r.organization_id AND r2.deleted_at IS NULL
			  AND r2.is_system = TRUE AND r2.name IN ('admin', 'manager')
			  AND p2.resource = 'chat' AND p2.action = 'triage')
		ON CONFLICT DO NOTHING`)
	if res.Error != nil {
		return fmt.Errorf("failed to backfill chat:triage: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		lo.Info("Chat triage permission backfill complete", "roles_granted", res.RowsAffected)
	}
	return nil
}
