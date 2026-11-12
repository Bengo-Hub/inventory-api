package handlers

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	entgo "entgo.io/ent"

	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	arule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	"github.com/bengobox/inventory-service/internal/ent/hook"
	entcount "github.com/bengobox/inventory-service/internal/ent/stockcount"
	entcountline "github.com/bengobox/inventory-service/internal/ent/stockcountline"
	"github.com/bengobox/inventory-service/internal/modules/approvals"
)

// approvedCount is a count in review with one item counted 7 against 10 (amount 3), plus an
// approved request for it whose action has not run (executed_at as given).
func (e *gatedEnv) approvedCount(sku string, amount float64, executedAt *time.Time) (*ent.StockCount, *ent.Item, *ent.ApprovalRequest) {
	e.t.Helper()
	ctx := context.Background()
	it := e.item(sku, 10)
	c := e.client.StockCount.Create().SetTenantID(e.tenant).SetWarehouseID(e.wh).SetStatus(entcount.StatusReview).SaveX(ctx)
	if code, body := e.countLine(c, it, 7); code != http.StatusOK {
		e.t.Fatalf("count line = %d %v", code, body)
	}
	rc := e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModuleStockCount).
		SetObjectID(c.ID).SetAmount(amount).SetStatus(areq.StatusApproved).SetDecidedAt(time.Now())
	if executedAt != nil {
		rc = rc.SetExecutedAt(*executedAt)
	}
	return c, it, rc.SaveX(ctx)
}

func (e *gatedEnv) countedQty(c *ent.StockCount, it *ent.Item) float64 {
	e.t.Helper()
	ln := e.client.StockCountLine.Query().
		Where(entcountline.StockCountID(c.ID), entcountline.ItemID(it.ID)).OnlyX(context.Background())
	return *ln.CountedQty
}

// The race the lock closes: a claim of the old approval that starts at the moment a line is being
// changed waits for the line change to commit, then finds the approval withdrawn and never posts.
// An ent hook starts the claim exactly when the withdraw runs (the request rows are locked by then).
func TestStockCount_ClaimDuringLineEditFindsApprovalWithdrawn(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	ctx := context.Background()
	c, it, req := e.approvedCount("LK-1", 3, nil)

	var (
		armed atomic.Bool
		won   bool
		cerr  error
		claim = make(chan struct{})
	)
	armed.Store(true)
	svc := approvals.NewService(e.client)
	e.client.ApprovalRequest.Use(hook.On(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(hctx context.Context, m ent.Mutation) (ent.Value, error) {
			// Only the first update (the withdraw inside the line edit) starts the claim; the
			// claim's own update passes straight through and blocks on the locked row.
			if armed.CompareAndSwap(true, false) {
				go func() {
					defer close(claim)
					won, cerr = svc.ClaimExecution(ctx, e.tenant, req.ID)
				}()
				// Give the claim time to reach the database and wait on the row lock.
				time.Sleep(300 * time.Millisecond)
			}
			return next.Mutate(hctx, m)
		})
	}, entgo.OpUpdate))

	if code, body := e.countLine(c, it, 9); code != http.StatusOK {
		t.Fatalf("line edit = %d %v", code, body)
	}
	select {
	case <-claim:
	case <-time.After(10 * time.Second):
		t.Fatal("the claim never finished")
	}
	if cerr != nil || won {
		t.Fatalf("the claim must lose against the withdrawn approval: won=%v err=%v", won, cerr)
	}
	got := e.client.ApprovalRequest.GetX(ctx, req.ID)
	if got.Status != areq.StatusCancelled || got.ExecutedAt != nil {
		t.Fatalf("want the approval withdrawn and never executed, got %s executed_at=%v", got.Status, got.ExecutedAt)
	}
	if e.countedQty(c, it) != 9 || e.onHandAt(it, e.wh) != 10 {
		t.Fatal("the edit is saved and nothing posts")
	}
}

// A request that is already claimed (being posted) freezes the lines.
func TestStockCount_LineEditRefusedWhilePosting(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	now := time.Now()
	c, it, _ := e.approvedCount("LK-2", 3, &now)

	code, body := e.countLine(c, it, 9)
	if code != http.StatusConflict || body["code"] != "COUNT_POSTING" {
		t.Fatalf("edit while posting = %d %v, want 409 COUNT_POSTING", code, body)
	}
	if e.countedQty(c, it) != 7 {
		t.Fatal("a refused edit changes nothing")
	}
}

// Second guard: after claiming, the executor compares the count with the approved amount and
// refuses (claim released, nothing posted) when they differ.
func TestStockCount_ExecuteRefusesChangedCount(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	c, it, req := e.approvedCount("LK-3", 99, nil) // approved for a different total than the count's 3

	code, body := e.call(http.MethodPost, "/inventory/approval-requests/"+req.ID.String()+"/execute", nil)
	if code != http.StatusConflict || body["code"] != "COUNT_CHANGED" {
		t.Fatalf("execute = %d %v, want 409 COUNT_CHANGED", code, body)
	}
	if e.executedAt(req) != nil {
		t.Fatal("the claim must be released")
	}
	if e.onHandAt(it, e.wh) != 10 || e.client.StockCount.GetX(context.Background(), c.ID).Status != entcount.StatusReview {
		t.Fatal("nothing may post on a changed count")
	}
}
