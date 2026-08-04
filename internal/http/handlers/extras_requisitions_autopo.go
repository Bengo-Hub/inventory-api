package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entreq "github.com/bengobox/inventory-service/internal/ent/requisition"
	entreqline "github.com/bengobox/inventory-service/internal/ent/requisitionline"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// autoCreatePOsFromRequisition raises draft purchase order(s) from an approved
// requisition, grouping orderable lines by supplier (one PO per vendor). It covers
// every request type:
//   - inventory / external lines are ordered against their linked Item (external
//     lines carry an item_id because the request form resolves them to an existing
//     or newly-created inventory item);
//   - service lines are ordered against a SERVICE-type Item resolved from the
//     service description (created on the fly if needed), so a service engagement
//     also produces a PO the buyer can send and reconcile.
//
// It is best-effort and idempotent: it does nothing when the requisition already
// has a PO, when there is no delivery warehouse, or when no line resolves to an
// item + supplier. The requisition flips to "ordered" only once at least one PO is
// created; otherwise it stays "approved" and the manual convert-to-po path remains
// available.
func (h *InventoryExtrasHandler) autoCreatePOsFromRequisition(ctx context.Context, tenantID uuid.UUID, rq *ent.Requisition) {
	warehouseID := h.resolveDeliveryWarehouse(ctx, tenantID, rq.OutletID)
	if warehouseID == uuid.Nil {
		h.log.Warn("auto-PO: no delivery warehouse; requisition left approved for manual conversion",
			zap.String("requisition", rq.ID.String()))
		return
	}

	lines, _ := h.orm.RequisitionLine.Query().
		Where(entreqline.RequisitionID(rq.ID), entreqline.TenantID(tenantID)).
		All(ctx)

	type orderable struct {
		itemID uuid.UUID
		qty    float64
		price  float64
	}
	bySupplier := map[uuid.UUID][]orderable{}
	var skipped int
	for _, l := range lines {
		itemID, ok := h.resolveLineItemID(ctx, tenantID, l)
		if !ok {
			skipped++
			continue
		}
		supplier := h.resolveLineSupplier(ctx, tenantID, l, itemID)
		if supplier == uuid.Nil {
			skipped++
			continue
		}
		price := 0.0
		if l.EstimatedPrice != nil {
			price = *l.EstimatedPrice
		}
		bySupplier[supplier] = append(bySupplier[supplier], orderable{itemID: itemID, qty: l.Quantity, price: price})
	}

	if len(bySupplier) == 0 {
		h.log.Info("auto-PO: no orderable lines (missing item or supplier); requisition left approved",
			zap.String("requisition", rq.ID.String()), zap.Int("skipped_lines", skipped))
		return
	}

	// Idempotency — never double-order a requisition. A requisition legitimately fans out into
	// MULTIPLE POs (one per supplier group, below), so requisition_id can't carry a plain unique
	// DB constraint the way a 1:1 relationship could. The previous guard (Query().Exist() at the
	// top of this function, then Create() in a loop many lines later) was a classic check-then-act
	// race: this function is called from two different approval call sites, and a double-submitted
	// "approve" click (or both call sites firing for the same requisition) could both pass the
	// Exist() check and both create a full duplicate batch of POs, doubling the buying-cost
	// commitment. Fixed by claiming a single atomic key via the same (tenant_id, key)
	// unique-constrained idempotency_keys table used elsewhere (see quotation_accepted_events.go).
	// Claimed here, right before the mutating work, rather than at function entry — an earlier
	// return above (no warehouse, no orderable lines) must NOT burn the claim, or fixing the
	// underlying data (adding a supplier, configuring a warehouse) could never trigger a retry.
	idemKey := "requisition-auto-po:" + rq.ID.String()
	if _, err := h.orm.IdempotencyKey.Create().
		SetTenantID(tenantID).
		SetKey(idemKey).
		SetEndpoint("auto:requisition.approved").
		SetStatus("completed").
		SetExpiresAt(time.Now().Add(30 * 24 * time.Hour)).
		Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			h.log.Info("auto-PO: requisition already processed, skipping (idempotent)",
				zap.String("requisition", rq.ID.String()))
			return
		}
		h.log.Warn("auto-PO: claim idempotency key failed", zap.Error(err), zap.String("requisition", rq.ID.String()))
		return
	}

	createdAny := false
	for supplier, ols := range bySupplier {
		poNumber := "PO-" + strings.ToUpper(uuid.New().String()[:8])
		if h.docSvc != nil {
			if n, e := h.docSvc.Seq().GenerateNumber(ctx, tenantID, documents.DocTypePurchaseOrder); e == nil && n != "" {
				poNumber = n
			}
		}
		po, err := h.orm.PurchaseOrder.Create().
			SetTenantID(tenantID).
			SetSupplierID(supplier).
			SetWarehouseID(warehouseID).
			SetPoNumber(poNumber).
			SetCurrency("KES").
			SetRequisitionID(rq.ID).
			SetNillableProjectID(rq.ProjectID). // carry the project so treasury attributes the cost
			SetNotes(fmt.Sprintf("Auto-created from requisition %s on approval", rq.ReferenceNumber)).
			Save(ctx)
		if err != nil {
			h.log.Warn("auto-PO: create PO failed", zap.Error(err), zap.String("supplier", supplier.String()))
			continue
		}
		var total float64
		for _, ol := range ols {
			lineTotal := ol.price * ol.qty
			total += lineTotal
			if _, lerr := h.orm.PurchaseOrderLine.Create().
				SetPoID(po.ID).
				SetItemID(ol.itemID).
				SetQuantityOrdered(ol.qty).
				SetUnitPrice(ol.price).
				SetTotalPrice(lineTotal).
				Save(ctx); lerr != nil {
				h.log.Warn("auto-PO: create PO line failed", zap.Error(lerr), zap.String("po", po.ID.String()))
			}
		}
		po, _ = h.orm.PurchaseOrder.UpdateOneID(po.ID).SetTotalAmount(total).Save(ctx)
		createdAny = true
		h.publishOutbox(ctx, tenantID, "purchase_order", po.ID, "inventory.purchase_order.created", map[string]any{
			"id": po.ID, "po_number": po.PoNumber, "supplier_id": supplier, "total_amount": total,
			"from_requisition_id": rq.ID, "auto": true,
		})
	}

	if createdAny {
		_, _ = h.orm.Requisition.UpdateOneID(rq.ID).SetStatus(entreq.StatusOrdered).Save(ctx)
		h.publishOutbox(ctx, tenantID, "requisition", rq.ID, "inventory.requisition.ordered", map[string]any{
			"id": rq.ID, "reference_number": rq.ReferenceNumber, "auto": true,
		})
	}
}

