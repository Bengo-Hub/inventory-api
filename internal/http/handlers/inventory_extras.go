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
	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entinventorybalance "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entinventorylot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpurchaseorder "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entstockadjustment "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
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
			"id":                              s.ID,
			"tenant_id":                       s.TenantID,
			"name":                            s.Name,
			"code":                            s.Code,
			"contact_name":                    s.ContactName,
			"contact_email":                   s.ContactEmail,
			"contact_phone":                   s.ContactPhone,
			"payment_method_type":             s.PaymentMethodType,
			"mpesa_phone":                     s.MpesaPhone,
			"mpesa_business_name":             s.MpesaBusinessName,
			"bank_account_number":             s.BankAccountNumber,
			"bank_name":                       s.BankName,
			"bank_branch":                     s.BankBranch,
			"tax_pin":                         s.TaxPin,
			"paystack_recipient_code":         s.PaystackRecipientCode,
			"requires_invoice_before_payment": s.RequiresInvoiceBeforePayment,
			"is_active":                       s.IsActive,
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

	// Stock levels
	r.Get("/inventory/stock", h.ListStock)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/stock/{sku}/reorder-config", h.UpdateReorderConfig)

	// Lots & Batches
	r.Get("/inventory/lots", h.ListLots)
	r.With(perm(rbac.PermItemsAdd)).Post("/inventory/lots", h.CreateLot)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/lots/{lotID}", h.UpdateLot)
	r.With(perm(rbac.PermItemsDelete)).Delete("/inventory/lots/{lotID}", h.DeleteLot)

	// Suppliers
	r.Get("/inventory/suppliers", h.ListSuppliers)
	r.With(perm(rbac.PermItemsAdd)).Post("/inventory/suppliers", h.CreateSupplier)
	r.Get("/inventory/suppliers/{supplierID}", h.GetSupplier)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/suppliers/{supplierID}", h.UpdateSupplier)
	r.With(perm(rbac.PermItemsDelete)).Delete("/inventory/suppliers/{supplierID}", h.DeleteSupplier)

	// Purchase Orders
	r.Get("/inventory/purchase-orders", h.ListPurchaseOrders)
	r.Get("/inventory/purchase-orders/{poID}", h.GetPurchaseOrder)
	r.With(perm(rbac.PermItemsAdd)).Post("/inventory/purchase-orders", h.CreatePurchaseOrder)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/purchase-orders/{poID}/send", h.SendPurchaseOrder)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/purchase-orders/{poID}/receive", h.ReceivePurchaseOrder)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/purchase-orders/{poID}/cancel", h.CancelPurchaseOrder)

	// Activity
	r.Get("/inventory/activity", h.ListActivity)
}

// ─── Stock ────────────────────────────────────────────────────────────────────

type stockLevelDTO struct {
	ID           uuid.UUID `json:"id"`
	ItemName     string    `json:"item_name"`
	SKU          string    `json:"sku"`
	WarehouseName string   `json:"warehouse_name"`
	WarehouseID  uuid.UUID `json:"warehouse_id"`
	Available    int       `json:"available"`
	Reserved     int       `json:"reserved"`
	ReorderPoint *int      `json:"reorder_point"`
	Unit         string    `json:"unit"`
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
	ID               uuid.UUID  `json:"id"`
	LotNumber        string     `json:"lot_number"`
	ItemID           uuid.UUID  `json:"item_id"`
	ItemName         string     `json:"item_name"`
	ItemSKU          string     `json:"item_sku"`
	WarehouseID      uuid.UUID  `json:"warehouse_id"`
	WarehouseName    string     `json:"warehouse_name"`
	ExpiryDate       *time.Time `json:"expiry_date"`
	ManufacturedDate *time.Time `json:"manufacture_date"`
	Quantity         int        `json:"quantity"`
	CostPrice        *float64   `json:"cost_per_unit"`
	Status           string     `json:"status"`
	SupplierRef      string     `json:"supplier_reference"`
	CreatedAt        time.Time  `json:"created_at"`
}

