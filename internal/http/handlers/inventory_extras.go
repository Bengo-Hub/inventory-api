package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entinventorybalance "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entinventorylot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
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

// RegisterRoutes wires all extra inventory routes under /inventory/... on the given tenant router.
func (h *InventoryExtrasHandler) RegisterRoutes(r chi.Router) {
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	r.Route("/inventory", func(inv chi.Router) {
		inv.Get("/stock", h.ListStock)
		inv.Get("/lots", h.ListLots)
		inv.Get("/suppliers", h.ListSuppliers)
		inv.With(perm(rbac.PermItemsAdd)).Post("/suppliers", h.CreateSupplier)
		inv.With(perm(rbac.PermItemsChange)).Put("/suppliers/{supplierID}", h.UpdateSupplier)
		inv.Get("/purchase-orders", h.ListPurchaseOrders)
		inv.Get("/activity", h.ListActivity)
	})
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
	writeJSON(w, http.StatusCreated, supplierDTO{
		ID:          s.ID,
		Name:        s.Name,
		ContactName: s.ContactName,
		Email:       s.ContactEmail,
		Phone:       s.ContactPhone,
		Status:      "active",
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
