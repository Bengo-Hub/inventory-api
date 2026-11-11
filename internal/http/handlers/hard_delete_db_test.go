package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entact "github.com/bengobox/inventory-service/internal/ent/approvalaction"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	"github.com/bengobox/inventory-service/internal/ent/auditlog"
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entgrl "github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	"github.com/bengobox/inventory-service/internal/ent/outboxevent"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpol "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// hardDeleteEnv is the goods-receipt approval setup with switchable caller claims, so the same
// data can be hit as a platform owner, a tenant admin or a superuser role.
type hardDeleteEnv struct {
	*grnApprovalEnv
	claims *authclient.Claims
}

func newHardDeleteEnv(t *testing.T) *hardDeleteEnv {
	t.Helper()
	client := testutil.SchemaClient(t, "inv_harddel_")
	ctx := context.Background()
	tenant := uuid.New()
	client.Tenant.Create().SetID(tenant).SetName("Hard Delete").SetSlug("hd-" + tenant.String()).SaveX(ctx)
	wh := client.Warehouse.Create().SetTenantID(tenant).SetName("Main Store").SetCode("MAIN").SetIsDefault(true).SaveX(ctx)
	sup := client.Supplier.Create().SetTenantID(tenant).SetName("Supplier").SetCode("SUP").SaveX(ctx)
	rule := client.ApprovalRule.Create().SetTenantID(tenant).SetModule("goods_receipt").
		SetName("All receipts").SetMinAmount(0).SaveX(ctx)
	client.ApprovalStep.Create().SetTenantID(tenant).SetRuleID(rule.ID).SetSequence(1).
		SetName("Admin").SetApproverRole("admin").SaveX(ctx)

	e := &hardDeleteEnv{}
	e.asPlatformOwner()
	h := &InventoryExtrasHandler{log: zap.NewNop(), orm: client, stockSvc: stock.NewService(client, zap.NewNop())}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := context.WithValue(req.Context(), httpware.TenantIDKey, tenant.String())
			c = authclient.ContextWithClaims(c, e.claims)
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	pass := func(string) func(http.Handler) http.Handler { return func(n http.Handler) http.Handler { return n } }
	h.registerGoodsReceiptRoutes(r, pass, "add", "change")
	h.registerApprovalRoutes(r, pass)
	r.Put("/inventory/purchase-orders/{poID}/receive", h.ReceivePurchaseOrder)
	r.Delete("/inventory/purchase-orders/{poID}", h.HardDeletePurchaseOrder)
	r.Post("/inventory/requisitions/{reqID}/submit", h.SubmitRequisition)
	e.grnApprovalEnv = &grnApprovalEnv{t: t, client: client, tenant: tenant, wh: wh.ID, supplier: sup.ID, router: r}
	return e
}

func (e *hardDeleteEnv) setClaims(owner bool, roles ...string) {
	c := &authclient.Claims{IsPlatformOwner: owner, Roles: roles}
	c.Subject = uuid.NewString()
	e.claims = c
}

func (e *hardDeleteEnv) asPlatformOwner() { e.setClaims(true) }

// pendingReceipt opens one draft receipt awaiting approval on a fresh sent PO.
func (e *hardDeleteEnv) pendingReceipt() (*ent.PurchaseOrder, *ent.GoodsReceipt, *ent.ApprovalRequest) {
	e.t.Helper()
	po, _ := e.sentPO(4)
	if code, body := e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil); code != http.StatusConflict {
		e.t.Fatalf("receive should open a pending receipt (409), got %d %v", code, body)
	}
	g := e.receipts(po.ID, entgr.StatusDraft)[0]
	return po, g, e.requestFor(g.ID)
}

func (e *hardDeleteEnv) requestExists(id uuid.UUID) bool {
	ok, _ := e.client.ApprovalRequest.Query().Where(areq.ID(id)).Exist(context.Background())
	return ok
}

func (e *hardDeleteEnv) receiptExists(id uuid.UUID) bool {
	ok, _ := e.client.GoodsReceipt.Query().Where(entgr.ID(id)).Exist(context.Background())
	return ok
}

// ─── Approval request hard delete ────────────────────────────────────────────

