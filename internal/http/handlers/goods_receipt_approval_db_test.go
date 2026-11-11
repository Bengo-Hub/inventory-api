package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	arule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// grnApprovalEnv reproduces the gram-auto-spares setup: one tenant with a goods_receipt approval
// rule ("All P.Os", min 0, one admin step), a supplier, a warehouse and a sent purchase order.
type grnApprovalEnv struct {
	t        *testing.T
	client   *ent.Client
	tenant   uuid.UUID
	wh       uuid.UUID
	supplier uuid.UUID
	router   chi.Router
}

func newGRNApprovalEnv(t *testing.T, withRule bool) *grnApprovalEnv {
	t.Helper()
	client := testutil.SchemaClient(t, "inv_grnappr_")
	ctx := context.Background()
	tenant := uuid.New()
	client.Tenant.Create().SetID(tenant).SetName("Gram Test").SetSlug("gram-" + tenant.String()).SaveX(ctx)
	wh := client.Warehouse.Create().SetTenantID(tenant).SetName("Main Store").SetCode("MAIN").SetIsDefault(true).SaveX(ctx)
	sup := client.Supplier.Create().SetTenantID(tenant).SetName("LUGIC AUTO").SetCode("LUGIC").SaveX(ctx)
	if withRule {
		rule := client.ApprovalRule.Create().SetTenantID(tenant).SetModule(arule.ModuleGoodsReceipt).
			SetName("All P.Os").SetMinAmount(0).SaveX(ctx)
		client.ApprovalStep.Create().SetTenantID(tenant).SetRuleID(rule.ID).SetSequence(1).
			SetName("Admin").SetApproverRole("admin").SaveX(ctx)
	}

	h := &InventoryExtrasHandler{log: zap.NewNop(), orm: client, stockSvc: stock.NewService(client, zap.NewNop())}
	user := uuid.New()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			claims := &authclient.Claims{IsPlatformOwner: true}
			claims.Subject = user.String()
			c := context.WithValue(req.Context(), httpware.TenantIDKey, tenant.String())
			c = authclient.ContextWithClaims(c, claims)
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	pass := func(string) func(http.Handler) http.Handler { return func(n http.Handler) http.Handler { return n } }
	h.registerGoodsReceiptRoutes(r, pass, "add", "change")
	h.registerApprovalRoutes(r, pass)
	r.Get("/inventory/purchase-orders/{poID}", h.GetPurchaseOrder)
	r.Put("/inventory/purchase-orders/{poID}/receive", h.ReceivePurchaseOrder)
	r.Put("/inventory/purchase-orders/{poID}/amend", h.AmendPurchaseOrder)
	r.Put("/inventory/purchase-orders/{poID}/cancel", h.CancelPurchaseOrder)
	return &grnApprovalEnv{t: t, client: client, tenant: tenant, wh: wh.ID, supplier: sup.ID, router: r}
}

// sentPO creates a sent purchase order with one line of qty units at unit cost 100.
func (e *grnApprovalEnv) sentPO(qty float64) (*ent.PurchaseOrder, *ent.Item) {
	e.t.Helper()
	ctx := context.Background()
	it := e.client.Item.Create().SetTenantID(e.tenant).SetSku("BRK-" + uuid.NewString()[:6]).SetName("Brake pad").SetCostPrice(100).SaveX(ctx)
	po := e.client.PurchaseOrder.Create().SetTenantID(e.tenant).SetPoNumber("000021").
		SetSupplierID(e.supplier).SetWarehouseID(e.wh).SetStatus(entpo.StatusSent).SetTotalAmount(qty * 100).SaveX(ctx)
	e.client.PurchaseOrderLine.Create().SetPoID(po.ID).SetItemID(it.ID).SetQuantityOrdered(qty).
		SetUnitPrice(100).SetTotalPrice(qty * 100).SaveX(ctx)
	return po, it
}

func (e *grnApprovalEnv) call(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, httptest.NewRequest(method, path, &buf))
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (e *grnApprovalEnv) onHand(it *ent.Item) float64 {
	e.t.Helper()
	rows := e.client.InventoryBalance.Query().
		Where(inventorybalance.TenantID(e.tenant), inventorybalance.ItemID(it.ID)).AllX(context.Background())
	total := 0.0
	for _, b := range rows {
		total += b.OnHand
	}
	return total
}

