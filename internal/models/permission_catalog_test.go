package models

import "testing"

// Every catalog key must land in a real group (not "other"), exactly once, and
// the grouping must not add or drop keys.
func TestPermissionCatalogGroupsEveryKey(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range DefaultPermissions() {
		key := p.Resource + ":" + p.Action
		if seen[key] {
			t.Fatalf("duplicate key %s", key)
		}
		seen[key] = true
		if g, _, _ := PermissionPresentation(p.Resource, p.Action); g == PermissionGroupOther {
			t.Errorf("%s is not mapped to a functional group", key)
		}
	}
	// Not a fixed count: new permissions are expected. What must hold is that the
	// keys other code authorizes with exist (a rename or removal is caught), that no
	// key is duplicated (checked above) and that the admin role receives every key.
	for _, key := range []string{
		"occurrences:read", "occurrences:write", "occurrences:view_all",
		"conversations:view_all", "conversations:view_team",
		"chat:read", "chat:write", "team_chat:read", "team_chat:write",
	} {
		if !seen[key] {
			t.Errorf("catalog lost %s, which authorization code depends on", key)
		}
	}
	admin := map[string]bool{}
	for _, k := range SystemRolePermissions()["admin"] {
		admin[k] = true
	}
	for key := range seen {
		if !admin[key] {
			t.Errorf("admin role does not receive catalog key %s", key)
		}
	}

	owner := map[string]string{}
	for _, g := range permissionGroups {
		for _, r := range g.Resources {
			if prev, ok := owner[r]; ok {
				t.Errorf("resource %s is in both %s and %s", r, prev, g.Key)
			}
			owner[r] = g.Key
		}
	}
}

func TestPermissionPresentationOrder(t *testing.T) {
	_, _, read := PermissionPresentation(ResourceUsers, ActionRead)
	_, _, write := PermissionPresentation(ResourceUsers, ActionWrite)
	_, _, teamsRead := PermissionPresentation(ResourceTeams, ActionRead)
	if read >= write || write >= teamsRead {
		t.Errorf("order wrong: %d %d %d", read, write, teamsRead)
	}
	if g, _, _ := PermissionPresentation("nope", "read"); g != PermissionGroupOther {
		t.Errorf("unmapped resource group = %s", g)
	}
}
