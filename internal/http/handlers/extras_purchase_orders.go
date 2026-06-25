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

	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entinventorybalance "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpurchaseorder "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpoline "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	"github.com/bengobox/inventory-service/internal/modules/documents"
)

// ─── Purchase Orders ──────────────────────────────────────────────────────────

type purchaseOrderDTO struct {
	ID            uuid.UUID  `json:"id"`
	PONumber      string     `json:"po_number"`
	SupplierName  string     `json:"supplier_name"`
	SupplierID    uuid.UUID  `json:"supplier_id"`
	WarehouseName string     `json:"warehouse_name"`
	WarehouseID   uuid.UUID  `json:"warehouse_id"`
	Status        string     `json:"status"`
	TotalAmount   float64    `json:"total_amount"`
	ExpectedDate  *time.Time `json:"expected_date"`
	Notes         string     `json:"notes"`
	PayTermDays               *int    `json:"pay_term_days"`
	AdditionalShippingCharges float64 `json:"additional_shipping_charges"`
	CreatedAt     time.Time  `json:"created_at"`
}

// ListPurchaseOrders handles GET /inventory/purchase-orders.
//
//	@Summary      List purchase orders
//	@Tags         Procurement
//	@Produce      json
//	@Param        search  query     string  false  "Filter by PO number or supplier name"
//	@Success      200     {array}   purchaseOrderDTO
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
	search := r.URL.Query().Get("search")

	orders, err := h.orm.PurchaseOrder.Query().
		Where(entpurchaseorder.TenantID(tenantID)).
		WithSupplier().
		WithWarehouse().
		Order(entpurchaseorder.ByCreatedAt()).
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
		if search != "" {
			needle := strings.ToLower(search)
			if !strings.Contains(strings.ToLower(o.PoNumber), needle) &&
				!strings.Contains(strings.ToLower(supplierName), needle) {
				continue
			}
		}
		dto := purchaseOrderDTO{
			ID:            o.ID,
			PONumber:      o.PoNumber,
			SupplierName:  supplierName,
			SupplierID:    o.SupplierID,
			WarehouseName: warehouseName,
			WarehouseID:   o.WarehouseID,
			Status:        o.Status.String(),
			TotalAmount:   o.TotalAmount,
			Notes:         o.Notes,
			PayTermDays:               o.PayTermDays,
			AdditionalShippingCharges: o.AdditionalShippingCharges,
			CreatedAt:     o.CreatedAt,
		}
		if o.ExpectedDate != nil {
			dto.ExpectedDate = o.ExpectedDate
		}
		result = append(result, dto)
	}
	writeJSON(w, http.StatusOK, result)
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
	if len(itemIDs) > 0 {
		items, _ := h.orm.Item.Query().
			Where(entitem.IDIn(itemIDs...)).
			All(r.Context())
		for _, it := range items {
			itemNames[it.ID] = it.Name
			itemSKUs[it.ID] = it.Sku
		}
	}

	type lineDTO struct {
		ID          uuid.UUID `json:"id"`
		ItemID      uuid.UUID `json:"item_id"`
		ItemName    string    `json:"item_name"`
		ItemSKU     string    `json:"item_sku"`
		Quantity    float64   `json:"quantity"`
		ReceivedQty float64   `json:"received_qty"`
		UnitCost    float64   `json:"unit_cost"`
		TotalCost   float64   `json:"total_cost"`
	}

	lines := make([]lineDTO, 0, len(po.Edges.Lines))
	for _, l := range po.Edges.Lines {
		lines = append(lines, lineDTO{
			ID:          l.ID,
			ItemID:      l.ItemID,
			ItemName:    itemNames[l.ItemID],
			ItemSKU:     itemSKUs[l.ItemID],
			Quantity:    l.QuantityOrdered,
			ReceivedQty: l.QuantityReceived,
			UnitCost:    l.UnitPrice,
			TotalCost:   l.TotalPrice,
		})
	}

	type poDetailDTO struct {
		purchaseOrderDTO
		LineItems []lineDTO `json:"line_items"`
	}

	dto := purchaseOrderDTO{
		ID:            po.ID,
		PONumber:      po.PoNumber,
		SupplierName:  supplierName,
		SupplierID:    po.SupplierID,
		WarehouseName: warehouseName,
		WarehouseID:   po.WarehouseID,
		Status:        po.Status.String(),
		TotalAmount:   po.TotalAmount,
		Notes:         po.Notes,
		PayTermDays:               po.PayTermDays,
		AdditionalShippingCharges: po.AdditionalShippingCharges,
		CreatedAt:     po.CreatedAt,
	}
	if po.ExpectedDate != nil {
		dto.ExpectedDate = po.ExpectedDate
	}

	writeJSON(w, http.StatusOK, poDetailDTO{purchaseOrderDTO: dto, LineItems: lines})
}

// ─── Purchase Order Writes ────────────────────────────────────────────────────

