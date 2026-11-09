package database

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// Supervising cases (occurrences:view_all) and reading customers' WhatsApp
// conversations (conversations:view_all) are different powers. These two steps
// move the "Supervisor de SAC" preset from the second to the first without taking
// anything away from anybody by surprise.

// BackfillOccurrenceViewAll gives occurrences:view_all to every role that already
// has conversations:view_all, so nobody who supervised cases before stops doing
// so. Purely additive. Per organisation it runs once: an organisation where any
// role already has occurrences:view_all is skipped, so an admin who later removes
// it from a role is not overruled at the next start.
func BackfillOccurrenceViewAll(db *gorm.DB, lo logf.Logger) error {
	var seeded int64
	if err := db.Model(&models.Permission{}).Where("resource = ? AND action = ?", models.ResourceOccurrences, models.ActionViewAll).Count(&seeded).Error; err != nil {
		return fmt.Errorf("failed to look up occurrences:view_all: %w", err)
	}
	if seeded == 0 {
		lo.Warn("Occurrence view_all backfill: permission not seeded yet, did nothing")
		return nil
	}
	res := db.Exec(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT DISTINCT r.id, target.id
		FROM custom_roles r
		JOIN role_permissions rp ON rp.custom_role_id = r.id
		JOIN permissions src ON src.id = rp.permission_id AND src.resource = 'conversations' AND src.action = 'view_all'
		JOIN permissions target ON target.resource = 'occurrences' AND target.action = 'view_all'
		WHERE r.deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM custom_roles r2
			JOIN role_permissions rp2 ON rp2.custom_role_id = r2.id
			JOIN permissions p2 ON p2.id = rp2.permission_id
			WHERE r2.organization_id = r.organization_id AND r2.deleted_at IS NULL
			  AND p2.resource = 'occurrences' AND p2.action = 'view_all')`)
	if res.Error != nil {
		return fmt.Errorf("failed to backfill occurrences:view_all: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		lo.Info("Occurrence view_all backfill complete", "roles_granted", res.RowsAffected)
	}
	return nil
}

// legacySupervisorSACExtras is what the "Supervisor de SAC" preset added over the
// agent baseline before conversations:view_all was taken out of it.
var legacySupervisorSACExtras = []string{
	"contacts:read", "contacts:write", "contacts:export", "tags:read", "templates:read",
	"conversations:view_all", "chat.assign:write",
	"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_all",
	"team_chat:manage",
	"analytics:read", "analytics.agents:read",
	"call_logs:read", "teams:read", "users:read",
}

// legacySupervisorSACKeys is the full former preset, sorted.
func legacySupervisorSACKeys() []string {
	set := map[string]bool{}
	for _, k := range models.SystemRolePermissions()["agent"] {
		if !strings.HasPrefix(k, "sales_opportunities:") {
			set[k] = true
		}
	}
	for _, k := range legacySupervisorSACExtras {
		set[k] = true
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ReconcileSupervisorSACPreset removes conversations:view_all from a
// "Supervisor de SAC" role ONLY when the role is still exactly the former preset
// (plus the additive occurrences:view_all), i.e. nobody ever customised it. It
// swaps it for conversations:view_team. A role that was edited in any way is left
// alone and reported, because someone chose its permissions. Every change is
// logged. Individual grants live on other roles and are never touched.
func ReconcileSupervisorSACPreset(db *gorm.DB, lo logf.Logger) error {
	var roles []models.CustomRole
	if err := db.Preload("Permissions").Where("name = ? AND deleted_at IS NULL", "Supervisor de SAC").Find(&roles).Error; err != nil {
		return fmt.Errorf("failed to list Supervisor de SAC roles: %w", err)
	}
	var viewAll, viewTeam models.Permission
	if err := db.Where("resource = ? AND action = ?", models.ResourceConversations, models.ActionViewAll).First(&viewAll).Error; err != nil {
		return nil // permissions not seeded yet
	}
	if err := db.Where("resource = ? AND action = ?", models.ResourceConversations, models.ActionViewTeam).First(&viewTeam).Error; err != nil {
		return nil
	}
	legacy := legacySupervisorSACKeys()
	for _, r := range roles {
		have := map[string]bool{}
		for _, p := range r.Permissions {
			have[p.Resource+":"+p.Action] = true
		}
		if !have["conversations:view_all"] {
			continue // already reconciled or never had it
		}
		delete(have, "occurrences:view_all")
		keys := make([]string, 0, len(have))
		for k := range have {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != strings.Join(legacy, ",") {
			lo.Info("Supervisor de SAC was customised: conversations:view_all left as configured",
				"role_id", r.ID, "organization_id", r.OrganizationID, "differs", diffKeys(keys, legacy))
			continue
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("DELETE FROM role_permissions WHERE custom_role_id = ? AND permission_id = ?", r.ID, viewAll.ID).Error; err != nil {
				return err
			}
			return tx.Exec("INSERT INTO role_permissions (custom_role_id, permission_id) VALUES (?, ?) ON CONFLICT DO NOTHING", r.ID, viewTeam.ID).Error
		})
		if err != nil {
			return fmt.Errorf("failed to reconcile role %s: %w", r.ID, err)
		}
		lo.Warn("Supervisor de SAC preset narrowed: conversations:view_all replaced by conversations:view_team (role was unmodified)",
			"role_id", r.ID, "organization_id", r.OrganizationID)
	}
	return nil
}

// diffKeys lists what separates a role from the former preset (+ extra, - missing).
func diffKeys(have, want []string) string {
	h, w := map[string]bool{}, map[string]bool{}
	for _, k := range have {
		h[k] = true
	}
	for _, k := range want {
		w[k] = true
	}
	var out []string
	for _, k := range have {
		if !w[k] {
			out = append(out, "+"+k)
		}
	}
	for _, k := range want {
		if !h[k] {
			out = append(out, "-"+k)
		}
	}
	return strings.Join(out, ",")
}
