package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	areq "github.com/bengobox/inventory-service/internal/ent/approvalrequest"
	entcol "github.com/bengobox/inventory-service/internal/ent/contractorderlink"
	entgr "github.com/bengobox/inventory-service/internal/ent/goodsreceipt"
	entgrl "github.com/bengobox/inventory-service/internal/ent/goodsreceiptline"
	"github.com/bengobox/inventory-service/internal/ent/outboxevent"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpol "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
	entreq "github.com/bengobox/inventory-service/internal/ent/requisition"
)

func (e *hardDeleteEnv) poExists(id uuid.UUID) bool {
	ok, _ := e.client.PurchaseOrder.Query().Where(entpo.ID(id)).Exist(context.Background())
	return ok
}

func (e *hardDeleteEnv) deletePO(id uuid.UUID) (int, map[string]any) {
	return e.call(http.MethodDelete, "/inventory/purchase-orders/"+id.String()+"?hard=true", nil)
}

// I1: an older order that was partly received and then cancelled has no posted receipt, but
// its lines show stock came in. It must not be deleted.
func TestHardDeletePO_RefusedWhenALineReceivedStock(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, _ := e.sentPO(4)
	ctx := context.Background()
	e.client.PurchaseOrderLine.Update().Where(entpol.PoID(po.ID)).SetQuantityReceived(2).ExecX(ctx)
	e.client.PurchaseOrder.UpdateOneID(po.ID).SetStatus(entpo.StatusCancelled).ExecX(ctx)

	code, body := e.deletePO(po.ID)
	if code != http.StatusConflict || body["code"] != "RECEIPT_POSTED" {
		t.Fatalf("received line: want 409 RECEIPT_POSTED, got %d %v", code, body)
	}
	if !e.poExists(po.ID) {
		t.Fatal("PO must stay")
	}
}

func TestHardDeletePO_RefusedForReceivedStatuses(t *testing.T) {
	for _, st := range []entpo.Status{entpo.StatusReceived, entpo.StatusPartiallyReceived} {
		e := newHardDeleteEnv(t)
		po, _ := e.sentPO(1)
		e.client.PurchaseOrder.UpdateOneID(po.ID).SetStatus(st).ExecX(context.Background())
		if code, body := e.deletePO(po.ID); code != http.StatusConflict || body["code"] != "RECEIPT_POSTED" {
			t.Fatalf("status %s: want 409 RECEIPT_POSTED, got %d %v", st, code, body)
		}
	}
}

func TestHardDeletePO_RefusedWithExecutedApproval(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, _ := e.sentPO(1)
	e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModulePurchaseOrder).SetObjectID(po.ID).
		SetAmount(100).SetStatus(areq.StatusApproved).SetExecutedAt(time.Now()).SaveX(context.Background())
	if code, body := e.deletePO(po.ID); code != http.StatusConflict || body["code"] != "APPROVAL_EXECUTED" {
		t.Fatalf("executed approval: want 409 APPROVAL_EXECUTED, got %d %v", code, body)
	}
}

func TestHardDelete_OtherTenantIsNotFound(t *testing.T) {
	e := newHardDeleteEnv(t)
	ctx := context.Background()
	other := uuid.New()
	e.client.Tenant.Create().SetID(other).SetName("Other").SetSlug("other-" + other.String()).SaveX(ctx)
	po := e.client.PurchaseOrder.Create().SetTenantID(other).SetPoNumber("X-1").SaveX(ctx)
	req := e.client.ApprovalRequest.Create().SetTenantID(other).SetModule(areq.ModulePurchaseOrder).
		SetObjectID(po.ID).SetAmount(1).SaveX(ctx)

	if code, _ := e.deletePO(po.ID); code != http.StatusNotFound {
		t.Fatalf("another tenant's PO: want 404, got %d", code)
	}
	if code, _ := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String(), nil); code != http.StatusNotFound {
		t.Fatalf("another tenant's request: want 404, got %d", code)
	}
	if !e.poExists(po.ID) || !e.requestExists(req.ID) {
		t.Fatal("other tenant's rows must stay")
	}
}

