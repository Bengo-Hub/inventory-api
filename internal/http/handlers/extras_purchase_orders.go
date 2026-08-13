package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpurchaseorder "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpoline "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	entunit "github.com/bengobox/inventory-service/internal/ent/unit"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// ─── Purchase Orders ──────────────────────────────────────────────────────────

type purchaseOrderDTO struct {
	ID                        uuid.UUID  `json:"id"`
	PONumber                  string     `json:"po_number"`
	SupplierName              string     `json:"supplier_name"`
	SupplierID                uuid.UUID  `json:"supplier_id"`
	WarehouseName             string     `json:"warehouse_name"`
	WarehouseID               uuid.UUID  `json:"warehouse_id"`
	Status                    string     `json:"status"`
	TotalAmount               float64    `json:"total_amount"`
	ExpectedDate              *time.Time `json:"expected_date"`
	Notes                     string     `json:"notes"`
	PayTermDays               *int       `json:"pay_term_days"`
	AdditionalShippingCharges float64    `json:"additional_shipping_charges"`
	CreatedAt                 time.Time  `json:"created_at"`
}

// ListPurchaseOrders handles GET /inventory/purchase-orders.
//
//	@Summary      List purchase orders
//	@Tags         Procurement
//	@Produce      json
//	@Param        search  query     string  false  "Filter by PO number or supplier name"
//	@Param        status  query     string  false  "Filter by status"
//	@Param        page    query     int     false  "Page number (1-based)"
//	@Param        limit   query     int     false  "Page size (max 100)"
//	@Success      200     {object}  map[string]interface{}
//	@Failure      400     {object}  map[string]string
//	@Failure      500     {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders [get]
func (h *InventoryExtrasHandler) ListPurchaseOrders(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)

	q := h.orm.PurchaseOrder.Query().Where(entpurchaseorder.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entpurchaseorder.StatusEQ(entpurchaseorder.Status(s)))
	}
	if search := strings.TrimSpace(r.URL.Query().Get("search")); search != "" {
		q = q.Where(entpurchaseorder.Or(
			entpurchaseorder.PoNumberContainsFold(search),
			entpurchaseorder.HasSupplierWith(entsupplier.NameContainsFold(search)),
		))
	}
	if from, to, ok := parseCreatedAtRange(r); ok {
		q = q.Where(entpurchaseorder.CreatedAtGTE(from), entpurchaseorder.CreatedAtLTE(to))
	}

	total, _ := q.Clone().Count(r.Context())
	orders, err := q.
		WithSupplier().
		WithWarehouse().
		Order(ent.Desc(entpurchaseorder.FieldCreatedAt)).
		Limit(p.Limit).
		Offset(p.Offset).
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list purchase orders")
		return
	}

	result := make([]purchaseOrderDTO, 0, len(orders))
	for _, o := range orders {
		supplierName := ""
		if o.Edges.Supplier != nil {
			supplierName = o.Edges.Supplier.Name
		}
		warehouseName := ""
		if o.Edges.Warehouse != nil {
			warehouseName = o.Edges.Warehouse.Name
		}
		dto := purchaseOrderDTO{
			ID:                        o.ID,
			PONumber:                  o.PoNumber,
			SupplierName:              supplierName,
			SupplierID:                derefUUID(o.SupplierID),
			WarehouseName:             warehouseName,
			WarehouseID:               derefUUID(o.WarehouseID),
			Status:                    o.Status.String(),
			TotalAmount:               o.TotalAmount,
			Notes:                     o.Notes,
			PayTermDays:               o.PayTermDays,
			AdditionalShippingCharges: o.AdditionalShippingCharges,
			CreatedAt:                 o.CreatedAt,
		}
		if o.ExpectedDate != nil {
			dto.ExpectedDate = o.ExpectedDate
		}
		result = append(result, dto)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(result, total, p))
}

