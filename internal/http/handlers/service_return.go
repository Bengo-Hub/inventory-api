package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	entpr "github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	entprline "github.com/bengobox/inventory-service/internal/ent/purchasereturnline"
	entstockadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// Service / repair destination of a purchase return: list filters, cost snapshot, receiving the
// goods back, and the open-orders summary. The pure rules live in service_return_plan.go.

// serviceSummaryCap bounds the open-order scan. Open repair orders are few by nature; the cap only
// protects against a pathological tenant and is reported through `truncated`.
const serviceSummaryCap = 2000

// returnLocationPredicate narrows returns to a warehouse or an outlet's warehouses. A return
// raised before warehouse capture carries only its goods receipt's warehouse, so those match too,
// through a subselect on goods_receipts rather than an id list that grows with the tenant.
func (h *InventoryExtrasHandler) returnLocationPredicate(ctx context.Context, tenantID uuid.UUID, qp url.Values) predicate.PurchaseReturn {
	var whIDs []uuid.UUID
	if id, e := uuid.Parse(qp.Get("warehouse_id")); e == nil {
		whIDs = []uuid.UUID{id}
	} else if oid, oe := uuid.Parse(qp.Get("outlet_id")); oe == nil {
		ids, err := h.orm.Warehouse.Query().
			Where(entwarehouse.TenantID(tenantID), entwarehouse.OutletIDEQ(oid)).IDs(ctx)
		if err != nil {
			h.log.Warn("purchase returns: outlet warehouse lookup failed", zap.Error(err))
		}
		whIDs = ids
		if whIDs == nil {
			whIDs = []uuid.UUID{}
		}
	}
	if whIDs == nil {
		return nil
	}
	args := make([]any, len(whIDs))
	for i, id := range whIDs {
		args[i] = id
	}
	grnWarehouse := predicate.PurchaseReturn(func(s *sql.Selector) {
		t := sql.Table(entgr.Table)
		sub := sql.Select(t.C(entgr.FieldID)).From(t).
			Where(sql.And(sql.EQ(t.C(entgr.FieldTenantID), tenantID), sql.In(t.C(entgr.FieldWarehouseID), args...)))
		s.Where(sql.In(s.C(entpr.FieldGoodsReceiptID), sub))
	})
	return entpr.Or(
		entpr.WarehouseIDIn(whIDs...),
		entpr.And(entpr.WarehouseIDIsNil(), grnWarehouse),
	)
}

// returnKindPredicates applies the destination / service_status / overdue list filters.
func returnKindPredicates(qp url.Values, today time.Time) ([]predicate.PurchaseReturn, *returnError) {
	var preds []predicate.PurchaseReturn
	switch d := strings.ToLower(strings.TrimSpace(qp.Get("destination"))); d {
	case "":
	case string(entpr.DestinationSupplier), string(entpr.DestinationService):
		preds = append(preds, entpr.DestinationEQ(entpr.Destination(d)))
	default:
		return nil, &returnError{http.StatusBadRequest, "INVALID_DESTINATION", "destination must be supplier or service"}
	}
	if raw := strings.TrimSpace(qp.Get("service_status")); raw != "" {
		var sts []entpr.ServiceStatus
		for _, p := range strings.Split(raw, ",") {
			st := entpr.ServiceStatus(strings.TrimSpace(p))
			if err := entpr.ServiceStatusValidator(st); err != nil {
				return nil, &returnError{http.StatusBadRequest, "INVALID_SERVICE_STATUS", "service_status must be out, partial, returned or written_off"}
			}
			sts = append(sts, st)
		}
		preds = append(preds, entpr.ServiceStatusIn(sts...))
	}
	if qp.Get("overdue") == "true" {
		preds = append(preds,
			entpr.DestinationEQ(entpr.DestinationService),
			entpr.ServiceStatusIn(entpr.ServiceStatusOut, entpr.ServiceStatusPartial),
			entpr.ExpectedBackDateLT(today))
	}
	return preds, nil
}

// fillServiceLineCosts sets a zero sub-total to quantity x the item's cost price, so a repair
// order always carries the cost value of what is away (used to restore the cost layer on return).
func (h *InventoryExtrasHandler) fillServiceLineCosts(ctx context.Context, tenantID uuid.UUID, lines []purchaseReturnLinePayload) []purchaseReturnLinePayload {
	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		if l.SubTotal == 0 {
			ids = append(ids, l.ItemID)
		}
	}
	if len(ids) == 0 {
		return lines
	}
	cost := map[uuid.UUID]float64{}
	if its, err := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IDIn(uniqueUUIDs(ids)...)).
		Select(entitem.FieldID, entitem.FieldCostPrice).All(ctx); err == nil {
		for _, it := range its {
			if it.CostPrice != nil {
				cost[it.ID] = *it.CostPrice
			}
		}
	}
	for i := range lines {
		if lines[i].SubTotal == 0 {
			lines[i].SubTotal = roundDecimal(lines[i].Quantity * cost[lines[i].ItemID])
		}
	}
	return lines
}