func TestHardDeletePO_CleansContractLinksAndRFQAwards(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, _ := e.sentPO(1)
	ctx := context.Background()
	c := e.client.Contract.Create().SetTenantID(e.tenant).SetSupplierID(e.supplier).SetTitle("Annual").
		SetStartDate(time.Now()).SetEndDate(time.Now().AddDate(1, 0, 0)).SaveX(ctx)
	e.client.ContractOrderLink.Create().SetTenantID(e.tenant).SetContractID(c.ID).SetPurchaseOrderID(po.ID).SaveX(ctx)
	rfq := e.client.RFQ.Create().SetTenantID(e.tenant).SetRfqNumber("RFQ-1").SaveX(ctx)
	award := e.client.RFQAward.Create().SetTenantID(e.tenant).SetRfqID(rfq.ID).SetRfqLineID(uuid.New()).
		SetSupplierID(e.supplier).SetPoID(po.ID).SaveX(ctx)

	if code, body := e.deletePO(po.ID); code != http.StatusOK {
		t.Fatalf("delete: %d %v", code, body)
	}
	if n := e.client.ContractOrderLink.Query().Where(entcol.PurchaseOrderID(po.ID)).CountX(ctx); n != 0 {
		t.Fatalf("contract links must be deleted, %d left", n)
	}
	if got := e.client.RFQAward.GetX(ctx, award.ID); got.PoID != nil {
		t.Fatalf("RFQ award must lose its PO link, still %v", *got.PoID)
	}
	if e.client.Contract.Query().CountX(ctx) != 1 || e.client.RFQ.Query().CountX(ctx) != 1 {
		t.Fatal("the contract and the RFQ themselves stay")
	}
}

// The cancelled event treasury consumes carries ids only.
func TestHardDeletePO_OutboxPayloadIsIdsOnly(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, _ := e.sentPO(1)
	if code, body := e.deletePO(po.ID); code != http.StatusOK {
		t.Fatalf("delete: %d %v", code, body)
	}
	row := e.client.OutboxEvent.Query().Where(outboxevent.EventType("purchase_order.cancelled"), outboxevent.AggregateID(po.ID.String())).OnlyX(context.Background())
	var env struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(row.Payload, &env); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(env.Payload))
	for k := range env.Payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"deleted_at", "hard_deleted", "po_id", "status", "tenant_id"}
	if len(keys) != len(want) {
		t.Fatalf("payload keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("payload keys = %v, want %v", keys, want)
		}
	}
	if env.Payload["po_id"] != po.ID.String() || env.Payload["tenant_id"] != e.tenant.String() || env.Payload["status"] != "cancelled" {
		t.Fatalf("payload = %v", env.Payload)
	}
}

// Outside goods receipts an approved request may already have been acted on.
func TestHardDeleteApproval_ApprovedOutsideGoodsReceiptIsRefused(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, _ := e.sentPO(1)
	req := e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModulePurchaseOrder).
		SetObjectID(po.ID).SetAmount(100).SetStatus(areq.StatusApproved).SaveX(context.Background())
	code, body := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String(), nil)
	if code != http.StatusConflict || body["code"] != "APPROVAL_APPLIED" {
		t.Fatalf("approved PO request: want 409 APPROVAL_APPLIED, got %d %v", code, body)
	}
}

func TestHardDeleteApproval_WithObjectOnOtherModuleIs400(t *testing.T) {
	e := newHardDeleteEnv(t)
	po, _ := e.sentPO(1)
	req := e.client.ApprovalRequest.Create().SetTenantID(e.tenant).SetModule(areq.ModulePurchaseOrder).
		SetObjectID(po.ID).SetAmount(100).SaveX(context.Background())
	code, body := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String()+"?with_object=true", nil)
	if code != http.StatusBadRequest || body["code"] != "WITH_OBJECT_UNSUPPORTED" {
		t.Fatalf("want 400 WITH_OBJECT_UNSUPPORTED, got %d %v", code, body)
	}
}

func TestHardDeleteApproval_WithObjectRefusesReferencedReceipt(t *testing.T) {
	e := newHardDeleteEnv(t)
	_, g, req := e.pendingReceipt()
	ctx := context.Background()
	line := e.client.GoodsReceiptLine.Query().Where(entgrl.GoodsReceiptID(g.ID)).FirstX(ctx)
	e.client.InventoryLot.Create().SetTenantID(e.tenant).SetItemID(line.ItemID).SetWarehouseID(e.wh).
		SetLotNumber("LOT-1").SetGoodsReceiptLineID(line.ID).SaveX(ctx)
	code, body := e.call(http.MethodDelete, "/inventory/approval-requests/"+req.ID.String()+"?with_object=true", nil)
	if code != http.StatusConflict || body["code"] != "RECEIPT_REFERENCED" {
		t.Fatalf("want 409 RECEIPT_REFERENCED, got %d %v", code, body)
	}
	if !e.receiptExists(g.ID) || !e.requestExists(req.ID) {
		t.Fatal("nothing may be deleted when refused")
	}
}

