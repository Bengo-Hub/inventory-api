package items

import (
	"testing"
	"time"

	"github.com/bengobox/inventory-service/internal/ent"
)

// Pure eligibility rules for bulk item actions — DB-free (promotions-style
// pattern): hand-built ent.Item literals, no client.
func TestBulkTargetState(t *testing.T) {
	now := time.Now()
	active := &ent.Item{IsActive: true}
	inactive := &ent.Item{IsActive: false}
	eol := &ent.Item{IsActive: false, EndOfLifeAt: &now}
	nfs := &ent.Item{IsActive: true, NotForSale: true}

	cases := []struct {
		name    string
		action  string
		item    *ent.Item
		already bool
	}{
		{"delete active item proceeds", BulkActionDelete, active, false},
		{"delete inactive item skips (idempotent)", BulkActionDelete, inactive, true},
		{"deactivate active proceeds", BulkActionDeactivate, active, false},
		{"deactivate inactive skips", BulkActionDeactivate, inactive, true},
		{"activate inactive proceeds", BulkActionActivate, inactive, false},
		{"activate active skips", BulkActionActivate, active, true},
		{"activate EOL item skips (must use EOL restore)", BulkActionActivate, eol, true},
		{"flag not-for-sale proceeds", BulkActionNotForSaleOn, active, false},
		{"flag already-not-for-sale skips", BulkActionNotForSaleOn, nfs, true},
		{"unflag not-for-sale proceeds", BulkActionNotForSaleOff, nfs, false},
		{"unflag for-sale item skips", BulkActionNotForSaleOff, active, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			already, reason, ok := bulkTargetState(c.action, c.item)
			if !ok {
				t.Fatalf("action %q unexpectedly unknown", c.action)
			}
			if already != c.already {
				t.Errorf("already = %v (reason %q), want %v", already, reason, c.already)
			}
			if already && reason == "" {
				t.Error("skip must carry a reason")
			}
		})
	}
}

func TestValidBulkAction(t *testing.T) {
	for _, a := range []string{BulkActionDelete, BulkActionActivate, BulkActionDeactivate, BulkActionNotForSaleOn, BulkActionNotForSaleOff} {
		if !ValidBulkAction(a) {
			t.Errorf("ValidBulkAction(%q) = false, want true", a)
		}
	}
	for _, a := range []string{"", "purge", "DELETE", "not_for_sale"} {
		if ValidBulkAction(a) {
			t.Errorf("ValidBulkAction(%q) = true, want false", a)
		}
	}
}