func (e *grnApprovalEnv) receipts(poID uuid.UUID, status entgr.Status) []*ent.GoodsReceipt {
	e.t.Helper()
	return e.client.GoodsReceipt.Query().
		Where(entgr.TenantID(e.tenant), entgr.PurchaseOrderID(poID), entgr.StatusEQ(status)).AllX(context.Background())
}

func (e *grnApprovalEnv) requestFor(objectID uuid.UUID) *ent.ApprovalRequest {
	e.t.Helper()
	req, err := e.client.ApprovalRequest.Query().
		Where(areq.TenantID(e.tenant), areq.ObjectID(objectID)).
		Order(ent.Desc(areq.FieldCreatedAt)).First(context.Background())
	if err != nil {
		e.t.Fatalf("no approval request for %s: %v", objectID, err)
	}
	return req
}

func (e *grnApprovalEnv) poStatus(poID uuid.UUID) string {
	return e.client.PurchaseOrder.GetX(context.Background(), poID).Status.String()
}

// The reported bug: Mark Received on a PO gated by a goods_receipt rule must open ONE draft
// receipt; clicking it again while that receipt awaits approval must not open another.
func TestReceivePO_ReusesOpenReceiptWhileApprovalPending(t *testing.T) {
	e := newGRNApprovalEnv(t, true)
	po, it := e.sentPO(4)

	for i := 0; i < 4; i++ {
		code, body := e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil)
		if code != http.StatusConflict {
			t.Fatalf("click %d: want 409 while approval is outstanding, got %d %v", i+1, code, body)
		}
	}
	drafts := e.receipts(po.ID, entgr.StatusDraft)
	if len(drafts) != 1 {
		t.Fatalf("four clicks must leave exactly one draft receipt, got %d", len(drafts))
	}
	if n := e.client.ApprovalRequest.Query().Where(areq.TenantID(e.tenant), areq.ObjectID(drafts[0].ID), areq.StatusEQ(areq.StatusPending)).CountX(context.Background()); n != 1 {
		t.Fatalf("want one pending approval request for the receipt, got %d", n)
	}
	if e.onHand(it) != 0 {
		t.Fatalf("nothing may be stocked before approval, on hand = %v", e.onHand(it))
	}

	// The PO detail tells the UI a receipt is waiting for approval.
	_, detail := e.call(http.MethodGet, "/inventory/purchase-orders/"+po.ID.String(), nil)
	open, _ := detail["open_receipt"].(map[string]any)
	if open == nil || open["approval_status"] != "pending" || open["grn_number"] != drafts[0].GrnNumber {
		t.Fatalf("detail must expose the pending receipt, got %v", detail["open_receipt"])
	}
}

// Approving the goods_receipt request must post the receipt (stock in, PO received) exactly once
// and stamp executed_at.
func TestApproveGoodsReceipt_PostsExactlyOnce(t *testing.T) {
	e := newGRNApprovalEnv(t, true)
	po, it := e.sentPO(4)
	e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil)
	g := e.receipts(po.ID, entgr.StatusDraft)[0]
	req := e.requestFor(g.ID)

	code, body := e.call(http.MethodPost, "/inventory/approval-requests/"+req.ID.String()+"/approve", map[string]string{})
	if code != http.StatusOK {
		t.Fatalf("approve: %d %v", code, body)
	}
	if got := e.client.GoodsReceipt.GetX(context.Background(), g.ID).Status; got != entgr.StatusPosted {
		t.Fatalf("approved receipt must be posted, status = %s", got)
	}
	if e.onHand(it) != 4 {
		t.Fatalf("on hand after approval = %v, want 4", e.onHand(it))
	}
	if e.poStatus(po.ID) != "received" {
		t.Fatalf("po status = %s, want received", e.poStatus(po.ID))
	}
	if e.requestFor(g.ID).ExecutedAt == nil {
		t.Fatal("executed_at must be set once the approved receipt posts")
	}

	// A second approve is refused and a later Mark Received cannot stock again.
	if code, _ := e.call(http.MethodPost, "/inventory/approval-requests/"+req.ID.String()+"/approve", map[string]string{}); code != http.StatusConflict {
		t.Fatalf("second approve: want 409, got %d", code)
	}
	e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil)
	if e.onHand(it) != 4 {
		t.Fatalf("stock doubled: on hand = %v", e.onHand(it))
	}
}

