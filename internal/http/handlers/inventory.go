package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Bengo-Hub/pagination"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/modifiers"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/modules/tickets"
	"github.com/bengobox/inventory-service/internal/modules/units"
	"github.com/bengobox/inventory-service/internal/platform/subscriptions"
)

// ItemsServicer defines the contract for item availability and CRUD operations.
type ItemsServicer interface {
	GetStockAvailability(ctx context.Context, tenantID uuid.UUID, sku string) (*items.StockAvailability, error)
	BulkAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]items.StockAvailability, error)
	GetBOMAvailability(ctx context.Context, tenantID uuid.UUID, skus []string) ([]items.BOMAvailabilityResult, error)
	GetInventorySummary(ctx context.Context, tenantID uuid.UUID) (*items.InventorySummary, error)
	StockValuation(ctx context.Context, tenantID uuid.UUID) (*items.StockValuation, error)
	StockDeadstock(ctx context.Context, tenantID uuid.UUID, days int) (*items.DeadstockReport, error)
	CreateItem(ctx context.Context, tenantID uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error)
	UpdateItem(ctx context.Context, tenantID uuid.UUID, id uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error)
	DeactivateItemBySKU(ctx context.Context, tenantID uuid.UUID, sku string) error
	EnsureDefaultPrice(ctx context.Context, tenantID, itemID uuid.UUID, price float64) error
	SetSellingPriceBySKU(ctx context.Context, tenantID uuid.UUID, sku string, price float64) (*items.ItemDTO, error)
	ListItems(ctx context.Context, tenantID uuid.UUID, typeFilter, statusFilter string, limit, offset int, categoryID *uuid.UUID, unitID *uuid.UUID, search string, outletID *uuid.UUID, useCase string, tagsFilter ...string) ([]items.ItemDTO, int, error)
	ListItemVariants(ctx context.Context, tenantID, itemID uuid.UUID) ([]items.VariantDTO, error)
	ListEventItems(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]items.ItemDTO, int, error)
	ListCategories(ctx context.Context, tenantID uuid.UUID) ([]items.CategoryDTO, error)
	ListCategoriesFiltered(ctx context.Context, tenantID uuid.UUID, hasItems bool) ([]items.CategoryDTO, error)
	CreateCategory(ctx context.Context, tenantID uuid.UUID, dto items.CategoryDTO) (*items.CategoryDTO, error)
	UpdateCategory(ctx context.Context, tenantID, id uuid.UUID, dto items.CategoryDTO) (*items.CategoryDTO, error)
	DeleteCategory(ctx context.Context, tenantID, id uuid.UUID) error
	ListBrands(ctx context.Context, tenantID uuid.UUID) ([]items.BrandDTO, error)
	CreateBrand(ctx context.Context, tenantID uuid.UUID, dto items.BrandDTO) (*items.BrandDTO, error)
	// Multi-image (ItemAsset) management.
	CountItemImages(ctx context.Context, tenantID, itemID uuid.UUID) (int, error)
	ListItemImages(ctx context.Context, tenantID, itemID uuid.UUID) ([]items.ItemImageDTO, error)
	AddItemImage(ctx context.Context, tenantID, itemID uuid.UUID, file multipart.File, header *multipart.FileHeader, setPrimary bool) (*items.ItemImageDTO, error)
	UpdateItemImage(ctx context.Context, tenantID, itemID, imageID uuid.UUID, in items.UpdateItemImageInput) (*items.ItemImageDTO, error)
	DeleteItemImage(ctx context.Context, tenantID, itemID, imageID uuid.UUID) error
}

// StockServicer defines the contract for stock reservation and consumption operations.
type StockServicer interface {
	CreateReservation(ctx context.Context, tenantID uuid.UUID, req stock.ReservationRequest) (*stock.ReservationResponse, error)
	GetReservation(ctx context.Context, tenantID, reservationID uuid.UUID) (*stock.ReservationResponse, error)
	GetReservationsByOrderID(ctx context.Context, tenantID, orderID uuid.UUID) ([]stock.ReservationResponse, error)
	ReleaseReservation(ctx context.Context, tenantID, reservationID uuid.UUID, reason string) error
	ConsumeReservation(ctx context.Context, tenantID, reservationID uuid.UUID) error
	RecordConsumption(ctx context.Context, tenantID uuid.UUID, req stock.ConsumptionRequest) (*stock.ConsumptionResponse, error)
	ReverseConsumption(ctx context.Context, tenantID uuid.UUID, req stock.ReverseConsumptionRequest) (*stock.ReverseConsumptionResponse, error)
	AdjustStock(ctx context.Context, tenantID uuid.UUID, req stock.AdjustStockRequest) (*stock.AdjustStockResponse, error)
	Breakdown(ctx context.Context, tenantID uuid.UUID, req stock.BreakdownRequest) (*stock.BreakdownResponse, error)
	ListAdjustments(ctx context.Context, tenantID uuid.UUID, req stock.ListAdjustmentsRequest) ([]stock.StockAdjustmentDTO, error)
}

// RecipesServicer defines the contract for recipe management.
type RecipesServicer interface {
	ListRecipes(ctx context.Context, tenantID uuid.UUID, limit, offset int, outletID *uuid.UUID) ([]recipes.RecipeDTO, int, error)
	GetRecipe(ctx context.Context, tenantID, id uuid.UUID) (*recipes.RecipeDTO, error)
	CreateRecipe(ctx context.Context, tenantID uuid.UUID, dto recipes.RecipeDTO) (*recipes.RecipeDTO, error)
	UpdateRecipe(ctx context.Context, tenantID uuid.UUID, recipeID uuid.UUID, dto recipes.RecipeDTO) (*recipes.RecipeDTO, error)
	DeleteRecipe(ctx context.Context, tenantID uuid.UUID, recipeID uuid.UUID) error
	GetRecipeBySKU(ctx context.Context, tenantID uuid.UUID, sku string) (*recipes.RecipeDTO, error)
	// RecalculateCostsForIngredient cascades cost recalculation to all recipes using the given ingredient.
	RecalculateCostsForIngredient(ctx context.Context, tenantID, ingredientItemID uuid.UUID) error
	// RecalculateRecipeCosts recomputes total/unit cost for a single recipe from current ingredient prices.
	RecalculateRecipeCosts(ctx context.Context, tenantID, recipeID uuid.UUID) error
	// AuditRecipeUnits lists existing recipe lines whose units cannot deduct stock.
	AuditRecipeUnits(ctx context.Context, tenantID uuid.UUID) ([]recipes.UnitIssue, error)
	// SetSellingPriceByItem updates the linked recipe's selling price (RECIPE items are
	// priced by their recipe at the POS). Returns false when the item has no active recipe.
	SetSellingPriceByItem(ctx context.Context, tenantID, itemID uuid.UUID, price float64) (bool, error)
}