type createPOLineInput struct {
	ItemID   uuid.UUID `json:"item_id"`
	Quantity float64   `json:"quantity"`
	UnitCost float64   `json:"unit_cost"`
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
		total += float64(l.Quantity) * l.UnitCost
	}

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
		_, err = tx.PurchaseOrderLine.Create().
			SetPoID(po.ID).
			SetItemID(l.ItemID).
			SetQuantityOrdered(l.Quantity).
			SetUnitPrice(l.UnitCost).
			SetTotalPrice(float64(l.Quantity) * l.UnitCost).
			Save(r.Context())
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
		lineTotal := float64(l.Quantity) * l.UnitCost
		total += lineTotal
		if _, err = tx.PurchaseOrderLine.Create().
			SetPoID(po.ID).SetItemID(l.ItemID).SetQuantityOrdered(l.Quantity).
			SetUnitPrice(l.UnitCost).SetTotalPrice(lineTotal).Save(r.Context()); err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to write amended line")
			return
		}
	}
	upd := tx.PurchaseOrder.UpdateOneID(po.ID).SetTotalAmount(total).SetNotes(req.Notes)
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

	for _, line := range po.Edges.Lines {
		qty := line.QuantityOrdered
		bal, balErr := tx.InventoryBalance.Query().
			Where(entinventorybalance.TenantID(tenantID), entinventorybalance.ItemID(line.ItemID)).
			First(r.Context())
		if balErr != nil {
			if !ent.IsNotFound(balErr) {
				err = balErr
				writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to query balance")
				return
			}
			_, err = tx.InventoryBalance.Create().
				SetTenantID(tenantID).
				SetItemID(line.ItemID).
				SetWarehouseID(po.WarehouseID).
				SetOnHand(qty).
				SetAvailable(qty).
				SetReserved(0).
				Save(r.Context())
		} else {
			_, err = tx.InventoryBalance.UpdateOneID(bal.ID).
				SetOnHand(bal.OnHand + qty).
				SetAvailable(bal.Available + qty).
				Save(r.Context())
		}
		if err != nil {
			h.log.Error("update balance on PO receive failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to update inventory balance")
			return
		}
	}

	if _, err = tx.PurchaseOrder.UpdateOneID(poID).SetStatus("received").Save(r.Context()); err != nil {
		h.log.Error("mark PO received failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to mark order received")
		return
	}

	if err = tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "COMMIT_FAILED", "Failed to commit")
		return
	}

	// Publish inventory.purchase_order.received with enriched supplier payment details for treasury auto-payout
	go func() {
		ctx := context.Background()

		payload := map[string]any{
			"po_id":        po.ID,
			"po_number":    po.PoNumber,
			"tenant_id":    tenantID,
			"supplier_id":  po.SupplierID,
			"total_amount": po.TotalAmount,
			"currency":     po.Currency,
		}
		if po.Edges.Supplier != nil {
			supplier := po.Edges.Supplier
			payload["supplier_name"] = supplier.Name
			payload["supplier_contact_email"] = supplier.ContactEmail
			payload["supplier_contact_phone"] = supplier.ContactPhone
			payload["supplier_payment_method"] = supplier.PaymentMethodType
			payload["supplier_mpesa_phone"] = supplier.MpesaPhone
			payload["supplier_mpesa_business_name"] = supplier.MpesaBusinessName
			payload["supplier_bank_account_number"] = supplier.BankAccountNumber
			payload["supplier_bank_name"] = supplier.BankName
			payload["supplier_tax_pin"] = supplier.TaxPin
			payload["supplier_paystack_recipient_code"] = supplier.PaystackRecipientCode
			payload["requires_invoice_before_payment"] = supplier.RequiresInvoiceBeforePayment
			payload["auto_pay_enabled"] = supplier.AutoPayEnabled
		}

		evt := &events.Event{
			ID:            uuid.New(),
			TenantID:      tenantID,
			AggregateType: "inventory",
			AggregateID:   po.ID,
			EventType:     "purchase_order.received",
			Payload:       payload,
			Timestamp:     time.Now().UTC(),
		}
		evtPayload, marshalErr := evt.ToJSON()
		if marshalErr != nil {
			h.log.Warn("PO received event: marshal failed", zap.Error(marshalErr))
			return
		}
		_, writeErr := h.orm.OutboxEvent.Create().
			SetID(evt.ID).
			SetTenantID(tenantID).
			SetAggregateType(evt.AggregateType).
			SetAggregateID(evt.AggregateID.String()).
			SetEventType(evt.EventType).
			SetPayload(json.RawMessage(evtPayload)).
			SetStatus("PENDING").
			SetCreatedAt(evt.Timestamp).
			Save(ctx)
		if writeErr != nil {
			h.log.Warn("PO received event: outbox write failed", zap.Error(writeErr))
		}
	}()

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
