package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	arule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	"github.com/bengobox/inventory-service/internal/ent/auditlog"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
)

// approvedUnexecuted inserts an approved stock adjustment request whose action never ran, with a
// replayable payload, for the given tenant.
func approvedUnexecuted(t *testing.T, c *ent.Client, tenant uuid.UUID, sku string) *ent.ApprovalRequest {
	t.Helper()
	return c.ApprovalRequest.Create().SetTenantID(tenant).SetModule(areq.ModuleStockAdjustment).
		SetObjectID(uuid.New()).SetObjectReference("ADJ-OLD").SetAmount(3).
		SetStatus(areq.StatusApproved).SetDecidedAt(time.Now().Add(-24 * time.Hour)).
		SetPayload(map[string]any{"sku": sku, "adjustment": -3.0, "reason": "correction"}).
		SaveX(context.Background())
}

// A platform owner cancels an approved request whose action never ran: it becomes cancelled, stays
// as history, nothing posts, and an audit row records it with the reason.
func TestCancelApprovedRequest_PlatformOwner(t *testing.T) {
	e := newHardDeleteEnv(t)
	ctx := context.Background()
	req := approvedUnexecuted(t, e.client, e.tenant, "NEVER-POSTED")

	code, body := e.call(http.MethodPost, "/inventory/approval-requests/"+req.ID.String()+"/cancel?reason=stale+adjustment", nil)
	if code != http.StatusOK || body["status"] != "cancelled" {
		t.Fatalf("cancel = %d %v", code, body)
	}
	got := e.client.ApprovalRequest.GetX(ctx, req.ID)
	if got.Status != areq.StatusCancelled || got.ExecutedAt != nil {
		t.Fatalf("want cancelled and never executed, got %s executed_at=%v", got.Status, got.ExecutedAt)
	}
	logs := e.client.AuditLog.Query().Where(auditlog.TenantID(e.tenant), auditlog.EntityID(req.ID.String())).AllX(ctx)
	if len(logs) != 1 || logs[0].Action != "approval.cancelled" || logs[0].Reason != "stale adjustment" {
		t.Fatalf("want one approval.cancelled audit row with the reason, got %v", logs)
	}
	if logs[0].BeforeJSON["status"] != "approved" {
		t.Fatalf("the audit snapshot must show the approved state, got %v", logs[0].BeforeJSON)
	}

	// A cancelled request cannot be executed afterwards.
	if code, _ := e.call(http.MethodPost, "/inventory/approval-requests/"+req.ID.String()+"/execute", nil); code != http.StatusConflict {
		t.Fatalf("execute after cancel = %d, want 409", code)
	}
	if won, _ := approvals.NewService(e.client).ClaimExecution(ctx, e.tenant, req.ID); won {
		t.Fatal("a cancelled request must never be claimed for execution")
	}
}

// Only the signed platform-owner claim may cancel; admins and the superuser role get 403.
func TestCancelApprovedRequest_ForbiddenWithoutPlatformOwnerClaim(t *testing.T) {
	e := newHardDeleteEnv(t)
	req := approvedUnexecuted(t, e.client, e.tenant, "ADMIN-TRY")
	for _, roles := range [][]string{{"admin"}, {"superuser"}} {
		e.setClaims(false, roles...)
		if code, _ := e.call(http.MethodPost, "/inventory/approval-requests/"+req.ID.String()+"/cancel", nil); code != http.StatusForbidden {
			t.Fatalf("roles %v: want 403, got %d", roles, code)
		}
	}
	if got := e.client.ApprovalRequest.GetX(context.Background(), req.ID).Status; got != areq.StatusApproved {
		t.Fatalf("a refused cancel must leave the request approved, got %s", got)
	}
}

// Executed requests, requests that are not approved, and other tenants' requests are refused.
func TestCancelApprovedRequest_Refusals(t *testing.T) {
	e := newHardDeleteEnv(t)
	ctx := context.Background()

	executed := approvedUnexecuted(t, e.client, e.tenant, "DONE")
	e.client.ApprovalRequest.UpdateOneID(executed.ID).SetExecutedAt(time.Now()).ExecX(ctx)
	if code, body := e.call(http.MethodPost, "/inventory/approval-requests/"+executed.ID.String()+"/cancel", nil); code != http.StatusConflict || body["code"] != "APPROVAL_EXECUTED" {
		t.Fatalf("executed: want 409 APPROVAL_EXECUTED, got %d %v", code, body)
	}

	pending := e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModuleContract).
		SetObjectID(uuid.New()).SetAmount(1).SaveX(ctx)
	if code, body := e.call(http.MethodPost, "/inventory/approval-requests/"+pending.ID.String()+"/cancel", nil); code != http.StatusConflict || body["code"] != "NOT_APPROVED" {
		t.Fatalf("pending: want 409 NOT_APPROVED, got %d %v", code, body)
	}

	other := approvedUnexecuted(t, e.client, uuid.New(), "OTHER")
	if code, _ := e.call(http.MethodPost, "/inventory/approval-requests/"+other.ID.String()+"/cancel", nil); code != http.StatusNotFound {
		t.Fatalf("another tenant's request: want 404, got %d", code)
	}
	if got := e.client.ApprovalRequest.GetX(ctx, other.ID).Status; got != areq.StatusApproved {
		t.Fatalf("another tenant's request must be untouched, got %s", got)
	}
}

// A supplier-only change on a purchase order withdraws its unused approval, the same as a total
// change, so Send needs a new approval for the new supplier.
func TestAmendPO_SupplierChangeWithdrawsApproval(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModulePurchaseOrder)
	ctx := context.Background()
	it := e.item("PO-SUP", 0)
	other := e.client.Supplier.Create().SetTenantID(e.tenant).SetName("OTHER SUPPLIES").SetCode("OTH").SaveX(ctx)
	po := e.client.PurchaseOrder.Create().SetTenantID(e.tenant).SetPoNumber("PO-0002").SetSupplierID(e.sup).
		SetWarehouseID(e.wh).SetTotalAmount(400).SaveX(ctx)
	e.client.PurchaseOrderLine.Create().SetPoID(po.ID).SetItemID(it.ID).SetQuantityOrdered(4).
		SetUnitPrice(100).SetTotalPrice(400).SaveX(ctx)
	base := "/inventory/purchase-orders/" + po.ID.String()

	if code, _ := e.call(http.MethodPost, base+"/submit-for-approval", nil); code != http.StatusOK {
		t.Fatalf("submit = %d", code)
	}
	req := e.lastRequest(areq.ModulePurchaseOrder, po.ID)
	e.decide(req, "approve")

	// Same lines and total, new supplier.
	code, body := e.call(http.MethodPut, base+"/amend", map[string]any{
		"supplier_id": other.ID, "warehouse_id": e.wh,
		"line_items": []map[string]any{{"item_id": it.ID, "quantity": 4, "unit_cost": 100}},
	})
	if code != http.StatusOK {
		t.Fatalf("amend = %d %v", code, body)
	}
	if got := e.client.ApprovalRequest.GetX(ctx, req.ID).Status; got != areq.StatusCancelled {
		t.Fatalf("a supplier change must withdraw the approval, got %s", got)
	}
	if code, body := e.call(http.MethodPut, base+"/send", nil); code != http.StatusConflict || body["approval_required"] != true {
		t.Fatalf("send after a supplier change = %d %v, want 409 approval required", code, body)
	}
	if got := e.client.PurchaseOrder.GetX(ctx, po.ID).Status; got != entpo.StatusDraft {
		t.Fatalf("the order must stay a draft, got %s", got)
	}
}
