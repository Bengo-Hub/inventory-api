package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	arule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	entasset "github.com/bengobox/inventory-service/internal/ent/asset"
	entdisp "github.com/bengobox/inventory-service/internal/ent/assetdisposal"
	entmaint "github.com/bengobox/inventory-service/internal/ent/assetmaintenance"
	enttrf "github.com/bengobox/inventory-service/internal/ent/assettransfer"
	"github.com/bengobox/inventory-service/internal/ent/auditlog"
	entcontract "github.com/bengobox/inventory-service/internal/ent/contract"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	"github.com/bengobox/inventory-service/internal/ent/outboxevent"
	entpb "github.com/bengobox/inventory-service/internal/ent/productionbatch"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpr "github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	entrfq "github.com/bengobox/inventory-service/internal/ent/rfq"
	entrfqaward "github.com/bengobox/inventory-service/internal/ent/rfqaward"
	entcount "github.com/bengobox/inventory-service/internal/ent/stockcount"
	"github.com/bengobox/inventory-service/internal/ent/stocktransfer"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/modules/transfers"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// gatedEnv is one tenant whose router carries every approval-gated action plus the generic
// approval endpoints, all backed by the real stock and transfer services. Rules are added per test
// with rule(module).
type gatedEnv struct {
	t      *testing.T
	client *ent.Client
	tenant uuid.UUID
	wh     uuid.UUID
	wh2    uuid.UUID
	sup    uuid.UUID
	router chi.Router
	user   uuid.UUID // the caller every request runs as
}

func newGatedEnv(t *testing.T) *gatedEnv {
	t.Helper()
	client := testutil.SchemaClient(t, "inv_gated_")
	ctx := context.Background()
	tenant := uuid.New()
	client.Tenant.Create().SetID(tenant).SetName("Gated Test").SetSlug("gated-" + tenant.String()).SaveX(ctx)
	wh := client.Warehouse.Create().SetTenantID(tenant).SetName("Main Store").SetCode("MAIN").SetIsDefault(true).SaveX(ctx)
	wh2 := client.Warehouse.Create().SetTenantID(tenant).SetName("Branch").SetCode("BR1").SaveX(ctx)
	sup := client.Supplier.Create().SetTenantID(tenant).SetName("ACME SUPPLIES").SetCode("ACME").SaveX(ctx)

	stockSvc := stock.NewService(client, zap.NewNop())
	transferSvc := transfers.NewService(client, zap.NewNop())
	h := &InventoryExtrasHandler{log: zap.NewNop(), orm: client, stockSvc: stockSvc}
	h.SetTransferService(transferSvc)
	th := NewTransferHandler(zap.NewNop(), transferSvc, nil, approvals.NewService(client))
	sc := NewStockCountHandler(zap.NewNop(), client, stockSvc, nil, nil)
	sc.SetApprovalService(approvals.NewService(client))

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
	h.registerApprovalRoutes(r, pass)
	h.registerPurchaseReturnRoutes(r, pass, "add", "change")
	r.Post("/inventory/contracts/{contractID}/activate", h.ActivateContract)
	r.Post("/inventory/rfqs/{rfqID}/award", h.AwardRFQ)
	r.Post("/inventory/production-batches/{batchID}/start", h.StartProductionBatch)
	r.Post("/inventory/asset-maintenance/{recID}/complete", h.CompleteAssetMaintenance)
	r.Post("/inventory/asset-transfers/{recID}/complete", h.CompleteAssetTransfer)
	r.Post("/inventory/asset-disposals/{recID}/complete", h.CompleteAssetDisposal)
	r.Put("/inventory/purchase-orders/{poID}/send", h.SendPurchaseOrder)
	r.Put("/inventory/purchase-orders/{poID}/amend", h.AmendPurchaseOrder)
	r.Post("/inventory/purchase-orders/{poID}/submit-for-approval", h.SubmitPurchaseOrderForApproval)
	r.Put("/inventory/transfers/{transferId}", th.UpdateTransfer)
	r.Post("/inventory/transfers/{transferId}/ship", th.ShipTransfer)
	r.Post("/inventory/stock-counts/{id}/approve", sc.Approve)
	r.Post("/inventory/stock-counts/{id}/cancel", sc.Cancel)
	r.Post("/inventory/stock-counts/{id}/lines", sc.UpsertLine)
	return &gatedEnv{t: t, client: client, tenant: tenant, wh: wh.ID, wh2: wh2.ID, sup: sup.ID, router: r, user: user}
}