// UnitsServicer defines the contract for unit management.
type UnitsServicer interface {
	ListUnits(ctx context.Context, tenantID uuid.UUID) ([]units.UnitDTO, error)
	CreateUnit(ctx context.Context, tenantID uuid.UUID, dto units.UnitDTO) (*units.UnitDTO, error)
	UpdateUnit(ctx context.Context, tenantID, id uuid.UUID, dto units.UnitDTO) (*units.UnitDTO, error)
	DeleteUnit(ctx context.Context, tenantID, id uuid.UUID) error
}

// ModifiersServicer defines the contract for modifier group/option management.
type ModifiersServicer interface {
	ListAllModifierGroups(ctx context.Context, tenantID uuid.UUID, limit, offset int, search string) ([]modifiers.ModifierGroupDTO, int, error)
	GetModifierGroup(ctx context.Context, tenantID, groupID uuid.UUID) (*modifiers.ModifierGroupDTO, error)
	ListModifierGroups(ctx context.Context, tenantID, itemID uuid.UUID) ([]modifiers.ModifierGroupDTO, error)
	CreateModifierGroup(ctx context.Context, tenantID uuid.UUID, req modifiers.CreateModifierGroupRequest) (*modifiers.ModifierGroupDTO, error)
	UpdateModifierGroup(ctx context.Context, tenantID, groupID uuid.UUID, req modifiers.UpdateModifierGroupRequest) (*modifiers.ModifierGroupDTO, error)
	DeleteModifierGroup(ctx context.Context, tenantID, groupID uuid.UUID) error
	CreateModifierOption(ctx context.Context, tenantID, groupID uuid.UUID, req modifiers.CreateModifierOptionRequest) (*modifiers.ModifierOptionDTO, error)
	UpdateModifierOption(ctx context.Context, tenantID, optionID uuid.UUID, req modifiers.UpdateModifierOptionRequest) (*modifiers.ModifierOptionDTO, error)
	DeleteModifierOption(ctx context.Context, tenantID, optionID uuid.UUID) error
}

// InventoryHandler handles all inventory-related HTTP endpoints.
type InventoryHandler struct {
	log          *zap.Logger
	itemsSvc     ItemsServicer
	stockSvc     StockServicer
	recipeSvc    RecipesServicer
	unitSvc      UnitsServicer
	modifiersSvc ModifiersServicer
	ticketsSvc   *tickets.Service
	docSvc       *documents.Service
	rbacSvc      *rbac.Service
	authMW       *authclient.AuthMiddleware
	auditSvc     *audit.Service
	approvalSvc  *approvals.Service
	orm          *ent.Client
	pinSecret    []byte // terminal/PIN JWT secret; feature-gated GETs accept PIN sessions too
}

// SetEntClient wires the Ent client used by route-level middleware (e.g. resolving an
// outlet's use_case from its warehouse mirror for RequireOutletUseCase gating).
func (h *InventoryHandler) SetEntClient(c *ent.Client) { h.orm = c }

// SetAuditService wires the centralized audit trail for stock adjustments / write-offs.
func (h *InventoryHandler) SetAuditService(a *audit.Service) { h.auditSvc = a }

// SetApprovalService wires the amount-tiered approval workflow for large adjustments.
func (h *InventoryHandler) SetApprovalService(a *approvals.Service) { h.approvalSvc = a }

// NewInventoryHandler creates a new inventory handler.
func NewInventoryHandler(log *zap.Logger, itemsSvc ItemsServicer, stockSvc StockServicer, recipeSvc RecipesServicer, unitSvc UnitsServicer) *InventoryHandler {
	return &InventoryHandler{
		log:       log.Named("inventory.handler"),
		itemsSvc:  itemsSvc,
		stockSvc:  stockSvc,
		recipeSvc: recipeSvc,
		unitSvc:   unitSvc,
	}
}

// SetRBACService injects the RBAC service for per-route permission enforcement.
// When set, mutation routes require the corresponding inventory.*.{action} permission.
func (h *InventoryHandler) SetRBACService(svc *rbac.Service) {
	h.rbacSvc = svc
}

// SetAuthMiddleware injects the auth middleware so feature-gated GET routes can
// require authentication. The route group skips auth for GETs (to keep public/S2S
// reads open), which means claims are never extracted — so a feature-gated GET like
// /adjustments must opt back into auth explicitly, otherwise RequireFeature sees no
// claims and returns 401 even for logged-in users.
func (h *InventoryHandler) SetAuthMiddleware(mw *authclient.AuthMiddleware) {
	h.authMW = mw
}

// SetTerminalSecret wires the PIN/terminal JWT secret so feature-gated GET routes accept
// terminal (PIN) sessions in addition to SSO sessions.
func (h *InventoryHandler) SetTerminalSecret(secret []byte) { h.pinSecret = secret }

// requireAuthForFeatureGet returns RequireAuth when the auth middleware is wired,
// or a pass-through otherwise (preserving prior behavior in tests / setups without auth).
func (h *InventoryHandler) requireAuthForFeatureGet() func(http.Handler) http.Handler {
	if h.authMW == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	// Accept terminal (PIN) sessions as well as SSO on feature-gated GETs.
	return RequireAnyAuth(h.pinSecret, h.authMW)
}

// SetModifiersService injects the modifiers service (optional; modifier endpoints are skipped if nil).
func (h *InventoryHandler) SetModifiersService(svc ModifiersServicer) {
	h.modifiersSvc = svc
}

// SetTicketsService injects the tickets service (optional; ticket endpoints are skipped if nil).
func (h *InventoryHandler) SetTicketsService(svc *tickets.Service) {
	h.ticketsSvc = svc
}

// SetDocService injects the documents service (tenant branding + numbering) for ticket PDFs.
func (h *InventoryHandler) SetDocService(svc *documents.Service) {
	h.docSvc = svc
}

// parseTenantID is now defined in tenant.go with platform-owner override support.

