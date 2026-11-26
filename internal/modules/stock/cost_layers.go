package stock

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	"github.com/bengobox/inventory-service/internal/ent/item"
)

// CostLayerIn is one batch of units entering a warehouse at a known cost. Every stock-in path
// that does not come from a goods receipt (adjustments, opening stock, transfers, relocations,
// reversals, returns, direct job purchases) goes through AddCostLayer with one of these, so the
// layer table keeps mirroring the balance and a later sale is costed from a real layer instead of
// falling back to the item's standard cost.
type CostLayerIn struct {
	ItemID      uuid.UUID
	WarehouseID uuid.UUID
	// LotNumber names the layer. A real lot keeps its supplier batch number; an internal layer
	// gets a generated name. Units of a lot that already sits in another warehouse land under
	// "<lot>@<warehouse prefix>" because lot numbers are unique per item, not per warehouse.
	LotNumber string
	Quantity  float64
	// UnitCost is the cost per stock unit. Nil records the units without a cost, the same way
	// the opening-balance backfill does for an item that was never priced.
	UnitCost *float64
	// ReceivedAt orders the layer for fifo/lifo draws. Moved stock keeps the source layer's date
	// so a transfer never makes old stock look new.
	ReceivedAt        time.Time
	ExpiryDate        *time.Time
	SupplierReference string
	// RealLot marks a supplier batch (shown on the Lots page, labels and expiry alerts). False
	// creates an internal cost layer (is_cost_layer), which carries no lot identity.
	RealLot bool
}

// lotWarehouseSep separates a lot number from the warehouse prefix added when the same lot
// lands in a second warehouse.
const lotWarehouseSep = "@"

// baseLotNumber strips the warehouse suffix a moved lot carries, so a lot moved A to B to C is
// named "<lot>@<C>" rather than growing a suffix per hop.
func baseLotNumber(lotNumber string) string {
	if i := strings.Index(lotNumber, lotWarehouseSep); i > 0 {
		return lotNumber[:i]
	}
	return lotNumber
}

// warehouseLotNumber is the name a lot takes in a warehouse other than the one that owns its
// plain number.
func warehouseLotNumber(lotNumber string, warehouseID uuid.UUID) string {
	return baseLotNumber(lotNumber) + lotWarehouseSep + warehouseID.String()[:8]
}

// blendUnitCost is the quantity-weighted cost after adding addQty at addCost to haveQty at
// haveCost. A side with no cost takes the other side's cost, so topping up an unpriced layer
// with priced units prices it instead of leaving it blank. Nil only when neither side has one.
func blendUnitCost(haveQty float64, haveCost *float64, addQty float64, addCost *float64) *float64 {
	switch {
	case haveCost == nil && addCost == nil:
		return nil
	case addCost == nil:
		c := *haveCost
		return &c
	case haveCost == nil || haveQty <= 0:
		c := *addCost
		return &c
	case addQty <= 0:
		c := *haveCost
		return &c
	}
	c := round4((haveQty**haveCost + addQty**addCost) / (haveQty + addQty))
	return &c
}

// AddCostLayer records in.Quantity units entering in.WarehouseID. When a layer with the same
// name already sits in that warehouse it is topped up (cost blended by quantity, a depleted
// layer reactivated) instead of duplicated, so moving the same lot twice, or restoring units to
// the lot they came from, keeps one row. Runs inside the caller's transaction, so a retried
// caller that rolls back never leaves a layer behind.
func AddCostLayer(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, in CostLayerIn) (*ent.InventoryLot, error) {
	if in.Quantity <= 0 || in.LotNumber == "" {
		return nil, nil
	}
	base := baseLotNumber(in.LotNumber)
	local := warehouseLotNumber(base, in.WarehouseID)
	existing, err := tx.InventoryLot.Query().
		Where(entlot.TenantID(tenantID), entlot.ItemID(in.ItemID), entlot.LotNumberIn(base, local)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: cost layer lookup %s: %w", base, err)
	}
	name := base
	for _, l := range existing {
		if l.WarehouseID == in.WarehouseID {
			return topUpLot(ctx, tx, l, in.Quantity, in.UnitCost, in.ExpiryDate)
		}
		if l.LotNumber == base {
			name = local
		}
	}
	receivedAt := in.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	create := tx.InventoryLot.Create().
		SetTenantID(tenantID).
		SetItemID(in.ItemID).
		SetWarehouseID(in.WarehouseID).
		SetLotNumber(name).
		SetQuantity(round4(in.Quantity)).
		SetStatus(entlot.StatusActive).
		SetIsCostLayer(!in.RealLot).
		SetReceivedAt(receivedAt).
		SetNillableCostPrice(in.UnitCost).
		SetNillableExpiryDate(in.ExpiryDate)
	if in.SupplierReference != "" {
		create = create.SetSupplierReference(in.SupplierReference)
	}
	lot, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: create cost layer %s: %w", name, err)
	}
	return lot, nil
}

