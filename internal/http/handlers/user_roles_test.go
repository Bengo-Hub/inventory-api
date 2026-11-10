package handlers

import "testing"

// Security audit 2026-11-11: POST /users passed any roles to auth-api; admin-tier roles are given
// only by an organisation admin in the accounts site.
func TestAdminTierRole(t *testing.T) {
	for _, r := range []string{"admin", "Superuser", "owner", "super_admin", "tenant_admin", "administrator"} {
		if !adminTierRole(r) {
			t.Errorf("%s is admin-tier", r)
		}
	}
	for _, r := range []string{"store_manager", "stock_clerk", "viewer"} {
		if adminTierRole(r) {
			t.Errorf("%s is not admin-tier", r)
		}
	}
}
