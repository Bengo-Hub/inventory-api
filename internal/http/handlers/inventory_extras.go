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

	authclient "github.com/Bengo-Hub/shared-auth-client"
	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/bundles"
	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/reports"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// InventoryExtrasHandler handles stock, lots, suppliers, purchase-orders, bundles, activity, and report endpoints.
type InventoryExtrasHandler struct {
	log         *zap.Logger
	orm         *ent.Client
	rbacSvc     *rbac.Service
	bundleSvc   *bundles.Service
	varianceSvc *recipes.VarianceService
	menuEngSvc  *recipes.MenuEngineeringService
	reportsSvc  *reports.Service
	docSvc      *documents.Service
	stockSvc    *stock.Service
	itemsSvc    *items.Service
	auditSvc    *audit.Service
	// authForFeatureGet authenticates feature-gated GET routes. The tenant router group
	// only authenticates non-GET requests, so a GET behind RequireFeatureCode must parse
	// claims itself or every caller 401s (same gotcha inventory.go solves with
	// requireAuthForFeatureGet).
	authForFeatureGet func(http.Handler) http.Handler
}

// SetAuthForFeatureGets wires the auth middleware used in front of feature-gated GETs
// (RequireAnyAuth: SSO or terminal PIN sessions).
func (h *InventoryExtrasHandler) SetAuthForFeatureGets(mw func(http.Handler) http.Handler) {
	h.authForFeatureGet = mw
}

// featureGetAuth returns the wired auth middleware or a pass-through (tests / setups
// without auth keep prior behavior).
func (h *InventoryExtrasHandler) featureGetAuth() func(http.Handler) http.Handler {
	if h.authForFeatureGet == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return h.authForFeatureGet
}

// SetDocService injects the document-generation service (PDF + numbering).
func (h *InventoryExtrasHandler) SetDocService(svc *documents.Service) {
	h.docSvc = svc
}

// SetStockService injects the stock service so procurement/manufacturing/return
// flows can apply real stock movements in-process.
func (h *InventoryExtrasHandler) SetStockService(svc *stock.Service) {
	h.stockSvc = svc
}

// SetAuditService wires the centralized audit trail for goods-receipt cost capture.
func (h *InventoryExtrasHandler) SetAuditService(a *audit.Service) {
	h.auditSvc = a
}

// SetItemsService injects the items service so goods-receipt posting can apply (or schedule) a
// selling-price change captured alongside the receipt's cost.
func (h *InventoryExtrasHandler) SetItemsService(svc *items.Service) {
	h.itemsSvc = svc
}

// skuForItem resolves an item's SKU from its ID (stock ops are SKU-based). Empty on miss.
func (h *InventoryExtrasHandler) skuForItem(ctx context.Context, tenantID, itemID uuid.UUID) string {
	it, err := h.orm.Item.Get(ctx, itemID)
	if err != nil || it.TenantID != tenantID {
		return ""
	}
	return it.Sku
}

// NewInventoryExtrasHandler creates the handler.
func NewInventoryExtrasHandler(log *zap.Logger, orm *ent.Client, rbacSvc *rbac.Service) *InventoryExtrasHandler {
	return &InventoryExtrasHandler{
		log:     log.Named("inventory_extras.handler"),
		orm:     orm,
		rbacSvc: rbacSvc,
	}
}

// SetBundleService injects the bundle service.
func (h *InventoryExtrasHandler) SetBundleService(svc *bundles.Service) {
	h.bundleSvc = svc
}

