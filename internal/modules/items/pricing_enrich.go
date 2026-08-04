package items

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itempricing"
	"github.com/bengobox/inventory-service/internal/ent/pendingpricechange"
	"github.com/bengobox/inventory-service/internal/ent/pricingtier"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
)

// enrichPrices populates SellingPrice + the tax split (NetPrice/TaxAmount/TaxRate) on the given
// DTOs so the POS/ordering proxies receive a usable customer price and tax. The effective price
// is resolved as: recipe selling price (RECIPE items) → default pricing-tier price → existing
// cost+margin suggestion. When the tenant (or the item) treats prices as tax-inclusive, the tax
// is computed backwards using the VAT rate resolved from treasury-api (falling back to
// DefaultVATRate when treasury is unavailable). cfg may be nil.
func (s *Service) enrichPrices(ctx context.Context, tenantID uuid.UUID, cfg *ent.TenantInventoryConfig, dtos []ItemDTO) {
	if len(dtos) == 0 {
		return
	}
	itemIDs := make([]uuid.UUID, len(dtos))
	for i := range dtos {
		itemIDs[i] = dtos[i].ID
	}

	recipePrice := s.recipeSellingPrices(ctx, tenantID, cfg, itemIDs)
	tierPrice := s.defaultTierPrices(ctx, tenantID, itemIDs)
	recipeCost := s.recipeCostPerPortion(ctx, tenantID, itemIDs)

	inclusiveDefault := cfg != nil && cfg.PricesInclusiveOfTax
	defaultTaxCode := ""
	if cfg != nil {
		defaultTaxCode = cfg.DefaultTaxCode
	}
	// A business that isn't VAT-registered must not charge VAT — suppress the rate entirely
	// (including the inclusive-price DefaultVATRate fallback) so no tax is split out.
	suppressVAT := s.vatSuppressed(ctx, tenantID)

	for i := range dtos {
		d := &dtos[i]
		// INGREDIENT items are never sold to customers — they are consumed to produce
		// recipe items. Deriving a "selling price" for them (cost+margin cooked
		// suggestion) only pollutes lists with meaningless retail/wholesale figures; the
		// only price that matters is cost_price per BASE unit, used in recipe line/EP
		// cost calculations. Skip enrichment entirely.
		if d.Type == "INGREDIENT" {
			continue
		}
		// RECIPE items never carry a raw Item.cost_price (there's nothing to "purchase" —
		// they're built from ingredients, not stocked): RecalculateRecipeCosts writes
		// total_cost/cost_per_portion onto the Recipe row only, never back onto the linked
		// Item. Without this, every RECIPE item's CostPrice silently reads 0 everywhere
		// that field is consumed downstream (pos-api catalog's view_cost column, the
		// Most Profitable report) — margin math for the majority of a restaurant/cafe
		// menu was effectively computing 100% margin on every recipe dish.
		if d.Type == "RECIPE" && d.CostPrice == nil {
			if cp, ok := recipeCost[d.ID]; ok && cp > 0 {
				d.CostPrice = &cp
			}
		}

		price := effectivePrice(d, recipePrice, tierPrice)
		if price <= 0 {
			continue
		}
		sp := price
		d.SellingPrice = &sp

		// Resolve the VAT rate per-item, preferring the item's OWN tax code (already
		// populated from Item.tax_code_id in mapToDTO) over the tenant default — a
		// zero-rated/exempt item must never be silently taxed at the tenant's default rate
		// just because it shares the page with standard-rated items. resolveVATRate is
		// Redis-cached per tenant, so this is a cache hit after the first item in the loop.
		rate, rateCode := s.resolveVATRate(ctx, tenantID, preferredTaxCode(d, defaultTaxCode))
		applyItemTax(d, price, rate, rateCode, defaultTaxCode, inclusiveDefault, suppressVAT)
	}
}

// preferredTaxCode picks the tax code to resolve an item's VAT rate against: the item's own
// tax_code_id when it has one, else the tenant's configured default. Pure/DB-free so it's
// unit-testable directly (enrichPrices itself needs a live ent client for its sibling
// recipe/tier-price lookups, so the DB-free logic is split out here per this package's existing
// test pattern — see bulk_test.go's bulkTargetState).
func preferredTaxCode(d *ItemDTO, defaultTaxCode string) string {
	if d.TaxCodeID != "" {
		return d.TaxCodeID
	}
	return defaultTaxCode
}

