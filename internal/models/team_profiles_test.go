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