// topUpLot adds qty at unitCost to an existing layer. A depleted layer becomes active again; an
// expired or recalled one keeps its status, since units going back into a recalled batch are
// still recalled.
func topUpLot(ctx context.Context, tx *ent.Tx, lot *ent.InventoryLot, qty float64, unitCost *float64, expiry *time.Time) (*ent.InventoryLot, error) {
	have := lot.Quantity
	if have < 0 {
		have = 0
	}
	upd := tx.InventoryLot.UpdateOne(lot).
		SetQuantity(round4(have + qty)).
		SetNillableCostPrice(blendUnitCost(have, lot.CostPrice, qty, unitCost))
	if lot.Status == entlot.StatusDepleted {
		upd = upd.SetStatus(entlot.StatusActive)
	}
	if lot.ExpiryDate == nil && expiry != nil {
		upd = upd.SetExpiryDate(*expiry)
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("stock: top up cost layer %s: %w", lot.LotNumber, err)
	}
	return saved, nil
}

// RestoreLot puts qty back into the layer a consumption drew it from, at the cost the
// consumption charged. Returns false when that layer no longer exists, so the caller can record
// the units as a new layer instead.
func RestoreLot(ctx context.Context, tx *ent.Tx, tenantID, lotID uuid.UUID, qty float64, unitCost *float64) (bool, error) {
	if qty <= 0 {
		return true, nil
	}
	lot, err := tx.InventoryLot.Query().Where(entlot.TenantID(tenantID), entlot.ID(lotID)).Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stock: restore lot %s: %w", lotID, err)
	}
	if _, err := topUpLot(ctx, tx, lot, qty, unitCost, nil); err != nil {
		return false, err
	}
	return true, nil
}

// positiveGain is how much physically present stock a balance gained going from before to
// after: the rise of the positive part only. Units that merely pay down negative on-hand (an
// oversell debt) are not on the shelf and get no layer.
func positiveGain(before, after float64) float64 {
	b, a := before, after
	if b < 0 {
		b = 0
	}
	if a < 0 {
		a = 0
	}
	if a <= b {
		return 0
	}
	return round4(a - b)
}

// restoreLineLayer returns qty of a reversed consumption line to the layer the sale drew it
// from, at the cost the sale charged, so the books get back exactly the value that left. When
// that layer is gone the units become a new internal layer at the same cost, named after the
// line so a second partial reversal tops it up. A line with no lot is the uncovered part of a
// sale that ran past its layers: it never took a layer, so it gives none back.
func restoreLineLayer(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, l *ent.ConsumptionLine, warehouseID uuid.UUID, qty float64) error {
	if qty <= 0 || l.LotID == nil {
		return nil
	}
	cost := l.UnitCost
	restored, err := RestoreLot(ctx, tx, tenantID, *l.LotID, qty, &cost)
	if err != nil || restored {
		return err
	}
	_, err = AddCostLayer(ctx, tx, tenantID, CostLayerIn{
		ItemID: l.IngredientItemID, WarehouseID: warehouseID, LotNumber: "REV-" + l.ID.String(),
		Quantity: qty, UnitCost: &cost, ReceivedAt: l.ConsumedAt, SupplierReference: "consumption-reversal",
	})
	return err
}