// rule adds a single-step, any-amount approval rule for a module.
func (e *gatedEnv) rule(module arule.Module) {
	e.t.Helper()
	ctx := context.Background()
	ru := e.client.ApprovalRule.Create().SetTenantID(e.tenant).SetModule(module).
		SetName("All " + string(module)).SetMinAmount(0).SaveX(ctx)
	e.client.ApprovalStep.Create().SetTenantID(e.tenant).SetRuleID(ru.ID).SetSequence(1).
		SetName("Admin").SetApproverRole("admin").SaveX(ctx)
}

func (e *gatedEnv) call(method, path string, body any) (int, map[string]any) {
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

// item creates a stocked item at the main warehouse.
func (e *gatedEnv) item(sku string, onHand float64) *ent.Item {
	e.t.Helper()
	ctx := context.Background()
	it := e.client.Item.Create().SetTenantID(e.tenant).SetSku(sku).SetName("Item " + sku).SetCostPrice(100).SaveX(ctx)
	e.client.InventoryBalance.Create().SetTenantID(e.tenant).SetItemID(it.ID).SetWarehouseID(e.wh).
		SetOnHand(onHand).SetAvailable(onHand).SaveX(ctx)
	if onHand > 0 {
		e.client.InventoryLot.Create().SetTenantID(e.tenant).SetItemID(it.ID).SetWarehouseID(e.wh).
			SetLotNumber("OPEN-" + sku).SetQuantity(onHand).SetStatus("active").SetIsCostLayer(true).
			SetCostPrice(100).SetReceivedAt(time.Now().Add(-48 * time.Hour)).SaveX(ctx)
	}
	return it
}

func (e *gatedEnv) onHandAt(it *ent.Item, wh uuid.UUID) float64 {
	e.t.Helper()
	b, err := e.client.InventoryBalance.Query().
		Where(inventorybalance.TenantID(e.tenant), inventorybalance.ItemID(it.ID), inventorybalance.WarehouseID(wh)).
		Only(context.Background())
	if ent.IsNotFound(err) {
		return 0
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return b.OnHand
}

// requests lists the module's requests for an object, oldest first.
func (e *gatedEnv) requests(module areq.Module, objectID uuid.UUID) []*ent.ApprovalRequest {
	e.t.Helper()
	return e.client.ApprovalRequest.Query().
		Where(areq.TenantID(e.tenant), areq.ModuleEQ(module), areq.ObjectID(objectID)).
		Order(ent.Asc(areq.FieldCreatedAt)).AllX(context.Background())
}

func (e *gatedEnv) lastRequest(module areq.Module, objectID uuid.UUID) *ent.ApprovalRequest {
	e.t.Helper()
	rows := e.requests(module, objectID)
	if len(rows) == 0 {
		e.t.Fatalf("no %s approval request for %s", module, objectID)
	}
	return rows[len(rows)-1]
}

// decide approves or rejects a request through the generic endpoint, as an approver would.
func (e *gatedEnv) decide(req *ent.ApprovalRequest, decision string) map[string]any {
	e.t.Helper()
	code, body := e.call(http.MethodPost, "/inventory/approval-requests/"+req.ID.String()+"/"+decision, map[string]any{"comment": "test"})
	if code != http.StatusOK {
		e.t.Fatalf("%s request = %d %v", decision, code, body)
	}
	return body
}

func (e *gatedEnv) events(eventType string) int {
	e.t.Helper()
	return e.client.OutboxEvent.Query().
		Where(outboxevent.TenantID(e.tenant), outboxevent.EventTypeEQ(eventType)).CountX(context.Background())
}

func (e *gatedEnv) executedAt(req *ent.ApprovalRequest) *time.Time {
	e.t.Helper()
	return e.client.ApprovalRequest.GetX(context.Background(), req.ID).ExecutedAt
}

// expectGated asserts a 409 approval answer with the given state.
func expectGated(t *testing.T, code int, body map[string]any, state string) {
	t.Helper()
	if code != http.StatusConflict || body["approval_required"] != true || body["state"] != state {
		t.Fatalf("want 409 approval_required state=%s, got %d %v", state, code, body)
	}
}

// Purchase return: rejected, fixed (pressed again) and resubmitted, then approved. The approval
// takes the goods out exactly once, and pressing Approve afterwards changes nothing.
func TestPurchaseReturn_RejectResubmitApproveExecutesOnce(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModulePurchaseReturn)
	ctx := context.Background()
	phone := e.item("PR-PH", 10)
	pr := e.client.PurchaseReturn.Create().SetTenantID(e.tenant).SetReturnNumber("PR-0001").
		SetSupplierID(e.sup).SetWarehouseID(e.wh).SetReturnAmount(200).SetDateReturned(time.Now()).SaveX(ctx)
	e.client.PurchaseReturnLine.Create().SetTenantID(e.tenant).SetPurchaseReturnID(pr.ID).
		SetItemID(phone.ID).SetQuantity(2).SetSubTotal(200).SaveX(ctx)
	path := "/inventory/purchase-returns/" + pr.ID.String() + "/approve"

	code, body := e.call(http.MethodPost, path, nil)
	expectGated(t, code, body, "submitted")
	e.decide(e.lastRequest(areq.ModulePurchaseReturn, pr.ID), "reject")

	// Fixed and pressed again: a new request, the rejected one kept as history.
	code, body = e.call(http.MethodPost, path, nil)
	expectGated(t, code, body, "resubmitted")
	reqs := e.requests(areq.ModulePurchaseReturn, pr.ID)
	if len(reqs) != 2 || reqs[0].Status != areq.StatusRejected || reqs[1].Status != areq.StatusPending {
		t.Fatalf("want rejected + pending, got %d rows", len(reqs))
	}
	if e.onHandAt(phone, e.wh) != 10 {
		t.Fatal("nothing may move before approval")
	}

	e.decide(reqs[1], "approve")
	if got := e.onHandAt(phone, e.wh); got != 8 {
		t.Fatalf("approval must take 2 out once, on hand %v", got)
	}
	if row := e.client.PurchaseReturn.GetX(ctx, pr.ID); row.PaymentStatus != entpr.PaymentStatusPaid {
		t.Fatalf("return must be approved, got %s", row.PaymentStatus)
	}
	if e.executedAt(reqs[1]) == nil {
		t.Fatal("the approval must be marked executed")
	}
	if n := e.events("purchase_return.approved"); n != 1 {
		t.Fatalf("want one credit event, got %d", n)
	}

	code, body = e.call(http.MethodPost, path, nil)
	if code != http.StatusConflict || body["code"] != "ALREADY_APPROVED" {
		t.Fatalf("second approve = %d %v, want 409 ALREADY_APPROVED", code, body)
	}
	if e.onHandAt(phone, e.wh) != 8 || len(e.requests(areq.ModulePurchaseReturn, pr.ID)) != 2 {
		t.Fatal("pressing again after execution must not move stock or open a request")
	}
}

// Stock transfer: a rejected transfer is amended and resubmitted; approval ships it once.
func TestStockTransfer_RejectAmendResubmitApproveShipsOnce(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockTransfer)
	ctx := context.Background()
	box := e.item("TR-BOX", 20)
	tr := e.client.StockTransfer.Create().SetTenantID(e.tenant).SetTransferNumber("TR-0001").
		SetSourceWarehouseID(e.wh).SetDestinationWarehouseID(e.wh2).SaveX(ctx)
	e.client.StockTransferLine.Create().SetTransferID(tr.ID).SetItemID(box.ID).SetQuantity(5).SaveX(ctx)
	ship := "/inventory/transfers/" + tr.ID.String() + "/ship"

	code, body := e.call(http.MethodPost, ship, nil)
	expectGated(t, code, body, "submitted")
	e.decide(e.lastRequest(areq.ModuleStockTransfer, tr.ID), "reject")

	// A rejected transfer is still a draft and can be amended.
	code, body = e.call(http.MethodPut, "/inventory/transfers/"+tr.ID.String(), map[string]any{
		"items": []map[string]any{{"item_id": box.ID.String(), "quantity": 4}},
	})
	if code != http.StatusOK {
		t.Fatalf("amend after rejection = %d %v", code, body)
	}
	code, body = e.call(http.MethodPost, ship, nil)
	expectGated(t, code, body, "resubmitted")

	e.decide(e.lastRequest(areq.ModuleStockTransfer, tr.ID), "approve")
	if got := e.client.StockTransfer.GetX(ctx, tr.ID).Status; got != stocktransfer.StatusInTransit {
		t.Fatalf("approval must ship the transfer, status %s", got)
	}
	if got := e.onHandAt(box, e.wh); got != 16 {
		t.Fatalf("the amended 4 must leave the source once, on hand %v", got)
	}
	if e.executedAt(e.lastRequest(areq.ModuleStockTransfer, tr.ID)) == nil {
		t.Fatal("the approval must be marked executed")
	}
	// Ship pressed again: refused, no second deduction, no new request.
	if code, _ := e.call(http.MethodPost, ship, nil); code == http.StatusOK {
		t.Fatal("a shipped transfer must not ship again")
	}
	if e.onHandAt(box, e.wh) != 16 || len(e.requests(areq.ModuleStockTransfer, tr.ID)) != 2 {
		t.Fatal("pressing Ship again must not move stock or open a request")
	}
}

