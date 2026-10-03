package items

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/inventorylot"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/itempricing"
	"github.com/bengobox/inventory-service/internal/ent/reservation"
	entschema "github.com/bengobox/inventory-service/internal/ent/schema"
	entunit "github.com/bengobox/inventory-service/internal/ent/unit"
	"github.com/bengobox/inventory-service/internal/modules/units"
)

// Changing an item's stock unit used to rewrite only items.unit_id. Every quantity already held
// (balances, lots, reorder levels, open reservations) and every per-stock-unit money figure
// (cost, lot cost layers, selling guardrails, tier prices) silently kept the OLD unit's meaning:
// 5000 g on hand became "5000 kg", a 0.45/g cost became "0.45/kg". On urban-loft (2026-09-23..25)
// that broke ~15 ingredients at once and led to 943 manual corrections. The helpers below rescale
// all of it atomically in the same transaction as the unit change.

// StockUnitChangeError is returned when a stock-unit change crosses dimensions (g to pc) with
// no conversion and the item still holds stock: the caller must supply how many old units make
// one new unit (ItemDTO.RescaleOldPerNew), otherwise quantities can't be carried over safely.
type StockUnitChangeError struct {
	From, To string
}

func (e *StockUnitChangeError) Error() string {
	return fmt.Sprintf("changing the stock unit from %s to %s needs a conversion: say how many %s make one %s", e.From, e.To, e.From, e.To)
}

// unitRescale describes an approved stock-unit change. Factor is NEW units per OLD unit
// (g to kg: 0.001): quantities are multiplied by it, per-unit money divided by it.
type unitRescale struct {
	From, To *ent.Unit
	Factor   float64
}