// GetPurchaseOrder handles GET /inventory/purchase-orders/{poID}.
//
//	@Summary      Get a purchase order with its line items
//	@Tags         Procurement
//	@Produce      json
//	@Param        poID  path      string  true  "Purchase order ID"
//	@Success      200   {object}  purchaseOrderDTO
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID} [get]
func (h *InventoryExtrasHandler) GetPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	po, err := h.orm.PurchaseOrder.Query().
		Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).
		WithSupplier().
		WithWarehouse().
		WithLines().
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	supplierName := ""
	if po.Edges.Supplier != nil {
		supplierName = po.Edges.Supplier.Name
	}
	warehouseName := ""
	if po.Edges.Warehouse != nil {
		warehouseName = po.Edges.Warehouse.Name
	}

	// Fetch item names/SKUs for lines
	itemIDs := make([]uuid.UUID, 0, len(po.Edges.Lines))
	for _, l := range po.Edges.Lines {
		itemIDs = append(itemIDs, l.ItemID)
	}
	itemNames := make(map[uuid.UUID]string)
	itemSKUs := make(map[uuid.UUID]string)
	itemCurrentPrice := make(map[uuid.UUID]*float64)
	if len(itemIDs) > 0 {
		items, _ := h.orm.Item.Query().
			Where(entitem.IDIn(itemIDs...)).
			All(r.Context())
		for _, it := range items {
			itemNames[it.ID] = it.Name
			itemSKUs[it.ID] = it.Sku
			itemCurrentPrice[it.ID] = it.MaxSellingPrice
		}
	}

	// Resolve unit abbreviations for lines that carry a unit_id, same lookup shape as
	// itemNames/itemSKUs above — PurchaseOrderLine has no Ent edge to Unit (mirrors item_id).
	unitIDs := make([]uuid.UUID, 0, len(po.Edges.Lines))
	for _, l := range po.Edges.Lines {
		if l.UnitID != nil {
			unitIDs = append(unitIDs, *l.UnitID)
		}
	}
	unitAbbrs := make(map[uuid.UUID]string)
	if len(unitIDs) > 0 {
		units, _ := h.orm.Unit.Query().
			Where(entunit.IDIn(unitIDs...)).
			All(r.Context())
		for _, u := range units {
			unitAbbrs[u.ID] = u.Abbreviation
		}
	}

	type lineDTO struct {
		ID          uuid.UUID  `json:"id"`
		ItemID      uuid.UUID  `json:"item_id"`
		ItemName    string     `json:"item_name"`
		ItemSKU     string     `json:"item_sku"`
		Quantity    float64    `json:"quantity"`
		ReceivedQty float64    `json:"received_qty"`
		UnitCost    float64    `json:"unit_cost"`
		TotalCost   float64    `json:"total_cost"`
		UnitID      *uuid.UUID `json:"unit_id,omitempty"`
		Unit        string     `json:"unit,omitempty"`
		// Selling-price adjustment decided at order time — carried through and applied when this
		// line is actually received (see postGoodsReceiptCore). CurrentSellingPrice is context
		// only (the item's price as of now), so the PO form can show "currently X" next to the
		// input; it is never itself written anywhere.
		NewSellingPrice     *float64 `json:"new_selling_price,omitempty"`
		PriceScope          string   `json:"price_scope,omitempty"`
		CurrentSellingPrice *float64 `json:"current_selling_price,omitempty"`
	}

	lines := make([]lineDTO, 0, len(po.Edges.Lines))
	for _, l := range po.Edges.Lines {
		var unitAbbr string
		if l.UnitID != nil {
			unitAbbr = unitAbbrs[*l.UnitID]
		}
		lines = append(lines, lineDTO{
			ID:                  l.ID,
			ItemID:              l.ItemID,
			ItemName:            itemNames[l.ItemID],
			ItemSKU:             itemSKUs[l.ItemID],
			Quantity:            l.QuantityOrdered,
			ReceivedQty:         l.QuantityReceived,
			UnitCost:            l.UnitPrice,
			TotalCost:           l.TotalPrice,
			UnitID:              l.UnitID,
			Unit:                unitAbbr,
			NewSellingPrice:     l.NewSellingPrice,
			PriceScope:          l.PriceScope,
			CurrentSellingPrice: itemCurrentPrice[l.ItemID],
		})
	}

	type poDetailDTO struct {
		purchaseOrderDTO
		LineItems []lineDTO `json:"line_items"`
	}

	dto := purchaseOrderDTO{
		ID:                        po.ID,
		PONumber:                  po.PoNumber,
		SupplierName:              supplierName,
		SupplierID:                derefUUID(po.SupplierID),
		WarehouseName:             warehouseName,
		WarehouseID:               derefUUID(po.WarehouseID),
		Status:                    po.Status.String(),
		TotalAmount:               po.TotalAmount,
		Notes:                     po.Notes,
		PayTermDays:               po.PayTermDays,
		AdditionalShippingCharges: po.AdditionalShippingCharges,
		CreatedAt:                 po.CreatedAt,
	}
	if po.ExpectedDate != nil {
		dto.ExpectedDate = po.ExpectedDate
	}

	writeJSON(w, http.StatusOK, poDetailDTO{purchaseOrderDTO: dto, LineItems: lines})
}

