package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/bundles"
	"github.com/bengobox/inventory-service/internal/modules/documents"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// InventoryExtrasHandler handles stock, lots, suppliers, purchase-orders, bundles, activity, and report endpoints.
type InventoryExtrasHandler struct {
	log          *zap.Logger
	orm          *ent.Client
	rbacSvc      *rbac.Service
	bundleSvc    *bundles.Service
	varianceSvc  *recipes.VarianceService
	menuEngSvc   *recipes.MenuEngineeringService
	docSvc       *documents.Service
	stockSvc     *stock.Service
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
	r.With(perm(rbac.PermProcurementAdd)).Post("/inventory/suppliers", h.CreateSupplier)
	r.Get("/inventory/suppliers/{supplierID}", h.GetSupplier)
	r.With(perm(rbac.PermProcurementChange)).Put("/inventory/suppliers/{supplierID}", h.UpdateSupplier)
	r.With(perm(rbac.PermProcurementDelete)).Delete("/inventory/suppliers/{supplierID}", h.DeleteSupplier)

	// Purchase Orders — mutations require the purchase_orders subscription feature.
	// (Only POST/PUT are gated: they pass through the group's auth so claims are present.
	// GET list/detail stay open like other inventory reads.)
	feat := authclient.RequireFeature("purchase_orders")
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

	// Reports
	r.Get("/inventory/reports/food-cost-variance", h.FoodCostVarianceReport)
	r.Get("/inventory/reports/food-cost-variance.pdf", h.FoodCostVarianceReportPDF)
	r.Get("/inventory/reports/menu-engineering", h.MenuEngineeringReport)
	r.Get("/inventory/reports/menu-engineering.pdf", h.MenuEngineeringReportPDF)

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

	// Approval matrix (rules + requests) gating purchase orders and requisitions
	h.registerApprovalRoutes(r, perm)

	// Per-unit serial registry (read)
	h.registerSerialRoutes(r)

	// Document numbering settings (PO/GRN/... sequence config)
	h.registerDocumentSequenceRoutes(r, perm, rbac.PermSettingsManage)
}