// RegisterRoutes wires inventory routes onto the given chi.Router.
// When rbacSvc is set (via SetRBACService), mutation routes enforce per-action permissions.
func (h *InventoryHandler) RegisterRoutes(r chi.Router) {
	// perm returns a per-route permission middleware when rbacSvc is set, or a pass-through.
	perm := func(code string) func(http.Handler) http.Handler {
		if h.rbacSvc == nil {
			return func(next http.Handler) http.Handler { return next }
		}
		return invmiddleware.RequirePermission(h.rbacSvc, h.log, code)
	}

	r.Route("/inventory", func(inv chi.Router) {
		// Items
		inv.Get("/items", h.ListItems)
		inv.With(perm(rbac.PermItemsAdd)).Post("/items", h.CreateItem)
		inv.Get("/items/{sku}", h.GetStockAvailability)
		inv.With(perm(rbac.PermItemsChange)).Put("/items/{sku}", h.UpdateItem)
		// Targeted price correction (guardrails + tier rows + recipe selling price) —
		// called S2S by pos-api's "also update the catalog price" order-line edit.
		inv.With(perm(rbac.PermItemsChange)).Patch("/items/{sku}/price", h.SetItemPrice)
		inv.With(perm(rbac.PermItemsDelete)).Delete("/items/{sku}", h.DeleteItem)
		inv.Get("/items/{itemId}/variants", h.ListItemVariants)

		// Barcode + label printing (single-item PNG read + bulk label-print job).
		h.registerBarcodeRoutes(inv, perm)

		// Item images (multi-image gallery via ItemAsset). List is open (read); mutations
		// require items.change. Upload additionally enforces the multi-image feature + per-item
		// image cap inside the handler (returns 403/402 on lock/overage).
		inv.Get("/items/{itemID}/images", h.ListItemImages)
		inv.With(perm(rbac.PermItemsChange)).Post("/items/{itemID}/images", h.UploadItemImage)
		inv.With(perm(rbac.PermItemsChange)).Patch("/items/{itemID}/images/{imageID}", h.UpdateItemImage)
		inv.With(perm(rbac.PermItemsChange)).Delete("/items/{itemID}/images/{imageID}", h.DeleteItemImage)

		// Availability
		inv.Post("/availability", h.BulkAvailability)
		inv.Get("/availability/bom", h.GetBOMAvailability)

		// Stock adjustments — requires stock_tracking feature
		inv.With(authclient.RequireFeatureCode("stock_tracking"), perm(rbac.PermStockAdd)).Post("/adjust", h.AdjustStock)
		inv.With(authclient.RequireFeatureCode("stock_tracking"), perm(rbac.PermStockAdd)).Post("/adjustments", h.CreateAdjustment)
		inv.With(authclient.RequireFeatureCode("stock_tracking"), perm(rbac.PermStockChange)).Post("/breakdowns", h.CreateBreakdown)
		// GET is exempt from the group-level auth (public/S2S reads), so opt back into
		// auth here to populate claims before the feature check — otherwise logged-in
		// users hit a spurious 401 "missing claims".
		inv.With(h.requireAuthForFeatureGet(), authclient.RequireFeatureCode("stock_tracking")).Get("/adjustments", h.ListAdjustments)

		// Categories
		inv.Get("/categories", h.ListCategories)
		inv.With(perm(rbac.PermItemsAdd)).Post("/categories", h.CreateCategory)
		inv.With(perm(rbac.PermItemsChange)).Put("/categories/{categoryID}", h.UpdateCategory)
		inv.With(perm(rbac.PermItemsDelete)).Delete("/categories/{categoryID}", h.DeleteCategory)

		// Reservations
		inv.With(perm(rbac.PermReservationsAdd)).Post("/reservations", h.CreateReservation)
		inv.Get("/reservations", h.GetReservationsByOrder)
		inv.Get("/reservations/{reservationID}", h.GetReservation)
		inv.With(perm(rbac.PermReservationsChange)).Post("/reservations/{reservationID}/release", h.ReleaseReservation)
		inv.With(perm(rbac.PermReservationsChange)).Post("/reservations/{reservationID}/consume", h.ConsumeReservation)

		// Consumption
		inv.With(perm(rbac.PermConsumptionsAdd)).Post("/consumption", h.RecordConsumption)
		// Reversal (S2S from pos-api's txn-reversal tool; same auth semantics as /consumption).
		inv.With(perm(rbac.PermConsumptionsAdd)).Post("/consumption/reverse", h.ReverseConsumption)

		// Summary
		inv.Get("/summary", h.GetInventorySummary)
		inv.Get("/reports/stock-valuation", h.StockValuationReport)
		inv.Get("/reports/stock-valuation.pdf", h.StockValuationReportPDF)
		inv.Get("/reports/deadstock", h.StockDeadstockReport)
		inv.Get("/reports/deadstock.pdf", h.StockDeadstockReportPDF)

		// Recipes / BOM — hospitality & quick_service (menu recipes) plus warehouse
		// & manufacturing (bills of materials). HQ/platform users bypass gating.
		inv.Group(func(rec chi.Router) {
			// Populate claims first (GET routes skip the group-level auth), then gate by
			// the active outlet's use_case — so the read list is gated for non-HQ users too,
			// not just the mutations.
			rec.Use(h.requireAuthForFeatureGet())
			rec.Use(invmiddleware.RequireOutletUseCase(h.orm, h.log, "hospitality", "quick_service", "warehouse", "manufacturing"))
			rec.Get("/recipes", h.ListRecipes)
			rec.With(perm(rbac.PermRecipesAdd)).Post("/recipes", h.CreateRecipe)
			rec.Get("/recipes/unit-audit", h.AuditRecipeUnits)
			rec.Get("/recipes/{recipeID}", h.GetRecipe)
			rec.With(perm(rbac.PermRecipesChange)).Put("/recipes/{recipeID}", h.UpdateRecipe)
			rec.With(perm(rbac.PermRecipesChange)).Post("/recipes/{recipeID}/recompute-cost", h.RecomputeRecipeCost)
			rec.With(perm(rbac.PermRecipesDelete)).Delete("/recipes/{recipeID}", h.DeleteRecipe)
		})

		// Events — SERVICE-type items with event_start_at set
		inv.Get("/events", h.ListEventItems)

		// Event ticketing (sell seats with capacity enforcement + check-in).
		// Mutations are tier-gated by events_module (use-case PowerSuite: hospitality Gold);
		// reads + the public PDF stay open so already-sold tickets keep working everywhere.
		if h.ticketsSvc != nil {
			eventsFeat := authclient.RequireFeatureCode("events_module")
			inv.Get("/events/{id}/availability", h.GetEventAvailability)
			// Public branded ticket PDF (with QR) by code — GET, no perm (the code is the secret).
			inv.Get("/tickets/{code}/pdf", h.GetPublicTicketPDF)
			inv.With(perm(rbac.PermTicketsView)).Get("/tickets", h.ListTickets)
			inv.With(perm(rbac.PermTicketsView)).Get("/tickets/{code}", h.GetTicket)
			inv.With(eventsFeat, perm(rbac.PermTicketsAdd)).Post("/tickets", h.CreateTicket)
			inv.With(eventsFeat, perm(rbac.PermTicketsChange)).Post("/tickets/{code}/redeem", h.RedeemTicket)
			inv.With(eventsFeat, perm(rbac.PermTicketsChange)).Post("/tickets/{id}/cancel", h.CancelTicket)
		}

		// Units (manage is platform-only; view is open)
		inv.Get("/units", h.ListUnits)
		inv.With(perm(rbac.PermUnitsAdd)).Post("/units", h.CreateUnit)
		inv.With(perm(rbac.PermUnitsChange)).Put("/units/{unitID}", h.UpdateUnit)
		inv.With(perm(rbac.PermUnitsDelete)).Delete("/units/{unitID}", h.DeleteUnit)

		// CSV bulk import (legacy — items only) — requires bulk_import feature
		inv.With(authclient.RequireFeatureCode("bulk_import"), perm(rbac.PermItemsAdd)).Post("/items/import", h.ImportItems)
		// Multi-format bulk import (CSV/XLSX — items, recipes, modifiers, stock) — requires bulk_import feature
		inv.With(authclient.RequireFeatureCode("bulk_import"), perm(rbac.PermItemsAdd)).Post("/bulk-import", h.BulkImport)
		inv.With(authclient.RequireFeatureCode("bulk_import")).Get("/import-template", h.ImportTemplate)
		// Composite menu-item create: item + recipe + ingredients + modifiers in one call
		inv.With(perm(rbac.PermItemsAdd)).Post("/items/menu-item", h.CreateMenuItemComposite)

		// Modifier Groups & Options
		inv.Get("/modifier-groups", h.ListAllModifierGroups)
		inv.Get("/modifier-groups/{id}", h.GetModifierGroup)
		inv.Get("/items/{itemId}/modifier-groups", h.ListModifierGroups)
		inv.With(perm(rbac.PermVariantsAdd)).Post("/modifier-groups", h.CreateModifierGroup)
		inv.With(perm(rbac.PermVariantsChange)).Put("/modifier-groups/{id}", h.UpdateModifierGroup)
		inv.With(perm(rbac.PermVariantsDelete)).Delete("/modifier-groups/{id}", h.DeleteModifierGroup)
		inv.With(perm(rbac.PermVariantsAdd)).Post("/modifier-groups/{id}/options", h.CreateModifierOption)
		inv.With(perm(rbac.PermVariantsChange)).Put("/modifier-options/{id}", h.UpdateModifierOption)
		inv.With(perm(rbac.PermVariantsDelete)).Delete("/modifier-options/{id}", h.DeleteModifierOption)
	})
}

