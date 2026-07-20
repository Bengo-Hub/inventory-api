package items

import (
	"context"
	"fmt"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/google/uuid"
)

// Bulk item operations (DataTable multi-select actions).
//
// All actions are IDEMPOTENT: re-running the same request is a no-op — items
// already in the target state are reported as skipped ("already …"), never an
// error, and ids that don't resolve are skipped ("not found"). Valid state
// changes are applied atomically in one transaction and each mutated item
// emits `inventory.item.updated` so downstream catalogs (pos-api, ordering)
// resync — the single-item EOL flow set this precedent.

// BulkSkipped explains why one requested id was not mutated.
type BulkSkipped struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// BulkActionResult reports per-item outcomes of a bulk mutation.
type BulkActionResult struct {
	Processed int           `json:"processed"`
	Skipped   []BulkSkipped `json:"skipped"`
}

// Bulk item actions. "delete" mirrors the single DELETE /items/{sku} semantics
// (soft delete: is_active=false).
const (
	BulkActionDelete        = "delete"
	BulkActionActivate      = "activate"
	BulkActionDeactivate    = "deactivate"
	BulkActionNotForSaleOn  = "not_for_sale_on"
	BulkActionNotForSaleOff = "not_for_sale_off"
)

// bulkTargetState returns whether the item is ALREADY in the action's target
// state (skip) and the mutation to apply otherwise. Kept pure for unit tests.
func bulkTargetState(action string, it *ent.Item) (alreadyThere bool, skipReason string, ok bool) {
	switch action {
	case BulkActionDelete, BulkActionDeactivate:
		return !it.IsActive, "already inactive", true
	case BulkActionActivate:
		// EOL items must be restored through the explicit EOL flow (clears
		// end_of_life_at + purge schedule), not silently re-activated in bulk.
		if it.EndOfLifeAt != nil {
			return true, "marked end-of-life — restore via the EOL tab", true
		}
		return it.IsActive, "already active", true
	case BulkActionNotForSaleOn:
		return it.NotForSale, "already not-for-sale", true
	case BulkActionNotForSaleOff:
		return !it.NotForSale, "already for sale", true
	}
	return false, "", false
}

// applyBulkAction mutates one item builder per the action.
func applyBulkAction(action string, upd *ent.ItemUpdateOne) *ent.ItemUpdateOne {
	switch action {
	case BulkActionDelete, BulkActionDeactivate:
		return upd.SetIsActive(false)
	case BulkActionActivate:
		return upd.SetIsActive(true)
	case BulkActionNotForSaleOn:
		return upd.SetNotForSale(true)
	case BulkActionNotForSaleOff:
		return upd.SetNotForSale(false)
	}
	return upd
}

// ValidBulkAction reports whether the action name is a known bulk item action.
func ValidBulkAction(action string) bool {
	switch action {
	case BulkActionDelete, BulkActionActivate, BulkActionDeactivate, BulkActionNotForSaleOn, BulkActionNotForSaleOff:
		return true
	}
	return false
}

// BulkItemAction applies one action to many items by id. Unknown actions error;
// per-item outcomes are reported in the result (see file comment for semantics).
func (s *Service) BulkItemAction(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID, action string) (*BulkActionResult, error) {
	if !ValidBulkAction(action) {
		return nil, fmt.Errorf("items: unknown bulk action %q", action)
	}
	res := &BulkActionResult{Skipped: []BulkSkipped{}}
	if len(ids) == 0 {
		return res, nil
	}

	found, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.IDIn(ids...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: bulk query: %w", err)
	}
	byID := make(map[uuid.UUID]*ent.Item, len(found))
	for _, it := range found {
		byID[it.ID] = it
	}

	var toMutate []*ent.Item
	for _, id := range ids {
		it, ok := byID[id]
		if !ok {
			res.Skipped = append(res.Skipped, BulkSkipped{ID: id.String(), Reason: "not found"})
			continue
		}
		already, reason, _ := bulkTargetState(action, it)
		if already {
			res.Skipped = append(res.Skipped, BulkSkipped{ID: id.String(), Reason: reason})
			continue
		}
		toMutate = append(toMutate, it)
	}
	if len(toMutate) == 0 {
		return res, nil
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: bulk begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, it := range toMutate {
		updated, uErr := applyBulkAction(action, tx.Item.UpdateOneID(it.ID)).Save(ctx)
		if uErr != nil {
			err = fmt.Errorf("items: bulk update %s: %w", it.Sku, uErr)
			return nil, err
		}
		// Emit item.updated per mutation so POS/ordering catalogs resync the flag.
		if err = s.emitItemUpdatedEvent(ctx, tx, tenantID, updated); err != nil {
			return nil, err
		}
		res.Processed++
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("items: bulk commit: %w", err)
	}
	return res, nil
}