// applyItemTax stamps the resolved VAT rate/code onto an item DTO's tax fields (NetPrice/
// TaxAmount/TaxRate/TaxInclusive, and TaxCodeID when it was previously unset). rate/rateCode are
// the already-resolved output of resolveVATRate for this item's preferred code (see
// preferredTaxCode) — this function itself does no I/O, so it's unit-testable without a DB.
func applyItemTax(d *ItemDTO, price, rate float64, rateCode, defaultTaxCode string, inclusiveDefault, suppressVAT bool) {
	inclusive := inclusiveDefault || d.TaxInclusive
	effRate := rate
	if suppressVAT {
		effRate = 0
	} else if effRate <= 0 && inclusive {
		effRate = DefaultVATRate // must still back-compute when treasury is unreachable
	}
	if effRate <= 0 {
		return // no VAT (exclusive w/ no rate, or suppressed) → leave tax fields unset
	}
	split := ComputeTaxSplit(price, effRate, inclusive)
	net, tax, r := split.Net, split.Tax, split.Rate
	d.NetPrice = &net
	d.TaxAmount = &tax
	d.TaxRate = &r
	d.TaxInclusive = inclusive
	if d.TaxCodeID == "" {
		if rateCode != "" {
			d.TaxCodeID = rateCode
		} else if defaultTaxCode != "" {
			d.TaxCodeID = defaultTaxCode
		}
	}
}

// effectivePrice resolves an item's customer price: recipe → default tier → the merchant's
// max/ceiling selling price → cost+margin suggestion (last resort only).
//
// The max_selling_price (the merchant-entered retail ceiling) is preferred over the cost+margin
// SuggestedPrice so a GOODS item that has a real selling price never surfaces a "cooked" price on
// the POS/ordering channels. SuggestedPrice remains the very last fallback so a fully-costed item
// with no explicit price is still sellable rather than priced 0/hidden.
func effectivePrice(d *ItemDTO, recipePrice, tierPrice map[uuid.UUID]float64) float64 {
	if p, ok := recipePrice[d.ID]; ok && p > 0 {
		return p
	}
	if p, ok := tierPrice[d.ID]; ok && p > 0 {
		return p
	}
	if d.MaxSellingPrice != nil && *d.MaxSellingPrice > 0 {
		return *d.MaxSellingPrice
	}
	if d.SuggestedPrice != nil && *d.SuggestedPrice > 0 {
		return *d.SuggestedPrice
	}
	return 0
}

// EnsureDefaultPrice writes `price` onto the tenant's default pricing tier for an item, creating the
// default RETAIL tier if none exists, and upserting the all-outlets ItemPricing row. Used by bulk
// import so an uploaded selling_price becomes a real tier price the POS pricing-resolve can read.
func (s *Service) EnsureDefaultPrice(ctx context.Context, tenantID, itemID uuid.UUID, price float64) error {
	if price <= 0 {
		return nil
	}
	tier, err := s.client.PricingTier.Query().
		Where(pricingtier.TenantID(tenantID), pricingtier.IsDefault(true), pricingtier.IsActive(true)).
		First(ctx)
	if ent.IsNotFound(err) {
		tier, err = s.client.PricingTier.Create().
			SetTenantID(tenantID).SetName("Retail").SetCode("RETAIL").
			SetIsDefault(true).SetIsActive(true).SetSortOrder(0).
			Save(ctx)
	}
	if err != nil {
		return err
	}
	return s.upsertItemTierPrice(ctx, tenantID, itemID, tier.ID, price)
}

// EnsureGuardrailTierPrices materializes an item's selling-price guardrails as real pricing-tier
// prices so the POS/ordering price-resolve reads the merchant's ACTUAL price instead of a
// cost+margin "suggested" (cooked) fallback: Retail (default tier) = maxPrice, Wholesale = minPrice.
// A non-positive price for a tier is skipped so it never clobbers an existing profile with 0.
func (s *Service) EnsureGuardrailTierPrices(ctx context.Context, tenantID, itemID uuid.UUID, maxPrice, minPrice float64) error {
	if maxPrice > 0 {
		if err := s.EnsureDefaultPrice(ctx, tenantID, itemID, maxPrice); err != nil {
			return err
		}
	}
	if minPrice > 0 {
		tier, err := s.client.PricingTier.Query().
			Where(pricingtier.TenantID(tenantID), pricingtier.Code("WHOLESALE"), pricingtier.IsActive(true)).
			First(ctx)
		if ent.IsNotFound(err) {
			tier, err = s.client.PricingTier.Create().
				SetTenantID(tenantID).SetName("Wholesale").SetCode("WHOLESALE").
				SetIsDefault(false).SetIsActive(true).SetSortOrder(1).
				Save(ctx)
		}
		if err != nil {
			return err
		}
		if err := s.upsertItemTierPrice(ctx, tenantID, itemID, tier.ID, minPrice); err != nil {
			return err
		}
	}
	return nil
}