// planStockUnitChange decides whether this update changes the stock unit and, if so, the
// factor to carry quantities and prices across. Returns nil when nothing needs rescaling
// (unit unchanged, no previous unit, or a cross-dimension change on an item holding no stock).
func (s *Service) planStockUnitChange(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, prev *ent.Item, dto *ItemDTO) (*unitRescale, error) {
	if prev == nil || dto.UnitID == nil || prev.UnitID == nil || *dto.UnitID == *prev.UnitID {
		return nil, nil
	}
	from := prev.Edges.Units
	if from == nil {
		return nil, nil // legacy item with a dangling unit: nothing meaningful to convert from
	}
	to, err := tx.Unit.Query().Where(entunit.ID(*dto.UnitID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: load new stock unit: %w", err)
	}
	if f, ok := units.Convert(1, from.Abbreviation, to.Abbreviation); ok && f > 0 {
		return &unitRescale{From: from, To: to, Factor: f}, nil
	}
	if dto.RescaleOldPerNew != nil && *dto.RescaleOldPerNew > 0 {
		return &unitRescale{From: from, To: to, Factor: 1 / *dto.RescaleOldPerNew}, nil
	}
	held, err := holdsAnyStock(ctx, tx, tenantID, prev.ID)
	if err != nil {
		return nil, err
	}
	if held {
		return nil, &StockUnitChangeError{From: from.Abbreviation, To: to.Abbreviation}
	}
	return nil, nil
}

// holdsAnyStock reports whether any balance or lot of the item is non-zero.
func holdsAnyStock(ctx context.Context, tx *ent.Tx, tenantID, itemID uuid.UUID) (bool, error) {
	n, err := tx.InventoryBalance.Query().
		Where(
			inventorybalance.TenantID(tenantID),
			inventorybalance.ItemID(itemID),
			inventorybalance.Or(
				inventorybalance.OnHandNEQ(0),
				inventorybalance.ReservedNEQ(0),
			),
		).
		Exist(ctx)
	if err != nil || n {
		return n, err
	}
	return tx.InventoryLot.Query().
		Where(inventorylot.TenantID(tenantID), inventorylot.ItemID(itemID), inventorylot.QuantityNEQ(0)).
		Exist(ctx)
}

// rescaleDTOForUnitChange rewrites the request's per-stock-unit money and pack-size fields so
// they keep the same real-world meaning in the new unit. A field the client changed in the same
// request is taken as already expressed in the new unit and left alone; a field it sent back
// unchanged (or omitted) is converted from the stored value. Example (g to kg): cost 0.45/g
// becomes 450/kg; purchase "450 per 1000 g" becomes "450 per 1 kg" (price kept, pack size and
// label rescaled).
func rescaleDTOForUnitChange(prev *ent.Item, dto *ItemDTO, r *unitRescale) {
	perUnit := func(dtoVal, prevVal *float64) *float64 {
		if prevVal == nil || (dtoVal != nil && !floatPtrEqual(dtoVal, prevVal)) {
			return dtoVal
		}
		v := *prevVal / r.Factor
		return &v
	}
	dto.CostPrice = perUnit(dto.CostPrice, prev.CostPrice)
	if prev.Type != item.TypeRECIPE {
		// A recipe item is priced per portion, not per stock unit: its selling guardrails stay.
		dto.MinSellingPrice = perUnit(dto.MinSellingPrice, prev.MinSellingPrice)
		dto.MaxSellingPrice = perUnit(dto.MaxSellingPrice, prev.MaxSellingPrice)
	}
	if prev.PurchasePackSize != nil && (dto.PurchasePackSize == nil || floatPtrEqual(dto.PurchasePackSize, prev.PurchasePackSize)) {
		v := *prev.PurchasePackSize * r.Factor
		dto.PurchasePackSize = &v
		if dto.PurchaseUnit == "" || dto.PurchaseUnit == prev.PurchaseUnit {
			dto.PurchaseUnit = rewritePurchaseUnit(prev.PurchaseUnit, r.From.Abbreviation, r.To.Abbreviation, r.Factor)
		}
	}
}

// rewritePurchaseUnit converts a purchase-unit label written in the old stock unit ("1000 g",
// "g", "5 kg bag") to the new one ("1 kg", "kg", "5000 g bag"). Labels that don't mention the
// old unit (a "tin", "crate") are returned unchanged.
func rewritePurchaseUnit(label, fromAbbr, toAbbr string, factor float64) string {
	fields := strings.Fields(label)
	norm := units.NormalizeUnit(fromAbbr)
	for i, f := range fields {
		if units.NormalizeUnit(f) != norm {
			continue
		}
		fields[i] = toAbbr
		if i > 0 {
			if n, err := strconv.ParseFloat(fields[i-1], 64); err == nil {
				fields[i-1] = strconv.FormatFloat(roundTo(n*factor, 6), 'f', -1, 64)
			}
		} else {
			// Bare unit ("g"): one old unit is factor new units ("0.001 kg").
			if factor != 1 {
				fields[i] = strconv.FormatFloat(roundTo(factor, 6), 'f', -1, 64) + " " + toAbbr
			}
		}
		return strings.Join(fields, " ")
	}
	return label
}

func roundTo(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// scaleReorder converts an integer reorder level/quantity, never letting a positive policy
// collapse to 0 (which would disable the reorder alert).
func scaleReorder(v int, factor float64) int {
	if v <= 0 {
		return v
	}
	return max(1, int(math.Round(float64(v)*factor)))
}

// applyStockUnitRescale converts every stored quantity and per-stock-unit price of the item in
// the caller's transaction. Returns a summary for the audit trail.
func (s *Service) applyStockUnitRescale(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, prev *ent.Item, r *unitRescale) (map[string]any, error) {
	f := r.Factor
	label := strings.ToUpper(r.To.Name)
	bals, err := tx.InventoryBalance.Query().
		Where(inventorybalance.TenantID(tenantID), inventorybalance.ItemID(prev.ID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: load balances for rescale: %w", err)
	}
	for _, b := range bals {
		if _, err := tx.InventoryBalance.UpdateOneID(b.ID).
			SetOnHand(roundTo(b.OnHand*f, 4)).
			SetAvailable(roundTo(b.Available*f, 4)).
			SetReserved(roundTo(b.Reserved*f, 4)).
			SetReorderLevel(scaleReorder(b.ReorderLevel, f)).
			SetReorderQuantity(scaleReorder(b.ReorderQuantity, f)).
			SetUnitOfMeasure(label).
			Save(ctx); err != nil {
			return nil, fmt.Errorf("items: rescale balance: %w", err)
		}
	}

	lots, err := tx.InventoryLot.Query().
		Where(inventorylot.TenantID(tenantID), inventorylot.ItemID(prev.ID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("items: load lots for rescale: %w", err)
	}
	for _, l := range lots {
		upd := tx.InventoryLot.UpdateOneID(l.ID).SetQuantity(roundTo(l.Quantity*f, 4))
		if l.CostPrice != nil {
			upd = upd.SetCostPrice(roundTo(*l.CostPrice/f, 6))
		}
		if _, err := upd.Save(ctx); err != nil {
			return nil, fmt.Errorf("items: rescale lot: %w", err)
		}
	}

	pricesScaled := 0
	if prev.Type != item.TypeRECIPE {
		pricings, perr := tx.ItemPricing.Query().
			Where(itempricing.TenantID(tenantID), itempricing.ItemID(prev.ID), itempricing.IsActive(true)).
			All(ctx)
		if perr != nil {
			return nil, fmt.Errorf("items: load tier prices for rescale: %w", perr)
		}
		for _, p := range pricings {
			if _, err := tx.ItemPricing.UpdateOneID(p.ID).SetPrice(roundTo(p.Price/f, 4)).Save(ctx); err != nil {
				return nil, fmt.Errorf("items: rescale tier price: %w", err)
			}
			pricesScaled++
		}
	}

	resvScaled, err := rescaleOpenReservations(ctx, tx, tenantID, prev.Sku, f)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"sku":                  prev.Sku,
		"from_unit":            r.From.Abbreviation,
		"to_unit":              r.To.Abbreviation,
		"new_units_per_old":    f,
		"balances_rescaled":    len(bals),
		"lots_rescaled":        len(lots),
		"tier_prices_rescaled": pricesScaled,
		"reservations_touched": resvScaled,
	}, nil
}

// rescaleOpenReservations converts the held quantities of this SKU on pending/confirmed
// reservations so a later release/consume moves the right amount in the new unit.
func rescaleOpenReservations(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, sku string, f float64) (int, error) {
	open, err := tx.Reservation.Query().
		Where(reservation.TenantID(tenantID), reservation.StatusIn("pending", "confirmed")).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("items: load open reservations for rescale: %w", err)
	}
	touched := 0
	for _, resv := range open {
		changed := false
		lines := make([]entschema.ReservedItemJSON, len(resv.Items))
		for i, ri := range resv.Items {
			if ri.SKU == sku && !ri.Composite {
				ri.RequestedQty = roundTo(ri.RequestedQty*f, 4)
				ri.ReservedQty = roundTo(ri.ReservedQty*f, 4)
				ri.AvailableQty = roundTo(ri.AvailableQty*f, 4)
				changed = true
			}
			lines[i] = ri
		}
		if !changed {
			continue
		}
		if _, err := tx.Reservation.UpdateOneID(resv.ID).SetItems(lines).Save(ctx); err != nil {
			return touched, fmt.Errorf("items: rescale reservation: %w", err)
		}
		touched++
	}
	return touched, nil
}
