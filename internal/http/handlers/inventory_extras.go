package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	invmiddleware "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/bundles"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
)

// InventoryExtrasHandler handles stock, lots, suppliers, purchase-orders, bundles, and activity endpoints.
type InventoryExtrasHandler struct {
	log       *zap.Logger
	orm       *ent.Client
	rbacSvc   *rbac.Service
	bundleSvc *bundles.Service
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

	// Bundles
	r.Get("/inventory/bundles", h.ListBundles)
	r.With(perm(rbac.PermItemsAdd)).Post("/inventory/bundles", h.CreateBundle)
	r.Get("/inventory/bundles/{bundleID}", h.GetBundle)
	r.With(perm(rbac.PermItemsChange)).Put("/inventory/bundles/{bundleID}", h.UpdateBundle)
	r.With(perm(rbac.PermItemsDelete)).Delete("/inventory/bundles/{bundleID}", h.DeleteBundle)
}