// GetStockAvailability handles GET /v1/{tenant}/inventory/items/{sku}
func (h *InventoryHandler) GetStockAvailability(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	sku := chi.URLParam(r, "sku")
	if sku == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}

	avail, err := h.itemsSvc.GetStockAvailability(r.Context(), tenantID, sku)
	if err != nil {
		h.log.Error("get stock availability failed", zap.Error(err), zap.String("sku", sku))
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, avail)
}

// BulkAvailability handles POST /v1/{tenant}/inventory/availability
func (h *InventoryHandler) BulkAvailability(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req struct {
		SKUs []string `json:"skus"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if len(req.SKUs) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_SKUS", "At least one SKU is required")
		return
	}

	results, err := h.itemsSvc.BulkAvailability(r.Context(), tenantID, req.SKUs)
	if err != nil {
		h.log.Error("bulk availability failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to check availability")
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// CreateReservation handles POST /v1/{tenant}/inventory/reservations
func (h *InventoryHandler) CreateReservation(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.ReservationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	req.TenantID = tenantID

	if req.OrderID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_ORDER_ID", "Order ID is required")
		return
	}

	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_ITEMS", "At least one item is required")
		return
	}

	result, err := h.stockSvc.CreateReservation(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create reservation failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "RESERVATION_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// GetReservation handles GET /v1/{tenant}/inventory/reservations/{reservationID}
func (h *InventoryHandler) GetReservation(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	reservationID, err := uuid.Parse(chi.URLParam(r, "reservationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid reservation ID")
		return
	}

	result, err := h.stockSvc.GetReservation(r.Context(), tenantID, reservationID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GetReservationsByOrder handles GET /v1/{tenant}/inventory/reservations?order_id={id}
func (h *InventoryHandler) GetReservationsByOrder(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	orderIDStr := r.URL.Query().Get("order_id")
	if orderIDStr == "" {
		writeError(w, http.StatusBadRequest, "MISSING_ORDER_ID", "order_id query parameter is required")
		return
	}

	orderID, err := uuid.Parse(orderIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ORDER_ID", "Invalid order_id")
		return
	}

	results, err := h.stockSvc.GetReservationsByOrderID(r.Context(), tenantID, orderID)
	if err != nil {
		h.log.Error("get reservations by order failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// ReleaseReservation handles POST /v1/{tenant}/inventory/reservations/{reservationID}/release
func (h *InventoryHandler) ReleaseReservation(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	reservationID, err := uuid.Parse(chi.URLParam(r, "reservationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid reservation ID")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	if err := h.stockSvc.ReleaseReservation(r.Context(), tenantID, reservationID, req.Reason); err != nil {
		h.log.Error("release reservation failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "RELEASE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

// ConsumeReservation handles POST /v1/{tenant}/inventory/reservations/{reservationID}/consume
func (h *InventoryHandler) ConsumeReservation(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	reservationID, err := uuid.Parse(chi.URLParam(r, "reservationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid reservation ID")
		return
	}

	if err := h.stockSvc.ConsumeReservation(r.Context(), tenantID, reservationID); err != nil {
		h.log.Error("consume reservation failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CONSUME_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "consumed"})
}

// RecordConsumption handles POST /v1/{tenant}/inventory/consumption
func (h *InventoryHandler) RecordConsumption(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.ConsumptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	req.TenantID = tenantID

	if req.OrderID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_ORDER_ID", "Order ID is required")
		return
	}

	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_ITEMS", "At least one item is required")
		return
	}

	result, err := h.stockSvc.RecordConsumption(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("record consumption failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CONSUMPTION_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// ReverseConsumption handles POST /v1/{tenant}/inventory/consumption/reverse — the stock
// side of a POS sale reversal (called S2S by pos-api's txn-reversal tool). Returns the
// actually-deducted quantities to the warehouse balance and compensates the utilization
// records; idempotent on idempotency_key, capped so replays never over-return stock.
func (h *InventoryHandler) ReverseConsumption(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.ReverseConsumptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.OrderID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_ORDER_ID", "Order ID is required")
		return
	}

	result, err := h.stockSvc.ReverseConsumption(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("reverse consumption failed", zap.Error(err))
		writeError(w, http.StatusUnprocessableEntity, "REVERSAL_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// ListRecipes handles GET /v1/{tenant}/inventory/recipes
func (h *InventoryHandler) ListRecipes(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	sku := r.URL.Query().Get("sku")
	if sku != "" {
		recipe, err := h.recipeSvc.GetRecipeBySKU(r.Context(), tenantID, sku)
		if err != nil {
			if ent.IsNotFound(err) {
				writeJSON(w, http.StatusOK, []recipes.RecipeDTO{})
				return
			}
			h.log.Error("get recipe by sku failed", zap.Error(err), zap.String("sku", sku))
			writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to fetch recipe")
			return
		}
		writeJSON(w, http.StatusOK, []recipes.RecipeDTO{*recipe})
		return
	}

	var recipeOutletID *uuid.UUID
	if outletStr := invmiddleware.GetOutletID(r.Context()); outletStr != "" {
		if oid, err := uuid.Parse(outletStr); err == nil {
			recipeOutletID = &oid
		}
	}

	p := pagination.Parse(r)
	results, total, err := h.recipeSvc.ListRecipes(r.Context(), tenantID, p.Limit, p.Offset, recipeOutletID)
	if err != nil {
		h.log.Error("list recipes failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list recipes")
		return
	}

	writeJSON(w, http.StatusOK, pagination.NewResponse(results, total, p))
}

// GetRecipe handles GET /v1/{tenant}/inventory/recipes/{recipeID}
func (h *InventoryHandler) GetRecipe(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	recipeID, err := uuid.Parse(chi.URLParam(r, "recipeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid recipe ID")
		return
	}

	result, err := h.recipeSvc.GetRecipe(r.Context(), tenantID, recipeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// CreateRecipe handles POST /v1/{tenant}/inventory/recipes
func (h *InventoryHandler) CreateRecipe(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req recipes.RecipeDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := h.recipeSvc.CreateRecipe(r.Context(), tenantID, req)
	if err != nil {
		var unitErr *recipes.UnitValidationError
		if errors.As(err, &unitErr) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  map[string]string{"code": "UNIT_MISMATCH", "message": unitErr.Error()},
				"issues": unitErr.Issues,
			})
			return
		}
		h.log.Error("create recipe failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// UpdateRecipe handles PUT /v1/{tenant}/inventory/recipes/{recipeID}
func (h *InventoryHandler) UpdateRecipe(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	recipeID, err := uuid.Parse(chi.URLParam(r, "recipeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid recipe ID")
		return
	}

	var req recipes.RecipeDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := h.recipeSvc.UpdateRecipe(r.Context(), tenantID, recipeID, req)
	if err != nil {
		var unitErr *recipes.UnitValidationError
		if errors.As(err, &unitErr) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  map[string]string{"code": "UNIT_MISMATCH", "message": unitErr.Error()},
				"issues": unitErr.Issues,
			})
			return
		}
		h.log.Error("update recipe failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// AuditRecipeUnits handles GET /v1/{tenant}/inventory/recipes/unit-audit — lists every
// existing recipe line whose unit cannot deduct stock (legacy cross-dimension data),
// with per-line remediation guidance. Powers the recipes data-quality audit in the UI
// and the tenant remediation reports.
func (h *InventoryHandler) AuditRecipeUnits(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	issues, err := h.recipeSvc.AuditRecipeUnits(r.Context(), tenantID)
	if err != nil {
		h.log.Error("recipe unit audit failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "AUDIT_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": issues, "count": len(issues)})
}

// DeleteRecipe handles DELETE /v1/{tenant}/inventory/recipes/{recipeID}
func (h *InventoryHandler) DeleteRecipe(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	recipeID, err := uuid.Parse(chi.URLParam(r, "recipeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid recipe ID")
		return
	}

	if err := h.recipeSvc.DeleteRecipe(r.Context(), tenantID, recipeID); err != nil {
		h.log.Error("delete recipe failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// RecomputeRecipeCost handles POST /v1/{tenant}/inventory/recipes/{recipeID}/recompute-cost —
// recomputes a recipe/BOM's cost from current ingredient prices.
func (h *InventoryHandler) RecomputeRecipeCost(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	recipeID, err := uuid.Parse(chi.URLParam(r, "recipeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid recipe ID")
		return
	}
	if err := h.recipeSvc.RecalculateRecipeCosts(r.Context(), tenantID, recipeID); err != nil {
		h.log.Error("recompute recipe cost failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "RECOMPUTE_FAILED", err.Error())
		return
	}
	result, err := h.recipeSvc.GetRecipe(r.Context(), tenantID, recipeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Recipe not found")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GetInventorySummary handles GET /v1/{tenant}/inventory/summary
func (h *InventoryHandler) GetInventorySummary(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	summary, err := h.itemsSvc.GetInventorySummary(r.Context(), tenantID)
	if err != nil {
		h.log.Error("get inventory summary failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to fetch inventory summary")
		return
	}

	writeJSON(w, http.StatusOK, summary)
}

// StockValuationReport handles GET /v1/{tenant}/inventory/reports/stock-valuation
func (h *InventoryHandler) StockValuationReport(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	val, err := h.itemsSvc.StockValuation(r.Context(), tenantID)
	if err != nil {
		h.log.Error("stock valuation report failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to compute stock valuation")
		return
	}
	writeJSON(w, http.StatusOK, val)
}

// StockDeadstockReport handles GET /v1/{tenant}/inventory/reports/deadstock?days=90
func (h *InventoryHandler) StockDeadstockReport(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	days := 90
	if d := r.URL.Query().Get("days"); d != "" {
		if n, e := strconv.Atoi(d); e == nil && n > 0 {
			days = n
		}
	}
	rep, err := h.itemsSvc.StockDeadstock(r.Context(), tenantID, days)
	if err != nil {
		h.log.Error("deadstock report failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to compute deadstock report")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ListUnits handles GET /v1/{tenant}/inventory/units
func (h *InventoryHandler) ListUnits(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	results, err := h.unitSvc.ListUnits(r.Context(), tenantID)
	if err != nil {
		h.log.Error("list units failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list units")
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// CreateUnit handles POST /v1/{tenant}/inventory/units
func (h *InventoryHandler) CreateUnit(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req units.UnitDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	result, err := h.unitSvc.CreateUnit(r.Context(), tenantID, req)
	if err != nil {
		var dupErr *units.DuplicateUnitError
		if errors.As(err, &dupErr) {
			writeError(w, http.StatusConflict, "DUPLICATE_UNIT", fmt.Sprintf("A unit with %s %q already exists.", dupErr.Field, dupErr.Value))
			return
		}
		h.log.Error("create unit failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// UpdateUnit handles PUT /v1/{tenant}/inventory/units/{unitID} — updates a unit of measure.
func (h *InventoryHandler) UpdateUnit(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	unitID, err := uuid.Parse(chi.URLParam(r, "unitID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid unit ID")
		return
	}
	var req units.UnitDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	result, err := h.unitSvc.UpdateUnit(r.Context(), tenantID, unitID, req)
	if err != nil {
		var dupErr *units.DuplicateUnitError
		if errors.As(err, &dupErr) {
			writeError(w, http.StatusConflict, "DUPLICATE_UNIT", fmt.Sprintf("A unit with %s %q already exists.", dupErr.Field, dupErr.Value))
			return
		}
		h.log.Error("update unit failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ListItems handles GET /v1/{tenant}/inventory/items — returns a paginated list of active items.
func (h *InventoryHandler) ListItems(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	typeFilter := r.URL.Query().Get("type")
	statusFilter := r.URL.Query().Get("status") // "active" | "inactive" | "all"; default = active
	searchFilter := r.URL.Query().Get("search")
	useCaseFilter := r.URL.Query().Get("use_case") // e.g. HOSPITALITY_ROOM, CONFERENCE, AMENITY

	var categoryID *uuid.UUID
	if catStr := r.URL.Query().Get("category_id"); catStr != "" {
		if catID, parseErr := uuid.Parse(catStr); parseErr == nil {
			categoryID = &catID
		}
	}

	var unitID *uuid.UUID
	if unitStr := r.URL.Query().Get("unit_id"); unitStr != "" {
		if uID, parseErr := uuid.Parse(unitStr); parseErr == nil {
			unitID = &uID
		}
	}

	// Parse optional tags filter: ?tags=vegan,gluten_free
	var tagsFilter []string
	if tagsParam := r.URL.Query().Get("tags"); tagsParam != "" {
		for _, t := range strings.Split(tagsParam, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tagsFilter = append(tagsFilter, t)
			}
		}
	}

	var outletID *uuid.UUID
	if outletStr := invmiddleware.GetOutletID(r.Context()); outletStr != "" {
		if oid, err := uuid.Parse(outletStr); err == nil {
			outletID = &oid
		}
	}

	// ?id=<uuid> restricts the list to a single item by primary key while reusing the full
	// list enrichment — the item-detail page fetches this way so it renders the same enriched
	// shape (category name, effective price, on-hand, images) as a catalog row.
	ctx := r.Context()
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		if itemID, parseErr := uuid.Parse(idStr); parseErr == nil {
			ctx = items.WithItemIDFilter(ctx, itemID)
		}
	}

	// ?include=variants opts into eager-loading each item's active variations.
	for _, inc := range strings.Split(r.URL.Query().Get("include"), ",") {
		if strings.TrimSpace(inc) == "variants" {
			ctx = items.WithIncludeVariants(ctx)
		}
	}
	// ?include_non_billable=1 widens the type filter to also return non-billable items
	// (free accompaniments / supplies) — used by the POS catalog proxy.
	if v := r.URL.Query().Get("include_non_billable"); v == "1" || strings.EqualFold(v, "true") {
		ctx = items.WithIncludeNonBillable(ctx)
	}
	// ?for_recipe=1 scopes the list to recipe-ingredient candidates: GOODS + INGREDIENT
	// plus RECIPE items flagged usable_in_recipes (reusable menu components like Black
	// Tea inside an Iced Passion Tea). Used by the recipe-builder ingredient picker;
	// overrides the plain type filter.
	if v := r.URL.Query().Get("for_recipe"); v == "1" || strings.EqualFold(v, "true") {
		ctx = items.WithRecipeInputScope(ctx)
	}

	p := pagination.Parse(r)
	results, total, err := h.itemsSvc.ListItems(ctx, tenantID, typeFilter, statusFilter, p.Limit, p.Offset, categoryID, unitID, searchFilter, outletID, useCaseFilter, tagsFilter...)
	if err != nil {
		h.log.Error("list items failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list items")
		return
	}

	writeJSON(w, http.StatusOK, pagination.NewResponse(results, total, p))
}

// ListEventItems handles GET /v1/{tenant}/inventory/events
// Returns SERVICE-type items that have event_start_at set, ordered by start time ascending.
func (h *InventoryHandler) ListEventItems(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	results, total, err := h.itemsSvc.ListEventItems(r.Context(), tenantID, p.Limit, p.Offset)
	if err != nil {
		h.log.Error("list event items failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list events")
		return
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(results, total, p))
}

// ListItemVariants handles GET /v1/{tenant}/inventory/items/{itemId}/variants —
// returns the active product variations for an item so retail/POS can sell variations.
func (h *InventoryHandler) ListItemVariants(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	itemID, err := uuid.Parse(chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ITEM_ID", "Invalid item ID")
		return
	}

	variants, err := h.itemsSvc.ListItemVariants(r.Context(), tenantID, itemID)
	if err != nil {
		h.log.Error("list item variants failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list item variants")
		return
	}

	writeJSON(w, http.StatusOK, variants)
}

// CreateItem handles POST /v1/{tenant}/inventory/items — creates a new inventory item.
func (h *InventoryHandler) CreateItem(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req items.ItemDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	// SKU is optional — auto-generated if empty
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Name is required")
		return
	}
	if req.Type == "" {
		req.Type = "GOODS"
	}
	req.IsActive = true

	if err := items.ValidateTicketTiers(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "TIER_CAPACITY", err.Error())
		return
	}

	// Enforce the plan's inventory_max_sku structural cap (hard-block, no overage).
	if _, total, cerr := h.itemsSvc.ListItems(r.Context(), tenantID, "", "all", 1, 0, nil, nil, "", nil, ""); cerr == nil {
		if subscriptions.AssertLimit(w, r, "products", subscriptions.LimitSKU, total) {
			return
		}
	}

	result, err := h.itemsSvc.CreateItem(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create item failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// UpdateItem handles PUT /v1/{tenant}/inventory/items/{sku} — updates an existing item by SKU.
func (h *InventoryHandler) UpdateItem(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	sku := chi.URLParam(r, "sku")
	if sku == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}

	avail, err := h.itemsSvc.GetStockAvailability(r.Context(), tenantID, sku)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
		return
	}

	var req items.ItemDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if err := items.ValidateTicketTiers(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "TIER_CAPACITY", err.Error())
		return
	}

	// Capture cost_price before update for cascade detection.
	prevCostPrice := avail // only used to check if cost changed
	_ = prevCostPrice

	result, err := h.itemsSvc.UpdateItem(r.Context(), tenantID, avail.ItemID, req)
	if err != nil {
		h.log.Error("update item failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}

	// Cascade: if cost_price changed on an INGREDIENT, recalculate all recipe costs that use it.
	if req.CostPrice != nil && h.recipeSvc != nil {
		go func() {
			if cErr := h.recipeSvc.RecalculateCostsForIngredient(r.Context(), tenantID, avail.ItemID); cErr != nil {
				h.log.Warn("ingredient price cascade failed", zap.Error(cErr), zap.String("sku", sku))
			}
		}()
	}

	writeJSON(w, http.StatusOK, result)
}

// SetItemPrice handles PATCH /v1/{tenant}/inventory/items/{sku}/price — a targeted
// selling-price correction that lands everywhere the POS price-resolve reads: the item's
// guardrails + RETAIL/WHOLESALE tier rows, and the linked recipe's selling_price for
// RECIPE items (recipe price outranks tier rows there). Called S2S by pos-api when a
// manager corrects a mispriced order line and opts to fix the catalog too.
func (h *InventoryHandler) SetItemPrice(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	sku := chi.URLParam(r, "sku")
	if sku == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}
	var req struct {
		Price *float64 `json:"price"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Price == nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "price is required")
		return
	}
	if *req.Price < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_PRICE", "price cannot be negative")
		return
	}

	dto, err := h.itemsSvc.SetSellingPriceBySKU(r.Context(), tenantID, sku, *req.Price)
	if err != nil {
		if ent.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
			return
		}
		h.log.Error("set item price failed", zap.Error(err), zap.String("sku", sku))
		writeError(w, http.StatusInternalServerError, "PRICE_UPDATE_FAILED", err.Error())
		return
	}

	recipeUpdated := false
	if dto.Type == "RECIPE" && h.recipeSvc != nil {
		recipeUpdated, err = h.recipeSvc.SetSellingPriceByItem(r.Context(), tenantID, dto.ID, *req.Price)
		if err != nil {
			// The item-side update already landed; report the partial failure rather than 500.
			h.log.Warn("set recipe selling price failed", zap.Error(err), zap.String("sku", sku))
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sku":            sku,
		"price":          *req.Price,
		"recipe_updated": recipeUpdated,
	})
}

