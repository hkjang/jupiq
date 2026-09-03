package store

import "testing"

func TestDefaultUserRoleCannotReadAdministrativeDashboard(t *testing.T) {
	permissions := defaultUserRolePermissions()
	for _, forbidden := range []string{"dashboard:read", "profiles:read", "profile:write"} {
		if EnsurePermission(permissions, forbidden) {
			t.Fatalf("default user role unexpectedly grants %q: %#v", forbidden, permissions)
		}
	}
	for _, required := range []string{"profile:read", "profile:keys"} {
		if !EnsurePermission(permissions, required) {
			t.Fatalf("default user role omitted %q: %#v", required, permissions)
		}
	}
}
