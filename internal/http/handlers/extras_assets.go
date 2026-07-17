package handlers

import (
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"context"
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
	featGate := authclient.RequireFeatureCode("fixed_assets")
	// Categories
	r.Get("/inventory/asset-categories", h.ListAssetCategories)
	r.With(featGate, perm(add)).Post("/inventory/asset-categories", h.CreateAssetCategory)
	r.With(featGate, perm(change)).Put("/inventory/asset-categories/{catID}", h.UpdateAssetCategory)
	r.With(featGate, perm(del)).Delete("/inventory/asset-categories/{catID}", h.DeleteAssetCategory)
	// Assets
	r.Get("/inventory/asset-dashboard", h.AssetDashboard)
	r.Get("/inventory/assets", h.ListAssets)
	r.Get("/inventory/assets/{assetID}", h.GetAsset)
	r.With(featGate, perm(add)).Post("/inventory/assets", h.CreateAsset)
	r.With(featGate, perm(change)).Put("/inventory/assets/{assetID}", h.UpdateAsset)
	r.With(featGate, perm(del)).Delete("/inventory/assets/{assetID}", h.DeleteAsset)
	r.With(featGate, perm(change)).Post("/inventory/assets/{assetID}/depreciation-run", h.RunAssetDepreciation)
	// Asset operations (maintenance/transfer/disposal/insurance/audit/reservation)
	h.registerAssetOpsRoutes(r, perm, add, change)
}

// AssetDashboard handles GET /inventory/asset-dashboard.
//
//	@Summary      Fixed-asset KPI dashboard
//	@Tags         Assets
//	@Produce      json
//	@Success      200  {object}  map[string]interface{}
//	@Failure      400  {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-dashboard [get]
func (h *InventoryExtrasHandler) AssetDashboard(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	ctx := r.Context()
	base := func() *ent.AssetQuery { return h.orm.Asset.Query().Where(entasset.TenantID(tenantID)) }
	statusCount := func(s entasset.Status) int {
		n, _ := base().Where(entasset.StatusEQ(s)).Count(ctx)
		return n
	}
	sumF := func(field string) float64 {
		var agg []struct {
			Sum float64 `json:"sum"`
		}
		_ = base().Aggregate(ent.Sum(field)).Scan(ctx, &agg)
		if len(agg) > 0 {
			return agg[0].Sum
		}
		return 0
	}
	total, _ := base().Count(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"total_assets":                   total,
		"total_purchase_cost":            sumF(entasset.FieldPurchaseCost),
		"total_current_value":            sumF(entasset.FieldCurrentValue),
		"total_accumulated_depreciation": sumF(entasset.FieldAccumulatedDepreciation),
		"assets_by_status": map[string]int{
			"active":      statusCount(entasset.StatusActive),
			"inactive":    statusCount(entasset.StatusInactive),
			"maintenance": statusCount(entasset.StatusMaintenance),
			"disposed":    statusCount(entasset.StatusDisposed),
			"lost":        statusCount(entasset.StatusLost),
			"damaged":     statusCount(entasset.StatusDamaged),
			"retired":     statusCount(entasset.StatusRetired),
		},
	})
}

// --- Asset categories ---

type assetCategoryPayload struct {
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	ParentID         *uuid.UUID `json:"parent_id"`
	DepreciationRate float64    `json:"depreciation_rate"`
	UsefulLifeYears  int        `json:"useful_life_years"`
}

// ListAssetCategories handles GET /inventory/asset-categories.
//
//	@Summary      List asset categories
//	@Tags         Assets
//	@Produce      json
//	@Success      200  {array}   map[string]interface{}  "Asset categories"
//	@Failure      400  {object}  map[string]string
//	@Failure      500  {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-categories [get]
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

// CreateAssetCategory handles POST /inventory/asset-categories.
//
//	@Summary      Create an asset category
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        body  body      assetCategoryPayload  true  "Asset category payload"
//	@Success      201   {object}  map[string]interface{}
//	@Failure      400   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-categories [post]
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

// UpdateAssetCategory handles PUT /inventory/asset-categories/{catID}.
//
//	@Summary      Update an asset category
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        catID  path      string                true  "Asset category ID"
//	@Param        body   body      assetCategoryPayload  true  "Asset category payload"
//	@Success      200    {object}  map[string]interface{}
//	@Failure      400    {object}  map[string]string
//	@Failure      404    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-categories/{catID} [put]
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