// AdjustStock handles POST /v1/{tenant}/inventory/adjust — adjusts stock levels for an item.
func (h *InventoryHandler) AdjustStock(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.AdjustStockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if req.SKU == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}
	if req.Adjustment == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ADJUSTMENT", "Adjustment must be non-zero")
		return
	}
	if req.Reason == "" {
		req.Reason = "adjustment"
	}

	result, err := h.stockSvc.AdjustStock(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("adjust stock failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "ADJUST_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// DeleteItem handles DELETE /v1/{tenant}/inventory/items/{sku} — soft-deletes an item by SKU.
func (h *InventoryHandler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	sku := chi.URLParam(r, "sku")
	if sku == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}

	// Soft-delete: resolve by SKU and set is_active=false only. Resolving directly (not via
	// stock-availability) means items with no balance row are still deletable; setting only the
	// flag avoids the empty-DTO UpdateItem path that blanked required fields like name.
	if err = h.itemsSvc.DeactivateItemBySKU(r.Context(), tenantID, sku); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Item not found")
			return
		}
		h.log.Error("delete item failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// CreateAdjustment handles POST /v1/{tenant}/inventory/adjustments — creates a stock adjustment with audit trail.
func (h *InventoryHandler) CreateAdjustment(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.AdjustStockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	if req.SKU == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKU", "SKU is required")
		return
	}
	if req.Adjustment == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ADJUSTMENT", "Adjustment must be non-zero")
		return
	}
	if req.Reason == "" {
		req.Reason = "other"
	}

	// Large-adjustment approval gate: route adjustments whose magnitude falls in a
	// configured ApprovalRule band through the approval workflow. Safe by default —
	// with no rule configured for the stock_adjustment/stock_writeoff module,
	// nothing is blocked.
	if h.approvalSvc != nil {
		module := "stock_adjustment"
		if req.Reason == "damaged" || req.Reason == "expired" || req.Reason == "shrinkage" {
			module = "stock_writeoff"
		}
		amount := req.Adjustment
		if amount < 0 {
			amount = -amount
		}
		actor := req.AdjustedBy
		if actor == uuid.Nil {
			if claims, ok := authclient.ClaimsFromContext(r.Context()); ok {
				actor, _ = claims.UserID()
			}
		}
		if req.ApprovalIntentID != nil {
			// Retry after a manager decision — only proceed when approved.
			ok, state, _ := h.approvalSvc.Satisfied(r.Context(), tenantID, module, *req.ApprovalIntentID, amount)
			if !ok {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "ADJUSTMENT_APPROVAL_" + strings.ToUpper(state), "approval_required": true,
					"intent_id": req.ApprovalIntentID, "state": state,
				})
				return
			}
		} else {
			// First attempt — create the approval request if a rule gates this amount.
			intent := uuid.New()
			request, gated, _ := h.approvalSvc.Submit(r.Context(), tenantID, module, intent, req.Reference, amount, &actor)
			if gated {
				resp := map[string]any{"approval_required": true, "intent_id": intent, "module": module}
				if request != nil {
					resp["request_id"] = request.ID
				}
				writeJSON(w, http.StatusUnprocessableEntity, resp)
				return
			}
		}
	}

	result, err := h.stockSvc.AdjustStock(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("create adjustment failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "ADJUST_FAILED", err.Error())
		return
	}

	if h.auditSvc != nil {
		actor := req.AdjustedBy
		if actor == uuid.Nil {
			if claims, ok := authclient.ClaimsFromContext(r.Context()); ok {
				actor, _ = claims.UserID()
			}
		}
		action := "stock.adjustment"
		if req.Reason == "damaged" || req.Reason == "expired" || req.Reason == "shrinkage" {
			action = "stock.writeoff"
		}
		amt := req.Adjustment
		var oid *uuid.UUID
		if req.WarehouseID != uuid.Nil {
			oid = &req.WarehouseID
		}
		h.auditSvc.Record(r.Context(), audit.Entry{
			TenantID:    tenantID,
			OutletID:    oid,
			ActorUserID: actor,
			Action:      action,
			EntityType:  "stock_adjustment",
			EntityID:    req.SKU,
			Reason:      req.Reason,
			Amount:      &amt,
			After:       map[string]any{"sku": req.SKU, "adjustment": req.Adjustment, "warehouse_id": req.WarehouseID.String()},
		})
	}

	writeJSON(w, http.StatusCreated, result)
}