// publishSupplierEvent writes a supplier event to the outbox table.
func (h *InventoryExtrasHandler) publishSupplierEvent(ctx context.Context, s *ent.Supplier, eventType string) {
	// Canonical convention: subject == AggregateType("inventory") + "." + EventType. Callers
	// pass the full "inventory.supplier.*" subject, so strip the domain prefix to avoid a
	// doubled subject ("supplier.inventory.supplier.created") that no consumer listens on.
	eventType = strings.TrimPrefix(eventType, "inventory.")
	evt := &events.Event{
		ID:            uuid.New(),
		TenantID:      s.TenantID,
		AggregateType: "inventory",
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
	// One-shot, idempotent, tenant-scoped admin action: seeds an opening cost layer for any
	// on-hand stock that predates the cost-layer feature, so valuation/COGS never silently read
	// zero for it. Settings-manage gated — this is an admin/finance action, not routine ops.
	r.With(perm(rbac.PermSettingsManage)).Post("/inventory/cost-layers/backfill", h.BackfillCostLayers)
	// Branded PDF/CSV export of stock levels — same filters as ListStock plus warehouse/
	// location drill-down, reuses queryStockLevels + the docs report engine (see
	// report_pdf_stock.go).
	r.Get("/inventory/stock/export", h.StockExportPDF)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/stock/{sku}/reorder-config", h.UpdateReorderConfig)

	// Lots & Batches — tier-gated (pharmacy family all tiers; hosp/retail never; see
	// docs/subscription-plans in subscriptions-api). Reads stay open per convention.
	lotsFeat := authclient.RequireFeatureCode("lots_batches")
	r.Get("/inventory/lots", h.ListLots)
	r.With(lotsFeat, perm(rbac.PermItemsAdd)).Post("/inventory/lots", h.CreateLot)
	r.With(lotsFeat, perm(rbac.PermItemsChange)).Put("/inventory/lots/{lotID}", h.UpdateLot)
	r.With(lotsFeat, perm(rbac.PermItemsDelete)).Delete("/inventory/lots/{lotID}", h.DeleteLot)

	// Suppliers
	r.Get("/inventory/suppliers", h.ListSuppliers)
	r.With(perm(rbac.PermProcurementAdd)).Post("/inventory/suppliers", h.CreateSupplier)
	r.Get("/inventory/suppliers/{supplierID}", h.GetSupplier)
	r.With(perm(rbac.PermProcurementChange)).Put("/inventory/suppliers/{supplierID}", h.UpdateSupplier)
	r.With(perm(rbac.PermProcurementDelete)).Delete("/inventory/suppliers/{supplierID}", h.DeleteSupplier)

	// Purchase Orders — mutations require the purchase_orders subscription feature.
	// (Only POST/PUT are gated: they pass through the group's auth so claims are present.
	// GET list/detail stay open like other inventory reads.)
	feat := authclient.RequireFeatureCode("purchase_orders")
	r.Get("/inventory/purchase-orders", h.ListPurchaseOrders)
	r.Get("/inventory/purchase-orders/{poID}", h.GetPurchaseOrder)
	r.With(feat, perm(rbac.PermProcurementAdd)).Post("/inventory/purchase-orders", h.CreatePurchaseOrder)
	r.With(feat, perm(rbac.PermProcurementChange)).Put("/inventory/purchase-orders/{poID}/send", h.SendPurchaseOrder)
	r.With(feat, perm(rbac.PermProcurementChange)).Put("/inventory/purchase-orders/{poID}/receive", h.ReceivePurchaseOrder)
	r.With(feat, perm(rbac.PermProcurementChange)).Put("/inventory/purchase-orders/{poID}/cancel", h.CancelPurchaseOrder)
	r.With(feat, perm(rbac.PermProcurementChange)).Put("/inventory/purchase-orders/{poID}/amend", h.AmendPurchaseOrder)
	r.With(feat, perm(rbac.PermProcurementChange)).Post("/inventory/purchase-orders/{poID}/submit-for-approval", h.SubmitPurchaseOrderForApproval)

	// Activity
	r.Get("/inventory/activity", h.ListActivity)

	// Bundles
	r.Get("/inventory/bundles", h.ListBundles)
	r.With(perm(rbac.PermItemsAdd)).Post("/inventory/bundles", h.CreateBundle)
	r.Get("/inventory/bundles/{bundleID}", h.GetBundle)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/bundles/{bundleID}", h.UpdateBundle)
	r.With(perm(rbac.PermItemsDelete)).Delete("/inventory/bundles/{bundleID}", h.DeleteBundle)

	// Reports — the read IS the feature here, so the GETs are gated (unlike module reads):
	// per-tier report gating from the use-case PowerSuite specs. "Ingredient Utilization"
	// is being renamed "Stock Reconciliation" product-wide (routes kept for compatibility).
	getAuth := h.featureGetAuth()
	fcvFeat := authclient.RequireFeatureCode("report_food_cost_variance")
	menuFeat := authclient.RequireFeatureCode("report_menu_engineering")
	reconFeat := authclient.RequireFeatureCode("report_stock_reconciliation")
	r.With(getAuth, fcvFeat).Get("/inventory/reports/food-cost-variance", h.FoodCostVarianceReport)
	r.With(getAuth, fcvFeat).Get("/inventory/reports/food-cost-variance.pdf", h.FoodCostVarianceReportPDF)
	r.With(getAuth, menuFeat).Get("/inventory/reports/menu-engineering", h.MenuEngineeringReport)
	r.With(getAuth, menuFeat).Get("/inventory/reports/menu-engineering.pdf", h.MenuEngineeringReportPDF)
	r.With(getAuth, reconFeat).Get("/inventory/reports/ingredient-utilization/summary", h.IngredientUtilizationSummary)
	r.With(getAuth, reconFeat).Get("/inventory/reports/ingredient-utilization/timeseries", h.IngredientUtilizationTimeseries)
	r.With(getAuth, reconFeat).Get("/inventory/reports/ingredient-utilization/by-recipe", h.IngredientUtilizationByRecipe)
	r.With(getAuth, reconFeat).Get("/inventory/reports/ingredient-utilization.pdf", h.IngredientUtilizationReportPDF)

	// Procurement (migrated from ERP procurement/*)
	h.registerRequisitionRoutes(r, perm, rbac.PermProcurementAdd, rbac.PermProcurementChange)
	h.registerContractRoutes(r, perm, rbac.PermProcurementAdd, rbac.PermProcurementChange)
	h.registerPurchaseReturnRoutes(r, perm, rbac.PermProcurementAdd, rbac.PermProcurementChange)
	h.registerGoodsReceiptRoutes(r, perm, rbac.PermProcurementAdd, rbac.PermProcurementChange)
	h.registerProcurementMiscRoutes(r, perm, rbac.PermProcurementAdd, rbac.PermProcurementChange)
	h.registerProcurementAnalyticsRoutes(r)
	h.registerRFQRoutes(r, perm, rbac.PermProcurementAdd, rbac.PermProcurementChange, rbac.PermProcurementDelete)
	r.Get("/inventory/purchase-orders/{poID}/pdf", h.GeneratePurchaseOrderPDF)

	// Manufacturing (migrated from ERP manufacturing/*)
	h.registerManufacturingRoutes(r, perm, rbac.PermManufacturingAdd, rbac.PermManufacturingChange)
	h.registerManufacturingAnalyticsRoutes(r)

	// Fixed assets register (migrated from ERP assets/*)
	h.registerAssetRoutes(r, perm, rbac.PermAssetsAdd, rbac.PermAssetsChange, rbac.PermAssetsDelete)

	// Warranty tracking for serialized items (retail use case, warranties feature)
	h.registerWarrantyRoutes(r, perm)

	// Approval matrix (rules + requests) gating purchase orders and requisitions
	h.registerApprovalRoutes(r, perm)

	// Per-unit serial registry (read)
	h.registerSerialRoutes(r)

	// Document numbering settings (PO/GRN/... sequence config)
	h.registerDocumentSequenceRoutes(r, perm, rbac.PermSettingsManage)

	// Pharmacy drug-class resolution + interaction-check (S2S, called by pos-api)
	h.registerPharmacyInteractionRoutes(r)
}
