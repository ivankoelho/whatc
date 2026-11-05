package database

import (
	"fmt"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// BackfillResolveUncertainSendPermission gives occurrences:resolve_send to the roles that are meant to settle a
// customer send whose delivery is unknown: the system roles admin and manager and the presets "Supervisor de
// Loja" and "Supervisor de SAC". SeedSystemRolesForOrg skips an organization that already has system roles, so a
// new permission never reaches an existing installation that way. No other role receives it (SAC, Vendas,
// Logística, Jurídico, agent and custom roles keep their permissions exactly as they were).
//
// EXACT behavior (the guard is the shape of the data, like the other backfills; there is no stored marker):
//   - Purely additive: it never revokes anything.
//   - An organization is skipped when its admin or manager SYSTEM role already holds the permission. That is the
//     state of a new organization (seeded with it) and of one this backfill already served.
//   - So it is idempotent, and a second run changes nothing.
//   - Otherwise it grants the four roles above (those that exist and are not deleted). This also covers a preset
//     that the seed recreated in the same boot (and so already carries the permission): admin and manager are
//     still granted.
//   - What an administrator does afterwards is respected only while admin or manager keep it. If it is removed from
//     BOTH admin and manager, the next -migrate grants it again (the data cannot tell "never granted" from
//     "removed on purpose"). Removing it from a supervisor preset alone, or from a custom role, is never undone.
//   - Two boots running at once cannot fail on a duplicate link (ON CONFLICT DO NOTHING).
func BackfillResolveUncertainSendPermission(db *gorm.DB, lo logf.Logger) error {
	var seeded int64
	if err := db.Model(&models.Permission{}).Where("resource = ? AND action = ?", models.ResourceOccurrences, models.ActionResolveSend).Count(&seeded).Error; err != nil {
		return fmt.Errorf("failed to look up occurrences:resolve_send: %w", err)
	}
	if seeded == 0 {
		lo.Warn("Resolve uncertain send backfill: permission not seeded yet, did nothing")
		return nil
	}
	res := db.Exec(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT r.id, target.id
		FROM custom_roles r
		JOIN permissions target ON target.resource = 'occurrences' AND target.action = 'resolve_send'
		WHERE r.deleted_at IS NULL
		  AND ((r.is_system = TRUE AND r.name IN ('admin', 'manager')) OR r.name IN ('Supervisor de Loja', 'Supervisor de SAC'))
		  AND NOT EXISTS (
			SELECT 1 FROM custom_roles r2
			JOIN role_permissions rp2 ON rp2.custom_role_id = r2.id
			JOIN permissions p2 ON p2.id = rp2.permission_id
			WHERE r2.organization_id = r.organization_id AND r2.deleted_at IS NULL
			  AND r2.is_system = TRUE AND r2.name IN ('admin', 'manager')
			  AND p2.resource = 'occurrences' AND p2.action = 'resolve_send')
		ON CONFLICT DO NOTHING`)
	if res.Error != nil {
		return fmt.Errorf("failed to backfill occurrences:resolve_send: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		lo.Info("Resolve uncertain send backfill complete", "roles_granted", res.RowsAffected)
	}
	return nil
}