// SetSellingPriceBySKU repoints an item's customer-facing selling price everywhere the POS
// price-resolve reads it: the Retail/max guardrail plus the materialized RETAIL tier row,
// and — when the Wholesale/min guardrail was equal to the old ceiling (single-price item)
// or would now exceed the new price — the min guardrail and WHOLESALE tier row too.
// The corrective tool behind pos-api's "also update the catalog price" line edit; RECIPE
// items must additionally update their recipe via recipes.SetSellingPriceByItem (the
// recipe price outranks tier rows for those).
func (s *Service) SetSellingPriceBySKU(ctx context.Context, tenantID uuid.UUID, sku string, price float64) (*ItemDTO, error) {
	if price < 0 {
		return nil, fmt.Errorf("items: price cannot be negative")
	}
	itm, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.Sku(sku)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: item %q: %w", sku, err)
	}
	return s.setSellingPrice(ctx, tenantID, itm, price, "all_stock")
}

// SetSellingPriceByItemID is SetSellingPriceBySKU keyed by item ID instead of SKU — for callers
// (goods-receipt posting, pending-price promotion) that already have the item ID and would
// otherwise need an extra SKU round-trip. scope is carried onto the audit entry only.
func (s *Service) SetSellingPriceByItemID(ctx context.Context, tenantID, itemID uuid.UUID, price float64, scope string) (*ItemDTO, error) {
	if price < 0 {
		return nil, fmt.Errorf("items: price cannot be negative")
	}
	itm, err := s.client.Item.Query().
		Where(item.TenantID(tenantID), item.ID(itemID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: item %s: %w", itemID, err)
	}
	return s.setSellingPrice(ctx, tenantID, itm, price, scope)
}

// setSellingPrice is the shared body of SetSellingPriceBySKU/SetSellingPriceByItemID: repoints
// the item's guardrails + materialized RETAIL/WHOLESALE tier rows to `price`, audits a real
// change, and returns the updated DTO. scope is carried onto the audit entry only (e.g.
// "new_stock_only" when called from PromotePendingPriceChanges) — the write itself is always an
// immediate, everywhere-effective change; scoping the effect to only NEW stock is what
// PendingPriceChange + PromotePendingPriceChanges exists to defer until it's safe to do this.
func (s *Service) setSellingPrice(ctx context.Context, tenantID uuid.UUID, itm *ent.Item, price float64, scope string) (*ItemDTO, error) {
	collapseMin := itm.MinSellingPrice != nil &&
		(*itm.MinSellingPrice > price ||
			(itm.MaxSellingPrice != nil && *itm.MinSellingPrice == *itm.MaxSellingPrice))

	prevMaxPrice := itm.MaxSellingPrice

	upd := s.client.Item.UpdateOneID(itm.ID).SetMaxSellingPrice(price)
	if collapseMin {
		upd = upd.SetMinSellingPrice(price)
	}
	itm, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: set selling price for %q: %w", itm.Sku, err)
	}

	minP := 0.0
	if collapseMin {
		minP = price
	}
	if err := s.EnsureGuardrailTierPrices(ctx, tenantID, itm.ID, price, minP); err != nil {
		return nil, fmt.Errorf("items: upsert tier prices for %q: %w", itm.Sku, err)
	}

	if s.auditSvc != nil && !floatPtrEqual(prevMaxPrice, &price) {
		s.auditSvc.Record(ctx, audit.Entry{
			TenantID:    tenantID,
			ActorUserID: actorFromContext(ctx),
			Action:      "item.selling_price_changed",
			EntityType:  "item",
			EntityID:    itm.ID.String(),
			Before:      map[string]any{"sku": itm.Sku, "price": prevMaxPrice},
			After:       map[string]any{"sku": itm.Sku, "price": price, "scope": scope},
		})
	}

	// Publish inventory.item.updated so POS/ordering catalog sync picks up the new price —
	// this was the root cause of "I changed the price but POS/treasury never reflects it":
	// a price-only change previously wrote no event at all, so the catalog-version fingerprint
	// POS polls never moved. A short-lived tx is used only for the outbox row (the price write
	// above already committed); a publish failure here is logged, never rolled back into the
	// price change itself — the item is correctly priced either way.
	if !floatPtrEqual(prevMaxPrice, &price) {
		if tx, txErr := s.client.Tx(ctx); txErr == nil {
			if err := s.emitItemUpdatedEvent(ctx, tx, tenantID, itm); err != nil {
				_ = tx.Rollback()
				s.log.Warn("setSellingPrice: emit item.updated failed", zap.String("sku", itm.Sku), zap.Error(err))
			} else if err := tx.Commit(); err != nil {
				s.log.Warn("setSellingPrice: commit item.updated event failed", zap.String("sku", itm.Sku), zap.Error(err))
			}
		}
	}

	return s.mapToDTO(itm), nil
}