// ─── Purchase Order Writes ────────────────────────────────────────────────────

type createPOLineInput struct {
	ItemID   uuid.UUID  `json:"item_id"`
	Quantity float64    `json:"quantity"`
	UnitCost float64    `json:"unit_cost"`
	UnitID   *uuid.UUID `json:"unit_id"` // FK to Unit — unit of measure ordered in (e.g. kg, box); optional
	// Selling-price adjustment decided at order time. Applied only when this line is actually
	// received (see postGoodsReceiptCore) — nothing changes at PO creation/amend time.
	NewSellingPrice *float64 `json:"new_selling_price,omitempty"`
	PriceScope      string   `json:"price_scope,omitempty"`
}

type createPOInput struct {
	SupplierID                uuid.UUID           `json:"supplier_id"`
	WarehouseID               uuid.UUID           `json:"warehouse_id"`
	ExpectedDate              *string             `json:"expected_date"` // accepts "YYYY-MM-DD" or RFC3339
	Notes                     string              `json:"notes"`
	PayTermDays               *int                `json:"pay_term_days"`
	AdditionalShippingCharges float64             `json:"additional_shipping_charges"`
	LineItems                 []createPOLineInput `json:"line_items"`
}

// CreatePurchaseOrder handles POST /inventory/purchase-orders.
//
//	@Summary      Create a purchase order with line items
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        body  body      createPOInput  true  "Purchase order payload"
//	@Success      201   {object}  map[string]interface{}
//	@Failure      400   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders [post]
func (h *InventoryExtrasHandler) CreatePurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req createPOInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.SupplierID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_SUPPLIER", "supplier_id is required")
		return
	}
	if req.WarehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_WAREHOUSE", "warehouse_id is required")
		return
	}
	if len(req.LineItems) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_LINES", "at least one line item is required")
		return
	}

	tx, err := h.orm.Tx(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to begin transaction")
		return
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var total float64
	for _, l := range req.LineItems {
		total += roundDecimal(l.Quantity * l.UnitCost)
	}
	total = roundDecimal(total)

	// Configurable per-tenant document number (e.g. PO-260625-000001). Falls back to the legacy
	// timestamp form only if the sequence service is unavailable, so creation never fails on it.
	poNumber := "PO-" + strings.ToUpper(tenantID.String()[:8]) + "-" + time.Now().Format("20060102150405")
	if h.docSvc != nil {
		if n, derr := h.docSvc.Seq().GenerateNumber(r.Context(), tenantID, documents.DocTypePurchaseOrder); derr == nil && n != "" {
			poNumber = n
		} else if derr != nil {
			h.log.Warn("PO number sequence failed, using fallback", zap.Error(derr))
		}
	}
	poCreate := tx.PurchaseOrder.Create().
		SetTenantID(tenantID).
		SetSupplierID(req.SupplierID).
		SetWarehouseID(req.WarehouseID).
		SetPoNumber(poNumber).
		SetTotalAmount(total).
		SetNotes(req.Notes).
		SetNillablePayTermDays(req.PayTermDays).
		SetAdditionalShippingCharges(req.AdditionalShippingCharges)
	// Stamp the creator (the "Prepared By" on the PO document) from the authenticated user.
	if claims, ok := authclient.ClaimsFromContext(r.Context()); ok && claims.Subject != "" {
		if uid, perr := uuid.Parse(claims.Subject); perr == nil {
			poCreate.SetCreatedBy(uid)
		}
	}
	if req.ExpectedDate != nil && *req.ExpectedDate != "" {
		var expDate time.Time
		// Accept both "YYYY-MM-DD" (from date inputs) and RFC3339
		if t, parseErr := time.Parse("2006-01-02", *req.ExpectedDate); parseErr == nil {
			expDate = t
		} else if t, parseErr := time.Parse(time.RFC3339, *req.ExpectedDate); parseErr == nil {
			expDate = t
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_DATE", "expected_date must be YYYY-MM-DD or RFC3339")
			return
		}
		poCreate = poCreate.SetExpectedDate(expDate)
	}
	po, err := poCreate.Save(r.Context())
	if err != nil {
		h.log.Error("create purchase order failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase order")
		return
	}

	for _, l := range req.LineItems {
		lc := tx.PurchaseOrderLine.Create().
			SetPoID(po.ID).
			SetItemID(l.ItemID).
			SetQuantityOrdered(l.Quantity).
			SetUnitPrice(l.UnitCost).
			SetTotalPrice(roundDecimal(l.Quantity * l.UnitCost)).
			SetNillableUnitID(l.UnitID)
		if l.NewSellingPrice != nil && *l.NewSellingPrice > 0 {
			lc = lc.SetNewSellingPrice(*l.NewSellingPrice)
		}
		if scope := strings.TrimSpace(l.PriceScope); scope != "" {
			lc = lc.SetPriceScope(scope)
		}
		_, err = lc.Save(r.Context())
		if err != nil {
			h.log.Error("create PO line failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase order line")
			return
		}
	}

	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "COMMIT_FAILED", "Failed to commit purchase order")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           po.ID,
		"po_number":    po.PoNumber,
		"status":       po.Status.String(),
		"total_amount": po.TotalAmount,
	})
}

