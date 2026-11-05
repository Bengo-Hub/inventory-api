package middleware

import (
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

func TestOutletLimitOutranksManagerRole(t *testing.T) {
	limited := &authclient.Claims{Roles: []string{"warehouse_manager"}, OutletIDs: []string{"a"}}
	if CanAccessAllOutletsFor(limited) {
		t.Fatal("a limited manager must not see every outlet")
	}
	if !CanAccessAllOutletsFor(&authclient.Claims{Roles: []string{"admin"}, OutletIDs: []string{"a"}}) {
		t.Fatal("an admin is always tenant-wide")
	}
}
