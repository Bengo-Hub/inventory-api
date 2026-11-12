package approvals

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entreq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entrule "github.com/bengobox/inventory-service/internal/ent/approvalrule"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// gateEnv is one tenant with a single-step rule on the given module, so every Gate call is gated.
func gateEnv(t *testing.T, module entrule.Module) (*ent.Client, *Service, uuid.UUID) {
	t.Helper()
	client := testutil.SchemaClient(t, "inv_apprgate_")
	ctx := context.Background()
	tenant := uuid.New()
	rule := client.ApprovalRule.Create().SetTenantID(tenant).SetModule(module).
		SetName("Everything").SetMinAmount(0).SaveX(ctx)
	client.ApprovalStep.Create().SetTenantID(tenant).SetRuleID(rule.ID).SetSequence(1).
		SetName("Admin").SetApproverRole("admin").SaveX(ctx)
	return client, NewService(client), tenant
}

func decide(t *testing.T, svc *Service, tenant uuid.UUID, req *ent.ApprovalRequest, d Decision) *ent.ApprovalRequest {
	t.Helper()
	out, err := svc.Act(context.Background(), tenant, req.ID, uuid.New(), d, "", nil)
	if err != nil {
		t.Fatalf("act %s: %v", d, err)
	}
	return out
}

// The first press of a gated action opens a request and reports it as freshly submitted.
func TestGate_FirstCallSubmits(t *testing.T) {
	client, svc, tenant := gateEnv(t, entrule.ModuleContract)
	ctx := context.Background()
	obj := uuid.New()

	res, err := svc.Gate(ctx, tenant, "contract", obj, "Cleaning", 900, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != StateSubmitted || res.Request == nil || res.Request.Status != entreq.StatusPending {
		t.Fatalf("want a new pending request, got state=%s req=%v", res.State, res.Request)
	}
	// Pressing again while it waits changes nothing.
	again, err := svc.Gate(ctx, tenant, "contract", obj, "Cleaning", 900, nil, nil)
	if err != nil || again.State != StatePending || again.Request.ID != res.Request.ID {
		t.Fatalf("want the same pending request, got state=%s err=%v", again.State, err)
	}
	if n := countFor(t, client, tenant, obj); n != 1 {
		t.Fatalf("want one request row, got %d", n)
	}
}

// A rejected document can be fixed and pressed again: a new request opens and the rejected one
// stays as history.
func TestGate_RejectedDocumentIsResubmitted(t *testing.T) {
	client, svc, tenant := gateEnv(t, entrule.ModulePurchaseReturn)
	ctx := context.Background()
	obj := uuid.New()

	first, _ := svc.Gate(ctx, tenant, "purchase_return", obj, "PR-1", 250, nil, nil)
	decide(t, svc, tenant, first.Request, DecisionReject)

	check, err := svc.Check(ctx, tenant, "purchase_return", obj, 250, nil)
	if err != nil || check.State != StateRejected {
		t.Fatalf("Check after a rejection = %s (%v), want rejected", check.State, err)
	}

	res, err := svc.Gate(ctx, tenant, "purchase_return", obj, "PR-1", 200, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != StateResubmitted || res.Request.ID == first.Request.ID || res.Request.Status != entreq.StatusPending {
		t.Fatalf("want a fresh pending request, got state=%s req=%v", res.State, res.Request)
	}
	if got := client.ApprovalRequest.GetX(ctx, first.Request.ID).Status; got != entreq.StatusRejected {
		t.Fatalf("the rejected request must stay as history, got %s", got)
	}
	if n := countFor(t, client, tenant, obj); n != 2 {
		t.Fatalf("want two request rows (rejected + new), got %d", n)
	}
}

// An approved, not yet executed request is handed back for the caller to claim.
func TestGate_ApprovedRequestIsReturnedForClaim(t *testing.T) {
	_, svc, tenant := gateEnv(t, entrule.ModuleContract)
	ctx := context.Background()
	obj := uuid.New()

	first, _ := svc.Gate(ctx, tenant, "contract", obj, "Cleaning", 900, nil, nil)
	decide(t, svc, tenant, first.Request, DecisionApprove)

	res, err := svc.Gate(ctx, tenant, "contract", obj, "Cleaning", 900, nil, nil)
	if err != nil || res.State != StateApproved || res.Request == nil || res.Request.ID != first.Request.ID {
		t.Fatalf("want the approved request back, got state=%s err=%v", res.State, err)
	}
}

// An approval authorises one execution. Once executed, pressing the action again needs a new
// approval, and the executed request is left untouched.
func TestGate_ExecutedApprovalIsNotReused(t *testing.T) {
	client, svc, tenant := gateEnv(t, entrule.ModuleContract)
	ctx := context.Background()
	obj := uuid.New()

	first, _ := svc.Gate(ctx, tenant, "contract", obj, "Cleaning", 900, nil, nil)
	decide(t, svc, tenant, first.Request, DecisionApprove)
	if won, err := svc.ClaimExecution(ctx, tenant, first.Request.ID); err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}

	res, err := svc.Gate(ctx, tenant, "contract", obj, "Cleaning", 900, nil, nil)
	if err != nil || res.State != StateResubmitted || res.Request.ID == first.Request.ID {
		t.Fatalf("want a new request after execution, got state=%s err=%v", res.State, err)
	}
	old := client.ApprovalRequest.GetX(ctx, first.Request.ID)
	if old.Status != entreq.StatusApproved || old.ExecutedAt == nil {
		t.Fatalf("the executed approval is history: status=%s executed_at=%v", old.Status, old.ExecutedAt)
	}
}

// Same amount but a different payload (a different RFQ award selection) is a different decision:
// the old approval is cancelled and a new request opens.
func TestGate_ChangedPayloadSupersedesApproval(t *testing.T) {
	client, svc, tenant := gateEnv(t, entrule.ModuleRfq)
	ctx := context.Background()
	obj := uuid.New()
	supA, supB, line := uuid.New(), uuid.New(), uuid.New()
	award := func(sup uuid.UUID) map[string]any {
		return map[string]any{"awards": []map[string]any{{"rfq_line_id": line.String(), "supplier_id": sup.String(), "unit_price": 50.0, "quantity": 4.0}}}
	}

	first, _ := svc.Gate(ctx, tenant, "rfq", obj, "RFQ-1", 200, nil, award(supA))
	decide(t, svc, tenant, first.Request, DecisionApprove)

	// The same selection after a JSON round trip still matches the approval.
	same, err := svc.Gate(ctx, tenant, "rfq", obj, "RFQ-1", 200, nil, award(supA))
	if err != nil || same.State != StateApproved || same.Request.ID != first.Request.ID {
		t.Fatalf("same selection must match the approval, got state=%s err=%v", same.State, err)
	}

	other, err := svc.Gate(ctx, tenant, "rfq", obj, "RFQ-1", 200, nil, award(supB))
	if err != nil || other.State != StateResubmitted || other.Request.ID == first.Request.ID {
		t.Fatalf("a different selection needs a new request, got state=%s err=%v", other.State, err)
	}
	if got := client.ApprovalRequest.GetX(ctx, first.Request.ID).Status; got != entreq.StatusCancelled {
		t.Fatalf("the approval for the old selection must be cancelled, got %s", got)
	}
	stored := client.ApprovalRequest.GetX(ctx, other.Request.ID).Payload
	if aw, _ := stored["awards"].([]any); len(aw) != 1 || aw[0].(map[string]any)["supplier_id"] != supB.String() {
		t.Fatalf("the new request must carry the new selection, got %v", stored)
	}
}

// No rule for the module: nothing is written and the action proceeds.
func TestGate_NoRuleIsNotRequired(t *testing.T) {
	client, svc, tenant := gateEnv(t, entrule.ModuleContract)
	ctx := context.Background()
	obj := uuid.New()
	res, err := svc.Gate(ctx, tenant, "asset_disposal", obj, "", 10, nil, nil)
	if err != nil || res.State != StateNotRequired || res.Request != nil {
		t.Fatalf("want not_required, got state=%s err=%v", res.State, err)
	}
	// Another tenant's rule never gates this tenant.
	if res, _ := svc.Gate(ctx, uuid.New(), "contract", obj, "", 10, nil, nil); res.State != StateNotRequired {
		t.Fatalf("a rule of another tenant must not apply, got %s", res.State)
	}
	if n := countFor(t, client, tenant, obj); n != 0 {
		t.Fatalf("no request may be written, got %d", n)
	}
}

// Check never writes: a document never submitted reports not_submitted.
func TestCheck_DoesNotSubmit(t *testing.T) {
	client, svc, tenant := gateEnv(t, entrule.ModulePurchaseOrder)
	ctx := context.Background()
	obj := uuid.New()
	res, err := svc.Check(ctx, tenant, "purchase_order", obj, 400, nil)
	if err != nil || res.State != StateNotSubmitted {
		t.Fatalf("want not_submitted, got %s (%v)", res.State, err)
	}
	if n := countFor(t, client, tenant, obj); n != 0 {
		t.Fatalf("Check must not write, got %d rows", n)
	}
}

// WithdrawLive closes a pending or approved-but-not-executed request (the document changed under
// it) and leaves executed history alone.
func TestWithdrawLive(t *testing.T) {
	client, svc, tenant := gateEnv(t, entrule.ModuleStockTransfer)
	ctx := context.Background()
	obj := uuid.New()

	done, _ := svc.Gate(ctx, tenant, "stock_transfer", obj, "TR-1", 0, nil, nil)
	client.ApprovalRequest.UpdateOneID(done.Request.ID).SetStatus(entreq.StatusApproved).SetExecutedAt(time.Now()).ExecX(ctx)
	live, _ := svc.Gate(ctx, tenant, "stock_transfer", obj, "TR-1", 0, nil, nil)
	decide(t, svc, tenant, live.Request, DecisionApprove)

	n, err := svc.WithdrawLive(ctx, tenant, "stock_transfer", obj)
	if err != nil || n != 1 {
		t.Fatalf("want one request withdrawn, got %d (%v)", n, err)
	}
	if got := client.ApprovalRequest.GetX(ctx, live.Request.ID).Status; got != entreq.StatusCancelled {
		t.Fatalf("the unexecuted approval must be cancelled, got %s", got)
	}
	if got := client.ApprovalRequest.GetX(ctx, done.Request.ID).Status; got != entreq.StatusApproved {
		t.Fatalf("executed history must stay approved, got %s", got)
	}
	// Another tenant's request with the same object id is never touched.
	if n, _ := svc.WithdrawLive(ctx, uuid.New(), "stock_transfer", obj); n != 0 {
		t.Fatalf("another tenant withdrew %d requests", n)
	}
}
