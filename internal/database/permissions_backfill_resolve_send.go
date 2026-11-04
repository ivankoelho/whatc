package database

import (
	"fmt"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// BackfillResolveUncertainSendPermission gives occurrences:resolve_send to the roles that are meant
// to settle a customer send whose delivery is unknown: the system roles admin and manager and the supervisor
// presets. SeedSystemRolesForOrg skips an organization that already has system roles, so a new permission never
// reaches an existing installation that way.
//
// Purely additive, and once per organization: an organization where any role already has the permission is
// skipped, so an administrator who later takes it away from a role is not overruled at the next start. SAC,
// Vendas, Logística, Jurídico and agent roles never receive it.
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
			  AND p2.resource = 'occurrences' AND p2.action = 'resolve_send')`)
	if res.Error != nil {
		return fmt.Errorf("failed to backfill occurrences:resolve_send: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		lo.Info("Resolve uncertain send backfill complete", "roles_granted", res.RowsAffected)
	}
	return nil
}
