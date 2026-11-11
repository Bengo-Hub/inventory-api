package approvals

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entreq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entrule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// dedupeEnv is one tenant with a single-step purchase_order rule, so every Submit is gated.
func dedupeEnv(t *testing.T) (*ent.Client, *Service, uuid.UUID) {
	t.Helper()
	client := testutil.SchemaClient(t, "inv_apprdup_")
	ctx := context.Background()
	tenant := uuid.New()
	rule := client.ApprovalRule.Create().SetTenantID(tenant).SetModule(entrule.ModulePurchaseOrder).
		SetName("All orders").SetMinAmount(0).SaveX(ctx)
	client.ApprovalStep.Create().SetTenantID(tenant).SetRuleID(rule.ID).SetSequence(1).
		SetName("Admin").SetApproverRole("admin").SaveX(ctx)
	return client, NewService(client), tenant
}

func countFor(t *testing.T, c *ent.Client, tenant, object uuid.UUID) int {
	t.Helper()
	return c.ApprovalRequest.Query().Where(entreq.TenantID(tenant), entreq.ObjectID(object)).CountX(context.Background())
}

// Submitting the same document twice while the first request is pending returns that request.
func TestSubmit_ReturnsExistingPendingRequest(t *testing.T) {
	client, svc, tenant := dedupeEnv(t)
	ctx := context.Background()
	obj := uuid.New()

	first, required, err := svc.Submit(ctx, tenant, "purchase_order", obj, "000021", 400, nil)
	if err != nil || !required {
		t.Fatalf("first submit: required=%v err=%v", required, err)
	}
	second, required, err := svc.Submit(ctx, tenant, "purchase_order", obj, "000021", 400, nil)
	if err != nil || !required {
		t.Fatalf("second submit: required=%v err=%v", required, err)
	}
	if second.ID != first.ID {
		t.Fatalf("second submit must return the pending request %s, got %s", first.ID, second.ID)
	}
	if n := countFor(t, client, tenant, obj); n != 1 {
		t.Fatalf("want exactly one request row, got %d", n)
	}
	if got := client.ApprovalRequest.GetX(ctx, first.ID).Status; got != entreq.StatusPending {
		t.Fatalf("the original request must stay pending, got %s", got)
	}
}

// An approved request whose effect has not run yet is still the live one: no second request.
func TestSubmit_ReturnsApprovedNotExecutedRequest(t *testing.T) {
	client, svc, tenant := dedupeEnv(t)
	ctx := context.Background()
	obj := uuid.New()
	first, _, _ := svc.Submit(ctx, tenant, "purchase_order", obj, "000021", 400, nil)
	client.ApprovalRequest.UpdateOneID(first.ID).SetStatus(entreq.StatusApproved).SetDecidedAt(time.Now()).ExecX(ctx)

	again, required, err := svc.Submit(ctx, tenant, "purchase_order", obj, "000021", 400, nil)
	if err != nil || !required || again.ID != first.ID {
		t.Fatalf("want the approved request back, got %v required=%v err=%v", again, required, err)
	}
	if n := countFor(t, client, tenant, obj); n != 1 {
		t.Fatalf("want one request row, got %d", n)
	}
}

// A changed amount is a different approval: the stale pending request is superseded, never kept
// alongside a second pending one.
func TestSubmit_ChangedAmountSupersedesPending(t *testing.T) {
	client, svc, tenant := dedupeEnv(t)
	ctx := context.Background()
	obj := uuid.New()
	first, _, _ := svc.Submit(ctx, tenant, "purchase_order", obj, "000021", 400, nil)
	second, _, err := svc.Submit(ctx, tenant, "purchase_order", obj, "000021", 500, nil)
	if err != nil || second.ID == first.ID {
		t.Fatalf("a new amount needs a new request, err=%v", err)
	}
	if got := client.ApprovalRequest.GetX(ctx, first.ID).Status; got != entreq.StatusCancelled {
		t.Fatalf("stale request must be cancelled, got %s", got)
	}
	pending := client.ApprovalRequest.Query().Where(entreq.TenantID(tenant), entreq.ObjectID(obj), entreq.StatusEQ(entreq.StatusPending)).CountX(ctx)
	if pending != 1 {
		t.Fatalf("want one pending request, got %d", pending)
	}
}