// SchedulePendingPriceChange queues a selling-price change that must apply only to stock
// received from now on — old stock (any active cost layer with received_at before
// triggerBefore) keeps selling at its current price until it's gone. No batch-priced checkout
// is needed: promotion is lazy, see PromotePendingPriceChanges. Idempotent per
// goodsReceiptLineID: re-posting the same GRN line never queues a duplicate row.
func (s *Service) SchedulePendingPriceChange(ctx context.Context, tx *ent.Tx, tenantID, itemID uuid.UUID, newPrice float64, triggerBefore time.Time, reason string, actorID uuid.UUID, goodsReceiptLineID *uuid.UUID) error {
	if goodsReceiptLineID != nil {
		exists, err := tx.PendingPriceChange.Query().
			Where(pendingpricechange.GoodsReceiptLineID(*goodsReceiptLineID)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("items: check existing pending price change: %w", err)
		}
		if exists {
			return nil // idempotent re-post — already queued
		}
	}
	create := tx.PendingPriceChange.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetNewPrice(newPrice).
		SetTriggerBefore(triggerBefore).
		SetReason(reason)
	if actorID != uuid.Nil {
		create = create.SetCreatedBy(actorID)
	}
	if goodsReceiptLineID != nil {
		create = create.SetGoodsReceiptLineID(*goodsReceiptLineID)
	}
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("items: schedule pending price change: %w", err)
	}

	if s.auditSvc != nil {
		sku := ""
		if itm, iErr := tx.Item.Get(ctx, itemID); iErr == nil {
			sku = itm.Sku
		}
		s.auditSvc.Record(ctx, audit.Entry{
			TenantID:    tenantID,
			ActorUserID: actorID,
			Action:      "item.selling_price_changed",
			EntityType:  "item",
			EntityID:    itemID.String(),
			Reason:      reason,
			After:       map[string]any{"sku": sku, "pending_price": newPrice, "scope": "new_stock_only", "trigger_before": triggerBefore},
		})
	}
	return nil
}

// PromotePendingPriceChanges applies any queued "new_stock_only" price changes for the given
// items whose trigger has fired — every cost layer that existed before the change was requested
// has depleted, tenant-wide (a single shelf price is shared across every outlet/warehouse, so
// leftover old stock anywhere means it's not time yet). Cheap no-op when none of the items have
// a pending row. Best-effort: called from the price-resolution paths (GetItemPrice,
// ListAllItemPricing) so promotion happens lazily, exactly when it matters, with no scheduler
// needed; a failure here never blocks price resolution.
func (s *Service) PromotePendingPriceChanges(ctx context.Context, tenantID uuid.UUID, itemIDs []uuid.UUID) {
	if len(itemIDs) == 0 {
		return
	}
	pending, err := s.client.PendingPriceChange.Query().
		Where(pendingpricechange.TenantID(tenantID), pendingpricechange.ItemIDIn(itemIDs...), pendingpricechange.StatusEQ(pendingpricechange.StatusPending)).
		All(ctx)
	if err != nil || len(pending) == 0 {
		return
	}
	for _, pc := range pending {
		stillOldStock, sErr := s.client.InventoryLot.Query().
			Where(entlot.TenantID(tenantID), entlot.ItemID(pc.ItemID), entlot.StatusEQ(entlot.StatusActive), entlot.QuantityGT(0), entlot.ReceivedAtLT(pc.TriggerBefore)).
			Exist(ctx)
		if sErr != nil || stillOldStock {
			continue // old stock remains (or the check failed) — not yet time to promote
		}
		// Compare-and-swap on (id, status=pending): only the caller that wins this flip
		// actually applies the price, so a concurrent promotion attempt can't double-apply.
		n, uErr := s.client.PendingPriceChange.Update().
			Where(pendingpricechange.ID(pc.ID), pendingpricechange.StatusEQ(pendingpricechange.StatusPending)).
			SetStatus(pendingpricechange.StatusApplied).
			SetAppliedAt(time.Now()).
			Save(ctx)
		if uErr != nil || n == 0 {
			continue
		}
		if _, err := s.SetSellingPriceByItemID(ctx, tenantID, pc.ItemID, pc.NewPrice, "new_stock_only_applied"); err != nil {
			s.log.Warn("promote pending price change: apply failed",
				zap.String("item_id", pc.ItemID.String()), zap.Error(err))
		}
	}
}

