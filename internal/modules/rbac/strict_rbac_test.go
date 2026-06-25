package rbac

import (
	"testing"
)

// TestStrictRBACEnabled locks in the security-critical defaults for the SEC-1 fix:
// strict mode is ON unless explicitly disabled, so a tenant superuser/admin never
// silently bypasses inventory permission checks by default.
func TestStrictRBACEnabled(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{name: "empty defaults to strict", val: "", want: true},
		{name: "true", val: "true", want: true},
		{name: "1", val: "1", want: true},
		{name: "garbage defaults to strict", val: "maybe", want: true},
		{name: "false disables", val: "false", want: false},
		{name: "0 disables", val: "0", want: false},
		{name: "off disables", val: "off", want: false},
		{name: "OFF disables", val: "OFF", want: false},
		{name: "no disables", val: "no", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INVENTORY_STRICT_RBAC", tc.val)
			if got := strictRBACEnabled(); got != tc.want {
				t.Fatalf("strictRBACEnabled()=%v, want %v (env=%q)", got, tc.want, tc.val)
			}
		})
	}
}

// mapGlobalRoleToInventoryRole is the migration backbone: a global superuser/admin MUST
// map to the explicit inventory_admin role so that, once the blanket bypass is removed,
// JIT provisioning grants them real access rather than locking them out.
func TestMapGlobalRoleToInventoryRole_SuperuserGetsInventoryAdmin(t *testing.T) {
	for _, role := range []string{"superuser", "admin", "tenant_admin", "owner"} {
		if got := mapGlobalRoleToInventoryRole([]string{role}); got != "inventory_admin" {
			t.Fatalf("role %q mapped to %q, want inventory_admin", role, got)
		}
	}
}