// Amending a transfer whose approval has not run yet withdraws that approval, so the changed
// quantities are signed off again.
func TestStockTransfer_AmendWithdrawsUnexecutedApproval(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockTransfer)
	ctx := context.Background()
	box := e.item("TR-AMD", 20)
	tr := e.client.StockTransfer.Create().SetTenantID(e.tenant).SetTransferNumber("TR-0002").
		SetSourceWarehouseID(e.wh).SetDestinationWarehouseID(e.wh2).SaveX(ctx)
	e.client.StockTransferLine.Create().SetTransferID(tr.ID).SetItemID(box.ID).SetQuantity(5).SaveX(ctx)
	// An approval that never ran (for example the executor failed on a stock shortage).
	req := e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModuleStockTransfer).
		SetObjectID(tr.ID).SetAmount(0).SetStatus(areq.StatusApproved).SetDecidedAt(time.Now()).SaveX(ctx)

	code, body := e.call(http.MethodPut, "/inventory/transfers/"+tr.ID.String(), map[string]any{
		"items": []map[string]any{{"item_id": box.ID.String(), "quantity": 9}},
	})
	if code != http.StatusOK {
		t.Fatalf("amend = %d %v", code, body)
	}
	if got := e.client.ApprovalRequest.GetX(ctx, req.ID).Status; got != areq.StatusCancelled {
		t.Fatalf("the unexecuted approval must be withdrawn, got %s", got)
	}
	code, body = e.call(http.MethodPost, "/inventory/transfers/"+tr.ID.String()+"/ship", nil)
	expectGated(t, code, body, "resubmitted")
	if e.onHandAt(box, e.wh) != 20 {
		t.Fatal("nothing may ship on the withdrawn approval")
	}
}