func lotToDTO(l *ent.InventoryLot) lotDTO {
	dto := lotDTO{
		ID:          l.ID,
		LotNumber:   l.LotNumber,
		ItemID:      l.ItemID,
		WarehouseID: l.WarehouseID,
		Quantity:    l.Quantity,
		Status:      l.Status.String(),
		SupplierRef: l.SupplierReference,
		CreatedAt:   l.CreatedAt,
	}
	if l.ExpiryDate != nil {
		dto.ExpiryDate = l.ExpiryDate
	}
	if l.ManufacturedDate != nil {
		dto.ManufacturedDate = l.ManufacturedDate
	}
	if l.CostPrice != nil {
		dto.CostPrice = l.CostPrice
	}
	if l.Edges.Item != nil {
		dto.ItemName = l.Edges.Item.Name
		dto.ItemSKU = l.Edges.Item.Sku
	}
	if l.Edges.Warehouse != nil {
		dto.WarehouseName = l.Edges.Warehouse.Name
	}
	return dto
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
		WithWarehouse().
		All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list lots")
		return
	}

	result := make([]lotDTO, 0, len(lots))
	for _, l := range lots {
		dto := lotToDTO(l)
		if search != "" {
			needle := strings.ToLower(search)
			if !strings.Contains(strings.ToLower(dto.LotNumber), needle) &&
				!strings.Contains(strings.ToLower(dto.ItemName), needle) &&
				!strings.Contains(strings.ToLower(dto.ItemSKU), needle) {
				continue
			}
		}
		result = append(result, dto)
	}
	writeJSON(w, http.StatusOK, result)
}

type createLotInput struct {
	ItemID           uuid.UUID  `json:"item_id"`
	WarehouseID      uuid.UUID  `json:"warehouse_id"`
	LotNumber        string     `json:"lot_number"`
	ExpiryDate       *time.Time `json:"expiry_date"`
	ManufacturedDate *time.Time `json:"manufacture_date"`
	Quantity         int        `json:"quantity"`
	CostPrice        float64    `json:"cost_per_unit"`
	SupplierRef      string     `json:"supplier_reference"`
}

func (h *InventoryExtrasHandler) CreateLot(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req createLotInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.ItemID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_ITEM", "item_id is required")
		return
	}
	if req.WarehouseID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_WAREHOUSE", "warehouse_id is required")
		return
	}
	if req.LotNumber == "" {
		writeError(w, http.StatusBadRequest, "MISSING_LOT_NUMBER", "lot_number is required")
		return
	}
	if req.Quantity <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_QUANTITY", "quantity must be positive")
		return
	}

	create := h.orm.InventoryLot.Create().
		SetTenantID(tenantID).
		SetItemID(req.ItemID).
		SetWarehouseID(req.WarehouseID).
		SetLotNumber(req.LotNumber).
		SetQuantity(req.Quantity).
		SetSupplierReference(req.SupplierRef)

	if req.ExpiryDate != nil {
		create = create.SetExpiryDate(*req.ExpiryDate)
	}
	if req.ManufacturedDate != nil {
		create = create.SetManufacturedDate(*req.ManufacturedDate)
	}
	if req.CostPrice > 0 {
		create = create.SetCostPrice(req.CostPrice)
	}

	lot, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create lot failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create lot")
		return
	}

	// Reload with edges
	lot, _ = h.orm.InventoryLot.Query().
		Where(entinventorylot.ID(lot.ID)).
		WithItem().
		WithWarehouse().
		Only(r.Context())

	writeJSON(w, http.StatusCreated, lotToDTO(lot))
}

type updateLotInput struct {
	ExpiryDate *time.Time `json:"expiry_date"`
	Quantity   int        `json:"quantity"`
	Status     string     `json:"status"`
	SupplierRef string    `json:"supplier_reference"`
}

