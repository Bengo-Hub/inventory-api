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
	entinventorylot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpurchaseorder "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entstockadjustment "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// InventoryExtrasHandler handles stock, lots, suppliers, purchase-orders, and activity endpoints.
type InventoryExtrasHandler struct {
	log     *zap.Logger
	orm     *ent.Client
	rbacSvc *rbac.Service
}

// NewInventoryExtrasHandler creates the handler.
func NewInventoryExtrasHandler(log *zap.Logger, orm *ent.Client, rbacSvc *rbac.Service) *InventoryExtrasHandler {
	return &InventoryExtrasHandler{
		log:     log.Named("inventory_extras.handler"),
		orm:     orm,
		rbacSvc: rbacSvc,
	}
}

// publishSupplierEvent writes a supplier event to the outbox table.
func (h *InventoryExtrasHandler) publishSupplierEvent(ctx context.Context, s *ent.Supplier, eventType string) {
	evt := &events.Event{
		ID:            uuid.New(),
		TenantID:      s.TenantID,
		AggregateType: "supplier",
		AggregateID:   s.ID,
		EventType:     eventType,
		Payload: map[string]any{
			"id":                           s.ID,
			"tenant_id":                    s.TenantID,
			"name":                         s.Name,
			"code":                         s.Code,
			"contact_name":                 s.ContactName,
			"contact_email":                s.ContactEmail,
			"contact_phone":                s.ContactPhone,
			"payment_method_type":          s.PaymentMethodType,
			"mpesa_phone":                  s.MpesaPhone,
			"mpesa_business_name":          s.MpesaBusinessName,
			"bank_account_number":          s.BankAccountNumber,
			"bank_name":                    s.BankName,
			"bank_branch":                  s.BankBranch,
			"tax_pin":                      s.TaxPin,
			"paystack_recipient_code":      s.PaystackRecipientCode,
			"requires_invoice_before_payment": s.RequiresInvoiceBeforePayment,
			"is_active":                    s.IsActive,
		},
		Timestamp: time.Now().UTC(),
	}

	payload, err := evt.ToJSON()
	if err != nil {
		h.log.Warn("supplier event: marshal failed", zap.Error(err))
		return
	}
	_, err = h.orm.OutboxEvent.Create().
		SetID(evt.ID).
		SetTenantID(s.TenantID).
		SetAggregateType(evt.AggregateType).
		SetAggregateID(evt.AggregateID.String()).
		SetEventType(evt.EventType).
		SetPayload(json.RawMessage(payload)).
		SetStatus("PENDING").
		SetCreatedAt(evt.Timestamp).
		Save(ctx)
	if err != nil {
		h.log.Warn("supplier event: outbox write failed", zap.Error(err))
	}
}

// RegisterRoutes wires all extra inventory routes under /inventory/... on the given tenant router.
func (h *InventoryExtrasHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	// Register directly (no sub-Route) so we don't conflict with inventoryHandler's
	// r.Route("/inventory", ...) which is registered first on the same router.
	r.Get("/inventory/stock", h.ListStock)
	r.Get("/inventory/lots", h.ListLots)
	r.Get("/inventory/suppliers", h.ListSuppliers)
	r.With(perm(rbac.PermItemsAdd)).Post("/inventory/suppliers", h.CreateSupplier)
	r.Get("/inventory/suppliers/{supplierID}", h.GetSupplier)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/suppliers/{supplierID}", h.UpdateSupplier)
	r.With(perm(rbac.PermItemsDelete)).Delete("/inventory/suppliers/{supplierID}", h.DeleteSupplier)
	r.Get("/inventory/purchase-orders", h.ListPurchaseOrders)
	r.Get("/inventory/purchase-orders/{poID}", h.GetPurchaseOrder)
	r.With(perm(rbac.PermItemsAdd)).Post("/inventory/purchase-orders", h.CreatePurchaseOrder)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/purchase-orders/{poID}/receive", h.ReceivePurchaseOrder)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/purchase-orders/{poID}/cancel", h.CancelPurchaseOrder)
	r.Get("/inventory/activity", h.ListActivity)
}

// ─── Stock ────────────────────────────────────────────────────────────────────

type stockLevelDTO struct {
	ID            uuid.UUID `json:"id"`
	ItemName      string    `json:"itemName"`
	SKU           string    `json:"sku"`
	WarehouseName string    `json:"warehouseName"`
	WarehouseID   uuid.UUID `json:"warehouseId"`
	Available     int       `json:"available"`
	Reserved      int       `json:"reserved"`
	ReorderPoint  *int      `json:"reorderPoint"`
	Unit          string    `json:"unit"`
}