func TestHardDeleteApproval_PlatformOwnerDeletesRequestAndActions(t *testing.T) {
	e := newHardDeleteEnv(t)
	_, g, req := e.pendingReceipt()
	ctx := context.Background()

	code, body := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String(), nil)
	if code != http.StatusOK || body["deleted"] != true {
		t.Fatalf("platform owner delete: %d %v", code, body)
	}
	if e.requestExists(req.ID) {
		t.Fatal("request row must be gone")
	}
	if n := e.client.ApprovalAction.Query().Where(entact.RequestID(req.ID)).CountX(ctx); n != 0 {
		t.Fatalf("actions must be gone, %d left", n)
	}
	// Without with_object the draft receipt stays.
	if !e.receiptExists(g.ID) {
		t.Fatal("the receipt must survive without with_object=true")
	}
	logRow, err := e.client.AuditLog.Query().Where(auditlog.TenantID(e.tenant), auditlog.Action("approval.hard_deleted")).Only(ctx)
	if err != nil {
		t.Fatalf("one audit row expected: %v", err)
	}
	if logRow.EntityID != req.ID.String() || logRow.EntityType != "approval_request" || logRow.BeforeJSON["status"] != "pending" {
		t.Fatalf("audit row must snapshot the request, got %+v", logRow)
	}
	if logRow.ActorUserID.String() != e.claims.Subject {
		t.Fatalf("audit actor = %s, want caller %s", logRow.ActorUserID, e.claims.Subject)
	}
}

func TestHardDeleteApproval_ForbiddenWithoutPlatformOwnerClaim(t *testing.T) {
	e := newHardDeleteEnv(t)
	_, _, req := e.pendingReceipt()
	for _, roles := range [][]string{{"admin"}, {"superuser"}, {"inventory_admin", "admin", "superuser"}} {
		e.setClaims(false, roles...)
		if code, body := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String(), nil); code != http.StatusForbidden {
			t.Fatalf("roles %v: want 403, got %d %v", roles, code, body)
		}
	}
	if !e.requestExists(req.ID) {
		t.Fatal("a refused delete must leave the request")
	}
}

func TestHardDeleteApproval_ExecutedRequestIsRefused(t *testing.T) {
	e := newHardDeleteEnv(t)
	_, _, req := e.pendingReceipt()
	if code, body := e.call(http.MethodPost, "/inventory/approval-requests/"+req.ID.String()+"/approve", map[string]string{}); code != http.StatusOK {
		t.Fatalf("approve: %d %v", code, body)
	}
	code, body := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String()+"?with_object=true", nil)
	if code != http.StatusConflict {
		t.Fatalf("executed request: want 409, got %d %v", code, body)
	}
	if !e.requestExists(req.ID) {
		t.Fatal("executed request must stay")
	}
}

// A request whose receipt was posted is history even without executed_at (legacy rows), so it
// stays.
func TestHardDeleteApproval_RefusedWhenReceiptPosted(t *testing.T) {
	e := newHardDeleteEnv(t)
	_, g, req := e.pendingReceipt()
	ctx := context.Background()
	e.client.GoodsReceipt.UpdateOneID(g.ID).SetStatus(entgr.StatusPosted).ExecX(ctx)
	if code, body := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String(), nil); code != http.StatusConflict {
		t.Fatalf("posted receipt: want 409, got %d %v", code, body)
	}
}

func TestHardDeleteApproval_WithObjectDeletesDraftReceipt(t *testing.T) {
	e := newHardDeleteEnv(t)
	_, g, req := e.pendingReceipt()
	ctx := context.Background()
	code, body := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String()+"?with_object=true", nil)
	if code != http.StatusOK || body["object_deleted"] != true {
		t.Fatalf("with_object delete: %d %v", code, body)
	}
	if e.receiptExists(g.ID) {
		t.Fatal("draft receipt must be deleted")
	}
	if n := e.client.GoodsReceiptLine.Query().Where(entgrl.GoodsReceiptID(g.ID)).CountX(ctx); n != 0 {
		t.Fatalf("receipt lines must be deleted, %d left", n)
	}
	if e.requestExists(req.ID) {
		t.Fatal("request must be deleted")
	}
}

func TestHardDeleteApproval_WithObjectRefusesNonDraftReceipt(t *testing.T) {
	e := newHardDeleteEnv(t)
	_, g, req := e.pendingReceipt()
	ctx := context.Background()
	e.client.GoodsReceipt.UpdateOneID(g.ID).SetStatus(entgr.StatusCancelled).ExecX(ctx)
	code, body := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String()+"?with_object=true", nil)
	if code != http.StatusConflict || body["code"] != "RECEIPT_NOT_DRAFT" {
		t.Fatalf("non-draft receipt with_object: want 409 RECEIPT_NOT_DRAFT, got %d %v", code, body)
	}
	if !e.receiptExists(g.ID) || !e.requestExists(req.ID) {
		t.Fatal("nothing may be deleted when the request is refused")
	}
}

