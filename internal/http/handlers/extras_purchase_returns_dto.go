package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entprline "github.com/bengobox/inventory-service/internal/ent/purchasereturnline"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// purchaseReturnLineDTO is one returned item, carrying its human identifiers (name, SKU) so the
// Returns list, drawer and treasury credit note never have to show a raw item UUID.
type purchaseReturnLineDTO struct {
	ID       uuid.UUID  `json:"id"`
	ItemID   uuid.UUID  `json:"item_id"`
	ItemName string     `json:"item_name"`
	Sku      string     `json:"sku"`
	LotID    *uuid.UUID `json:"lot_id,omitempty"`
	Quantity float64    `json:"quantity"`
	UnitCost float64    `json:"unit_cost"`
	SubTotal float64    `json:"sub_total"`
}

type purchaseReturnDTO struct {
	ID              uuid.UUID  `json:"id"`
	ReturnNumber    string     `json:"return_number"`
	PurchaseOrderID *uuid.UUID `json:"purchase_order_id"`
	GoodsReceiptID  *uuid.UUID `json:"goods_receipt_id,omitempty"`
	SupplierID      *uuid.UUID `json:"supplier_id"`
	SupplierName    string     `json:"supplier_name,omitempty"`
	// WarehouseID is the location the goods leave from: the return's own warehouse, else (for a
	// return raised before warehouse capture existed) the linked goods receipt's warehouse.
	WarehouseID   *uuid.UUID `json:"warehouse_id,omitempty"`
	WarehouseName string     `json:"warehouse_name,omitempty"`
	OutletID      *uuid.UUID `json:"outlet_id,omitempty"`
	Reason        string     `json:"reason"`
	ReturnAmount  float64    `json:"return_amount"`
	PaymentStatus string     `json:"payment_status"`
	// DateReturned is the calendar day the goods went back (stored at 00:00 UTC for a
	// date-only entry). DateReturnedDay is that day as YYYY-MM-DD so clients can render it
	// without timezone conversion shifting it to the previous day.
	DateReturned    time.Time               `json:"date_returned"`
	DateReturnedDay string                  `json:"date_returned_day"`
	AddedBy         *uuid.UUID              `json:"added_by,omitempty"`
	AddedByName     string                  `json:"added_by_name,omitempty"`
	ItemCount       int                     `json:"item_count"`
	TotalQuantity   float64                 `json:"total_quantity"`
	ItemsSummary    string                  `json:"items_summary"`
	Lines           []purchaseReturnLineDTO `json:"lines"`
	// StockWarnings lists items whose stock-out failed on approval (logged too), so the
	// approver sees it immediately instead of discovering a stock gap later.
	StockWarnings []string `json:"stock_warnings,omitempty"`
}

func purchaseReturnToDTO(pr *ent.PurchaseReturn) purchaseReturnDTO {
	return purchaseReturnDTO{
		ID: pr.ID, ReturnNumber: pr.ReturnNumber, PurchaseOrderID: pr.PurchaseOrderID,
		GoodsReceiptID: pr.GoodsReceiptID, SupplierID: pr.SupplierID, WarehouseID: pr.WarehouseID,
		Reason: pr.Reason, ReturnAmount: pr.ReturnAmount,
		PaymentStatus: string(pr.PaymentStatus), DateReturned: pr.DateReturned,
		DateReturnedDay: pr.DateReturned.UTC().Format("2006-01-02"), AddedBy: pr.AddedBy,
		Lines: []purchaseReturnLineDTO{},
	}
}