// DeleteAssetCategory handles DELETE /inventory/asset-categories/{catID}.
//
//	@Summary      Delete (deactivate) an asset category
//	@Tags         Assets
//	@Produce      json
//	@Param        catID  path      string  true  "Asset category ID"
//	@Success      200    {object}  map[string]string
//	@Failure      400    {object}  map[string]string
//	@Failure      500    {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/asset-categories/{catID} [delete]
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
	KRACAClass         string     `json:"kra_ca_class"` // KRA capital-allowance class → carried to treasury
	Location           string     `json:"location"`
	OutletID           *uuid.UUID `json:"outlet_id"`
	CustodianID        *uuid.UUID `json:"custodian_id"`
	ItemID             *uuid.UUID `json:"item_id"`
	Condition          string     `json:"condition"`
	Notes              string     `json:"notes"`
	CreatedBy          *uuid.UUID `json:"created_by"`
}

// ListAssets handles GET /inventory/assets.
//
//	@Summary      List fixed assets
//	@Tags         Assets
//	@Produce      json
//	@Param        status       query     string  false  "Filter by status"
//	@Param        category_id  query     string  false  "Filter by category ID"
//	@Param        search       query     string  false  "Search by name, tag or serial number"
//	@Success      200          {object}  map[string]interface{}  "Paginated list of assets"
//	@Failure      400          {object}  map[string]string
//	@Failure      500          {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets [get]
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

// GetAsset handles GET /inventory/assets/{assetID}.
//
//	@Summary      Get a fixed asset
//	@Tags         Assets
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Success      200      {object}  map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID} [get]
func (h *InventoryExtrasHandler) GetAsset(w http.ResponseWriter, r *http.Request) {
	_, a, ok := h.loadAsset(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// CreateAsset handles POST /inventory/assets.
//
//	@Summary      Create a fixed asset
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        body  body      assetPayload  true  "Asset payload"
//	@Success      201   {object}  map[string]interface{}
//	@Failure      400   {object}  map[string]string
//	@Failure      500   {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets [post]
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
	if req.KRACAClass != "" {
		c = c.SetKraCaClass(req.KRACAClass)
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
	if req.ItemID != nil {
		c = c.SetItemID(*req.ItemID)
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
		// Extra fields let treasury auto-register the capital-allowance asset richly. When the
		// KRA class is set here (inventory owns the asset + its class), treasury classifies the
		// synced CA asset immediately instead of parking it UNCLASSIFIED with zero allowance.
		"purchase_date":       a.PurchaseDate,
		"depreciation_method": a.DepreciationMethod,
		"ca_class_code":       a.KraCaClass,
	})
	writeJSON(w, http.StatusCreated, a)
}

// UpdateAsset handles PUT /inventory/assets/{assetID}.
//
//	@Summary      Update a fixed asset
//	@Tags         Assets
//	@Accept       json
//	@Produce      json
//	@Param        assetID  path      string        true  "Asset ID"
//	@Param        body     body      assetPayload  true  "Asset payload"
//	@Success      200      {object}  map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID} [put]
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
	if req.ItemID != nil {
		upd = upd.SetItemID(*req.ItemID)
	}
	if req.DepreciationMethod != "" {
		upd = upd.SetDepreciationMethod(entasset.DepreciationMethod(req.DepreciationMethod))
	}
	if req.KRACAClass != "" {
		upd = upd.SetKraCaClass(req.KRACAClass)
	}
	_ = tenantID
	updated, err := upd.Save(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update asset")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DeleteAsset handles DELETE /inventory/assets/{assetID}.
//
//	@Summary      Delete (retire) a fixed asset
//	@Tags         Assets
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Success      200      {object}  map[string]string
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID} [delete]
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
//
//	@Summary      Trigger a depreciation run for an asset
//	@Tags         Assets
//	@Produce      json
//	@Param        assetID  path      string  true  "Asset ID"
//	@Success      202      {object}  map[string]interface{}
//	@Failure      400      {object}  map[string]string
//	@Failure      404      {object}  map[string]string
//	@Security     bearerAuth
//	@Router       /{tenant}/inventory/assets/{assetID}/depreciation-run [post]
func (h *InventoryExtrasHandler) RunAssetDepreciation(w http.ResponseWriter, r *http.Request) {
	tenantID, a, ok := h.loadAsset(w, r)
	if !ok {
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		period = time.Now().UTC().Format("2006-01")
	}
	amount, applied, err := h.applyAssetDepreciation(r.Context(), tenantID, a, period)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to apply depreciation")
		return
	}
	if !applied {
		writeJSON(w, http.StatusOK, map[string]any{"status": "already_depreciated_this_period", "asset_id": a.ID, "period": period})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "depreciation_applied", "asset_id": a.ID, "period": period, "amount": amount})
}