type serviceReceivePayload struct {
	// Date backdates the movement ("YYYY-MM-DD"); blank means now. Never a future date.
	Date *string `json:"date"`
	// WarehouseID is where the repaired goods are put back; blank means the location they left from.
	WarehouseID *uuid.UUID           `json:"warehouse_id"`
	Notes       string               `json:"notes"`
	Lines       []serviceReceiveLine `json:"lines"`
}

// ReceiveServiceReturn handles POST /inventory/purchase-returns/{returnID}/receive.
//
//	@Summary      Receive repaired goods back from a service provider
//	@Description  Restocks the returned units (and restores their cost layer) and writes off the units that could not be repaired. Only for destination=service orders that are out for repair.
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        returnID  path      string                 true  "Purchase return ID"
//	@Param        body      body      serviceReceivePayload  true  "Quantities received back per line"
//	@Success      200       {object}  purchaseReturnDTO
//	@Failure      400       {object}  map[string]string
//	@Failure      404       {object}  map[string]string
//	@Failure      409       {object}  map[string]string
//	@Failure      500       {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-returns/{returnID}/receive [post]
func (h *InventoryExtrasHandler) ReceiveServiceReturn(w http.ResponseWriter, r *http.Request) {
	tenantID, pr, ok := h.loadPurchaseReturn(w, r)
	if !ok {
		return
	}
	if pr.Destination != entpr.DestinationService {
		writeError(w, http.StatusBadRequest, "NOT_SERVICE_ORDER", "Only an order sent for repair can be received back")
		return
	}
	if pr.ServiceStatus == nil || (*pr.ServiceStatus != entpr.ServiceStatusOut && *pr.ServiceStatus != entpr.ServiceStatusPartial) {
		writeError(w, http.StatusConflict, "NOT_OUT_FOR_SERVICE", "This order is not out for repair. Approve it first, or it has already been closed")
		return
	}
	var req serviceReceivePayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	ctx := r.Context()
	adjDate := ""
	if req.Date != nil {
		adjDate = strings.TrimSpace(*req.Date)
	}
	if _, derr := stock.ResolveAdjustmentDate(adjDate, time.Now()); derr != nil {
		writeError(w, http.StatusBadRequest, "INVALID_DATE", derr.Error())
		return
	}

	lines, err := h.orm.PurchaseReturnLine.Query().
		Where(entprline.PurchaseReturnID(pr.ID), entprline.TenantID(tenantID)).All(ctx)
	if err != nil || len(lines) == 0 {
		writeError(w, http.StatusBadRequest, "NO_ITEMS", "This order has no items")
		return
	}
	states := make([]serviceLineState, len(lines))
	lineByID := make(map[uuid.UUID]int, len(lines))
	for i, l := range lines {
		states[i] = serviceLineState{ID: l.ID, Quantity: l.Quantity, Received: l.ReceivedBackQty, WrittenOff: l.WrittenOffQty}
		lineByID[l.ID] = i
	}
	entries, perr := planServiceReceive(states, req.Lines)
	if perr != nil {
		writeError(w, perr.status, perr.code, perr.msg)
		return
	}

	warehouseID := h.purchaseReturnWarehouse(ctx, tenantID, pr)
	if req.WarehouseID != nil && *req.WarehouseID != uuid.Nil {
		if ok, _ := h.orm.Warehouse.Query().
			Where(entwarehouse.TenantID(tenantID), entwarehouse.ID(*req.WarehouseID)).Exist(ctx); !ok {
			writeError(w, http.StatusBadRequest, "INVALID_WAREHOUSE", "The selected location was not found")
			return
		}
		warehouseID = *req.WarehouseID
	}
	if warehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "NO_WAREHOUSE", "No location found to put the goods back into")
		return
	}

	type itemInfo struct{ sku, name string }
	itemIDs := make([]uuid.UUID, 0, len(entries))
	for _, e := range entries {
		itemIDs = append(itemIDs, lines[lineByID[e.State.ID]].ItemID)
	}
	itemsByID := map[uuid.UUID]itemInfo{}
	if its, ierr := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.IDIn(uniqueUUIDs(itemIDs)...)).
		Select(entitem.FieldID, entitem.FieldSku, entitem.FieldName).All(ctx); ierr == nil {
		for _, it := range its {
			itemsByID[it.ID] = itemInfo{sku: it.Sku, name: it.Name}
		}
	}
	for _, id := range itemIDs {
		if itemsByID[id].sku == "" {
			writeError(w, http.StatusBadRequest, "UNKNOWN_ITEM", "A returned item no longer exists")
			return
		}
	}

	// Claim the quantities first, each line guarded on the values just read, so two people
	// receiving at once (or a double click) can never restock the same units twice.
	newStatus := serviceStatusFor(applyReceive(states, entries))
	tx, err := h.orm.Tx(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to receive the goods")
		return
	}
	for _, e := range entries {
		after := e.after()
		n, uerr := tx.PurchaseReturnLine.Update().
			Where(entprline.ID(e.State.ID), entprline.TenantID(tenantID),
				entprline.ReceivedBackQtyEQ(e.State.Received), entprline.WrittenOffQtyEQ(e.State.WrittenOff)).
			SetReceivedBackQty(after.Received).SetWrittenOffQty(after.WrittenOff).Save(ctx)
		if uerr != nil || n == 0 {
			_ = tx.Rollback()
			if uerr != nil {
				h.log.Error("service return: claim line failed", zap.Error(uerr))
				writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to receive the goods")
				return
			}
			writeError(w, http.StatusConflict, "CHANGED_CONCURRENTLY", "This order was just updated by someone else. Reload and try again")
			return
		}
	}
	if n, uerr := tx.PurchaseReturn.Update().
		Where(entpr.ID(pr.ID), entpr.TenantID(tenantID), entpr.ServiceStatusIn(entpr.ServiceStatusOut, entpr.ServiceStatusPartial)).
		SetServiceStatus(newStatus).Save(ctx); uerr != nil || n == 0 {
		_ = tx.Rollback()
		writeError(w, http.StatusConflict, "CHANGED_CONCURRENTLY", "This order was just updated by someone else. Reload and try again")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to receive the goods")
		return
	}

	provider := serviceProviderOf(pr.Metadata)
	actor := actorFromRequest(r)
	back := "Received back from repair"
	if provider != "" {
		back += " (" + provider + ")"
	}
	if n := strings.TrimSpace(req.Notes); n != "" {
		back += ": " + n
	}
	// Everything settled comes back into stock first (repaired AND unrepairable, at the cost it
	// left with), then the unrepairable units are written off as damaged, so the wastage reaches
	// the books through the normal damaged path with the right cost.
	moves := make([]stockMove, 0, len(entries)*2)
	for _, e := range entries {
		l := lines[lineByID[e.State.ID]]
		unitCost := 0.0
		if l.Quantity != 0 {
			unitCost = roundDecimal(l.SubTotal / l.Quantity)
		}
		moves = append(moves, stockMove{UnitCost: unitCost, Req: stock.AdjustStockRequest{
			SKU: itemsByID[l.ItemID].sku, Adjustment: roundDecimal(e.Returned + e.WrittenOff),
			Reason: string(entstockadj.ReasonServiceIn), Reference: pr.ReturnNumber, Notes: back,
			AdjustedBy: actor, WarehouseID: warehouseID, AdjustmentDate: adjDate, CostLayerUnitCost: unitCost,
		}})
	}
	for _, e := range entries {
		if e.WrittenOff <= 0 {
			continue
		}
		l := lines[lineByID[e.State.ID]]
		moves = append(moves, stockMove{Req: stock.AdjustStockRequest{
			SKU: itemsByID[l.ItemID].sku, Adjustment: -e.WrittenOff,
			Reason: string(entstockadj.ReasonDamaged), Reference: pr.ReturnNumber,
			Notes:      "Could not be repaired, written off (" + pr.ReturnNumber + ")",
			AdjustedBy: actor, WarehouseID: warehouseID, AdjustmentDate: adjDate,
		}})
	}
	if h.stockSvc != nil {
		failed, merr, uerr := runStockMoves(ctx, tenantID, moves, h.stockSvc.AdjustStock)
		if merr != nil {
			h.log.Error("service return: receive stock move failed",
				zap.String("return_number", pr.ReturnNumber), zap.String("sku", moves[failed].Req.SKU),
				zap.NamedError("stock_error", merr), zap.NamedError("reverse_error", uerr))
			if uerr != nil {
				writeError(w, http.StatusInternalServerError, "STOCK_IN_PARTIAL",
					"Some items could not be put back into stock and earlier ones could not be undone automatically. Check this order in the stock history before retrying.")
				return
			}
			h.revertServiceReceive(ctx, tenantID, pr, entries)
			writeError(w, http.StatusConflict, "STOCK_IN_FAILED", "The goods could not be put back into stock. Nothing was changed.")
			return
		}
	}

	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		l := lines[lineByID[e.State.ID]]
		items = append(items, map[string]any{
			"item_id": l.ItemID, "sku": itemsByID[l.ItemID].sku, "item_name": itemsByID[l.ItemID].name,
			"returned_qty": e.Returned, "written_off_qty": e.WrittenOff,
		})
	}
	outletID := ""
	if wh, werr := h.orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.ID(warehouseID)).Only(ctx); werr == nil && wh.OutletID != nil {
		outletID = wh.OutletID.String()
	}
	h.publishOutbox(ctx, tenantID, "purchase_return", pr.ID, "inventory.service_order.returned", map[string]any{
		"tenant_id": tenantID, "id": pr.ID, "return_number": pr.ReturnNumber,
		"service_status": string(newStatus), "service_provider": provider,
		"warehouse_id": warehouseID.String(), "outlet_id": outletID, "items": items,
	})

	updated, err := h.orm.PurchaseReturn.Get(ctx, pr.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "The goods were received but the order could not be reloaded")
		return
	}
	writeJSON(w, http.StatusOK, h.purchaseReturnsToDTOs(ctx, tenantID, []*ent.PurchaseReturn{updated})[0])
}