// purchaseReturnsToDTOs builds enriched DTOs for a page of returns in a fixed number of batched
// queries (lines, items, suppliers, GRN warehouses, warehouses, actors), never one per row.
func (h *InventoryExtrasHandler) purchaseReturnsToDTOs(ctx context.Context, tenantID uuid.UUID, rows []*ent.PurchaseReturn) []purchaseReturnDTO {
	out := make([]purchaseReturnDTO, len(rows))
	if len(rows) == 0 {
		return out
	}
	returnIDs := make([]uuid.UUID, 0, len(rows))
	supplierIDs := make([]uuid.UUID, 0, len(rows))
	grnIDs := make([]uuid.UUID, 0)
	actorIDs := make([]uuid.UUID, 0, len(rows))
	for i, pr := range rows {
		out[i] = purchaseReturnToDTO(pr)
		returnIDs = append(returnIDs, pr.ID)
		if pr.SupplierID != nil {
			supplierIDs = append(supplierIDs, *pr.SupplierID)
		}
		if pr.WarehouseID == nil && pr.GoodsReceiptID != nil {
			grnIDs = append(grnIDs, *pr.GoodsReceiptID)
		}
		if pr.AddedBy != nil {
			actorIDs = append(actorIDs, *pr.AddedBy)
		}
	}

	// Legacy returns (no warehouse of their own) show the linked goods receipt's warehouse, the
	// same fallback ApprovePurchaseReturn uses for the stock-out.
	grnWarehouse := map[uuid.UUID]uuid.UUID{}
	if len(grnIDs) > 0 {
		if grns, err := h.orm.GoodsReceipt.Query().
			Where(entgr.TenantID(tenantID), entgr.IDIn(grnIDs...)).
			Select(entgr.FieldID, entgr.FieldWarehouseID).All(ctx); err == nil {
			for _, g := range grns {
				if g.WarehouseID != nil {
					grnWarehouse[g.ID] = *g.WarehouseID
				}
			}
		}
	}
	warehouseIDs := make([]uuid.UUID, 0, len(rows))
	for i, pr := range rows {
		if out[i].WarehouseID == nil && pr.GoodsReceiptID != nil {
			if wid, ok := grnWarehouse[*pr.GoodsReceiptID]; ok {
				w := wid
				out[i].WarehouseID = &w
			}
		}
		if out[i].WarehouseID != nil {
			warehouseIDs = append(warehouseIDs, *out[i].WarehouseID)
		}
	}
	type whInfo struct {
		name   string
		outlet *uuid.UUID
	}
	warehouses := map[uuid.UUID]whInfo{}
	if len(warehouseIDs) > 0 {
		if whs, err := h.orm.Warehouse.Query().
			Where(entwarehouse.TenantID(tenantID), entwarehouse.IDIn(warehouseIDs...)).
			Select(entwarehouse.FieldID, entwarehouse.FieldName, entwarehouse.FieldOutletID).All(ctx); err == nil {
			for _, wh := range whs {
				warehouses[wh.ID] = whInfo{name: wh.Name, outlet: wh.OutletID}
			}
		}
	}

	supplierNames := map[uuid.UUID]string{}
	if len(supplierIDs) > 0 {
		if sups, err := h.orm.Supplier.Query().
			Where(entsupplier.TenantID(tenantID), entsupplier.IDIn(supplierIDs...)).
			Select(entsupplier.FieldID, entsupplier.FieldName).All(ctx); err == nil {
			for _, s := range sups {
				supplierNames[s.ID] = s.Name
			}
		}
	}
	actorNames := actorNamesByID(ctx, h.orm, tenantID, actorIDs)

	lines, _ := h.orm.PurchaseReturnLine.Query().
		Where(entprline.TenantID(tenantID), entprline.PurchaseReturnIDIn(returnIDs...)).
		Order(ent.Asc(entprline.FieldCreatedAt)).All(ctx)
	itemIDs := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		itemIDs = append(itemIDs, l.ItemID)
	}
	type itemInfo struct{ name, sku string }
	itemsByID := map[uuid.UUID]itemInfo{}
	if len(itemIDs) > 0 {
		if its, err := h.orm.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.IDIn(itemIDs...)).
			Select(entitem.FieldID, entitem.FieldName, entitem.FieldSku).All(ctx); err == nil {
			for _, it := range its {
				itemsByID[it.ID] = itemInfo{name: it.Name, sku: it.Sku}
			}
		}
	}
	linesByReturn := map[uuid.UUID][]purchaseReturnLineDTO{}
	for _, l := range lines {
		info := itemsByID[l.ItemID]
		unitCost := 0.0
		if l.Quantity != 0 {
			unitCost = roundDecimal(l.SubTotal / l.Quantity)
		}
		linesByReturn[l.PurchaseReturnID] = append(linesByReturn[l.PurchaseReturnID], purchaseReturnLineDTO{
			ID: l.ID, ItemID: l.ItemID, ItemName: ifEmptyStr(info.name, "Unknown item"), Sku: info.sku,
			LotID: l.LotID, Quantity: l.Quantity, UnitCost: unitCost, SubTotal: l.SubTotal,
		})
	}

	for i, pr := range rows {
		if pr.SupplierID != nil {
			out[i].SupplierName = supplierNames[*pr.SupplierID]
		}
		if pr.AddedBy != nil {
			out[i].AddedByName = actorNames[*pr.AddedBy]
		}
		if out[i].WarehouseID != nil {
			if wh, ok := warehouses[*out[i].WarehouseID]; ok {
				out[i].WarehouseName = wh.name
				out[i].OutletID = wh.outlet
			}
		}
		if ls := linesByReturn[pr.ID]; len(ls) > 0 {
			out[i].Lines = ls
		}
		out[i].ItemCount = len(out[i].Lines)
		names := make([]string, 0, len(out[i].Lines))
		for _, l := range out[i].Lines {
			out[i].TotalQuantity = roundDecimal(out[i].TotalQuantity + l.Quantity)
			names = append(names, fmt.Sprintf("%s x%s", l.ItemName, formatQty(l.Quantity)))
		}
		out[i].ItemsSummary = summarizeNames(names, 3)
	}
	return out
}

// summarizeNames joins up to max entries and collapses the rest into "+N more", so a list
// cell stays one readable line however many items a return carries.
func summarizeNames(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(names[:max], ", "), len(names)-max)
}