// AmendPurchaseOrder handles PUT /inventory/purchase-orders/{poID}/amend.
// Replaces the line items + notes/expected date of a draft or sent PO (before any
// goods are received) and recomputes the total. Received/partially-received/
// cancelled POs cannot be amended.
//
//	@Summary      Amend a draft/sent purchase order's lines
//	@Tags         Procurement
//	@Accept       json
//	@Produce      json
//	@Param        poID  path      string        true  "Purchase order ID"
//	@Param        body  body      createPOInput  true  "Amended lines + notes/expected_date"
//	@Success      200   {object}  map[string]interface{}
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID}/amend [put]
func (h *InventoryExtrasHandler) AmendPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	po, err := h.orm.PurchaseOrder.Query().Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	st := po.Status.String()
	if st != "draft" && st != "sent" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Only draft or sent purchase orders can be amended")
		return
	}
	var req createPOInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if len(req.LineItems) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_LINES", "at least one line item is required")
		return
	}

	tx, err := h.orm.Tx(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to begin transaction")
		return
	}
	if _, err = tx.PurchaseOrderLine.Delete().Where(entpoline.PoID(po.ID)).Exec(r.Context()); err != nil {
		_ = tx.Rollback()
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to clear existing lines")
		return
	}
	var total float64
	for _, l := range req.LineItems {
		lineTotal := roundDecimal(l.Quantity * l.UnitCost)
		total += lineTotal
		lc := tx.PurchaseOrderLine.Create().
			SetPoID(po.ID).SetItemID(l.ItemID).SetQuantityOrdered(l.Quantity).
			SetUnitPrice(l.UnitCost).SetTotalPrice(lineTotal).SetNillableUnitID(l.UnitID)
		if l.NewSellingPrice != nil && *l.NewSellingPrice > 0 {
			lc = lc.SetNewSellingPrice(*l.NewSellingPrice)
		}
		if scope := strings.TrimSpace(l.PriceScope); scope != "" {
			lc = lc.SetPriceScope(scope)
		}
		if _, err = lc.Save(r.Context()); err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to write amended line")
			return
		}
	}
	upd := tx.PurchaseOrder.UpdateOneID(po.ID).SetTotalAmount(roundDecimal(total)).SetNotes(req.Notes)
	if req.ExpectedDate != nil && *req.ExpectedDate != "" {
		if t, e := time.Parse("2006-01-02", *req.ExpectedDate); e == nil {
			upd = upd.SetExpectedDate(t)
		} else if t, e := time.Parse(time.RFC3339, *req.ExpectedDate); e == nil {
			upd = upd.SetExpectedDate(t)
		}
	}
	if _, err = upd.Save(r.Context()); err != nil {
		_ = tx.Rollback()
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update purchase order")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "COMMIT_FAILED", "Failed to commit amendment")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "purchase_order", po.ID, "inventory.purchase_order.amended", map[string]any{
		"id": po.ID, "po_number": po.PoNumber, "total_amount": total,
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": po.ID, "po_number": po.PoNumber, "total_amount": total, "status": st})
}

