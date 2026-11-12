package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	arule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entcount "github.com/bengobox/inventory-service/internal/ent/stockcount"
)

// countLine records a counted quantity through the app's line endpoint.
func (e *gatedEnv) countLine(count *ent.StockCount, it *ent.Item, counted float64) (int, map[string]any) {
	e.t.Helper()
	return e.call(http.MethodPost, "/inventory/stock-counts/"+count.ID.String()+"/lines", map[string]any{
		"item_id": it.ID.String(), "sku": it.Sku, "counted_qty": counted,
	})
}

// An edit while the count waits for approval withdraws that request, even when the total variance
// stays the same, so the approver never signs off numbers the count no longer has. The next
// Approve opens a fresh request, and only that one posts.
func TestStockCount_EditWhilePendingNeedsNewApproval(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	ctx := context.Background()
	a := e.item("ED-A", 10)
	b := e.item("ED-B", 10)
	count := e.client.StockCount.Create().SetTenantID(e.tenant).SetWarehouseID(e.wh).SetStatus(entcount.StatusReview).SaveX(ctx)
	for _, it := range []*ent.Item{a, b} {
		if code, body := e.countLine(count, it, 10); code != http.StatusOK {
			t.Fatalf("count line = %d %v", code, body)
		}
	}
	if code, body := e.countLine(count, a, 8); code != http.StatusOK { // A short by 2
		t.Fatalf("count line = %d %v", code, body)
	}
	approve := "/inventory/stock-counts/" + count.ID.String() + "/approve"
	if code, _ := e.call(http.MethodPost, approve, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("approve = %d, want 422 submitted", code)
	}
	first := e.lastRequest(areq.ModuleStockCount, count.ID)

	// Same total variance (2), different numbers: A back to 10, B short by 2.
	e.countLine(count, a, 10)
	e.countLine(count, b, 8)
	if got := e.client.ApprovalRequest.GetX(ctx, first.ID).Status; got != areq.StatusCancelled {
		t.Fatalf("an edit must withdraw the pending request, got %s", got)
	}
	if code, _ := e.call(http.MethodPost, "/inventory/approval-requests/"+first.ID.String()+"/approve", map[string]any{}); code == http.StatusOK {
		t.Fatal("the withdrawn request can no longer be approved")
	}
	if e.onHandAt(a, e.wh) != 10 || e.onHandAt(b, e.wh) != 10 {
		t.Fatal("nothing may post on the withdrawn approval")
	}

	code, body := e.call(http.MethodPost, approve, nil)
	if code != http.StatusUnprocessableEntity || body["state"] != "resubmitted" {
		t.Fatalf("approve after the edit = %d %v, want 422 resubmitted", code, body)
	}
	second := e.lastRequest(areq.ModuleStockCount, count.ID)
	e.decide(second, "approve")
	if e.onHandAt(a, e.wh) != 10 || e.onHandAt(b, e.wh) != 8 {
		t.Fatalf("the approved numbers post: A %v (want 10), B %v (want 8)", e.onHandAt(a, e.wh), e.onHandAt(b, e.wh))
	}
}

// Re-saving a line with the same numbers is not a change and keeps the pending request.
func TestStockCount_UnchangedLineKeepsApproval(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	ctx := context.Background()
	a := e.item("SAME-A", 10)
	count := e.client.StockCount.Create().SetTenantID(e.tenant).SetWarehouseID(e.wh).SetStatus(entcount.StatusReview).SaveX(ctx)
	e.countLine(count, a, 7)
	if code, _ := e.call(http.MethodPost, "/inventory/stock-counts/"+count.ID.String()+"/approve", nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("approve = %d, want 422", code)
	}
	req := e.lastRequest(areq.ModuleStockCount, count.ID)
	if code, _ := e.countLine(count, a, 7); code != http.StatusOK {
		t.Fatalf("re-save = %d", code)
	}
	if got := e.client.ApprovalRequest.GetX(ctx, req.ID).Status; got != areq.StatusPending {
		t.Fatalf("an unchanged line must keep the request pending, got %s", got)
	}
}

// After a partial post, a posted line is frozen (409) and the count can no longer be cancelled.
func TestStockCount_PostedLineAndCancelRefused(t *testing.T) {
	e := newGatedEnv(t)
	e.rule(arule.ModuleStockCount)
	ctx := context.Background()
	good := e.item("PL-GOOD", 10)
	bad := e.item("PL-BAD", 10)
	count := e.client.StockCount.Create().SetTenantID(e.tenant).SetWarehouseID(e.wh).SetStatus(entcount.StatusReview).SaveX(ctx)
	e.countLine(count, good, 8)
	e.countLine(count, bad, 8)
	// The second item becomes a SERVICE item, so its line cannot post.
	e.client.Item.UpdateOneID(bad.ID).SetType(entitem.TypeSERVICE).ExecX(ctx)

	if code, _ := e.call(http.MethodPost, "/inventory/stock-counts/"+count.ID.String()+"/approve", nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("approve = %d, want 422", code)
	}
	e.decide(e.lastRequest(areq.ModuleStockCount, count.ID), "approve")
	if e.onHandAt(good, e.wh) != 8 {
		t.Fatal("the good line posts")
	}

	code, body := e.countLine(count, good, 5)
	if code != http.StatusConflict || body["code"] != "LINE_POSTED" {
		t.Fatalf("editing a posted line = %d %v, want 409 LINE_POSTED", code, body)
	}
	if e.onHandAt(good, e.wh) != 8 {
		t.Fatal("a refused edit changes nothing")
	}
	code, body = e.call(http.MethodPost, "/inventory/stock-counts/"+count.ID.String()+"/cancel", nil)
	if code != http.StatusConflict || body["code"] != "COUNT_POSTED" {
		t.Fatalf("cancelling a partly posted count = %d %v, want 409 COUNT_POSTED", code, body)
	}
	if got := e.client.StockCount.GetX(ctx, count.ID).Status; got == entcount.StatusCancelled {
		t.Fatal("a partly posted count must not be cancelled")
	}
}