// revertServiceReceive restores the line quantities and status a failed receive had claimed.
func (h *InventoryExtrasHandler) revertServiceReceive(ctx context.Context, tenantID uuid.UUID, pr *ent.PurchaseReturn, entries []receiveEntry) {
	tx, err := h.orm.Tx(ctx)
	if err != nil {
		h.log.Error("service return: revert begin failed", zap.Error(err))
		return
	}
	for _, e := range entries {
		if _, uerr := tx.PurchaseReturnLine.Update().
			Where(entprline.ID(e.State.ID), entprline.TenantID(tenantID)).
			SetReceivedBackQty(e.State.Received).SetWrittenOffQty(e.State.WrittenOff).Save(ctx); uerr != nil {
			_ = tx.Rollback()
			h.log.Error("service return: revert line failed", zap.Error(uerr))
			return
		}
	}
	if pr.ServiceStatus != nil {
		if _, uerr := tx.PurchaseReturn.Update().
			Where(entpr.ID(pr.ID), entpr.TenantID(tenantID)).SetServiceStatus(*pr.ServiceStatus).Save(ctx); uerr != nil {
			_ = tx.Rollback()
			h.log.Error("service return: revert status failed", zap.Error(uerr))
			return
		}
	}
	if err := tx.Commit(); err != nil {
		h.log.Error("service return: revert commit failed", zap.Error(err))
	}
}

