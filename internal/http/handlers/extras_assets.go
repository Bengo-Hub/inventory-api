package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	"github.com/bengobox/inventory-service/internal/ent"
	entasset "github.com/bengobox/inventory-service/internal/ent/asset"
	entassetcat "github.com/bengobox/inventory-service/internal/ent/assetcategory"
)

// ─── Fixed Assets register (migrated from ERP assets/*) ─────────────────────
// inventory-api owns the register; depreciation schedules + GL posting are owned
// by treasury-api (this service emits inventory.asset.depreciation_due events).

func (h *InventoryExtrasHandler) registerAssetRoutes(r chi.Router, perm func(string) func(http.Handler) http.Handler, add, change, del string) {
	// Categories
	r.Get("/inventory/asset-categories", h.ListAssetCategories)
	r.With(perm(add)).Post("/inventory/asset-categories", h.CreateAssetCategory)
	r.With(perm(change)).Put("/inventory/asset-categories/{catID}", h.UpdateAssetCategory)
	r.With(perm(del)).Delete("/inventory/asset-categories/{catID}", h.DeleteAssetCategory)
	// Assets
	r.Get("/inventory/assets", h.ListAssets)
	r.Get("/inventory/assets/{assetID}", h.GetAsset)
	r.With(perm(add)).Post("/inventory/assets", h.CreateAsset)
	r.With(perm(change)).Put("/inventory/assets/{assetID}", h.UpdateAsset)
	r.With(perm(del)).Delete("/inventory/assets/{assetID}", h.DeleteAsset)
	r.With(perm(change)).Post("/inventory/assets/{assetID}/depreciation-run", h.RunAssetDepreciation)
	// Asset operations (maintenance/transfer/disposal/insurance/audit/reservation)
	h.registerAssetOpsRoutes(r, perm, add, change)
}

// --- Asset categories ---

type assetCategoryPayload struct {
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	ParentID         *uuid.UUID `json:"parent_id"`
	DepreciationRate float64    `json:"depreciation_rate"`
	UsefulLifeYears  int        `json:"useful_life_years"`
}

func (h *InventoryExtrasHandler) ListAssetCategories(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	rows, err := h.orm.AssetCategory.Query().
		Where(entassetcat.TenantID(tenantID), entassetcat.IsActive(true)).
		Order(ent.Asc(entassetcat.FieldName)).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list asset categories")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *InventoryExtrasHandler) CreateAssetCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req assetCategoryPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "name is required")
		return
	}
	c := h.orm.AssetCategory.Create().
		SetTenantID(tenantID).SetName(req.Name).SetDescription(req.Description).
		SetDepreciationRate(req.DepreciationRate)
	if req.UsefulLifeYears > 0 {
		c = c.SetUsefulLifeYears(req.UsefulLifeYears)
	}
	if req.ParentID != nil {
		c = c.SetParentID(*req.ParentID)
	}
	row, err := c.Save(r.Context())
	if err != nil {
		h.log.Error("create asset category failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create asset category")
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *InventoryExtrasHandler) UpdateAssetCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "catID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid category ID")
		return
	}
	existing, err := h.orm.AssetCategory.Query().Where(entassetcat.ID(id), entassetcat.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Asset category not found")
		return
	}
	var req assetCategoryPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	upd := h.orm.AssetCategory.UpdateOneID(existing.ID).SetDescription(req.Description).SetDepreciationRate(req.DepreciationRate)
	if req.Name != "" {
		upd = upd.SetName(req.Name)
	}
	if req.UsefulLifeYears > 0 {
		upd = upd.SetUsefulLifeYears(req.UsefulLifeYears)
	}
	row, err := upd.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update asset category")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *InventoryExtrasHandler) DeleteAssetCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "catID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid category ID")
		return
	}
	if _, err := h.orm.AssetCategory.Update().Where(entassetcat.ID(id), entassetcat.TenantID(tenantID)).SetIsActive(false).Save(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete asset category")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Assets ---

type assetPayload struct {
	AssetTag           string     `json:"asset_tag"`
	Name               string     `json:"name"`
	Description        string     `json:"description"`
	CategoryID         *uuid.UUID `json:"category_id"`
	SerialNumber       string     `json:"serial_number"`
	Model              string     `json:"model"`
	Manufacturer       string     `json:"manufacturer"`
	Barcode            string     `json:"barcode"`
	PurchaseDate       *time.Time `json:"purchase_date"`
	PurchaseCost       float64    `json:"purchase_cost"`
	SalvageValue       float64    `json:"salvage_value"`
	DepreciationRate   float64    `json:"depreciation_rate"`
	DepreciationMethod string     `json:"depreciation_method"`
	Location           string     `json:"location"`
	OutletID           *uuid.UUID `json:"outlet_id"`
	CustodianID        *uuid.UUID `json:"custodian_id"`
	Condition          string     `json:"condition"`
	Notes              string     `json:"notes"`
	CreatedBy          *uuid.UUID `json:"created_by"`
}

func (h *InventoryExtrasHandler) ListAssets(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	q := h.orm.Asset.Query().Where(entasset.TenantID(tenantID))
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(entasset.StatusEQ(entasset.Status(s)))
	}
	if c := r.URL.Query().Get("category_id"); c != "" {
		if id, e := uuid.Parse(c); e == nil {
			q = q.Where(entasset.CategoryID(id))
		}
	}
	if s := r.URL.Query().Get("search"); s != "" {
		q = q.Where(entasset.Or(entasset.NameContainsFold(s), entasset.AssetTagContainsFold(s), entasset.SerialNumberContainsFold(s)))
	}
	total, _ := q.Clone().Count(r.Context())
	rows, err := q.Order(ent.Desc(entasset.FieldCreatedAt)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list assets")
		return
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(rows, total, p))
}

