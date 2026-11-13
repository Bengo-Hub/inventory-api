package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/platform/subscriptions"
)

// activeItemCount is the catalog size the plan's inventory_max_sku counts: items with
// is_active=true (inactive and End of Life items are left out). ok is false when the count fails,
// and callers then let the change through, as CreateItem always has.
func (h *InventoryHandler) activeItemCount(r *http.Request, tenantID uuid.UUID) (int, bool) {
	_, total, err := h.itemsSvc.ListItems(r.Context(), tenantID, "", "active", 1, 0, nil, nil, "", nil, nil, "")
	return total, err == nil
}

// skuLimitReached writes the plan's 402 limit response and returns true when adding n items to
// the active catalog would pass inventory_max_sku. Creating one item and reactivating one item are
// the same check.
func (h *InventoryHandler) skuLimitReached(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, n int) bool {
	if n <= 0 {
		return false
	}
	total, ok := h.activeItemCount(r, tenantID)
	if !ok {
		return false
	}
	// AssertLimit refuses once the value reaches the limit, so n new items fit only while the
	// count after all but the last one is still below it.
	return subscriptions.AssertLimit(w, r, "skus", subscriptions.LimitSKU, total+n-1)
}

// withSKULimitGuard returns the request context with an activation guard for one request: a
// change that brings items back into the active catalog past the plan's limit is refused with the
// 402 response, and the service returns items.ErrActivationRefused so the handler just returns.
func (h *InventoryHandler) withSKULimitGuard(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID) context.Context {
	return items.WithActivationGuard(r.Context(), func(_ context.Context, n int) error {
		if h.skuLimitReached(w, r, tenantID, n) {
			return items.ErrActivationRefused
		}
		return nil
	})
}

// importSKUGuard checks, row by row through one import, every row that adds an active item or
// brings an inactive one back. The active count is read once and kept current here (the listing
// count is cached for a short while, so re-reading it per row would miss the rows this import
// already added). A refused row fails with a message; the rest of the import carries on.
type importSKUGuard struct {
	r      *http.Request
	active int
	known  bool
}

func (h *InventoryHandler) newImportSKUGuard(r *http.Request, tenantID uuid.UUID) *importSKUGuard {
	active, ok := h.activeItemCount(r, tenantID)
	return &importSKUGuard{r: r, active: active, known: ok}
}

// take reserves n slots, or refuses with ErrActivationRefused once the plan's limit is reached.
// An unknown count lets the row through, as CreateItem does.
func (g *importSKUGuard) take(n int, what string) error {
	if !g.known || n <= 0 {
		return nil
	}
	if !subscriptions.CheckLimit(g.r, subscriptions.LimitSKU, g.active+n-1) {
		return fmt.Errorf("%w: %s needs a plan upgrade", items.ErrActivationRefused, what)
	}
	g.active += n
	return nil
}

// ctx returns the request context carrying this import's activation guard, for the UpdateItem
// call on an existing SKU (it asks only when the row brings an inactive item back).
func (g *importSKUGuard) ctx() context.Context {
	return items.WithActivationGuard(g.r.Context(), func(_ context.Context, n int) error {
		return g.take(n, "reactivating this item")
	})
}

// beforeCreate reserves a slot for a new active item. Call release when the create then fails.
func (g *importSKUGuard) beforeCreate(dto items.ItemDTO) error {
	if !dto.IsActive {
		return nil
	}
	return g.take(1, "adding this item")
}

// release gives back the slot beforeCreate took for a row whose create failed.
func (g *importSKUGuard) release(dto items.ItemDTO) {
	if g.known && dto.IsActive {
		g.active--
	}
}
