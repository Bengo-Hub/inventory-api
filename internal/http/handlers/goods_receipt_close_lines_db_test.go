package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entpo "github.com/bengobox/inventory-service/internal/ent/purchaseorder"
	entpoline "github.com/bengobox/inventory-service/internal/ent/purchaseorderline"
)

// addLine adds a second item line to a purchase order.
func (e *grnApprovalEnv) addLine(po *ent.PurchaseOrder, name string, qty float64) (*ent.Item, *ent.PurchaseOrderLine) {
	e.t.Helper()
	ctx := context.Background()
	it := e.client.Item.Create().SetTenantID(e.tenant).SetSku("ITM-" + uuid.NewString()[:6]).SetName(name).SetCostPrice(50).SaveX(ctx)
	pl := e.client.PurchaseOrderLine.Create().SetPoID(po.ID).SetItemID(it.ID).SetQuantityOrdered(qty).
		SetUnitPrice(50).SetTotalPrice(qty * 50).SaveX(ctx)
	return it, pl
}

func (e *grnApprovalEnv) poLine(po *ent.PurchaseOrder, item *ent.Item) *ent.PurchaseOrderLine {
	e.t.Helper()
	return e.client.PurchaseOrderLine.Query().
		Where(entpoline.PoID(po.ID), entpoline.ItemID(item.ID)).OnlyX(context.Background())
}

// The client's report (26 November 2026): ordered 100 of one item with others, the item never
// came (0 received). Receiving the rest and closing that line on the same receipt settles the
// order as received instead of leaving it partially received.
func TestGoodsReceipt_ClosesUndeliveredLine(t *testing.T) {
	e := newGRNApprovalEnv(t, false)
	po, brake := e.sentPO(20)
	poya, _ := e.addLine(po, "Poya", 100)
	brakeLine, poyaLine := e.poLine(po, brake), e.poLine(po, poya)

	code, body := e.call(http.MethodPost, "/inventory/purchase-orders/"+po.ID.String()+"/goods-receipts", map[string]any{
		"lines": []map[string]any{
			{"purchase_order_line_id": brakeLine.ID, "item_id": brake.ID, "quantity_received": 20, "quantity_accepted": 20, "unit_cost": 100},
			{"purchase_order_line_id": poyaLine.ID, "item_id": poya.ID, "quantity_received": 0, "close_remaining": true},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create receipt: %d %v", code, body)
	}
	grnID, _ := body["id"].(string)
	if code, body := e.call(http.MethodPost, "/inventory/goods-receipts/"+grnID+"/post", nil); code != http.StatusOK {
		t.Fatalf("post receipt: %d %v", code, body)
	}

	if got := e.poStatus(po.ID); got != entpo.StatusReceived.String() {
		t.Fatalf("order status = %s, want received", got)
	}
	if l := e.poLine(po, poya); l.QuantityClosed != 100 || l.QuantityReceived != 0 {
		t.Fatalf("poya line received=%v closed=%v, want 0 and 100", l.QuantityReceived, l.QuantityClosed)
	}
	if got := e.onHand(brake); got != 20 {
		t.Fatalf("brake on hand = %v, want 20", got)
	}
	if got := e.onHand(poya); got != 0 {
		t.Fatalf("poya on hand = %v, want 0", got)
	}
	notes := e.client.PurchaseOrder.GetX(context.Background(), po.ID).Notes
	if !strings.Contains(notes, "Poya: 0 of 100 received, 100 not delivered") {
		t.Fatalf("order notes do not record the closed line: %q", notes)
	}
}

// Without the tick the order stays partially received for a later delivery (unchanged).
func TestGoodsReceipt_UnclosedLineKeepsOrderOpen(t *testing.T) {
	e := newGRNApprovalEnv(t, false)
	po, brake := e.sentPO(20)
	poya, _ := e.addLine(po, "Poya", 100)

	code, body := e.call(http.MethodPost, "/inventory/purchase-orders/"+po.ID.String()+"/goods-receipts", map[string]any{
		"lines": []map[string]any{
			{"purchase_order_line_id": e.poLine(po, brake).ID, "item_id": brake.ID, "quantity_received": 20, "quantity_accepted": 20, "unit_cost": 100},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create receipt: %d %v", code, body)
	}
	grnID, _ := body["id"].(string)
	if code, body := e.call(http.MethodPost, "/inventory/goods-receipts/"+grnID+"/post", nil); code != http.StatusOK {
		t.Fatalf("post receipt: %d %v", code, body)
	}
	if got := e.poStatus(po.ID); got != entpo.StatusPartiallyReceived.String() {
		t.Fatalf("order status = %s, want partially_received", got)
	}
	if l := e.poLine(po, poya); l.QuantityClosed != 0 {
		t.Fatalf("poya closed = %v, want 0", l.QuantityClosed)
	}
}

// Partly rejected and the rest not replaced: 100 delivered, 10 rejected, close the line. The 90
// accepted are received and the 10 closed, so the order settles.
func TestGoodsReceipt_ClosesRejectedRemainder(t *testing.T) {
	e := newGRNApprovalEnv(t, false)
	po, brake := e.sentPO(100)
	line := e.poLine(po, brake)

	code, body := e.call(http.MethodPost, "/inventory/purchase-orders/"+po.ID.String()+"/goods-receipts", map[string]any{
		"lines": []map[string]any{
			{"purchase_order_line_id": line.ID, "item_id": brake.ID, "quantity_received": 100, "quantity_accepted": 90,
				"quantity_rejected": 10, "rejection_reason": "damaged", "unit_cost": 100, "close_remaining": true},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create receipt: %d %v", code, body)
	}
	grnID, _ := body["id"].(string)
	if code, body := e.call(http.MethodPost, "/inventory/goods-receipts/"+grnID+"/post", nil); code != http.StatusOK {
		t.Fatalf("post receipt: %d %v", code, body)
	}
	if got := e.poStatus(po.ID); got != entpo.StatusReceived.String() {
		t.Fatalf("order status = %s, want received", got)
	}
	if l := e.poLine(po, brake); l.QuantityReceived != 90 || l.QuantityClosed != 10 {
		t.Fatalf("line received=%v closed=%v, want 90 and 10", l.QuantityReceived, l.QuantityClosed)
	}
	if got := e.onHand(brake); got != 90 {
		t.Fatalf("on hand = %v, want 90", got)
	}
}

// Closing lines when nothing at all has arrived is refused: the order is cancelled instead.
func TestGoodsReceipt_CloseWithNothingReceivedRefused(t *testing.T) {
	e := newGRNApprovalEnv(t, false)
	po, brake := e.sentPO(10)
	code, body := e.call(http.MethodPost, "/inventory/purchase-orders/"+po.ID.String()+"/goods-receipts", map[string]any{
		"lines": []map[string]any{
			{"purchase_order_line_id": e.poLine(po, brake).ID, "item_id": brake.ID, "quantity_received": 0, "close_remaining": true},
		},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("create receipt: %d %v, want 422", code, body)
	}
	if n := len(e.receipts(po.ID, "draft")); n != 0 {
		t.Fatalf("%d draft receipts left behind, want 0", n)
	}
}