// upsertItemTierPrice upserts the all-outlets (outlet_id IS NULL) ItemPricing row for the given
// item + tier at `price`, reactivating it if it was soft-deleted.
//
// NOTE (2026-08): production carried two STALE unique constraints
// (tenant_id,item_id,pricing_tier_id) and (...,outlet_id) left over from an earlier schema
// iteration, predating effective_from being added to the real composite unique index. Ent's
// online auto-migrate never drops old indexes/constraints by default, so they silently survived
// every schema change since and made the second price change ever made to an item fail with
// SQLSTATE 23505 — the true root cause of "I changed the price and POS never reflects it": this
// insert errored out BEFORE setSellingPrice ever reached the item.updated event publish. Fixed by
// dropping the two stale constraints directly (see the accompanying Atlas migration) — this
// function's logic was always correct for the schema as actually declared in ent/schema/*.go.
func (s *Service) upsertItemTierPrice(ctx context.Context, tenantID, itemID, tierID uuid.UUID, price float64) error {
	existing, qErr := s.client.ItemPricing.Query().
		Where(
			itempricing.TenantID(tenantID),
			itempricing.ItemID(itemID),
			itempricing.PricingTierID(tierID),
			itempricing.OutletIDIsNil(),
			itempricing.IsActive(true),
		).
		First(ctx)
	if qErr == nil && existing != nil {
		if existing.Price == price {
			return nil // no real change — don't spam a history row
		}
		now := time.Now()
		if _, err := existing.Update().SetIsActive(false).SetEffectiveTo(now).Save(ctx); err != nil {
			return fmt.Errorf("items: close out prior tier price: %w", err)
		}
		_, err := s.client.ItemPricing.Create().
			SetTenantID(tenantID).
			SetItemID(itemID).
			SetPricingTierID(tierID).
			SetPrice(price).
			SetCurrency(existing.Currency).
			SetTierBasis(existing.TierBasis).
			SetEffectiveFrom(now).
			SetIsActive(true).
			Save(ctx)
		return err
	}
	_, err := s.client.ItemPricing.Create().
		SetTenantID(tenantID).
		SetItemID(itemID).
		SetPricingTierID(tierID).
		SetPrice(price).
		SetCurrency("KES").
		SetIsActive(true).
		Save(ctx)
	return err
}

// recipeSellingPrices maps RECIPE item_id → selling price. Resolution order:
// explicit recipe selling_price → recipe suggested_price → cost_per_portion at the recipe/tenant
// target margin (so a fully-costed menu item is NEVER silently priced 0 and hidden on the POS — the
// merchant can still override with an explicit price/tier). cfg may be nil.
func (s *Service) recipeSellingPrices(ctx context.Context, tenantID uuid.UUID, cfg *ent.TenantInventoryConfig, itemIDs []uuid.UUID) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	if len(itemIDs) == 0 {
		return out
	}
	recs, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.IsActive(true), recipe.ItemIDIn(itemIDs...)).
		All(ctx)
	if err != nil {
		return out
	}
	for _, r := range recs {
		if r.ItemID == nil {
			continue
		}
		switch {
		case r.SellingPrice != nil && *r.SellingPrice > 0:
			out[*r.ItemID] = *r.SellingPrice
		case r.SuggestedPrice != nil && *r.SuggestedPrice > 0:
			if _, ok := out[*r.ItemID]; !ok {
				out[*r.ItemID] = *r.SuggestedPrice
			}
		case r.CostPerPortion != nil && *r.CostPerPortion > 0:
			// Derive a sensible menu price from plate cost at the target margin (recipe override →
			// tenant default → 70% margin ≈ 30% food cost, the industry norm). Prevents costed
			// recipes from defaulting to KES 0 / unavailable.
			margin := 70.0
			if r.TargetMarginPercent != nil && *r.TargetMarginPercent > 0 && *r.TargetMarginPercent < 100 {
				margin = *r.TargetMarginPercent
			} else if cfg != nil && cfg.DefaultTargetMarginPercent != nil && *cfg.DefaultTargetMarginPercent > 0 && *cfg.DefaultTargetMarginPercent < 100 {
				margin = *cfg.DefaultTargetMarginPercent
			}
			if _, ok := out[*r.ItemID]; !ok {
				out[*r.ItemID] = *r.CostPerPortion / (1 - margin/100)
			}
		}
	}
	return out
}

