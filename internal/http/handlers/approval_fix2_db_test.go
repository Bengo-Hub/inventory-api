package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	entgo "entgo.io/ent"

	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	arule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	"github.com/bengobox/inventory-service/internal/ent/hook"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entpr "github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	entcount "github.com/bengobox/inventory-service/internal/ent/stockcount"
	"github.com/bengobox/inventory-service/internal/ent/stocktransfer"
)

// failRuleLookups makes every approval-rule read fail, as a database fault would.
func (e *gatedEnv) failRuleLookups() {
	e.client.ApprovalRule.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			return nil, errors.New("simulated database fault")
		})
	}))
}

// failRequestWrites makes every approval-request insert fail.
func (e *gatedEnv) failRequestWrites() {
	e.client.ApprovalRequest.Use(hook.On(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			return nil, errors.New("simulated database fault")
		})
	}, entgo.OpCreate))
}

func (e *gatedEnv) pendingReturn(sku string, onHand, qty float64) (*ent.PurchaseReturn, *ent.Item) {
	e.t.Helper()
	ctx := context.Background()
	it := e.item(sku, onHand)
	pr := e.client.PurchaseReturn.Create().SetTenantID(e.tenant).SetReturnNumber("PR-" + sku).
		SetSupplierID(e.sup).SetWarehouseID(e.wh).SetReturnAmount(qty * 100).SetDateReturned(time.Now()).SaveX(ctx)
	e.client.PurchaseReturnLine.Create().SetTenantID(e.tenant).SetPurchaseReturnID(pr.ID).
		SetItemID(it.ID).SetQuantity(qty).SetSubTotal(qty * 100).SaveX(ctx)
	return pr, it
}

// countWithLine creates a count in review with one counted line for a fresh item.
func (e *gatedEnv) countWithLine(sku string, onHand, counted float64) (*ent.StockCount, *ent.Item) {
	e.t.Helper()
	ctx := context.Background()
	it := e.item(sku, onHand)
	c := e.client.StockCount.Create().SetTenantID(e.tenant).SetWarehouseID(e.wh).SetStatus(entcount.StatusReview).SaveX(ctx)
	variance := counted - onHand
	e.client.StockCountLine.Create().SetTenantID(e.tenant).SetStockCountID(c.ID).SetItemID(it.ID).
		SetSku(it.Sku).SetSystemQty(onHand).SetCountedQty(counted).SetVariance(variance).SaveX(ctx)
	return c, it
}

// When the rule lookup fails, the gate cannot tell whether approval applies: the action must not
// run (503), and nothing moves.
func TestGate_FailsClosedWhenRuleLookupFails(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModulePurchaseReturn)
	pr, it := e.pendingReturn("FC-RULE", 10, 2)
	e.failRuleLookups()

	code, body := e.call(http.MethodPost, "/inventory/purchase-returns/"+pr.ID.String()+"/approve", nil)
	if code != http.StatusServiceUnavailable || body["code"] != "APPROVAL_UNAVAILABLE" {
		t.Fatalf("want 503 APPROVAL_UNAVAILABLE, got %d %v", code, body)
	}
	if e.onHandAt(it, e.wh) != 10 {
		t.Fatal("no stock may move when the approval check fails")
	}
	if got := e.client.PurchaseReturn.GetX(context.Background(), pr.ID).PaymentStatus; got != entpr.PaymentStatusPending {
		t.Fatalf("the return must stay pending, got %s", got)
	}
}

// When the approval request cannot be written, the action must not run either.
func TestGate_FailsClosedWhenRequestWriteFails(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModulePurchaseReturn)
	pr, it := e.pendingReturn("FC-WRITE", 10, 2)
	e.failRequestWrites()

	code, body := e.call(http.MethodPost, "/inventory/purchase-returns/"+pr.ID.String()+"/approve", nil)
	if code != http.StatusServiceUnavailable || body["code"] != "APPROVAL_UNAVAILABLE" {
		t.Fatalf("want 503 APPROVAL_UNAVAILABLE, got %d %v", code, body)
	}
	if e.onHandAt(it, e.wh) != 10 || len(e.requests(areq.ModulePurchaseReturn, pr.ID)) != 0 {
		t.Fatal("nothing may move and no request may exist after a failed write")
	}
}

// With no rule at all the gate never reads requests, so a fault there cannot block the action.
func TestGate_NoRuleStillProceedsWhenRequestWritesFail(t *testing.T) {
	e := newGatedEnv(t)
	pr, it := e.pendingReturn("FC-NORULE", 10, 2)
	e.failRequestWrites()
	if code, body := e.call(http.MethodPost, "/inventory/purchase-returns/"+pr.ID.String()+"/approve", nil); code != http.StatusOK {
		t.Fatalf("no rule matches, want 200, got %d %v", code, body)
	}
	if e.onHandAt(it, e.wh) != 8 {
		t.Fatal("the return must go out")
	}
}