// SendPurchaseOrder handles PUT /inventory/purchase-orders/{poID}/send.
// Transitions a draft PO to sent status.
//
//	@Summary      Send a draft purchase order
//	@Tags         Procurement
//	@Produce      json
//	@Param        poID  path      string  true  "Purchase order ID"
//	@Success      200   {object}  map[string]string
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID}/send [put]
func (h *InventoryExtrasHandler) SendPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	po, err := h.orm.PurchaseOrder.Query().
		Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	if po.Status.String() != "draft" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Only draft orders can be sent")
		return
	}
	// Approval-matrix gate: if a rule matches this PO's amount, an approved request is
	// required before sending. No matching rule → no gate (state "not_required").
	if ok, state, aerr := h.approvals().Satisfied(r.Context(), tenantID, "purchase_order", po.ID, po.TotalAmount); aerr == nil && !ok {
		writeError(w, http.StatusConflict, "APPROVAL_REQUIRED", "Purchase order requires approval before sending (status: "+state+")")
		return
	}
	if _, err := h.orm.PurchaseOrder.UpdateOneID(poID).SetStatus("sent").Save(r.Context()); err != nil {
		h.log.Error("send PO failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "SEND_FAILED", "Failed to send purchase order")
		return
	}

	// Publish event for notifications service
	go func() {
		ctx := context.Background()
		evt := &events.Event{
			ID:            uuid.New(),
			TenantID:      tenantID,
			AggregateType: "inventory",
			AggregateID:   po.ID,
			EventType:     "purchase_order.sent",
			Payload: map[string]any{
				"po_id":       po.ID,
				"po_number":   po.PoNumber,
				"tenant_id":   tenantID,
				"supplier_id": po.SupplierID,
			},
			Timestamp: time.Now().UTC(),
		}
		payload, marshalErr := evt.ToJSON()
		if marshalErr != nil {
			h.log.Warn("PO sent event: marshal failed", zap.Error(marshalErr))
			return
		}
		_, writeErr := h.orm.OutboxEvent.Create().
			SetID(evt.ID).
			SetTenantID(tenantID).
			SetAggregateType(evt.AggregateType).
			SetAggregateID(evt.AggregateID.String()).
			SetEventType(evt.EventType).
			SetPayload(json.RawMessage(payload)).
			SetStatus("PENDING").
			SetCreatedAt(evt.Timestamp).
			Save(ctx)
		if writeErr != nil {
			h.log.Warn("PO sent event: outbox write failed", zap.Error(writeErr))
		}
	}()

	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// ReceivePurchaseOrder handles PUT /inventory/purchase-orders/{poID}/receive.
// Marks the PO as received and increments on_hand stock for each line.
//
//	@Summary      Receive a purchase order into stock
//	@Tags         Procurement
//	@Produce      json
//	@Param        poID  path      string  true  "Purchase order ID"
//	@Success      200   {object}  map[string]string
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID}/receive [put]
//
// ReceivePurchaseOrder is the quick-action equivalent of receiving everything still
// outstanding on the PO right now: it auto-generates a goods receipt covering every line's
// remaining quantity (fully accepted, unit cost from the PO line), then posts it through
// EXACTLY the same path as the manual multi-line GRN flow (postGoodsReceiptCore) — stock-in,
// lot/serial capture, PO-line quantity_received tracking, approval gating, and the
// goods_receipt.posted / purchase_order.received ledger events. There is no longer a
// separate, ad-hoc stock-bump that skips the GRN paper trail.
func (h *InventoryExtrasHandler) ReceivePurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}

	po, err := h.orm.PurchaseOrder.Query().
		Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).
		WithLines().
		WithSupplier().
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	status := po.Status.String()
	if status != "draft" && status != "sent" && status != "partially_received" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Only draft, sent, or partially-received orders can be received")
		return
	}
	if po.WarehouseID == nil {
		writeError(w, http.StatusBadRequest, "MISSING_WAREHOUSE", "Assign a destination warehouse before receiving this purchase order")
		return
	}

	var remaining []*ent.PurchaseOrderLine
	for _, line := range po.Edges.Lines {
		if line.QuantityOrdered > line.QuantityReceived {
			remaining = append(remaining, line)
		}
	}
	if len(remaining) == 0 {
		writeError(w, http.StatusBadRequest, "NOTHING_TO_RECEIVE", "All lines are already fully received")
		return
	}

	grnNumber := "GRN-" + strings.ToUpper(uuid.New().String()[:8])
	if h.docSvc != nil {
		if n, derr := h.docSvc.Seq().GenerateNumber(r.Context(), tenantID, documents.DocTypeGRN); derr == nil && n != "" {
			grnNumber = n
		}
	}
	g, err := h.orm.GoodsReceipt.Create().
		SetTenantID(tenantID).SetGrnNumber(grnNumber).SetPurchaseOrderID(poID).
		SetNillableSupplierID(po.SupplierID).SetNillableWarehouseID(po.WarehouseID).
		SetNotes("Auto-generated via Mark Received").
		Save(r.Context())
	if err != nil {
		h.log.Error("auto-create GRN on mark-received failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to record goods receipt")
		return
	}
	for _, line := range remaining {
		qty := line.QuantityOrdered - line.QuantityReceived
		lineID := line.ID
		if _, lerr := h.orm.GoodsReceiptLine.Create().
			SetTenantID(tenantID).SetGoodsReceiptID(g.ID).SetItemID(line.ItemID).
			SetPurchaseOrderLineID(lineID).
			SetQuantityReceived(qty).SetQuantityAccepted(qty).SetQuantityRejected(0).
			SetUnitCost(line.UnitPrice).
			Save(r.Context()); lerr != nil {
			h.log.Warn("auto GRN line create failed", zap.Error(lerr))
		}
	}
	h.publishOutbox(r.Context(), tenantID, "inventory", g.ID, "goods_receipt.created", map[string]any{
		"id": g.ID, "grn_number": g.GrnNumber, "purchase_order_id": poID,
	})

	if !h.gateApproval(w, r, tenantID, "goods_receipt", g.ID, g.GrnNumber, po.TotalAmount) {
		return // GRN stays in draft, awaiting/rejected sign-off; PO status is untouched.
	}

	if _, err = h.postGoodsReceiptCore(r.Context(), tenantID, g, po); err != nil {
		h.log.Error("post goods receipt on mark-received failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to post goods receipt")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
}

// CancelPurchaseOrder handles PUT /inventory/purchase-orders/{poID}/cancel.
//
//	@Summary      Cancel a purchase order
//	@Tags         Procurement
//	@Produce      json
//	@Param        poID  path      string  true  "Purchase order ID"
//	@Success      200   {object}  map[string]string
//	@Failure      400   {object}  map[string]string
//	@Failure      404   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/purchase-orders/{poID}/cancel [put]
func (h *InventoryExtrasHandler) CancelPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	poID, err := uuid.Parse(chi.URLParam(r, "poID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid purchase order ID")
		return
	}
	po, err := h.orm.PurchaseOrder.Query().
		Where(entpurchaseorder.ID(poID), entpurchaseorder.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	if po.Status.String() == "received" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Received orders cannot be cancelled")
		return
	}
	if _, err := h.orm.PurchaseOrder.UpdateOneID(poID).SetStatus("cancelled").Save(r.Context()); err != nil {
		h.log.Error("cancel PO failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CANCEL_FAILED", "Failed to cancel purchase order")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}
