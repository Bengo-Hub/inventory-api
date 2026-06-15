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
	entinventorylot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
)

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
	Quantity         float64    `json:"quantity"`
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
	Quantity         float64    `json:"quantity"`
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

	if req.ManufacturedDate != nil {
		create = create.SetManufacturedDate(*req.ManufacturedDate)
	}
	if req.ExpiryDate != nil {
		create = create.SetExpiryDate(*req.ExpiryDate)
	} else if it, e := h.orm.Item.Get(r.Context(), req.ItemID); e == nil && it.ShelfLifeDays != nil && *it.ShelfLifeDays > 0 {
		// No explicit expiry — derive it from the item's default shelf life, counted from the
		// manufacture date when supplied, otherwise from receipt (now).
		base := time.Now()
		if req.ManufacturedDate != nil {
			base = *req.ManufacturedDate
		}
		create = create.SetExpiryDate(base.AddDate(0, 0, *it.ShelfLifeDays))
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
	ExpiryDate       *time.Time `json:"expiry_date"`
	ManufacturedDate *time.Time `json:"manufacture_date"`
	Quantity         float64    `json:"quantity"`
	CostPrice        *float64   `json:"cost_per_unit"`
	Status           string     `json:"status"`
	SupplierRef      string     `json:"supplier_reference"`
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
	if req.ManufacturedDate != nil {
		update = update.SetManufacturedDate(*req.ManufacturedDate)
	}
	if req.CostPrice != nil {
		update = update.SetCostPrice(*req.CostPrice)
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