func (h *InventoryExtrasHandler) ListStock(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	search := r.URL.Query().Get("search")

	balances, err := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID)).
		WithItem().
		WithWarehouse().
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list stock")
		return
	}

	result := make([]stockLevelDTO, 0, len(balances))
	for _, b := range balances {
		itemName, sku := "", ""
		if b.Edges.Item != nil {
			itemName = b.Edges.Item.Name
			sku = b.Edges.Item.Sku
		}
		warehouseName := ""
		if b.Edges.Warehouse != nil {
			warehouseName = b.Edges.Warehouse.Name
		}
		if search != "" {
			needle := strings.ToLower(search)
			if !strings.Contains(strings.ToLower(itemName), needle) &&
				!strings.Contains(strings.ToLower(sku), needle) &&
				!strings.Contains(strings.ToLower(warehouseName), needle) {
				continue
			}
		}
		var reorderPoint *int
		if b.ReorderLevel > 0 {
			v := b.ReorderLevel
			reorderPoint = &v
		}
		result = append(result, stockLevelDTO{
			ID:            b.ID,
			ItemName:      itemName,
			SKU:           sku,
			WarehouseName: warehouseName,
			WarehouseID:   b.WarehouseID,
			Available:     b.Available,
			Reserved:      b.Reserved,
			ReorderPoint:  reorderPoint,
			Unit:          b.UnitOfMeasure,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

// ─── Lots ─────────────────────────────────────────────────────────────────────

type lotDTO struct {
	ID          uuid.UUID  `json:"id"`
	LotNumber   string     `json:"lotNumber"`
	ItemName    string     `json:"itemName"`
	ItemID      uuid.UUID  `json:"itemId"`
	BatchNumber string     `json:"batchNumber"`
	ExpiryDate  *time.Time `json:"expiryDate"`
	Quantity    int        `json:"quantity"`
	Status      string     `json:"status"`
}

func (h *InventoryExtrasHandler) ListLots(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	search := r.URL.Query().Get("search")

	lots, err := h.orm.InventoryLot.Query().
		Where(entinventorylot.TenantID(tenantID)).
		WithItem().
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list lots")
		return
	}

	result := make([]lotDTO, 0, len(lots))
	for _, l := range lots {
		itemName := ""
		if l.Edges.Item != nil {
			itemName = l.Edges.Item.Name
		}
		if search != "" {
			needle := strings.ToLower(search)
			if !strings.Contains(strings.ToLower(itemName), needle) &&
				!strings.Contains(strings.ToLower(l.LotNumber), needle) {
				continue
			}
		}
		dto := lotDTO{
			ID:          l.ID,
			LotNumber:   l.LotNumber,
			ItemName:    itemName,
			ItemID:      l.ItemID,
			BatchNumber: l.LotNumber,
			Quantity:    l.Quantity,
			Status:      l.Status.String(),
		}
		if l.ExpiryDate != nil {
			dto.ExpiryDate = l.ExpiryDate
		}
		result = append(result, dto)
	}
	writeJSON(w, http.StatusOK, result)
}

// ─── Suppliers ────────────────────────────────────────────────────────────────

type supplierDTO struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	ContactName   string    `json:"contactName"`
	Email         string    `json:"email"`
	Phone         string    `json:"phone"`
	Status        string    `json:"status"`
	ItemsSupplied int       `json:"itemsSupplied"`
}

type supplierPayload struct {
	Name        string `json:"name"`
	ContactName string `json:"contactName"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
}

func (h *InventoryExtrasHandler) ListSuppliers(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	search := r.URL.Query().Get("search")

	suppliers, err := h.orm.Supplier.Query().
		Where(entsupplier.TenantID(tenantID)).
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list suppliers")
		return
	}

	result := make([]supplierDTO, 0, len(suppliers))
	for _, s := range suppliers {
		if search != "" {
			needle := strings.ToLower(search)
			if !strings.Contains(strings.ToLower(s.Name), needle) &&
				!strings.Contains(strings.ToLower(s.ContactName), needle) {
				continue
			}
		}
		status := "inactive"
		if s.IsActive {
			status = "active"
		}
		result = append(result, supplierDTO{
			ID:          s.ID,
			Name:        s.Name,
			ContactName: s.ContactName,
			Email:       s.ContactEmail,
			Phone:       s.ContactPhone,
			Status:      status,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *InventoryExtrasHandler) CreateSupplier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req supplierPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Supplier name is required")
		return
	}
	code := strings.ToUpper(strings.ReplaceAll(req.Name, " ", "_"))
	if len(code) > 20 {
		code = code[:20]
	}

	s, err := h.orm.Supplier.Create().
		SetTenantID(tenantID).
		SetName(req.Name).
		SetCode(code).
		SetContactName(req.ContactName).
		SetContactEmail(req.Email).
		SetContactPhone(req.Phone).
		Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create supplier")
		return
	}
	h.publishSupplierEvent(r.Context(), s, "inventory.supplier.created")
	writeJSON(w, http.StatusCreated, supplierDTO{
		ID:          s.ID,
		Name:        s.Name,
		ContactName: s.ContactName,
		Email:       s.ContactEmail,
		Phone:       s.ContactPhone,
		Status:      "active",
	})
}

func (h *InventoryExtrasHandler) GetSupplier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	supplierID, err := uuid.Parse(chi.URLParam(r, "supplierID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid supplier ID")
		return
	}
	s, err := h.orm.Supplier.Query().
		Where(entsupplier.ID(supplierID), entsupplier.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Supplier not found")
		return
	}
	status := "inactive"
	if s.IsActive {
		status = "active"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                             s.ID,
		"name":                           s.Name,
		"code":                           s.Code,
		"contact_name":                   s.ContactName,
		"contact_email":                  s.ContactEmail,
		"contact_phone":                  s.ContactPhone,
		"payment_method_type":            s.PaymentMethodType,
		"mpesa_phone":                    s.MpesaPhone,
		"mpesa_business_name":            s.MpesaBusinessName,
		"bank_account_number":            s.BankAccountNumber,
		"bank_name":                      s.BankName,
		"bank_branch":                    s.BankBranch,
		"tax_pin":                        s.TaxPin,
		"requires_invoice_before_payment": s.RequiresInvoiceBeforePayment,
		"credit_limit":                   s.CreditLimit,
		"paystack_recipient_code":        s.PaystackRecipientCode,
		"status":                         status,
	})
}

func (h *InventoryExtrasHandler) UpdateSupplier(w http.ResponseWriter, r *http.Request) {
	if _, err := parseTenantID(r); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	supplierID, err := uuid.Parse(chi.URLParam(r, "supplierID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid supplier ID")
		return
	}
	var req supplierPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	s, err := h.orm.Supplier.UpdateOneID(supplierID).
		SetName(req.Name).
		SetContactName(req.ContactName).
		SetContactEmail(req.Email).
		SetContactPhone(req.Phone).
		Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update supplier")
		return
	}
	h.publishSupplierEvent(r.Context(), s, "inventory.supplier.updated")
	status := "inactive"
	if s.IsActive {
		status = "active"
	}
	writeJSON(w, http.StatusOK, supplierDTO{
		ID:          s.ID,
		Name:        s.Name,
		ContactName: s.ContactName,
		Email:       s.ContactEmail,
		Phone:       s.ContactPhone,
		Status:      status,
	})
}

// ─── Purchase Orders ──────────────────────────────────────────────────────────

type purchaseOrderDTO struct {
	ID           uuid.UUID `json:"id"`
	PONumber     string    `json:"poNumber"`
	SupplierName string    `json:"supplierName"`
	SupplierID   uuid.UUID `json:"supplierId"`
	Status       string    `json:"status"`
	Total        float64   `json:"total"`
	CreatedAt    time.Time `json:"createdAt"`
}

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
		if search != "" {
			needle := strings.ToLower(search)
			if !strings.Contains(strings.ToLower(o.PoNumber), needle) &&
				!strings.Contains(strings.ToLower(supplierName), needle) {
				continue
			}
		}
		result = append(result, purchaseOrderDTO{
			ID:           o.ID,
			PONumber:     o.PoNumber,
			SupplierName: supplierName,
			SupplierID:   o.SupplierID,
			Status:       o.Status.String(),
			Total:        o.TotalAmount,
			CreatedAt:    o.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

// GetPurchaseOrder handles GET /inventory/purchase-orders/{poID}.
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
	type lineDTO struct {
		ID        uuid.UUID `json:"id"`
		ItemID    uuid.UUID `json:"itemId"`
		ItemName  string    `json:"itemName"`
		SKU       string    `json:"sku"`
		Quantity  int       `json:"quantity"`
		UnitPrice float64   `json:"unitPrice"`
		Total     float64   `json:"total"`
	}
	// Collect item IDs to fetch names/SKUs
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
	lines := make([]lineDTO, 0, len(po.Edges.Lines))
	for _, l := range po.Edges.Lines {
		lines = append(lines, lineDTO{
			ID:        l.ID,
			ItemID:    l.ItemID,
			ItemName:  itemNames[l.ItemID],
			SKU:       itemSKUs[l.ItemID],
			Quantity:  l.QuantityOrdered,
			UnitPrice: l.UnitPrice,
			Total:     l.TotalPrice,
		})
	}
	type poDetailDTO struct {
		purchaseOrderDTO
		LineItems []lineDTO `json:"lineItems"`
	}
	writeJSON(w, http.StatusOK, poDetailDTO{
		purchaseOrderDTO: purchaseOrderDTO{
			ID:           po.ID,
			PONumber:     po.PoNumber,
			SupplierName: supplierName,
			SupplierID:   po.SupplierID,
			Status:       po.Status.String(),
			Total:        po.TotalAmount,
			CreatedAt:    po.CreatedAt,
		},
		LineItems: lines,
	})
}

// DeleteSupplier handles DELETE /inventory/suppliers/{supplierID} — soft-deletes a supplier.
func (h *InventoryExtrasHandler) DeleteSupplier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	supplierID, err := uuid.Parse(chi.URLParam(r, "supplierID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid supplier ID")
		return
	}
	existing, err := h.orm.Supplier.Get(r.Context(), supplierID)
	if err != nil || existing.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Supplier not found")
		return
	}
	updated, err := h.orm.Supplier.UpdateOneID(supplierID).SetIsActive(false).Save(r.Context())
	if err != nil {
		h.log.Error("delete supplier failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete supplier")
		return
	}
	h.publishSupplierEvent(r.Context(), updated, "inventory.supplier.deleted")
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ─── Purchase Order Writes ────────────────────────────────────────────────────

type createPOLineInput struct {
	ItemID    uuid.UUID `json:"item_id"`
	Quantity  int       `json:"quantity"`
	UnitPrice float64   `json:"unit_price"`
}

type createPOInput struct {
	SupplierID           uuid.UUID         `json:"supplier_id"`
	ExpectedDeliveryDate *time.Time        `json:"expected_delivery_date"`
	Notes                string            `json:"notes"`
	Lines                []createPOLineInput `json:"lines"`
}

// CreatePurchaseOrder handles POST /inventory/purchase-orders.
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
	if len(req.Lines) == 0 {
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
	for _, l := range req.Lines {
		total += float64(l.Quantity) * l.UnitPrice
	}

	poNumber := "PO-" + strings.ToUpper(tenantID.String()[:8]) + "-" + time.Now().Format("20060102150405")
	poCreate := tx.PurchaseOrder.Create().
		SetTenantID(tenantID).
		SetSupplierID(req.SupplierID).
		SetPoNumber(poNumber).
		SetTotalAmount(total).
		SetNotes(req.Notes)
	if req.ExpectedDeliveryDate != nil {
		poCreate = poCreate.SetExpectedDate(*req.ExpectedDeliveryDate)
	}
	po, err := poCreate.Save(r.Context())
	if err != nil {
		h.log.Error("create purchase order failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create purchase order")
		return
	}

	for _, l := range req.Lines {
		_, err = tx.PurchaseOrderLine.Create().
			SetPoID(po.ID).
			SetItemID(l.ItemID).
			SetQuantityOrdered(l.Quantity).
			SetUnitPrice(l.UnitPrice).
			SetTotalPrice(float64(l.Quantity) * l.UnitPrice).
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
		"id":        po.ID,
		"po_number": po.PoNumber,
		"status":    po.Status.String(),
		"total":     po.TotalAmount,
	})
}

// ReceivePurchaseOrder handles PUT /inventory/purchase-orders/{poID}/receive.
// Marks the PO as received and increments on_hand stock for each line.
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
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Purchase order not found")
		return
	}
	if po.Status.String() != "pending" && po.Status.String() != "draft" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "Only pending/draft orders can be received")
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
			// Create balance record if it doesn't exist
			_, err = tx.InventoryBalance.Create().
				SetTenantID(tenantID).
				SetItemID(line.ItemID).
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
		supplier, supErr := h.orm.Supplier.Get(ctx, po.SupplierID)

		payload := map[string]any{
			"po_id":        po.ID,
			"po_number":    po.PoNumber,
			"tenant_id":    tenantID,
			"supplier_id":  po.SupplierID,
			"total_amount": po.TotalAmount,
			"currency":     po.Currency,
		}
		if supErr == nil {
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

// ─── Activity ─────────────────────────────────────────────────────────────────

type activityItemDTO struct {
	ID          uuid.UUID `json:"id"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Timestamp   time.Time `json:"timestamp"`
	Delta       *int      `json:"delta,omitempty"`
}

func (h *InventoryExtrasHandler) ListActivity(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	adjustments, err := h.orm.StockAdjustment.Query().
		Where(entstockadjustment.TenantID(tenantID)).
		Order(entstockadjustment.ByAdjustedAt()).
		Limit(10).
		All(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, []activityItemDTO{})
		return
	}

	result := make([]activityItemDTO, 0, len(adjustments))
	for _, adj := range adjustments {
		desc := adj.Reason.String() + " stock adjustment"
		if adj.Notes != "" {
			desc = adj.Notes
		}
		delta := int(adj.QuantityChange)
		result = append(result, activityItemDTO{
			ID:          adj.ID,
			Type:        "adjustment",
			Description: desc,
			Timestamp:   adj.AdjustedAt,
			Delta:       &delta,
		})
	}
	writeJSON(w, http.StatusOK, result)
}