// serviceOrderRow and serviceLineRow are the minimal columns the summary needs.
type serviceOrderRow struct {
	ID       uuid.UUID
	Expected *time.Time
	Status   entpr.ServiceStatus
	Provider string
}

type serviceLineRow struct {
	ReturnID                                uuid.UUID
	Quantity, Received, WrittenOff, SubTotal float64
}

type serviceProviderStat struct {
	Provider string  `json:"provider"`
	Orders   int     `json:"orders"`
	Units    float64 `json:"units"`
}

// serviceSummary is the "at service" figure: what is away, what it is worth, and what is late.
type serviceSummary struct {
	OpenOrders       int                   `json:"open_orders"`
	UnitsOutstanding float64               `json:"units_outstanding"`
	CostOutstanding  float64               `json:"cost_outstanding"`
	OverdueOrders    int                   `json:"overdue_orders"`
	OverdueUnits     float64               `json:"overdue_units"`
	Providers        []serviceProviderStat `json:"providers"`
	Truncated        bool                  `json:"truncated"`
}

// buildServiceSummary aggregates open repair orders. Cost outstanding values the units still
// away at the cost they left with (line sub-total is that cost for a service order).
func buildServiceSummary(orders []serviceOrderRow, lines []serviceLineRow, today time.Time) serviceSummary {
	type agg struct{ units, cost float64 }
	perOrder := make(map[uuid.UUID]agg, len(orders))
	for _, l := range lines {
		out := roundDecimal(l.Quantity - l.Received - l.WrittenOff)
		if out <= 0 || l.Quantity <= 0 {
			continue
		}
		a := perOrder[l.ReturnID]
		a.units += out
		a.cost += l.SubTotal * out / l.Quantity
		perOrder[l.ReturnID] = a
	}
	sum := serviceSummary{Providers: []serviceProviderStat{}}
	provs := map[string]*serviceProviderStat{}
	for _, o := range orders {
		a := perOrder[o.ID]
		if a.units <= 0 {
			continue
		}
		sum.OpenOrders++
		sum.UnitsOutstanding += a.units
		sum.CostOutstanding += a.cost
		st := o.Status
		if isOverdue(&st, o.Expected, today) {
			sum.OverdueOrders++
			sum.OverdueUnits += a.units
		}
		name := o.Provider
		if name == "" {
			name = "Unspecified"
		}
		ps := provs[name]
		if ps == nil {
			ps = &serviceProviderStat{Provider: name}
			provs[name] = ps
		}
		ps.Orders++
		ps.Units += a.units
	}
	for _, p := range provs {
		p.Units = roundDecimal(p.Units)
		sum.Providers = append(sum.Providers, *p)
	}
	sort.Slice(sum.Providers, func(i, j int) bool {
		if sum.Providers[i].Units != sum.Providers[j].Units {
			return sum.Providers[i].Units > sum.Providers[j].Units
		}
		return sum.Providers[i].Provider < sum.Providers[j].Provider
	})
	if len(sum.Providers) > 5 {
		sum.Providers = sum.Providers[:5]
	}
	sum.UnitsOutstanding = roundDecimal(sum.UnitsOutstanding)
	sum.CostOutstanding = roundDecimal(sum.CostOutstanding)
	sum.OverdueUnits = roundDecimal(sum.OverdueUnits)
	return sum
}