// Rejecting the goods_receipt request cancels the draft and leaves the PO receivable again.
func TestRejectGoodsReceipt_CancelsDraftAndPOStaysReceivable(t *testing.T) {
	e := newGRNApprovalEnv(t, true)
	po, it := e.sentPO(2)
	e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil)
	g := e.receipts(po.ID, entgr.StatusDraft)[0]
	req := e.requestFor(g.ID)

	if code, body := e.call(http.MethodPost, "/inventory/approval-requests/"+req.ID.String()+"/reject", map[string]string{"comment": "wrong qty"}); code != http.StatusOK {
		t.Fatalf("reject: %d %v", code, body)
	}
	if got := e.client.GoodsReceipt.GetX(context.Background(), g.ID).Status; got != entgr.StatusCancelled {
		t.Fatalf("rejected receipt must be cancelled, status = %s", got)
	}
	if e.poStatus(po.ID) != "sent" || e.onHand(it) != 0 {
		t.Fatalf("po must stay sent with no stock, got %s / %v", e.poStatus(po.ID), e.onHand(it))
	}
	// Receiving again opens a fresh receipt with a fresh approval request.
	if code, _ := e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil); code != http.StatusConflict {
		t.Fatalf("re-receive should submit a new approval (409), got %d", code)
	}
	drafts := e.receipts(po.ID, entgr.StatusDraft)
	if len(drafts) != 1 || drafts[0].ID == g.ID {
		t.Fatalf("want one new draft receipt, got %d", len(drafts))
	}
}

// Legacy data: a draft whose approval was rejected before rejects cancelled drafts is treated as
// dead on the next receive, and several approved drafts are refused rather than all posted.
func TestReceivePO_LegacyDuplicateDrafts(t *testing.T) {
	e := newGRNApprovalEnv(t, true)
	po, it := e.sentPO(4)
	ctx := context.Background()
	mk := func(n string, status areq.Status) *ent.GoodsReceipt {
		g := e.client.GoodsReceipt.Create().SetTenantID(e.tenant).SetGrnNumber(n).SetPurchaseOrderID(po.ID).
			SetSupplierID(e.supplier).SetWarehouseID(e.wh).SaveX(ctx)
		e.client.GoodsReceiptLine.Create().SetTenantID(e.tenant).SetGoodsReceiptID(g.ID).SetItemID(it.ID).
			SetQuantityReceived(4).SetQuantityAccepted(4).SetUnitCost(100).SaveX(ctx)
		e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModuleGoodsReceipt).
			SetObjectID(g.ID).SetObjectReference(n).SetAmount(400).SetStatus(status).SaveX(ctx)
		return g
	}
	g19 := mk("000019", areq.StatusApproved)
	g20 := mk("000020", areq.StatusApproved)
	g22 := mk("000022", areq.StatusRejected)

	code, body := e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil)
	if code != http.StatusConflict || body["code"] != "MULTIPLE_OPEN_RECEIPTS" {
		t.Fatalf("two approved drafts must be refused, got %d %v", code, body)
	}
	if e.client.GoodsReceipt.GetX(ctx, g22.ID).Status != entgr.StatusCancelled {
		t.Fatal("the rejected draft must be cancelled")
	}
	if e.onHand(it) != 0 {
		t.Fatal("nothing may post while duplicates are unresolved")
	}

	// Repair: cancel the duplicate, then Mark Received posts the surviving approved draft.
	if code, body := e.call(http.MethodPost, "/inventory/goods-receipts/"+g20.ID.String()+"/cancel", nil); code != http.StatusOK {
		t.Fatalf("cancel duplicate: %d %v", code, body)
	}
	if code, body := e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil); code != http.StatusOK {
		t.Fatalf("receive after repair: %d %v", code, body)
	}
	if e.client.GoodsReceipt.GetX(ctx, g19.ID).Status != entgr.StatusPosted || e.onHand(it) != 4 {
		t.Fatalf("surviving receipt must post once, on hand = %v", e.onHand(it))
	}
	if e.requestFor(g19.ID).ExecutedAt == nil {
		t.Fatal("executed_at must be stamped when the approved receipt posts")
	}
	// The cancelled duplicate can never be posted afterwards.
	if code, _ := e.call(http.MethodPost, "/inventory/goods-receipts/"+g20.ID.String()+"/post", nil); code == http.StatusOK {
		t.Fatal("a cancelled receipt must not post")
	}
	if e.onHand(it) != 4 {
		t.Fatalf("stock doubled: %v", e.onHand(it))
	}
}

