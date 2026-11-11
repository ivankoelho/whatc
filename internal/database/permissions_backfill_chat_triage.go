package database

import (
	"fmt"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// BackfillTeamProfileKeysAndChatTriage gives the team presets of organizations that predate custom_roles.profile_key
// their profile key and grants chat:triage (take a "new" conversation into attendance by status) to the SAC preset.
// New organizations never need it: the seed creates the presets with their key and chat:triage already in the SAC
// profile. Nobody else receives chat:triage (the other presets, the system roles and custom roles are untouched).
//
// WHICH role is the SAC preset is decided by its key, never by its name. The only place a name is used is the
// one-time recognition of roles created BEFORE keys existed, and there the name alone is not enough: a role is
// recognised as a preset only if it is not a system role, is not deleted, still has no key, and has BOTH the
// preset's canonical name AND its original seed description. So a custom role that someone named "SAC" is not a
// preset, and a preset whose name or description an administrator changed is not recognised (it can be given
// chat:triage by hand).
//
// EXACT behavior:
//   - Per organization, in ONE transaction: the keys of the recognised presets and the chat:triage grant are
//     written together. If anything fails, nothing is written and the next -migrate tries again.
//   - The keys are the run-once marker (there is no other: this project keeps no migration table, its backfills
//     are guarded by the shape of the data). An organization is considered when NONE of its roles has a key yet.
//     After a successful run it has them, so it is never considered again: a second run changes nothing, and what
//     an administrator does afterwards is respected (removing chat:triage from the SAC role is NOT undone).
//     An organization created after this release is seeded with keys and is never considered either.
//   - It does not depend on what admin or manager hold: neither receives chat:triage.
//   - Purely additive: it never revokes anything and never changes a name, description or other permission.
//   - Two boots at once cannot fail on a duplicate link (ON CONFLICT DO NOTHING); the unique index on
//     (organization, profile key) keeps a key from being given to two roles.
func BackfillTeamProfileKeysAndChatTriage(db *gorm.DB, lo logf.Logger) error {
	var triage models.Permission
	if err := db.Where("resource = ? AND action = ?", models.ResourceChat, models.ActionTriage).First(&triage).Error; err != nil {
		return fmt.Errorf("the permission chat:triage is not seeded: run the permission seed before this backfill: %w", err)
	}

	var orgIDs []string
	if err := db.Raw(`
		SELECT DISTINCT r.organization_id::text
		FROM custom_roles r
		WHERE r.deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM custom_roles k
			WHERE k.organization_id = r.organization_id AND k.deleted_at IS NULL AND k.profile_key IS NOT NULL)`).
		Scan(&orgIDs).Error; err != nil {
		return fmt.Errorf("failed to list organizations without profile keys: %w", err)
	}

	keyed, granted := 0, 0
	for _, orgID := range orgIDs {
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, tp := range models.TeamProfiles() {
				res := tx.Exec(`
					UPDATE custom_roles SET profile_key = ?
					WHERE organization_id = ? AND deleted_at IS NULL AND is_system = FALSE AND profile_key IS NULL
					  AND name = ? AND description = ?`, tp.Key, orgID, tp.Name, tp.Description)
				if res.Error != nil {
					return fmt.Errorf("failed to key preset %s: %w", tp.Key, res.Error)
				}
				keyed += int(res.RowsAffected)
			}
			res := tx.Exec(`
				INSERT INTO role_permissions (custom_role_id, permission_id)
				SELECT r.id, ? FROM custom_roles r
				WHERE r.organization_id = ? AND r.deleted_at IS NULL AND r.profile_key = 'sac'
				ON CONFLICT DO NOTHING`, triage.ID, orgID)
			if res.Error != nil {
				return fmt.Errorf("failed to grant chat:triage to the SAC preset: %w", res.Error)
			}
			granted += int(res.RowsAffected)
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