// ServiceReturnSummary handles GET /inventory/purchase-returns/service-summary.
//
//	@Summary      Items currently out for repair
//	@Description  Open repair orders, units and cost value still away, overdue orders and the busiest providers. Honours outlet_id / warehouse_id and ?today=YYYY-MM-DD (the caller's calendar day, for overdue).
//	@Tags         Procurement
//	@Produce      json
//	@Param        outlet_id     query     string  false  "Filter by outlet (any of its warehouses)"
//	@Param        warehouse_id  query     string  false  "Filter by the warehouse the goods left from"
//	@Param        today         query     string  false  "Caller's local day, YYYY-MM-DD"
//	@Success      200           {object}  serviceSummary
//	@Failure      500           {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-returns/service-summary [get]
func (h *InventoryExtrasHandler) ServiceReturnSummary(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	ctx := r.Context()
	qp := r.URL.Query()
	q := h.orm.PurchaseReturn.Query().Where(
		entpr.TenantID(tenantID), entpr.DestinationEQ(entpr.DestinationService),
		entpr.ServiceStatusIn(entpr.ServiceStatusOut, entpr.ServiceStatusPartial))
	if pred := h.returnLocationPredicate(ctx, tenantID, qp); pred != nil {
		q = q.Where(pred)
	}
	rows, err := q.Select(entpr.FieldID, entpr.FieldServiceStatus, entpr.FieldExpectedBackDate, entpr.FieldMetadata).
		Limit(serviceSummaryCap).All(ctx)
	if err != nil {
		h.log.Error("service summary: orders failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to load the repair summary")
		return
	}
	orders := make([]serviceOrderRow, 0, len(rows))
	ids := make([]uuid.UUID, 0, len(rows))
	for _, p := range rows {
		st := entpr.ServiceStatusOut
		if p.ServiceStatus != nil {
			st = *p.ServiceStatus
		}
		orders = append(orders, serviceOrderRow{ID: p.ID, Expected: p.ExpectedBackDate, Status: st, Provider: serviceProviderOf(p.Metadata)})
		ids = append(ids, p.ID)
	}
	lineRows := []serviceLineRow{}
	if len(ids) > 0 {
		ls, lerr := h.orm.PurchaseReturnLine.Query().
			Where(entprline.TenantID(tenantID), entprline.PurchaseReturnIDIn(ids...)).
			Select(entprline.FieldPurchaseReturnID, entprline.FieldQuantity, entprline.FieldReceivedBackQty,
				entprline.FieldWrittenOffQty, entprline.FieldSubTotal).All(ctx)
		if lerr != nil {
			h.log.Error("service summary: lines failed", zap.Error(lerr))
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to load the repair summary")
			return
		}
		for _, l := range ls {
			lineRows = append(lineRows, serviceLineRow{ReturnID: l.PurchaseReturnID, Quantity: l.Quantity,
				Received: l.ReceivedBackQty, WrittenOff: l.WrittenOffQty, SubTotal: l.SubTotal})
		}
	}
	sum := buildServiceSummary(orders, lineRows, resolveToday(qp.Get("today"), time.Now()))
	sum.Truncated = len(rows) >= serviceSummaryCap
	writeJSON(w, http.StatusOK, sum)
}