// ─── Purchase order hard delete ──────────────────────────────────────────────

func TestHardDeletePO_PlatformOwnerDeletesOrderReceiptsAndApprovals(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, g, req := e.pendingReceipt()
	ctx := context.Background()
	poReq := e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModulePurchaseOrder).
		SetObjectID(po.ID).SetAmount(400).SetStatus(areq.StatusApproved).SetDecidedAt(time.Now()).SaveX(ctx)

	code, body := e.call(http.MethodDelete, "/inventory/purchase-orders/"+po.ID.String()+"?hard=true", nil)
	if code != http.StatusOK || body["deleted"] != true {
		t.Fatalf("platform owner PO delete: %d %v", code, body)
	}
	if ok, _ := e.client.PurchaseOrder.Query().Where(entpo.ID(po.ID)).Exist(ctx); ok {
		t.Fatal("PO must be gone")
	}
	if n := e.client.PurchaseOrderLine.Query().Where(entpol.PoID(po.ID)).CountX(ctx); n != 0 {
		t.Fatalf("PO lines must be gone, %d left", n)
	}
	if e.receiptExists(g.ID) || e.requestExists(req.ID) || e.requestExists(poReq.ID) {
		t.Fatal("draft receipt and every approval request must be gone")
	}
	if n := e.client.AuditLog.Query().Where(auditlog.TenantID(e.tenant), auditlog.Action("purchase_order.hard_deleted"), auditlog.EntityID(po.ID.String())).CountX(ctx); n != 1 {
		t.Fatalf("want one audit row, got %d", n)
	}
	// Treasury releases any budget commitment through the usual cancelled event.
	if n := e.client.OutboxEvent.Query().Where(outboxevent.TenantID(e.tenant), outboxevent.EventType("purchase_order.cancelled"), outboxevent.AggregateID(po.ID.String())).CountX(ctx); n != 1 {
		t.Fatalf("want one purchase_order.cancelled outbox event, got %d", n)
	}
}

func TestHardDeletePO_ForbiddenWithoutPlatformOwnerClaim(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, _ := e.sentPO(2)
	for _, roles := range [][]string{{"admin"}, {"superuser"}} {
		e.setClaims(false, roles...)
		if code, body := e.call(http.MethodDelete, "/inventory/purchase-orders/"+po.ID.String()+"?hard=true", nil); code != http.StatusForbidden {
			t.Fatalf("roles %v: want 403, got %d %v", roles, code, body)
		}
	}
	if ok, _ := e.client.PurchaseOrder.Query().Where(entpo.ID(po.ID)).Exist(context.Background()); !ok {
		t.Fatal("a refused delete must leave the PO")
	}
}

func TestHardDeletePO_RefusedWhenAReceiptIsPosted(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, g, _ := e.pendingReceipt()
	e.client.GoodsReceipt.UpdateOneID(g.ID).SetStatus(entgr.StatusPosted).ExecX(context.Background())
	code, body := e.call(http.MethodDelete, "/inventory/purchase-orders/"+po.ID.String()+"?hard=true", nil)
	if code != http.StatusConflict || body["code"] != "RECEIPT_POSTED" {
		t.Fatalf("posted receipt: want 409 RECEIPT_POSTED, got %d %v", code, body)
	}
	if ok, _ := e.client.PurchaseOrder.Query().Where(entpo.ID(po.ID)).Exist(context.Background()); !ok {
		t.Fatal("PO must stay")
	}
}

func TestHardDeletePO_RefusedWithPurchaseReturn(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, _ := e.sentPO(1)
	e.client.PurchaseReturn.Create().SetTenantID(e.tenant).SetPurchaseOrderID(po.ID).SetSupplierID(e.supplier).
		SetReturnNumber("PR-1").SaveX(context.Background())
	code, body := e.call(http.MethodDelete, "/inventory/purchase-orders/"+po.ID.String()+"?hard=true", nil)
	if code != http.StatusConflict || body["code"] != "HAS_PURCHASE_RETURN" {
		t.Fatalf("PO with a return: want 409 HAS_PURCHASE_RETURN, got %d %v", code, body)
	}
}

func TestHardDeletePO_RequiresHardFlag(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, _ := e.sentPO(1)
	if code, _ := e.call(http.MethodDelete, "/inventory/purchase-orders/"+po.ID.String(), nil); code != http.StatusBadRequest {
		t.Fatalf("without hard=true: want 400, got %d", code)
	}
}