func (h *InventoryExtrasHandler) UpdateLot(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	lotID, err := uuid.Parse(chi.URLParam(r, "lotID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid lot ID")
		return
	}

	existing, err := h.orm.InventoryLot.Query().
		Where(entinventorylot.ID(lotID), entinventorylot.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Lot not found")
		return
	}

	var req updateLotInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	update := h.orm.InventoryLot.UpdateOneID(existing.ID).
		SetSupplierReference(req.SupplierRef)

	if req.Quantity > 0 {
		update = update.SetQuantity(req.Quantity)
	}
	if req.ExpiryDate != nil {
		update = update.SetExpiryDate(*req.ExpiryDate)
	}
	if req.Status != "" {
		update = update.SetStatus(entinventorylot.Status(req.Status))
	}

	lot, err := update.Save(r.Context())
	if err != nil {
		h.log.Error("update lot failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update lot")
		return
	}

	lot, _ = h.orm.InventoryLot.Query().
		Where(entinventorylot.ID(lot.ID)).
		WithItem().
		WithWarehouse().
		Only(r.Context())

	writeJSON(w, http.StatusOK, lotToDTO(lot))
}

func (h *InventoryExtrasHandler) DeleteLot(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	lotID, err := uuid.Parse(chi.URLParam(r, "lotID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid lot ID")
		return
	}
	existing, err := h.orm.InventoryLot.Query().
		Where(entinventorylot.ID(lotID), entinventorylot.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Lot not found")
		return
	}
	if err := h.orm.InventoryLot.DeleteOneID(existing.ID).Exec(r.Context()); err != nil {
		h.log.Error("delete lot failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete lot")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ─── Suppliers ────────────────────────────────────────────────────────────────

type supplierDTO struct {
	ID                          uuid.UUID `json:"id"`
	Name                        string    `json:"name"`
	Code                        string    `json:"code"`
	ContactPerson               string    `json:"contact_person"`
	Email                       string    `json:"email"`
	Phone                       string    `json:"phone"`
	Address                     string    `json:"address"`
	IsActive                    bool      `json:"is_active"`
	PaymentMethodType           string    `json:"payment_method_type"`
	MpesaPhone                  string    `json:"mpesa_phone"`
	MpesaBusinessName           string    `json:"mpesa_business_name"`
	BankAccountNumber           string    `json:"bank_account_number"`
	BankName                    string    `json:"bank_name"`
	BankBranch                  string    `json:"bank_branch"`
	TaxPin                      string    `json:"tax_pin"`
	RequiresInvoiceBeforePayment bool     `json:"requires_invoice_before_payment"`
	AutoPayEnabled              bool      `json:"auto_pay_enabled"`
	PaymentTermsDays            int       `json:"payment_terms_days"`
	CreditLimit                 *float64  `json:"credit_limit"`
	CreatedAt                   time.Time `json:"created_at"`
}

type supplierPayload struct {
	Name                        string  `json:"name"`
	ContactPerson               string  `json:"contact_person"`
	Email                       string  `json:"email"`
	Phone                       string  `json:"phone"`
	Address                     string  `json:"address"`
	PaymentMethodType           string  `json:"payment_method_type"`
	MpesaPhone                  string  `json:"mpesa_phone"`
	MpesaBusinessName           string  `json:"mpesa_business_name"`
	BankAccountNumber           string  `json:"bank_account_number"`
	BankName                    string  `json:"bank_name"`
	BankBranch                  string  `json:"bank_branch"`
	TaxPin                      string  `json:"tax_pin"`
	RequiresInvoiceBeforePayment bool   `json:"requires_invoice_before_payment"`
	AutoPayEnabled              bool    `json:"auto_pay_enabled"`
	PaymentTermsDays            int     `json:"payment_terms_days"`
	CreditLimit                 float64 `json:"credit_limit"`
}

func supplierToDTO(s *ent.Supplier) supplierDTO {
	dto := supplierDTO{
		ID:                          s.ID,
		Name:                        s.Name,
		Code:                        s.Code,
		ContactPerson:               s.ContactName,
		Email:                       s.ContactEmail,
		Phone:                       s.ContactPhone,
		Address:                     s.Address,
		IsActive:                    s.IsActive,
		PaymentMethodType:           string(s.PaymentMethodType),
		MpesaPhone:                  s.MpesaPhone,
		MpesaBusinessName:           s.MpesaBusinessName,
		BankAccountNumber:           s.BankAccountNumber,
		BankName:                    s.BankName,
		BankBranch:                  s.BankBranch,
		TaxPin:                      s.TaxPin,
		RequiresInvoiceBeforePayment: s.RequiresInvoiceBeforePayment,
		AutoPayEnabled:              s.AutoPayEnabled,
		PaymentTermsDays:            s.PaymentTermsDays,
		CreatedAt:                   s.CreatedAt,
	}
	if s.CreditLimit != 0 {
		v := s.CreditLimit
		dto.CreditLimit = &v
	}
	return dto
}

func (h *InventoryExtrasHandler) ListSuppliers(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	search := r.URL.Query().Get("search")

	q := h.orm.Supplier.Query().Where(entsupplier.TenantID(tenantID))
	if search != "" {
		q = q.Where(entsupplier.Or(
			entsupplier.NameContainsFold(search),
			entsupplier.ContactNameContainsFold(search),
		))
	}
	total, _ := q.Clone().Count(r.Context())
	suppliers, err := q.Order(ent.Asc(entsupplier.FieldName)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list suppliers")
		return
	}

	result := make([]supplierDTO, len(suppliers))
	for i, s := range suppliers {
		result[i] = supplierToDTO(s)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(result, total, p))
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

	create := h.orm.Supplier.Create().
		SetTenantID(tenantID).
		SetName(req.Name).
		SetCode(code).
		SetContactName(req.ContactPerson).
		SetContactEmail(req.Email).
		SetContactPhone(req.Phone).
		SetAddress(req.Address).
		SetRequiresInvoiceBeforePayment(req.RequiresInvoiceBeforePayment).
		SetAutoPayEnabled(req.AutoPayEnabled).
		SetPaymentTermsDays(req.PaymentTermsDays).
		SetMpesaPhone(req.MpesaPhone).
		SetMpesaBusinessName(req.MpesaBusinessName).
		SetBankAccountNumber(req.BankAccountNumber).
		SetBankName(req.BankName).
		SetBankBranch(req.BankBranch).
		SetTaxPin(req.TaxPin)

	if req.PaymentMethodType != "" {
		create = create.SetPaymentMethodType(entsupplier.PaymentMethodType(req.PaymentMethodType))
	}
	if req.CreditLimit > 0 {
		create = create.SetCreditLimit(req.CreditLimit)
	}

	s, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create supplier failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create supplier")
		return
	}
	h.publishSupplierEvent(r.Context(), s, "inventory.supplier.created")
	writeJSON(w, http.StatusCreated, supplierToDTO(s))
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
	writeJSON(w, http.StatusOK, supplierToDTO(s))
}

func (h *InventoryExtrasHandler) UpdateSupplier(w http.ResponseWriter, r *http.Request) {
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
	existing, err := h.orm.Supplier.Query().
		Where(entsupplier.ID(supplierID), entsupplier.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Supplier not found")
		return
	}

	var req supplierPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	update := h.orm.Supplier.UpdateOneID(existing.ID).
		SetContactName(req.ContactPerson).
		SetContactEmail(req.Email).
		SetContactPhone(req.Phone).
		SetAddress(req.Address).
		SetRequiresInvoiceBeforePayment(req.RequiresInvoiceBeforePayment).
		SetAutoPayEnabled(req.AutoPayEnabled).
		SetPaymentTermsDays(req.PaymentTermsDays).
		SetMpesaPhone(req.MpesaPhone).
		SetMpesaBusinessName(req.MpesaBusinessName).
		SetBankAccountNumber(req.BankAccountNumber).
		SetBankName(req.BankName).
		SetBankBranch(req.BankBranch).
		SetTaxPin(req.TaxPin)

	if req.Name != "" {
		update = update.SetName(req.Name)
	}
	if req.PaymentMethodType != "" {
		update = update.SetPaymentMethodType(entsupplier.PaymentMethodType(req.PaymentMethodType))
	}
	if req.CreditLimit > 0 {
		update = update.SetCreditLimit(req.CreditLimit)
	}

	s, err := update.Save(r.Context())
	if err != nil {
		h.log.Error("update supplier failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update supplier")
		return
	}
	h.publishSupplierEvent(r.Context(), s, "inventory.supplier.updated")
	writeJSON(w, http.StatusOK, supplierToDTO(s))
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
	CreatedAt     time.Time  `json:"created_at"`
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
		ID         uuid.UUID `json:"id"`
		ItemID     uuid.UUID `json:"item_id"`
		ItemName   string    `json:"item_name"`
		ItemSKU    string    `json:"item_sku"`
		Quantity   int       `json:"quantity"`
		ReceivedQty int      `json:"received_qty"`
		UnitCost   float64   `json:"unit_cost"`
		TotalCost  float64   `json:"total_cost"`
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
	Quantity int       `json:"quantity"`
	UnitCost float64   `json:"unit_cost"`
}

type createPOInput struct {
	SupplierID   uuid.UUID          `json:"supplier_id"`
	WarehouseID  uuid.UUID          `json:"warehouse_id"`
	ExpectedDate *time.Time         `json:"expected_date"`
	Notes        string             `json:"notes"`
	LineItems    []createPOLineInput `json:"line_items"`
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

	poNumber := "PO-" + strings.ToUpper(tenantID.String()[:8]) + "-" + time.Now().Format("20060102150405")
	poCreate := tx.PurchaseOrder.Create().
		SetTenantID(tenantID).
		SetSupplierID(req.SupplierID).
		SetWarehouseID(req.WarehouseID).
		SetPoNumber(poNumber).
		SetTotalAmount(total).
		SetNotes(req.Notes)
	if req.ExpectedDate != nil {
		poCreate = poCreate.SetExpectedDate(*req.ExpectedDate)
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

// SendPurchaseOrder handles PUT /inventory/purchase-orders/{poID}/send.
// Transitions a draft PO to sent status.
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

// ─── Reorder Config ───────────────────────────────────────────────────────────

type reorderConfigInput struct {
	WarehouseID        uuid.UUID `json:"warehouse_id"`
	ReorderLevel       int       `json:"reorder_level"`
	ReorderQuantity    int       `json:"reorder_quantity"`
	PreferredSupplierID *uuid.UUID `json:"preferred_supplier_id"`
	AutoReorderEnabled bool      `json:"auto_reorder_enabled"`
}

// UpdateReorderConfig handles PUT /inventory/stock/{sku}/reorder-config.
func (h *InventoryExtrasHandler) UpdateReorderConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	sku := chi.URLParam(r, "sku")

	var req reorderConfigInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	// Find item by SKU
	item, err := h.orm.Item.Query().
		Where(entitem.Sku(sku), entitem.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
		return
	}

	// Find or create balance record for this item+warehouse
	q := h.orm.InventoryBalance.Query().
		Where(entinventorybalance.TenantID(tenantID), entinventorybalance.ItemID(item.ID))
	if req.WarehouseID != uuid.Nil {
		q = q.Where(entinventorybalance.WarehouseID(req.WarehouseID))
	}

	bal, balErr := q.First(r.Context())
	if balErr != nil {
		if !ent.IsNotFound(balErr) {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to query balance")
			return
		}
		if req.WarehouseID == uuid.Nil {
			writeError(w, http.StatusBadRequest, "MISSING_WAREHOUSE", "warehouse_id required when creating reorder config")
			return
		}
		createQ := h.orm.InventoryBalance.Create().
			SetTenantID(tenantID).
			SetItemID(item.ID).
			SetWarehouseID(req.WarehouseID).
			SetReorderLevel(req.ReorderLevel).
			SetReorderQuantity(req.ReorderQuantity).
			SetAutoReorderEnabled(req.AutoReorderEnabled)
		if req.PreferredSupplierID != nil {
			createQ = createQ.SetPreferredSupplierID(*req.PreferredSupplierID)
		}
		_, err = createQ.Save(r.Context())
	} else {
		updateQ := h.orm.InventoryBalance.UpdateOneID(bal.ID).
			SetReorderLevel(req.ReorderLevel).
			SetReorderQuantity(req.ReorderQuantity).
			SetAutoReorderEnabled(req.AutoReorderEnabled)
		if req.PreferredSupplierID != nil {
			updateQ = updateQ.SetPreferredSupplierID(*req.PreferredSupplierID)
		}
		_, err = updateQ.Save(r.Context())
	}
	if err != nil {
		h.log.Error("update reorder config failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update reorder config")
		return
	}

	// Unused import guard
	_ = entwarehouse.TenantID
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