// recipeCostPerPortion maps RECIPE item_id → the recipe's actual production cost per portion
// (Recipe.cost_per_portion, computed by RecalculateRecipeCosts from ingredient cost_prices —
// see recipes/costing.go). This is the ONLY place a RECIPE item's true cost lives: it is never
// written back onto the linked Item row, so Item.cost_price stays nil for every recipe dish.
func (s *Service) recipeCostPerPortion(ctx context.Context, tenantID uuid.UUID, itemIDs []uuid.UUID) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	if len(itemIDs) == 0 {
		return out
	}
	recs, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.IsActive(true), recipe.ItemIDIn(itemIDs...)).
		All(ctx)
	if err != nil {
		return out
	}
	for _, r := range recs {
		if r.ItemID == nil || r.CostPerPortion == nil || *r.CostPerPortion <= 0 {
			continue
		}
		out[*r.ItemID] = *r.CostPerPortion
	}
	return out
}

// defaultTierPrices maps item_id → price from the tenant's default pricing tier, falling back
// to any active tier's price when no default tier is configured.
func (s *Service) defaultTierPrices(ctx context.Context, tenantID uuid.UUID, itemIDs []uuid.UUID) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	if len(itemIDs) == 0 {
		return out
	}
	var defaultTierID uuid.UUID
	if t, err := s.client.PricingTier.Query().
		Where(pricingtier.TenantID(tenantID), pricingtier.IsDefault(true), pricingtier.IsActive(true)).
		First(ctx); err == nil {
		defaultTierID = t.ID
	}
	prices, err := s.client.ItemPricing.Query().
		Where(itempricing.TenantID(tenantID), itempricing.IsActive(true), itempricing.ItemIDIn(itemIDs...)).
		All(ctx)
	if err != nil {
		return out
	}
	firstSeen := map[uuid.UUID]float64{}
	for _, p := range prices {
		if p.Price <= 0 {
			continue
		}
		if _, ok := firstSeen[p.ItemID]; !ok {
			firstSeen[p.ItemID] = p.Price
		}
		if defaultTierID != uuid.Nil && p.PricingTierID == defaultTierID {
			out[p.ItemID] = p.Price // default tier wins
		}
	}
	for id, p := range firstSeen {
		if _, ok := out[id]; !ok {
			out[id] = p
		}
	}
	return out
}

// resolveVATRate resolves the VAT rate (%) + code via the treasury resolver; (0, preferredCode)
// when no resolver is configured or treasury is unavailable.
// vatSuppressed reports whether VAT must NOT be charged because the business isn't
// VAT-registered. Permissive: false (charge VAT) unless the resolver affirmatively says
// the tenant is not VAT-active. Uses an optional interface so resolver mocks need not change.
func (s *Service) vatSuppressed(ctx context.Context, tenantID uuid.UUID) bool {
	if s.taxResolver == nil {
		return false
	}
	if va, ok := s.taxResolver.(interface {
		VATActive(context.Context, uuid.UUID) bool
	}); ok {
		return !va.VATActive(ctx, tenantID)
	}
	return false
}

func (s *Service) resolveVATRate(ctx context.Context, tenantID uuid.UUID, preferredCode string) (float64, string) {
	if s.taxResolver == nil {
		return 0, preferredCode
	}
	if r, c, ok := s.taxResolver.ResolveVATRate(ctx, tenantID, preferredCode); ok {
		return r, c
	}
	return 0, preferredCode
}