// applyAssetDepreciation computes one period's depreciation using the SAME formula
// as the treasury GL consumer (rate is a fraction; the amount is the full period),
// updates the inventory register (accumulated_depreciation/book_value/current_value
// + last_depreciation_period for idempotency), and emits inventory.asset.depreciation_due
// with the OPENING balances so treasury computes the identical amount for the GL
// journal — keeping the register and the ledger in lockstep without a cross-service
// round-trip. Idempotent per period. Returns (amount, applied).
func (h *InventoryExtrasHandler) applyAssetDepreciation(ctx context.Context, tenantID uuid.UUID, a *ent.Asset, period string) (float64, bool, error) {
	if a.LastDepreciationPeriod == period {
		return 0, false, nil
	}
	openingAccum := a.AccumulatedDepreciation
	openingBook := a.BookValue
	if openingBook == 0 {
		openingBook = a.PurchaseCost
	}
	var amount float64
	if a.DepreciationMethod == entasset.DepreciationMethodDecliningBalance {
		amount = openingBook * a.DepreciationRate
	} else {
		base := a.PurchaseCost - a.SalvageValue
		if base < 0 {
			base = 0
		}
		amount = base * a.DepreciationRate
	}
	if amount < 0 {
		amount = 0
	}
	if remaining := openingBook - a.SalvageValue; remaining <= 0 {
		amount = 0
	} else if amount > remaining {
		amount = remaining
	}

	upd := h.orm.Asset.UpdateOneID(a.ID).SetLastDepreciationPeriod(period)
	if amount > 0 {
		closingAccum := openingAccum + amount
		closingBook := a.PurchaseCost - closingAccum
		if closingBook < 0 {
			closingBook = 0
		}
		upd = upd.SetAccumulatedDepreciation(closingAccum).SetBookValue(closingBook).SetCurrentValue(closingBook)
	}
	if _, err := upd.Save(ctx); err != nil {
		return 0, false, err
	}

	h.publishOutbox(ctx, tenantID, "asset", a.ID, "inventory.asset.depreciation_due", map[string]any{
		"id": a.ID, "asset_id": a.ID, "asset_tag": a.AssetTag, "purchase_cost": a.PurchaseCost, "salvage_value": a.SalvageValue,
		"depreciation_rate": a.DepreciationRate, "depreciation_method": a.DepreciationMethod,
		"accumulated_depreciation": openingAccum, "book_value": openingBook,
		"period": period, "currency": "KES",
	})
	return amount, true, nil
}

// StartDepreciationScheduler applies the current calendar month's depreciation to
// every active depreciable asset (idempotent per month via last_depreciation_period),
// then repeats daily — so each asset is depreciated at most once per month. It is
// OPT-IN (wired only when ASSET_DEPRECIATION_SCHEDULER=true) because the period rate
// is applied in full per run: depreciation_rate must be a PER-MONTH fraction here, not
// an annual one. Blocks until ctx is done. Most tenants run depreciation manually at
// month-end (the "Run Depreciation" action) instead.
func (h *InventoryExtrasHandler) StartDepreciationScheduler(ctx context.Context) {
	run := func() {
		period := time.Now().UTC().Format("2006-01")
		assets, err := h.orm.Asset.Query().
			Where(entasset.IsActive(true), entasset.StatusEQ(entasset.StatusActive), entasset.DepreciationRateGT(0)).
			All(ctx)
		if err != nil {
			h.log.Warn("depreciation scheduler: query assets failed", zap.Error(err))
			return
		}
		applied := 0
		for _, a := range assets {
			if a.LastDepreciationPeriod == period {
				continue
			}
			if _, ok, e := h.applyAssetDepreciation(ctx, a.TenantID, a, period); e != nil {
				h.log.Warn("depreciation scheduler: apply failed", zap.String("asset", a.ID.String()), zap.Error(e))
			} else if ok {
				applied++
			}
		}
		if applied > 0 {
			h.log.Info("depreciation scheduler run complete", zap.Int("applied", applied), zap.String("period", period))
		}
	}
	startup := time.NewTimer(2 * time.Minute)
	defer startup.Stop()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-startup.C:
			run()
		case <-ticker.C:
			run()
		}
	}
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
