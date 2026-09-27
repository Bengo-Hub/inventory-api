package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/platform/treasury"
)

// Purchase orders against treasury budgets. Treasury owns budgets; inventory asks before a PO
// is sent (the point the spend is promised to the supplier) and reports the PO's lifecycle
// through events (purchase_order.sent commits it, purchase_order.cancelled releases it).

type purchaseBudgetChecker interface {
	CheckPurchaseBudget(ctx context.Context, tenantID uuid.UUID, in treasury.PurchaseBudgetInput) (*treasury.BudgetCheck, error)
}

// SetBudgetChecker wires the treasury budget check on PO send. Optional.
func (h *InventoryExtrasHandler) SetBudgetChecker(c purchaseBudgetChecker) { h.budgets = c }

// canOverrideBudget: a stop can be pushed through only by someone who manages approvals or
// procurement, and only when they ask for it explicitly.
func (h *InventoryExtrasHandler) canOverrideBudget(r *http.Request, tenantID uuid.UUID) bool {
	if !strings.EqualFold(r.URL.Query().Get("override_budget"), "true") &&
		!strings.EqualFold(r.Header.Get("X-Budget-Override"), "true") {
		return false
	}
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims == nil {
		return false
	}
	if claims.IsSuperuser() || claims.IsPlatformOwner {
		return true
	}
	perms := []string{rbac.PermApprovalsManage, rbac.PermProcurementManage}
	if claims.HasAnyPermission(perms...) {
		return true
	}
	if h.rbacSvc == nil {
		return false
	}
	uid, err := uuid.Parse(claims.Subject)
	if err != nil {
		return false
	}
	for _, p := range perms {
		if has, err := h.rbacSvc.HasPermission(r.Context(), tenantID, uid, p); err == nil && has {
			return true
		}
	}
	return false
}

// checkPOBudget runs the check for a PO about to be sent. ok=false means a 409 was written.
// Failures fail open: a treasury outage never blocks purchasing.
func (h *InventoryExtrasHandler) checkPOBudget(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, po *ent.PurchaseOrder) (*treasury.BudgetCheck, bool) {
	if h.budgets == nil {
		return nil, true
	}
	date := time.Now()
	if po.OrderDate != nil && !po.OrderDate.IsZero() {
		date = *po.OrderDate
	}
	res, err := h.budgets.CheckPurchaseBudget(r.Context(), tenantID, treasury.PurchaseBudgetInput{
		POID: po.ID, NetAmount: po.TotalAmount, Currency: po.Currency, ProjectID: po.ProjectID, Date: date,
	})
	if err != nil || res == nil {
		if err != nil {
			h.log.Warn("PO budget check failed; sending anyway", zap.String("po", po.PoNumber), zap.Error(err))
		}
		return nil, true
	}
	if res.Action == "stop" && !h.canOverrideBudget(r, tenantID) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "OVER_BUDGET",
			"code":    "over_budget",
			"message": "This purchase order exceeds the available budget",
			"budget":  res,
		})
		return res, false
	}
	return res, true
}

// poBudgetPayload is the event payload treasury uses to keep the PO's budget commitment.
func poBudgetPayload(tenantID uuid.UUID, po *ent.PurchaseOrder) map[string]any {
	p := map[string]any{
		"po_id":        po.ID,
		"po_number":    po.PoNumber,
		"tenant_id":    tenantID,
		"supplier_id":  po.SupplierID,
		"subtotal":     po.TotalAmount,
		"total_amount": po.TotalAmount,
		"currency":     po.Currency,
	}
	if po.ProjectID != nil {
		p["project_id"] = po.ProjectID.String()
	}
	if po.OrderDate != nil && !po.OrderDate.IsZero() {
		p["order_date"] = po.OrderDate.Format("2006-01-02")
	}
	return p
}