// Stock count: a rejected count is corrected and resubmitted; approval posts the variance once.
func TestStockCount_RejectResubmitApprovePostsOnce(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	ctx := context.Background()
	soap := e.item("SC-SOAP", 10)
	count := e.client.StockCount.Create().SetTenantID(e.tenant).SetWarehouseID(e.wh).SetStatus(entcount.StatusReview).SaveX(ctx)
	counted, variance := 7.0, -3.0
	line := e.client.StockCountLine.Create().SetTenantID(e.tenant).SetStockCountID(count.ID).SetItemID(soap.ID).
		SetSku(soap.Sku).SetSystemQty(10).SetCountedQty(counted).SetVariance(variance).SaveX(ctx)
	approve := "/inventory/stock-counts/" + count.ID.String() + "/approve"

	if code, _ := e.call(http.MethodPost, approve, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("first approve = %d, want 422 submitted", code)
	}
	e.decide(e.lastRequest(areq.ModuleStockCount, count.ID), "reject")

	// Recount: the line is corrected, then Approve is pressed again.
	e.client.StockCountLine.UpdateOneID(line.ID).SetCountedQty(8).SetVariance(-2).ExecX(ctx)
	code, body := e.call(http.MethodPost, approve, nil)
	if code != http.StatusUnprocessableEntity || body["state"] != "resubmitted" {
		t.Fatalf("approve after rejection = %d %v, want 422 resubmitted", code, body)
	}
	reqs := e.requests(areq.ModuleStockCount, count.ID)
	if len(reqs) != 2 || reqs[0].Status != areq.StatusRejected || reqs[1].Amount != 2 {
		t.Fatalf("want rejected + a new request for the corrected variance, got %d rows", len(reqs))
	}

	e.decide(reqs[1], "approve")
	if got := e.onHandAt(soap, e.wh); got != 8 {
		t.Fatalf("approval must post the corrected variance once, on hand %v", got)
	}
	if got := e.client.StockCount.GetX(ctx, count.ID).Status; got != entcount.StatusApproved {
		t.Fatalf("count must be approved, got %s", got)
	}
	if code, _ := e.call(http.MethodPost, approve, nil); code == http.StatusOK {
		t.Fatal("an approved count must not post again")
	}
	if e.onHandAt(soap, e.wh) != 8 {
		t.Fatal("the variance must post exactly once")
	}
}