// Transfers have their own gate path: it fails closed too.
func TestTransferShip_FailsClosedWhenRuleLookupFails(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockTransfer)
	ctx := context.Background()
	box := e.item("FC-TR", 20)
	tr := e.client.StockTransfer.Create().SetTenantID(e.tenant).SetTransferNumber("TR-FC").
		SetSourceWarehouseID(e.wh).SetDestinationWarehouseID(e.wh2).SaveX(ctx)
	e.client.StockTransferLine.Create().SetTransferID(tr.ID).SetItemID(box.ID).SetQuantity(5).SaveX(ctx)
	e.failRuleLookups()

	if code, body := e.call(http.MethodPost, "/inventory/transfers/"+tr.ID.String()+"/ship", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d %v", code, body)
	}
	if e.onHandAt(box, e.wh) != 20 || e.client.StockTransfer.GetX(ctx, tr.ID).Status != stocktransfer.StatusDraft {
		t.Fatal("the transfer must not ship when the approval check fails")
	}
}

// Stock counts have their own gate path: it fails closed too.
func TestStockCount_FailsClosedWhenRuleLookupFails(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	count, soap := e.countWithLine("FC-SC", 10, 7)
	e.failRuleLookups()

	if code, body := e.call(http.MethodPost, "/inventory/stock-counts/"+count.ID.String()+"/approve", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d %v", code, body)
	}
	if e.onHandAt(soap, e.wh) != 10 {
		t.Fatal("no variance may post when the approval check fails")
	}
}

// A count whose approval posted only part of its lines keeps that approval for the rest: after
// the failing item is fixed, pressing Approve finishes the post on the SAME request.
func TestStockCount_PartialPostFinishesOnSameApproval(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	ctx := context.Background()
	good := e.item("PP-GOOD", 10)
	// A SERVICE item cannot be stock-adjusted, so its line fails until the item is corrected.
	bad := e.item("PP-BAD", 10)
	e.client.Item.UpdateOneID(bad.ID).SetType(entitem.TypeSERVICE).ExecX(ctx)
	count := e.client.StockCount.Create().SetTenantID(e.tenant).SetWarehouseID(e.wh).SetStatus(entcount.StatusReview).SaveX(ctx)
	for _, it := range []*ent.Item{good, bad} {
		counted, variance := 8.0, -2.0
		e.client.StockCountLine.Create().SetTenantID(e.tenant).SetStockCountID(count.ID).SetItemID(it.ID).
			SetSku(it.Sku).SetSystemQty(10).SetCountedQty(counted).SetVariance(variance).SaveX(ctx)
	}
	approve := "/inventory/stock-counts/" + count.ID.String() + "/approve"

	if code, _ := e.call(http.MethodPost, approve, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("first approve = %d, want 422 submitted", code)
	}
	req := e.lastRequest(areq.ModuleStockCount, count.ID)
	e.decide(req, "approve")
	if e.onHandAt(good, e.wh) != 8 || e.onHandAt(bad, e.wh) != 10 {
		t.Fatal("the good line posts, the service line fails")
	}
	if e.executedAt(req) != nil {
		t.Fatal("a partial post must release the claim")
	}

	// Fix the item and press Approve again: the same approval finishes the post.
	e.client.Item.UpdateOneID(bad.ID).SetType(entitem.TypeGOODS).ExecX(ctx)
	if code, body := e.call(http.MethodPost, approve, nil); code != http.StatusOK {
		t.Fatalf("approve after the fix = %d %v, want 200 on the standing approval", code, body)
	}
	if e.onHandAt(bad, e.wh) != 8 || e.onHandAt(good, e.wh) != 8 {
		t.Fatal("the remaining line posts once and the posted line is not posted again")
	}
	reqs := e.requests(areq.ModuleStockCount, count.ID)
	if len(reqs) != 1 || reqs[0].ExecutedAt == nil || reqs[0].Status != areq.StatusApproved {
		t.Fatalf("want the one original approval, executed; got %d rows", len(reqs))
	}
	if got := e.client.StockCount.GetX(ctx, count.ID).Status; got != entcount.StatusApproved {
		t.Fatalf("count must be approved, got %s", got)
	}
}

// Cancelling a count withdraws its live approval, and an approval for a count that was cancelled
// anyway never posts.
func TestStockCount_CancelledCountNeverPosts(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	ctx := context.Background()
	submit := func(c *ent.StockCount) {
		if code, _ := e.call(http.MethodPost, "/inventory/stock-counts/"+c.ID.String()+"/approve", nil); code != http.StatusUnprocessableEntity {
			t.Fatalf("approve = %d, want 422 submitted", code)
		}
	}

	// Cancelled in the app: the pending request is withdrawn.
	c1, _ := e.countWithLine("CX-1", 10, 7)
	submit(c1)
	if code, _ := e.call(http.MethodPost, "/inventory/stock-counts/"+c1.ID.String()+"/cancel", nil); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	if got := e.lastRequest(areq.ModuleStockCount, c1.ID).Status; got != areq.StatusCancelled {
		t.Fatalf("cancelling the count must withdraw its request, got %s", got)
	}

	// Cancelled without touching the request: approving the request must not post.
	c2, it2 := e.countWithLine("CX-2", 10, 7)
	submit(c2)
	e.client.StockCount.UpdateOneID(c2.ID).SetStatus(entcount.StatusCancelled).ExecX(ctx)
	req := e.lastRequest(areq.ModuleStockCount, c2.ID)
	e.decide(req, "approve")
	if e.onHandAt(it2, e.wh) != 10 {
		t.Fatal("an approval for a cancelled count must not post")
	}
	if got := e.client.StockCount.GetX(ctx, c2.ID).Status; got != entcount.StatusCancelled {
		t.Fatalf("the count must stay cancelled, got %s", got)
	}
}
