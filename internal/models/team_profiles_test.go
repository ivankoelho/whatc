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
// Nobody else has it: not the other presets, not the agent baseline, not manager, not admin (both already change
// any status through contacts:write). An administrator can still grant it by hand, so it stays in the catalog.
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
		want := tp.Key == "sac"
		if has(tp.Permissions) != want {
			t.Errorf("profile %q: chat:triage = %v, want %v", tp.Name, has(tp.Permissions), want)
		}
	}
	roles := SystemRolePermissions()
	for _, role := range []string{"admin", "manager", "agent"} {
		if has(roles[role]) {
			t.Errorf("system role %q must not hold chat:triage", role)
		}
	}
	found := false
	for _, p := range DefaultPermissions() {
		found = found || (p.Resource == ResourceChat && p.Action == ActionTriage)
	}
	if !found {
		t.Error("chat:triage must stay in the permission catalog")
	}
}

// The key, not the name, identifies a preset: every preset has a distinct, non-empty key.
func TestTeamProfileKeysAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, tp := range TeamProfiles() {
		if tp.Key == "" {
			t.Errorf("profile %q has no key", tp.Name)
		}
		if other, dup := seen[tp.Key]; dup {
			t.Errorf("profiles %q and %q share the key %q", other, tp.Name, tp.Key)
		}
		seen[tp.Key] = tp.Name
	}
}

// The seller registers no contacts and imports none; it still reads them.
func TestVendasProfile_CannotWriteOrImportContacts(t *testing.T) {
	var perms []string
	for _, p := range TeamProfiles() {
		if p.Key == "vendas" {
			perms = p.Permissions
		}
	}
	has := func(k string) bool {
		for _, p := range perms {
			if p == k {
				return true
			}
		}
		return false
	}
	for _, k := range []string{"contacts:write", "contacts:import", "contacts:delete"} {
		if has(k) {
			t.Errorf("vendas must not hold %s", k)
		}
	}
	for _, k := range []string{"contacts:read", "chat:read", "chat:write", "sales_opportunities:write"} {
		if !has(k) {
			t.Errorf("vendas must keep %s", k)
		}
	}
}