// ListAdjustments handles GET /v1/{tenant}/inventory/adjustments — lists stock adjustments with filters.
func (h *InventoryHandler) ListAdjustments(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req stock.ListAdjustmentsRequest

	if itemIDStr := r.URL.Query().Get("item_id"); itemIDStr != "" {
		itemID, err := uuid.Parse(itemIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ITEM_ID", "Invalid item_id")
			return
		}
		req.ItemID = itemID
	}

	if whIDStr := r.URL.Query().Get("warehouse_id"); whIDStr != "" {
		whID, err := uuid.Parse(whIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_WAREHOUSE_ID", "Invalid warehouse_id")
			return
		}
		req.WarehouseID = whID
	} else if outletStr := invmiddleware.GetOutletID(r.Context()); outletStr != "" {
		// Outlet drill-down (X-Outlet-ID) with no explicit warehouse filter: scope to the
		// selected outlet's warehouses (+ shared ones with no outlet link), mirroring the
		// stock-levels list and ListItems outlet separation.
		if outletID, e := uuid.Parse(outletStr); e == nil {
			whIDs, werr := h.orm.Warehouse.Query().
				Where(
					entwarehouse.TenantID(tenantID),
					entwarehouse.Or(entwarehouse.OutletIDEQ(outletID), entwarehouse.OutletIDIsNil()),
				).
				IDs(r.Context())
			if werr == nil && len(whIDs) > 0 {
				req.WarehouseIDs = whIDs
			}
		}
	}

	if reason := r.URL.Query().Get("reason"); reason != "" {
		req.Reason = reason
	}

	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		t, err := time.Parse(time.RFC3339, dateFrom)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_DATE_FROM", "date_from must be RFC3339 format")
			return
		}
		req.DateFrom = t
	}

	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		t, err := time.Parse(time.RFC3339, dateTo)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_DATE_TO", "date_to must be RFC3339 format")
			return
		}
		req.DateTo = t
	}

	results, err := h.stockSvc.ListAdjustments(r.Context(), tenantID, req)
	if err != nil {
		h.log.Error("list adjustments failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list adjustments")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  results,
		"total": len(results),
	})
}