func (h *InventoryExtrasHandler) GetAsset(w http.ResponseWriter, r *http.Request) {
	_, a, ok := h.loadAsset(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *InventoryExtrasHandler) CreateAsset(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req assetPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "name is required")
		return
	}
	tag := req.AssetTag
	if tag == "" {
		tag = "AST-" + strings.ToUpper(uuid.New().String()[:8])
	}
	c := h.orm.Asset.Create().
		SetTenantID(tenantID).SetAssetTag(tag).SetName(req.Name).SetDescription(req.Description).
		SetSerialNumber(req.SerialNumber).SetModel(req.Model).SetManufacturer(req.Manufacturer).SetBarcode(req.Barcode).
		SetPurchaseCost(req.PurchaseCost).SetCurrentValue(req.PurchaseCost).SetBookValue(req.PurchaseCost).
		SetSalvageValue(req.SalvageValue).SetDepreciationRate(req.DepreciationRate).
		SetLocation(req.Location).SetCondition(req.Condition).SetNotes(req.Notes)
	if req.DepreciationMethod != "" {
		c = c.SetDepreciationMethod(entasset.DepreciationMethod(req.DepreciationMethod))
	}
	if req.CategoryID != nil {
		c = c.SetCategoryID(*req.CategoryID)
	}
	if req.PurchaseDate != nil {
		c = c.SetPurchaseDate(*req.PurchaseDate)
	}
	if req.OutletID != nil {
		c = c.SetOutletID(*req.OutletID)
	}
	if req.CustodianID != nil {
		c = c.SetCustodianID(*req.CustodianID)
	}
	if req.CreatedBy != nil {
		c = c.SetCreatedBy(*req.CreatedBy)
	}
	a, err := c.Save(r.Context())
	if err != nil {
		h.log.Error("create asset failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create asset")
		return
	}
	h.publishOutbox(r.Context(), tenantID, "asset", a.ID, "inventory.asset.created", map[string]any{
		"id": a.ID, "asset_tag": a.AssetTag, "name": a.Name, "purchase_cost": a.PurchaseCost,
	})
	writeJSON(w, http.StatusCreated, a)
}

func (h *InventoryExtrasHandler) UpdateAsset(w http.ResponseWriter, r *http.Request) {
	tenantID, a, ok := h.loadAsset(w, r)
	if !ok {
		return
	}
	var req assetPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	upd := h.orm.Asset.UpdateOneID(a.ID).
		SetDescription(req.Description).SetSerialNumber(req.SerialNumber).SetModel(req.Model).
		SetManufacturer(req.Manufacturer).SetBarcode(req.Barcode).SetLocation(req.Location).
		SetCondition(req.Condition).SetNotes(req.Notes).SetDepreciationRate(req.DepreciationRate).SetSalvageValue(req.SalvageValue)
	if req.Name != "" {
		upd = upd.SetName(req.Name)
	}
	if req.CategoryID != nil {
		upd = upd.SetCategoryID(*req.CategoryID)
	}
	if req.CustodianID != nil {
		upd = upd.SetCustodianID(*req.CustodianID)
	}
	if req.DepreciationMethod != "" {
		upd = upd.SetDepreciationMethod(entasset.DepreciationMethod(req.DepreciationMethod))
	}
	_ = tenantID
	updated, err := upd.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update asset")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *InventoryExtrasHandler) DeleteAsset(w http.ResponseWriter, r *http.Request) {
	tenantID, a, ok := h.loadAsset(w, r)
	if !ok {
		return
	}
	if _, err := h.orm.Asset.UpdateOneID(a.ID).SetIsActive(false).SetStatus(entasset.StatusRetired).Save(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete asset")
		return
	}
	_ = tenantID
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// RunAssetDepreciation emits a depreciation-due event for treasury to compute +
// post the depreciation journal (treasury owns the financial side).
func (h *InventoryExtrasHandler) RunAssetDepreciation(w http.ResponseWriter, r *http.Request) {
	tenantID, a, ok := h.loadAsset(w, r)
	if !ok {
		return
	}
	h.publishOutbox(r.Context(), tenantID, "asset", a.ID, "inventory.asset.depreciation_due", map[string]any{
		"id": a.ID, "asset_tag": a.AssetTag, "purchase_cost": a.PurchaseCost, "salvage_value": a.SalvageValue,
		"depreciation_rate": a.DepreciationRate, "depreciation_method": a.DepreciationMethod,
		"accumulated_depreciation": a.AccumulatedDepreciation, "book_value": a.BookValue,
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "depreciation_event_emitted", "asset_id": a.ID})
}

func (h *InventoryExtrasHandler) loadAsset(w http.ResponseWriter, r *http.Request) (uuid.UUID, *ent.Asset, bool) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return uuid.Nil, nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "assetID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid asset ID")
		return uuid.Nil, nil, false
	}
	a, err := h.orm.Asset.Query().Where(entasset.ID(id), entasset.TenantID(tenantID)).Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Asset not found")
		return uuid.Nil, nil, false
	}
	return tenantID, a, true
}