// A draft receipt for an order that has since been fully received must not post.
func TestPostGoodsReceipt_RefusesClosedOrder(t *testing.T) {
	e := newGRNApprovalEnv(t, false)
	po, it := e.sentPO(1)
	ctx := context.Background()
	g := e.client.GoodsReceipt.Create().SetTenantID(e.tenant).SetGrnNumber("GRN-X").SetPurchaseOrderID(po.ID).
		SetWarehouseID(e.wh).SaveX(ctx)
	e.client.GoodsReceiptLine.Create().SetTenantID(e.tenant).SetGoodsReceiptID(g.ID).SetItemID(it.ID).
		SetQuantityReceived(1).SetQuantityAccepted(1).SetUnitCost(100).SaveX(ctx)
	e.client.PurchaseOrder.UpdateOneID(po.ID).SetStatus(entpo.StatusReceived).ExecX(ctx)

	if code, _ := e.call(http.MethodPost, "/inventory/goods-receipts/"+g.ID.String()+"/post", nil); code != http.StatusConflict {
		t.Fatalf("want 409 posting against a received order, got %d", code)
	}
	if e.onHand(it) != 0 {
		t.Fatal("no stock may move")
	}
}

// Without a rule nothing changes: Mark Received posts straight away.
func TestReceivePO_NoRulePostsImmediately(t *testing.T) {
	e := newGRNApprovalEnv(t, false)
	po, it := e.sentPO(3)
	if code, body := e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil); code != http.StatusOK {
		t.Fatalf("receive: %d %v", code, body)
	}
	if e.onHand(it) != 3 || e.poStatus(po.ID) != "received" {
		t.Fatalf("on hand %v, status %s", e.onHand(it), e.poStatus(po.ID))
	}
}

// Amend and cancel are blocked while a receipt is open, and a sent PO whose total changes goes
// back to draft so approval and the budget check run again on re-send.
func TestAmendPO_Rules(t *testing.T) {
	e := newGRNApprovalEnv(t, true)
	po, it := e.sentPO(4)
	amend := map[string]any{
		"supplier_id": e.supplier, "warehouse_id": e.wh,
		"line_items": []map[string]any{{"item_id": it.ID, "quantity": 5, "unit_cost": 100}},
	}

	e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/receive", nil)
	if code, body := e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/amend", amend); code != http.StatusConflict || body["code"] != "RECEIPT_OPEN" {
		t.Fatalf("amend with an open receipt: want 409 RECEIPT_OPEN, got %d %v", code, body)
	}
	if code, body := e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/cancel", nil); code != http.StatusConflict || body["code"] != "RECEIPT_OPEN" {
		t.Fatalf("cancel with an open receipt: want 409 RECEIPT_OPEN, got %d %v", code, body)
	}

	// Cancel the receipt; now the amendment goes through and returns the order to draft.
	g := e.receipts(po.ID, entgr.StatusDraft)[0]
	e.call(http.MethodPost, "/inventory/goods-receipts/"+g.ID.String()+"/cancel", nil)
	code, body := e.call(http.MethodPut, "/inventory/purchase-orders/"+po.ID.String()+"/amend", amend)
	if code != http.StatusOK || body["status"] != "draft" || body["returned_to_draft"] != true {
		t.Fatalf("amend of a sent PO with a new total must return it to draft, got %d %v", code, body)
	}
	got := e.client.PurchaseOrder.GetX(context.Background(), po.ID)
	if got.TotalAmount != 500 || got.Status != entpo.StatusDraft {
		t.Fatalf("po after amend: total %v status %s", got.TotalAmount, got.Status)
	}
}