// Contract: approval activates it.
func TestContract_ApprovalActivates(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleContract)
	ctx := context.Background()
	c := e.client.Contract.Create().SetTenantID(e.tenant).SetSupplierID(e.sup).SetTitle("Cleaning").
		SetStartDate(time.Now()).SetEndDate(time.Now().AddDate(1, 0, 0)).SetValue(5000).SaveX(ctx)
	path := "/inventory/contracts/" + c.ID.String() + "/activate"

	code, body := e.call(http.MethodPost, path, nil)
	expectGated(t, code, body, "submitted")
	e.decide(e.lastRequest(areq.ModuleContract, c.ID), "approve")
	if got := e.client.Contract.GetX(ctx, c.ID).Status; got != entcontract.StatusActive {
		t.Fatalf("approval must activate the contract, got %s", got)
	}
	if n := e.events("contract.activated"); n != 1 {
		t.Fatalf("want one activation event, got %d", n)
	}
	if code, _ := e.call(http.MethodPost, path, nil); code == http.StatusOK {
		t.Fatal("an active contract must not be activated again")
	}
}

// RFQ award: the selection is stored on the request and applied on approval.
func TestRFQAward_ApprovalAppliesStoredSelection(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleRfq)
	ctx := context.Background()
	rfq := e.client.RFQ.Create().SetTenantID(e.tenant).SetRfqNumber("RFQ-0001").SetStatus(entrfq.StatusSent).SaveX(ctx)
	line := e.client.RFQLine.Create().SetTenantID(e.tenant).SetRfqID(rfq.ID).SetDescription("Gloves").SetQuantity(10).SaveX(ctx)
	path := "/inventory/rfqs/" + rfq.ID.String() + "/award"
	selection := map[string]any{"awards": []map[string]any{{
		"rfq_line_id": line.ID.String(), "supplier_id": e.sup.String(), "unit_price": 30, "quantity": 10,
	}}}

	code, body := e.call(http.MethodPost, path, selection)
	expectGated(t, code, body, "submitted")
	req := e.lastRequest(areq.ModuleRfq, rfq.ID)
	if aw, _ := req.Payload["awards"].([]any); len(aw) != 1 {
		t.Fatalf("the award selection must be stored on the request, got %v", req.Payload)
	}
	e.decide(req, "approve")

	awards := e.client.RFQAward.Query().Where(entrfqaward.RfqID(rfq.ID)).AllX(ctx)
	if len(awards) != 1 || awards[0].SupplierID != e.sup || awards[0].UnitPrice != 30 {
		t.Fatalf("approval must write the stored award, got %v", awards)
	}
	if got := e.client.RFQ.GetX(ctx, rfq.ID).Status; got != entrfq.StatusAwarded {
		t.Fatalf("RFQ must be awarded, got %s", got)
	}
	if e.executedAt(req) == nil {
		t.Fatal("the approval must be marked executed")
	}
}