// ─── I2: the delete statements carry their own condition ─────────────────────
//
// These call the delete steps directly after the row changed, which is what a concurrent
// approval or post committing between the guard read and the delete looks like.

func asHardDeleteRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var hd *hardDeleteError
	if !errors.As(err, &hd) || hd.status != http.StatusConflict || hd.code != code {
		t.Fatalf("want 409 %s refusal, got %v", code, err)
	}
}

func inTx(t *testing.T, c *ent.Client, fn func(tc *ent.Client) error) error {
	t.Helper()
	tx, err := c.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ferr := fn(tx.Client())
	_ = tx.Rollback()
	return ferr
}

func TestDeleteApprovalRequestsTx_RefusesRequestExecutedMeanwhile(t *testing.T) {
	e := newHardDeleteEnv(t)
	_, _, req := e.pendingReceipt()
	ctx := context.Background()
	e.client.ApprovalRequest.UpdateOneID(req.ID).SetExecutedAt(time.Now()).ExecX(ctx)
	err := inTx(t, e.client, func(tc *ent.Client) error {
		_, err := deleteApprovalRequestsTx(ctx, tc, e.tenant, []uuid.UUID{req.ID})
		return err
	})
	asHardDeleteRefusal(t, err, "APPROVAL_EXECUTED")
	if !e.requestExists(req.ID) {
		t.Fatal("executed request must survive")
	}
}

func TestDeleteReceiptsTx_RefusesReceiptPostedMeanwhile(t *testing.T) {
	e := newHardDeleteEnv(t)
	_, g, _ := e.pendingReceipt()
	ctx := context.Background()
	e.client.GoodsReceipt.UpdateOneID(g.ID).SetStatus(entgr.StatusPosted).ExecX(ctx)
	err := inTx(t, e.client, func(tc *ent.Client) error {
		return deleteReceiptsTx(ctx, tc, e.tenant, []uuid.UUID{g.ID}, entgr.StatusDraft, entgr.StatusCancelled)
	})
	asHardDeleteRefusal(t, err, "RECEIPT_POSTED")
	if !e.receiptExists(g.ID) {
		t.Fatal("posted receipt must survive")
	}
}

func TestDeletePurchaseOrderRowTx_RefusesOrderReceivedMeanwhile(t *testing.T) {
	e := newHardDeleteEnv(t)
	ctx := context.Background()

	po, _ := e.sentPO(1)
	e.client.PurchaseOrder.UpdateOneID(po.ID).SetStatus(entpo.StatusPartiallyReceived).ExecX(ctx)
	err := inTx(t, e.client, func(tc *ent.Client) error { return deletePurchaseOrderRowTx(ctx, tc, e.tenant, po.ID) })
	asHardDeleteRefusal(t, err, "RECEIPT_POSTED")

	po2, _ := e.sentPO(1)
	e.client.PurchaseOrderLine.Update().Where(entpol.PoID(po2.ID)).SetQuantityReceived(1).ExecX(ctx)
	err = inTx(t, e.client, func(tc *ent.Client) error { return deletePurchaseOrderRowTx(ctx, tc, e.tenant, po2.ID) })
	asHardDeleteRefusal(t, err, "RECEIPT_POSTED")

	if !e.poExists(po.ID) || !e.poExists(po2.ID) {
		t.Fatal("both orders must survive")
	}
}

// ─── Minor 4: requisition submit only from draft or rejected ─────────────────

func TestSubmitRequisition_OnlyFromDraftOrRejected(t *testing.T) {
	e := newHardDeleteEnv(t)
	ctx := context.Background()

	approved := e.client.Requisition.Create().SetTenantID(e.tenant).SetReferenceNumber("REQ-A").SetStatus(entreq.StatusApproved).SaveX(ctx)
	if code, body := e.call(http.MethodPost, "/inventory/requisitions/"+approved.ID.String()+"/submit", nil); code != http.StatusBadRequest {
		t.Fatalf("approved requisition: want 400, got %d %v", code, body)
	}
	if got := e.client.Requisition.GetX(ctx, approved.ID).Status; got != entreq.StatusApproved {
		t.Fatalf("status must stay approved, got %s", got)
	}
	for _, st := range []entreq.Status{entreq.StatusDraft, entreq.StatusRejected} {
		rq := e.client.Requisition.Create().SetTenantID(e.tenant).SetReferenceNumber("REQ-" + string(st)).SetStatus(st).SaveX(ctx)
		if code, body := e.call(http.MethodPost, "/inventory/requisitions/"+rq.ID.String()+"/submit", nil); code != http.StatusOK {
			t.Fatalf("%s requisition: want 200, got %d %v", st, code, body)
		}
	}
}