// GetBOMAvailability handles GET /v1/{tenant}/inventory/availability/bom?skus=SKU1,SKU2
func (h *InventoryHandler) GetBOMAvailability(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	skusParam := r.URL.Query().Get("skus")
	if skusParam == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SKUS", "skus query parameter is required")
		return
	}

	skus := strings.Split(skusParam, ",")
	if len(skus) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_SKUS", "At least one SKU is required")
		return
	}

	// Trim whitespace from SKUs
	for i := range skus {
		skus[i] = strings.TrimSpace(skus[i])
	}

	results, err := h.itemsSvc.GetBOMAvailability(r.Context(), tenantID, skus)
	if err != nil {
		h.log.Error("bom availability failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to check BOM availability")
		return
	}

	writeJSON(w, http.StatusOK, results)
}

// ListCategories handles GET /v1/{tenant}/inventory/categories — returns all active categories.
func (h *InventoryHandler) ListCategories(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	// has_items=true → only categories with at least one active item linked
	// (used by selection surfaces so a picked category never yields an empty set).
	hasItems := strings.EqualFold(r.URL.Query().Get("has_items"), "true") || r.URL.Query().Get("has_items") == "1"

	results, err := h.itemsSvc.ListCategoriesFiltered(r.Context(), tenantID, hasItems)
	if err != nil {
		h.log.Error("list categories failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list categories")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  results,
		"total": len(results),
	})
}