// allocateLanded spreads landQty over the drawn layers in draw order, so a transfer that lost
// units in transit lands the earliest-drawn layers in full and drops the remainder. The returned
// slice lines up with drawn; anything landQty exceeds the draws by is left for the caller.
func allocateLanded(drawn []LotConsumption, landQty float64) []float64 {
	out := make([]float64, len(drawn))
	left := landQty
	for i, d := range drawn {
		if left <= 0 {
			break
		}
		take := d.QtyTaken
		if take > left {
			take = left
		}
		out[i] = round4(take)
		left -= take
	}
	return out
}

// landLayers creates destination layers for units drawn from source layers, each at its source
// cost, with lot identity and received date carried over. Returns how much of landQty the drawn
// layers covered.
func landLayers(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, drawn []LotConsumption, landQty float64, ref string) (float64, error) {
	var covered float64
	for i, qty := range allocateLanded(drawn, landQty) {
		if qty <= 0 {
			continue
		}
		d := drawn[i]
		in := CostLayerIn{
			ItemID:            itemID,
			WarehouseID:       warehouseID,
			LotNumber:         d.LotNumber,
			Quantity:          qty,
			UnitCost:          d.UnitCost,
			ExpiryDate:        d.ExpiryDate,
			SupplierReference: ref,
			RealLot:           !d.IsCostLayer,
		}
		if d.ReceivedAt != nil {
			in.ReceivedAt = *d.ReceivedAt
		}
		if _, err := AddCostLayer(ctx, tx, tenantID, in); err != nil {
			return covered, err
		}
		covered += qty
	}
	return round4(covered), nil
}

// standardCostLayer records qty at the item's current standard cost per stock unit. Used for
// stock that enters with no purchase price of its own (a found or counted unit, an opening
// balance, the uncovered part of a move), which is the same cost a sale of it would fall back
// to anyway, so valuation does not change, only the layer table catches up with the balance.
func (s *Service) standardCostLayer(ctx context.Context, tx *ent.Tx, tenantID, warehouseID uuid.UUID, itm *ent.Item, qty float64, lotNumber string, receivedAt time.Time, ref string) error {
	if qty <= 0 || itm == nil {
		return nil
	}
	if itm.Edges.Units == nil && itm.UnitID != nil {
		if u, err := tx.Unit.Get(ctx, *itm.UnitID); err == nil {
			itm.Edges.Units = u
		}
	}
	var cost *float64
	if c := itemCostPrice(itm); c > 0 {
		cost = &c
	}
	_, err := AddCostLayer(ctx, tx, tenantID, CostLayerIn{
		ItemID: itm.ID, WarehouseID: warehouseID, LotNumber: lotNumber, Quantity: qty,
		UnitCost: cost, ReceivedAt: receivedAt, SupplierReference: ref,
	})
	return err
}

// MoveCostLayers moves qty units of an item's layers from one warehouse to another inside the
// caller's transaction: the source layers are drawn in the tenant's costing order (the same
// consumeLots a sale uses) and landQty of them is recreated at the destination at the costs
// drawn. landQty below drawQty drops the difference (lost in transit). Any part of landQty the
// source layers could not cover lands at the item's standard cost, so the destination layers
// always match what its balance gained. Implements transfers.CostLayerMover.
func (s *Service) MoveCostLayers(ctx context.Context, tx *ent.Tx, tenantID, itemID, fromWarehouseID, toWarehouseID uuid.UUID, drawQty, landQty float64, ref string) error {
	drawn := s.consumeLots(ctx, tx, tenantID, itemID, fromWarehouseID, drawQty, s.costingMethod(ctx, tenantID))
	return s.landDrawnLayers(ctx, tx, tenantID, itemID, toWarehouseID, drawn, landQty, "MOVE-"+uuid.NewString(), ref)
}