// The database refuses a second pending request for the same tenant, module and object, whatever
// code path writes it.
func TestApprovalRequests_PendingUniqueIndex(t *testing.T) {
	client, _, tenant := dedupeEnv(t)
	ctx := context.Background()
	obj := uuid.New()
	mk := func(status entreq.Status) error {
		_, err := client.ApprovalRequest.Create().SetTenantID(tenant).SetModule(entreq.ModulePurchaseOrder).
			SetObjectID(obj).SetAmount(1).SetStatus(status).Save(ctx)
		return err
	}
	if err := mk(entreq.StatusPending); err != nil {
		t.Fatal(err)
	}
	if err := mk(entreq.StatusPending); err == nil || !ent.IsConstraintError(err) {
		t.Fatalf("second pending insert must violate the unique index, got %v", err)
	}
	// Closed requests are history and may repeat.
	if err := mk(entreq.StatusCancelled); err != nil {
		t.Fatalf("cancelled rows are not constrained: %v", err)
	}
	if err := mk(entreq.StatusCancelled); err != nil {
		t.Fatalf("cancelled rows are not constrained: %v", err)
	}
	// A different module on the same object is a different workflow.
	if _, err := client.ApprovalRequest.Create().SetTenantID(tenant).SetModule(entreq.ModuleGoodsReceipt).
		SetObjectID(obj).SetAmount(1).Save(ctx); err != nil {
		t.Fatalf("other module must be allowed: %v", err)
	}
}

// Concurrent submits of the same document: the losers hit the unique index, get the winner's
// request back, and exactly one pending row exists.
func TestSubmit_ConcurrentSubmitsShareOneRequest(t *testing.T) {
	client, svc, tenant := dedupeEnv(t)
	ctx := context.Background()
	obj := uuid.New()
	const n = 8
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		ids   = make([]uuid.UUID, n)
		errs  = make([]error, n)
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req, _, err := svc.Submit(ctx, tenant, "purchase_order", obj, "000021", 400, nil)
			errs[i] = err
			if req != nil {
				ids[i] = req.ID
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("submit %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("submit %d got %s, submit 0 got %s", i, ids[i], ids[0])
		}
	}
	pending := client.ApprovalRequest.Query().Where(entreq.TenantID(tenant), entreq.ObjectID(obj), entreq.StatusEQ(entreq.StatusPending)).CountX(ctx)
	if pending != 1 {
		t.Fatalf("want one pending request, got %d", pending)
	}
	if total := countFor(t, client, tenant, obj); total != 1 {
		t.Fatalf("no request may be created and cancelled in the race, got %d rows", total)
	}
}

// A live approval at an old amount is superseded when the document is resubmitted at a new
// amount, so it can never come back into force (for example after the newer request is deleted).
func TestSubmit_ChangedAmountSupersedesApprovedNotExecuted(t *testing.T) {
	client, svc, tenant := dedupeEnv(t)
	ctx := context.Background()
	obj := uuid.New()
	first, _, _ := svc.Submit(ctx, tenant, "purchase_order", obj, "000021", 400, nil)
	client.ApprovalRequest.UpdateOneID(first.ID).SetStatus(entreq.StatusApproved).SetDecidedAt(time.Now()).ExecX(ctx)

	second, _, err := svc.Submit(ctx, tenant, "purchase_order", obj, "000021", 500, nil)
	if err != nil || second.ID == first.ID {
		t.Fatalf("a new amount needs a new request, err=%v", err)
	}
	if got := client.ApprovalRequest.GetX(ctx, first.ID).Status; got != entreq.StatusCancelled {
		t.Fatalf("the old approval must be cancelled, got %s", got)
	}
	// An executed approval is history and is left alone.
	obj2 := uuid.New()
	done, _, _ := svc.Submit(ctx, tenant, "purchase_order", obj2, "000022", 400, nil)
	client.ApprovalRequest.UpdateOneID(done.ID).SetStatus(entreq.StatusApproved).SetExecutedAt(time.Now()).ExecX(ctx)
	if _, _, err := svc.Submit(ctx, tenant, "purchase_order", obj2, "000022", 500, nil); err != nil {
		t.Fatal(err)
	}
	if got := client.ApprovalRequest.GetX(ctx, done.ID).Status; got != entreq.StatusApproved {
		t.Fatalf("an executed approval must stay approved, got %s", got)
	}
}
