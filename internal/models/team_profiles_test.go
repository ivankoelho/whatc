package models

import "testing"

// A typo in a profile key would silently drop that permission at seed time.
func TestTeamProfilesUseKnownPermissions(t *testing.T) {
	known := map[string]bool{}
	for _, p := range DefaultPermissions() {
		known[p.Resource+":"+p.Action] = true
	}
	names := map[string]bool{}
	for _, tp := range TeamProfiles() {
		if names[tp.Name] {
			t.Errorf("duplicate profile %q", tp.Name)
		}
		names[tp.Name] = true
		for _, k := range tp.Permissions {
			if !known[k] {
				t.Errorf("profile %q: unknown permission %q", tp.Name, k)
			}
		}
	}
}

// chat:triage (take a "new" conversation into attendance by status) is the SAC's initial-triage permission.
// The ordinary attendant baseline and the other presets do not have it; admin (everything) and manager do.
func TestChatTriageHolders(t *testing.T) {
	has := func(keys []string) bool {
		for _, k := range keys {
			if k == "chat:triage" {
				return true
			}
		}
		return false
	}
	for _, tp := range TeamProfiles() {
		want := tp.Name == "SAC"
		if has(tp.Permissions) != want {
			t.Errorf("profile %q: chat:triage = %v, want %v", tp.Name, has(tp.Permissions), want)
		}
	}
	roles := SystemRolePermissions()
	for role, want := range map[string]bool{"admin": true, "manager": true, "agent": false} {
		if has(roles[role]) != want {
			t.Errorf("system role %q: chat:triage = %v, want %v", role, has(roles[role]), want)
		}
	}
}