// landDrawnLayers lands drawn layers at a destination and tops up any uncovered remainder at
// the item's standard cost under remainderLot.
func (s *Service) landDrawnLayers(ctx context.Context, tx *ent.Tx, tenantID, itemID, warehouseID uuid.UUID, drawn []LotConsumption, landQty float64, remainderLot, ref string) error {
	if landQty <= 0 {
		return nil
	}
	covered, err := landLayers(ctx, tx, tenantID, itemID, warehouseID, drawn, landQty, ref)
	if err != nil {
		return err
	}
	rest := round4(landQty - covered)
	if rest <= 0 {
		return nil
	}
	itm, err := tx.Item.Query().Where(item.TenantID(tenantID), item.ID(itemID)).WithUnits().Only(ctx)
	if err != nil {
		return fmt.Errorf("stock: land cost layers: load item: %w", err)
	}
	return s.standardCostLayer(ctx, tx, tenantID, warehouseID, itm, rest, remainderLot, time.Now(), ref)
}

// adjustmentLayers records the units an upward adjustment added. Moved units (IncomingLayers)
// land at their source costs first. The rest enters as one layer: at CostLayerUnitCost when
// the caller knows what the units cost (a repair coming back, a direct purchase), else at the
// item's standard cost. Named after the adjustment, so the layer is traceable to its ledger row.
func (s *Service) adjustmentLayers(ctx context.Context, tx *ent.Tx, tenantID, warehouseID uuid.UUID, itm *ent.Item, adjID uuid.UUID, qty float64, at time.Time, req AdjustStockRequest) error {
	covered, err := landLayers(ctx, tx, tenantID, itm.ID, warehouseID, req.IncomingLayers, qty, req.Reference)
	if err != nil {
		return err
	}
	rest := round4(qty - covered)
	if rest <= 0 {
		return nil
	}
	if req.CostLayerUnitCost > 0 {
		cost := req.CostLayerUnitCost
		prefix := "ADJ-"
		if req.Reason == "service_in" {
			prefix = "SVC-" // units back from repair, named as before
		}
		if _, err := AddCostLayer(ctx, tx, tenantID, CostLayerIn{
			ItemID: itm.ID, WarehouseID: warehouseID, LotNumber: prefix + adjID.String(), Quantity: rest,
			UnitCost: &cost, ReceivedAt: at, SupplierReference: req.Reference,
		}); err != nil {
			return err
		}
		// A known cost differs from the running average, so a wavg tenant's layers reblend.
		s.reblendWavg(ctx, tx, tenantID, itm.ID, "adjustment")
		return nil
	}
	if req.LotNumber != "" {
		var cost *float64
		if c := itemCostPrice(itm); c > 0 {
			cost = &c
		}
		_, err := AddCostLayer(ctx, tx, tenantID, CostLayerIn{
			ItemID: itm.ID, WarehouseID: warehouseID, LotNumber: req.LotNumber, Quantity: rest,
			UnitCost: cost, ReceivedAt: at, ExpiryDate: req.LotExpiry, SupplierReference: req.Reference,
			RealLot: true,
		})
		return err
	}
	return s.standardCostLayer(ctx, tx, tenantID, warehouseID, itm, rest, "ADJ-"+adjID.String(), at, req.Reference)
}

// reblendWavg keeps a weighted-average tenant's open layers on one shared cost after units came
// back in at a cost of their own (a reversal at the sale's cost, a direct purchase at its price).
// fifo/lifo/fefo tenants keep each layer's own cost, so nothing runs for them. Best-effort: the
// stock movement already happened, and the next receipt reblends anyway.
func (s *Service) reblendWavg(ctx context.Context, tx *ent.Tx, tenantID, itemID uuid.UUID, source string) {
	if s.costingMethod(ctx, tenantID) != "wavg" {
		return
	}
	if _, err := s.RecomputeStandardCost(ctx, tx, tenantID, itemID, source); err != nil {
		s.log.Warn("cost layers: wavg reblend failed", zap.String("item_id", itemID.String()), zap.Error(err))
	}
}