// DeleteCategory handles DELETE /inventory/categories/{categoryID} — soft-deletes a category.
func (h *InventoryHandler) DeleteCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	categoryID, err := uuid.Parse(chi.URLParam(r, "categoryID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid category ID")
		return
	}
	if err := h.itemsSvc.DeleteCategory(r.Context(), tenantID, categoryID); err != nil {
		h.log.Error("delete category failed", zap.Error(err))
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Category not found or could not be deleted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// CreateCategory handles POST /v1/{tenant}/inventory/categories — creates a new item category.
func (h *InventoryHandler) CreateCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req items.CategoryDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Name is required")
		return
	}
	result, err := h.itemsSvc.CreateCategory(r.Context(), tenantID, req)
	if err != nil {
		var dupErr *items.DuplicateCategoryError
		if errors.As(err, &dupErr) {
			writeError(w, http.StatusConflict, "DUPLICATE_CATEGORY", fmt.Sprintf("A category named %q already exists.", dupErr.Name))
			return
		}
		h.log.Error("create category failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// UpdateCategory handles PUT /v1/{tenant}/inventory/categories/{categoryID} — updates an item category.
func (h *InventoryHandler) UpdateCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	categoryID, err := uuid.Parse(chi.URLParam(r, "categoryID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid category ID")
		return
	}
	var req items.CategoryDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Name is required")
		return
	}
	result, err := h.itemsSvc.UpdateCategory(r.Context(), tenantID, categoryID, req)
	if err != nil {
		var dupErr *items.DuplicateCategoryError
		if errors.As(err, &dupErr) {
			writeError(w, http.StatusConflict, "DUPLICATE_CATEGORY", fmt.Sprintf("A category named %q already exists.", dupErr.Name))
			return
		}
		h.log.Error("update category failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// DeleteUnit handles DELETE /inventory/units/{unitID} — soft-deletes a unit of measure.
func (h *InventoryHandler) DeleteUnit(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	unitID, err := uuid.Parse(chi.URLParam(r, "unitID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid unit ID")
		return
	}
	if err := h.unitSvc.DeleteUnit(r.Context(), tenantID, unitID); err != nil {
		h.log.Error("delete unit failed", zap.Error(err))
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Unit not found or could not be deleted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

type importResult struct {
	Created int      `json:"created"`
	Updated int      `json:"updated"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// ImportItems handles POST /inventory/items/import — CSV bulk upsert.
// Expected CSV columns (header row required): name, sku, type, category_id, unit_id
func (h *InventoryHandler) ImportItems(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FORM", "Expected multipart/form-data with a 'file' field")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "MISSING_FILE", "File field 'file' is required")
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CSV", "Failed to read CSV header")
		return
	}

	colIdx := make(map[string]int, len(header))
	for i, col := range header {
		colIdx[strings.ToLower(strings.TrimSpace(col))] = i
	}

	col := func(row []string, name string) string {
		i, ok := colIdx[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	// Load all existing items once for SKU→ID lookup (avoids N+1 queries).
	existingItems, _, _ := h.itemsSvc.ListItems(r.Context(), tenantID, "", "all", 10000, 0, nil, nil, "", nil, "")
	skuToID := make(map[string]uuid.UUID, len(existingItems))
	for _, it := range existingItems {
		skuToID[it.SKU] = it.ID
	}

	var result importResult
	rows, _ := reader.ReadAll()
	for i, row := range rows {
		name := col(row, "name")
		sku := col(row, "sku")
		if name == "" || sku == "" {
			result.Failed++
			result.Errors = append(result.Errors, "row "+strings.Join([]string{}, "")+sku+": name and sku are required")
			_ = i
			continue
		}

		itemType := strings.ToUpper(col(row, "type"))
		if itemType == "" {
			itemType = "GOODS"
		}

		dto := items.ItemDTO{SKU: sku, Name: name, Type: itemType, IsActive: true}
		if catStr := col(row, "category_id"); catStr != "" {
			if catID, parseErr := uuid.Parse(catStr); parseErr == nil {
				dto.CategoryID = &catID
			}
		}
		if unitStr := col(row, "unit_id"); unitStr != "" {
			if unitID, parseErr := uuid.Parse(unitStr); parseErr == nil {
				dto.UnitID = &unitID
			}
		}

		if existingID, exists := skuToID[sku]; exists {
			if _, updateErr := h.itemsSvc.UpdateItem(r.Context(), tenantID, existingID, dto); updateErr != nil {
				result.Failed++
				result.Errors = append(result.Errors, "sku="+sku+": "+updateErr.Error())
			} else {
				result.Updated++
			}
		} else {
			if created, createErr := h.itemsSvc.CreateItem(r.Context(), tenantID, dto); createErr != nil {
				result.Failed++
				result.Errors = append(result.Errors, "sku="+sku+": "+createErr.Error())
			} else {
				skuToID[sku] = created.ID
				result.Created++
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}