// Production batch: approval starts it once.
func TestProductionBatch_ApprovalStartsOnce(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleProductionBatch)
	ctx := context.Background()
	recipe := e.client.Recipe.Create().SetTenantID(e.tenant).SetSku("RC-SOAP").SetName("Bar soap").SaveX(ctx)
	b := e.client.ProductionBatch.Create().SetTenantID(e.tenant).SetBatchNumber("PB-0001").SetRecipeID(recipe.ID).
		SetScheduledDate(time.Now()).SetPlannedQuantity(10).SaveX(ctx)
	path := "/inventory/production-batches/" + b.ID.String() + "/start"

	code, body := e.call(http.MethodPost, path, nil)
	expectGated(t, code, body, "submitted")
	e.decide(e.lastRequest(areq.ModuleProductionBatch, b.ID), "approve")
	if got := e.client.ProductionBatch.GetX(ctx, b.ID).Status; got != entpb.StatusInProgress {
		t.Fatalf("approval must start the batch, got %s", got)
	}
	if n := e.events("production.started"); n != 1 {
		t.Fatalf("want one start event, got %d", n)
	}
	if code, _ := e.call(http.MethodPost, path, nil); code == http.StatusOK {
		t.Fatal("a started batch must not start again")
	}
	if n := e.events("production.started"); n != 1 {
		t.Fatalf("still one start event, got %d", n)
	}
}

func (e *gatedEnv) asset(value float64) *ent.Asset {
	e.t.Helper()
	return e.client.Asset.Create().SetTenantID(e.tenant).SetAssetTag("AST-" + uuid.NewString()[:8]).SetName("Van").
		SetCurrentValue(value).SetBookValue(value).
		SaveX(context.Background())
}

// Asset disposal: approval completes it and the disposal event (money to treasury) goes out once.
func TestAssetDisposal_ApprovalCompletesOnce(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleAssetDisposal)
	ctx := context.Background()
	a := e.asset(800)
	d := e.client.AssetDisposal.Create().SetTenantID(e.tenant).SetAssetID(a.ID).SetDisposalDate(time.Now()).
		SetDisposalValue(500).SaveX(ctx)
	path := "/inventory/asset-disposals/" + d.ID.String() + "/complete"

	code, body := e.call(http.MethodPost, path, nil)
	expectGated(t, code, body, "submitted")
	e.decide(e.lastRequest(areq.ModuleAssetDisposal, d.ID), "approve")
	if got := e.client.AssetDisposal.GetX(ctx, d.ID).Status; got != entdisp.StatusCompleted {
		t.Fatalf("approval must complete the disposal, got %s", got)
	}
	if got := e.client.Asset.GetX(ctx, a.ID).Status; got != entasset.StatusDisposed {
		t.Fatalf("asset must be disposed, got %s", got)
	}
	if code, _ := e.call(http.MethodPost, path, nil); code == http.StatusOK {
		t.Fatal("a completed disposal must not complete again")
	}
	if n := e.events("asset.disposed"); n != 1 {
		t.Fatalf("the disposal event must go out once, got %d", n)
	}
}