// resolveDeliveryWarehouse picks a warehouse to receive an auto-PO into: the
// requisition outlet's warehouse first, then the tenant default, then any active
// warehouse. Returns uuid.Nil when the tenant has no warehouse.
func (h *InventoryExtrasHandler) resolveDeliveryWarehouse(ctx context.Context, tenantID uuid.UUID, outletID *uuid.UUID) uuid.UUID {
	base := h.orm.Warehouse.Query().Where(entwarehouse.TenantID(tenantID), entwarehouse.IsActive(true))
	if outletID != nil {
		if wh, err := base.Clone().Where(entwarehouse.OutletID(*outletID)).First(ctx); err == nil && wh != nil {
			return wh.ID
		}
	}
	if wh, err := base.Clone().Where(entwarehouse.IsDefault(true)).First(ctx); err == nil && wh != nil {
		return wh.ID
	}
	if wh, err := base.First(ctx); err == nil && wh != nil {
		return wh.ID
	}
	return uuid.Nil
}

// resolveLineItemID returns the inventory Item id to order for a requisition line.
// Inventory/external lines use their linked item_id; service lines resolve (find or
// create) a SERVICE-type Item from the service description. Returns ok=false when
// the line can't be placed on a PO (no item reference and no description).
func (h *InventoryExtrasHandler) resolveLineItemID(ctx context.Context, tenantID uuid.UUID, l *ent.RequisitionLine) (uuid.UUID, bool) {
	if l.ItemID != nil {
		return *l.ItemID, true
	}
	if l.ItemType == entreqline.ItemTypeService {
		name := l.ServiceDescription
		if name == "" {
			name = l.Description
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return uuid.Nil, false
		}
		return h.findOrCreateServiceItem(ctx, tenantID, name)
	}
	return uuid.Nil, false
}

// resolveLineSupplier resolves the vendor for a line: the line's own supplier_id,
// else the ordered item's preferred supplier. Returns uuid.Nil when neither is set.
func (h *InventoryExtrasHandler) resolveLineSupplier(ctx context.Context, tenantID uuid.UUID, l *ent.RequisitionLine, itemID uuid.UUID) uuid.UUID {
	if l.SupplierID != nil {
		return *l.SupplierID
	}
	if it, err := h.orm.Item.Get(ctx, itemID); err == nil && it.TenantID == tenantID && it.PreferredSupplierID != nil {
		return *it.PreferredSupplierID
	}
	return uuid.Nil
}

// findOrCreateServiceItem returns a SERVICE-type Item id matching name, creating one
// when none exists so a service engagement can be represented as a PO line.
func (h *InventoryExtrasHandler) findOrCreateServiceItem(ctx context.Context, tenantID uuid.UUID, name string) (uuid.UUID, bool) {
	if it, err := h.orm.Item.Query().
		Where(entitem.TenantID(tenantID), entitem.TypeEQ(entitem.TypeSERVICE), entitem.NameEqualFold(name)).
		First(ctx); err == nil && it != nil {
		return it.ID, true
	}
	sku := "SVC-" + strings.ToUpper(uuid.New().String()[:8])
	it, err := h.orm.Item.Create().
		SetTenantID(tenantID).
		SetSku(sku).
		SetName(name).
		SetType(entitem.TypeSERVICE).
		SetIsActive(true).
		Save(ctx)
	if err != nil {
		h.log.Warn("auto-PO: create service item failed", zap.Error(err), zap.String("name", name))
		return uuid.Nil, false
	}
	return it.ID, true
}
