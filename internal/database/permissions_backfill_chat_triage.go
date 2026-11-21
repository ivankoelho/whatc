package database

import (
	"fmt"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// BackfillTeamProfileKeysAndChatTriage gives the team presets that predate custom_roles.profile_key their key and
// grants chat:triage (take a "new" conversation into attendance by status) to the SAC preset. New organizations
// never need it: the seed creates the presets with their key and chat:triage already in the SAC profile. Nobody else
// receives chat:triage (the other presets, the system roles and custom roles are untouched).
//
// WHICH role is the SAC preset is decided by its key, never by its name. The only place a name is used is the
// one-time recognition of roles created BEFORE keys existed, and there the name alone is not enough: a role is
// recognised as a preset only if it is not a system role, is not deleted, still has no key, and has BOTH the
// preset's canonical name AND its original seed description. So a custom role that someone named "SAC" is not a
// preset, and a preset whose name or description an administrator changed is not recognised (it can be given
// chat:triage by hand).
//
// WHEN a preset is considered done is decided per preset, not per organization. This matters because the seed runs
// BEFORE this backfill in every -migrate (RunMigrationWithProgress -> SeedSystemRolesForAllOrgs) and creates the
// presets an old organization is missing WITH their keys: "the organization already has some key" therefore says
// nothing about whether ITS legacy SAC was handled. A legacy role is processed when no active role of the
// organization carries that preset's key yet:
//   - its key and, for the SAC, the chat:triage grant are written in ONE transaction per organization; if anything
//     fails nothing is written and the next -migrate tries again;
//   - after that the preset has its key, so it is never processed again: it is the run-once marker (this project keeps
//     no migration table; its backfills are guarded by the shape of the data), and what an administrator does
//     afterwards is respected. Removing chat:triage from the SAC role is NOT undone, by any later boot.
//
// It never depends on what admin or manager hold, never changes a name, a description or another permission, and is
// purely additive. Two boots at once cannot fail on a duplicate link (ON CONFLICT DO NOTHING) and the unique index on
// (organization, profile key) keeps a key from being given to two roles.
func BackfillTeamProfileKeysAndChatTriage(db *gorm.DB, lo logf.Logger) error {
	var triage models.Permission
	if err := db.Where("resource = ? AND action = ?", models.ResourceChat, models.ActionTriage).First(&triage).Error; err != nil {
		return fmt.Errorf("the permission chat:triage is not seeded: run the permission seed before this backfill: %w", err)
	}

	type candidate struct {
		RoleID string
		OrgID  string
		Key    string
	}
	byOrg := map[string][]candidate{}
	var orgOrder []string
	for _, tp := range models.TeamProfiles() {
		// One legacy role per organization and preset (the oldest), and only where the preset has no key yet.
		var found []candidate
		if err := db.Raw(`
			SELECT DISTINCT ON (r.organization_id) r.id::text AS role_id, r.organization_id::text AS org_id, ? AS key
			FROM custom_roles r
			WHERE r.deleted_at IS NULL AND r.is_system = FALSE AND r.profile_key IS NULL
			  AND r.name = ? AND r.description = ?
			  AND NOT EXISTS (
				SELECT 1 FROM custom_roles k
				WHERE k.organization_id = r.organization_id AND k.deleted_at IS NULL AND k.profile_key = ?)
			ORDER BY r.organization_id, r.created_at, r.id`, tp.Key, tp.Name, tp.Description, tp.Key).
			Scan(&found).Error; err != nil {
			return fmt.Errorf("failed to look for legacy %s presets: %w", tp.Key, err)
		}
		for _, c := range found {
			if _, seen := byOrg[c.OrgID]; !seen {
				orgOrder = append(orgOrder, c.OrgID)
			}
			byOrg[c.OrgID] = append(byOrg[c.OrgID], c)
		}
	}

	keyed, granted := 0, 0
	for _, orgID := range orgOrder {
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, c := range byOrg[orgID] {
				res := tx.Exec(`UPDATE custom_roles SET profile_key = ? WHERE id = ? AND profile_key IS NULL`, c.Key, c.RoleID)
				if res.Error != nil {
					return fmt.Errorf("failed to key preset %s: %w", c.Key, res.Error)
				}
				if res.RowsAffected == 0 {
					continue // another boot got there first
				}
				keyed++
				if c.Key != "sac" {
					continue
				}
				g := tx.Exec(`INSERT INTO role_permissions (custom_role_id, permission_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, c.RoleID, triage.ID)
				if g.Error != nil {
					return fmt.Errorf("failed to grant chat:triage to the SAC preset: %w", g.Error)
				}
				granted += int(g.RowsAffected)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("organization %s: %w", orgID, err)
		}
	}
	if keyed > 0 || granted > 0 {
		lo.Info("Team profile keys and chat:triage backfill complete", "roles_keyed", keyed, "chat_triage_granted", granted)
	}
	return nil
}