// Asset maintenance: a rejected job is resubmitted and approval completes it.
func TestAssetMaintenance_RejectResubmitApprove(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleAssetMaintenance)
	ctx := context.Background()
	a := e.asset(800)
	m := e.client.AssetMaintenance.Create().SetTenantID(e.tenant).SetAssetID(a.ID).SetScheduledDate(time.Now()).
		SetCost(120).SaveX(ctx)
	path := "/inventory/asset-maintenance/" + m.ID.String() + "/complete"

	code, body := e.call(http.MethodPost, path, nil)
	expectGated(t, code, body, "submitted")
	e.decide(e.lastRequest(areq.ModuleAssetMaintenance, m.ID), "reject")
	code, body = e.call(http.MethodPost, path, nil)
	expectGated(t, code, body, "resubmitted")
	e.decide(e.lastRequest(areq.ModuleAssetMaintenance, m.ID), "approve")
	if got := e.client.AssetMaintenance.GetX(ctx, m.ID).Status; got != entmaint.StatusCompleted {
		t.Fatalf("approval must complete the maintenance, got %s", got)
	}
}

// When the approved action cannot run (an asset transfer not yet approved by its own workflow),
// the claim is released so the action can be pressed again later, and nothing changes.
func TestAssetTransfer_FailedExecutionReleasesClaim(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleAssetTransfer)
	ctx := context.Background()
	a := e.asset(800)
	tr := e.client.AssetTransfer.Create().SetTenantID(e.tenant).SetAssetID(a.ID).SetToLocation("Branch").
		SetStatus(enttrf.StatusApproved).SaveX(ctx)
	path := "/inventory/asset-transfers/" + tr.ID.String() + "/complete"

	code, body := e.call(http.MethodPost, path, nil)
	expectGated(t, code, body, "submitted")
	// The transfer is cancelled while the request waits; approval can then not complete it.
	e.client.AssetTransfer.UpdateOneID(tr.ID).SetStatus(enttrf.StatusPending).ExecX(ctx)
	req := e.lastRequest(areq.ModuleAssetTransfer, tr.ID)
	e.decide(req, "approve")
	if got := e.client.AssetTransfer.GetX(ctx, tr.ID).Status; got != enttrf.StatusPending {
		t.Fatalf("nothing may change, got %s", got)
	}
	if e.executedAt(req) != nil {
		t.Fatal("a failed execution must release the claim")
	}
	// Once the transfer is approved again, pressing Complete runs on the existing approval.
	e.client.AssetTransfer.UpdateOneID(tr.ID).SetStatus(enttrf.StatusApproved).ExecX(ctx)
	if code, body := e.call(http.MethodPost, path, nil); code != http.StatusOK {
		t.Fatalf("complete on the standing approval = %d %v", code, body)
	}
	if got := e.client.Asset.GetX(ctx, a.ID).Location; got != "Branch" {
		t.Fatalf("asset must move, location %q", got)
	}
	if e.executedAt(req) == nil || len(e.requests(areq.ModuleAssetTransfer, tr.ID)) != 1 {
		t.Fatal("the standing approval must be used and marked executed")
	}
}

// Purchase order: a rejected order is amended and resubmitted; Send uses the approval once and
// the approval is marked executed.
func TestPurchaseOrder_RejectResubmitSendOnce(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModulePurchaseOrder)
	ctx := context.Background()
	it := e.item("PO-GLV", 0)
	po := e.client.PurchaseOrder.Create().SetTenantID(e.tenant).SetPoNumber("PO-0001").SetSupplierID(e.sup).
		SetWarehouseID(e.wh).SetTotalAmount(400).SaveX(ctx)
	e.client.PurchaseOrderLine.Create().SetPoID(po.ID).SetItemID(it.ID).SetQuantityOrdered(4).
		SetUnitPrice(100).SetTotalPrice(400).SaveX(ctx)
	base := "/inventory/purchase-orders/" + po.ID.String()

	if code, _ := e.call(http.MethodPost, base+"/submit-for-approval", nil); code != http.StatusOK {
		t.Fatalf("submit = %d", code)
	}
	e.decide(e.lastRequest(areq.ModulePurchaseOrder, po.ID), "reject")
	code, body := e.call(http.MethodPut, base+"/send", nil)
	if code != http.StatusConflict || body["state"] != "rejected" {
		t.Fatalf("send after rejection = %d %v, want 409 state=rejected", code, body)
	}

	// Resubmit (the order stays a draft and may be amended first).
	code, body = e.call(http.MethodPost, base+"/submit-for-approval", nil)
	if code != http.StatusOK || body["already_submitted"] != false {
		t.Fatalf("resubmit = %d %v", code, body)
	}
	reqs := e.requests(areq.ModulePurchaseOrder, po.ID)
	if len(reqs) != 2 || reqs[0].Status != areq.StatusRejected {
		t.Fatalf("want rejected + new request, got %d rows", len(reqs))
	}
	e.decide(reqs[1], "approve")
	if got := e.client.PurchaseOrder.GetX(ctx, po.ID).Status; got != entpo.StatusDraft {
		t.Fatalf("approval does not send by itself (staff press Send), got %s", got)
	}
	if code, body := e.call(http.MethodPut, base+"/send", nil); code != http.StatusOK {
		t.Fatalf("send on the approval = %d %v", code, body)
	}
	if e.executedAt(reqs[1]) == nil {
		t.Fatal("Send must mark the approval executed")
	}
	if code, _ := e.call(http.MethodPut, base+"/send", nil); code == http.StatusOK {
		t.Fatal("a sent order must not send again")
	}
}

// The execute endpoint retries an approved request whose action never ran: a stock adjustment
// approved while its execution failed posts on retry, exactly once.
func TestExecuteApprovalRequest_RetriesUnexecutedAdjustment(t *testing.T) {
	e := newGatedEnv(t)
	ctx := context.Background()
	soap := e.item("ADJ-SOAP", 10)
	req := e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModuleStockAdjustment).
		SetObjectID(uuid.New()).SetAmount(3).SetStatus(areq.StatusApproved).SetDecidedAt(time.Now()).
		SetPayload(map[string]any{"sku": soap.Sku, "adjustment": -3.0, "reason": "correction", "warehouse_id": e.wh.String()}).
		SaveX(ctx)
	path := "/inventory/approval-requests/" + req.ID.String() + "/execute"

	code, body := e.call(http.MethodPost, path, nil)
	if code != http.StatusOK || body["executed_at"] == nil {
		t.Fatalf("execute = %d %v", code, body)
	}
	if got := e.onHandAt(soap, e.wh); got != 7 {
		t.Fatalf("the adjustment must post, on hand %v", got)
	}
	audits := e.client.AuditLog.Query().Where(auditlog.TenantID(e.tenant), auditlog.EntityID(req.ID.String())).AllX(ctx)
	if len(audits) != 1 || audits[0].Action != "approval.executed_manually" || audits[0].ActorUserID != e.user {
		t.Fatalf("want one approval.executed_manually audit row by the caller, got %v", audits)
	}
	if code, _ := e.call(http.MethodPost, path, nil); code != http.StatusConflict {
		t.Fatalf("second execute = %d, want 409", code)
	}
	if e.onHandAt(soap, e.wh) != 7 {
		t.Fatal("the adjustment must post exactly once")
	}

	// A request that is not approved cannot be executed, and another tenant's request is unseen.
	pending := e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModuleContract).
		SetObjectID(uuid.New()).SetAmount(1).SaveX(ctx)
	if code, _ := e.call(http.MethodPost, "/inventory/approval-requests/"+pending.ID.String()+"/execute", nil); code != http.StatusConflict {
		t.Fatalf("execute pending = %d, want 409", code)
	}
	other := e.client.ApprovalRequest.Create().SetTenantID(uuid.New()).SetModule(areq.ModuleStockAdjustment).
		SetObjectID(uuid.New()).SetAmount(1).SetStatus(areq.StatusApproved).SaveX(ctx)
	if code, _ := e.call(http.MethodPost, "/inventory/approval-requests/"+other.ID.String()+"/execute", nil); code != http.StatusNotFound {
		t.Fatalf("execute another tenant's request = %d, want 404", code)
	}
}
