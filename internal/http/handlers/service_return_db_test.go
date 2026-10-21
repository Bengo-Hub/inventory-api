package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bengo-Hub/httpware"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entlot "github.com/bengobox/inventory-service/internal/ent/inventorylot"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/outboxevent"
	entpr "github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	entprline "github.com/bengobox/inventory-service/internal/ent/purchasereturnline"
	entstockadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// repairEnv is a throwaway schema with one tenant, one warehouse and a router over the purchase
// return routes, with the real stock service behind it.
type repairEnv struct {
	t      *testing.T
	client *ent.Client
	tenant uuid.UUID
	wh     uuid.UUID
	router chi.Router
}

func newRepairEnv(t *testing.T) *repairEnv {
	t.Helper()
	client := testutil.SchemaClient(t, "inv_svc_")
	ctx := context.Background()
	tenant := uuid.New()
	client.Tenant.Create().SetID(tenant).SetName("Repair Test").SetSlug("repair-" + tenant.String()).SaveX(ctx)
	wh := client.Warehouse.Create().SetTenantID(tenant).SetName("Main Store").SetCode("MAIN").SetIsDefault(true).SaveX(ctx)

	h := &InventoryExtrasHandler{log: zap.NewNop(), orm: client, stockSvc: stock.NewService(client, zap.NewNop())}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), httpware.TenantIDKey, tenant.String())))
		})
	})
	pass := func(string) func(http.Handler) http.Handler { return func(n http.Handler) http.Handler { return n } }
	h.registerPurchaseReturnRoutes(r, pass, "add", "change")
	return &repairEnv{t: t, client: client, tenant: tenant, wh: wh.ID, router: r}
}

// item creates a stocked item (cost 100) with a balance and one cost layer of the same size.
func (e *repairEnv) item(sku string, onHand float64) *ent.Item {
	e.t.Helper()
	ctx := context.Background()
	it := e.client.Item.Create().SetTenantID(e.tenant).SetSku(sku).SetName("Phone " + sku).SetCostPrice(100).SaveX(ctx)
	e.client.InventoryBalance.Create().SetTenantID(e.tenant).SetItemID(it.ID).SetWarehouseID(e.wh).
		SetOnHand(onHand).SetAvailable(onHand).SaveX(ctx)
	e.client.InventoryLot.Create().SetTenantID(e.tenant).SetItemID(it.ID).SetWarehouseID(e.wh).
		SetLotNumber("OPEN-" + sku).SetQuantity(onHand).SetStatus("active").SetIsCostLayer(true).
		SetCostPrice(100).SetReceivedAt(time.Now().Add(-48 * time.Hour)).SaveX(ctx)
	return it
}

func (e *repairEnv) onHand(it *ent.Item) float64 {
	e.t.Helper()
	b := e.client.InventoryBalance.Query().
		Where(inventorybalance.TenantID(e.tenant), inventorybalance.ItemID(it.ID)).OnlyX(context.Background())
	return b.OnHand
}

func (e *repairEnv) call(method, path string, body any) (int, map[string]any) {
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

// events returns the stored outbox payload bodies for an event type.
func (e *repairEnv) events(eventType string) []map[string]any {
	e.t.Helper()
	rows := e.client.OutboxEvent.Query().
		Where(outboxevent.TenantID(e.tenant), outboxevent.EventTypeEQ(eventType)).AllX(context.Background())
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		var env struct {
			Payload map[string]any `json:"payload"`
		}
		_ = json.Unmarshal(r.Payload, &env)
		out = append(out, env.Payload)
	}
	return out
}

func (e *repairEnv) adjustments(it *ent.Item, reason entstockadj.Reason) int {
	e.t.Helper()
	n, err := e.client.StockAdjustment.Query().
		Where(entstockadj.TenantID(e.tenant), entstockadj.ItemID(it.ID), entstockadj.ReasonEQ(reason)).Count(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func lineIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	ls, _ := body["lines"].([]any)
	ids := make([]string, 0, len(ls))
	for _, l := range ls {
		ids = append(ids, l.(map[string]any)["id"].(string))
	}
	return ids
}

// The whole repair lifecycle: create (no supplier, no amount due, cost filled from the item),
// approve (stock out as service_out, own event only), partial receive, over-receive rejected,
// final receive with one unrepairable unit (restocked then written off as damaged), closed order
// refuses another receive. Treasury must see no supplier-return event and no GL posting for the
// repair movements.
func TestServiceReturn_FullLifecycle(t *testing.T) {
	e := newRepairEnv(t)
	ctx := context.Background()
	phone := e.item("PH-1", 10)

	code, created := e.call(http.MethodPost, "/inventory/purchase-returns", map[string]any{
		"destination": "service", "service_provider": "Calcare Centre", "service_contact": "0700 111 222",
		"fault": "swollen battery", "expected_back_date": "2026-10-01",
		"supplier_id": uuid.New().String(), // must be ignored for a repair order
		"lines":       []map[string]any{{"item_id": phone.ID.String(), "quantity": 3, "sub_total": 0}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, created)
	}
	id := created["id"].(string)
	if created["destination"] != "service" || created["service_provider"] != "Calcare Centre" || created["fault"] != "swollen battery" {
		t.Fatalf("service fields lost: %v", created)
	}
	if created["return_amount"].(float64) != 300 {
		t.Fatalf("cost must be filled from the item (3 x 100): %v", created["return_amount"])
	}
	if created["supplier_id"] != nil {
		t.Fatalf("a repair order has no supplier: %v", created["supplier_id"])
	}
	if _, has := created["service_status"]; has {
		t.Fatalf("service_status must be unset until approval: %v", created["service_status"])
	}
	row := e.client.PurchaseReturn.GetX(ctx, uuid.MustParse(id))
	if row.ReturnAmountDue != 0 || row.SupplierID != nil || row.PurchaseOrderID != nil {
		t.Fatalf("nothing is owed on a repair: due=%v supplier=%v", row.ReturnAmountDue, row.SupplierID)
	}
	if len(e.events("service_order.created")) != 1 || len(e.events("purchase_return.created")) != 0 {
		t.Fatal("a repair order publishes service_order.created only")
	}

	// receive before approval is refused
	if code, _ := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/receive", map[string]any{
		"lines": []map[string]any{{"line_id": uuid.New().String(), "returned_qty": 1}},
	}); code != http.StatusConflict {
		t.Fatalf("receive before approve = %d, want 409", code)
	}

	// approve: stock leaves as service_out
	code, approved := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/approve", nil)
	if code != http.StatusOK {
		t.Fatalf("approve = %d %v", code, approved)
	}
	if approved["service_status"] != "out" || approved["outstanding_quantity"].(float64) != 3 {
		t.Fatalf("after approve: %v", approved)
	}
	if got := e.onHand(phone); got != 7 {
		t.Fatalf("on hand after send = %v, want 7", got)
	}
	if e.adjustments(phone, entstockadj.ReasonServiceOut) != 1 || e.adjustments(phone, entstockadj.ReasonPurchaseReturn) != 0 {
		t.Fatal("the send-out must be a service_out adjustment, never purchase_return")
	}
	if len(e.events("service_order.sent")) != 1 {
		t.Fatal("approve must publish service_order.sent")
	}
	if len(e.events("purchase_return.approved")) != 0 {
		t.Fatal("a repair must NEVER publish purchase_return.approved (treasury would raise a vendor credit note and an eTIMS return)")
	}
	if len(e.events("stock.adjusted")) != 0 {
		t.Fatal("service_out must not post to the GL")
	}
	// double approve is refused and does not move stock again
	if code, _ := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/approve", nil); code != http.StatusConflict {
		t.Fatalf("second approve = %d, want 409", code)
	}
	if e.onHand(phone) != 7 {
		t.Fatal("second approve moved stock")
	}

	lid := lineIDs(t, approved)[0]

	// partial receive: 1 back
	code, body := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/receive", map[string]any{
		"lines": []map[string]any{{"line_id": lid, "returned_qty": 1}},
	})
	if code != http.StatusOK || body["service_status"] != "partial" || body["outstanding_quantity"].(float64) != 2 {
		t.Fatalf("partial receive = %d %v", code, body)
	}
	if e.onHand(phone) != 8 {
		t.Fatalf("on hand = %v, want 8", e.onHand(phone))
	}

	// more than outstanding, and a negative quantity, are refused without touching stock
	for _, bad := range []map[string]any{
		{"line_id": lid, "returned_qty": 3},
		{"line_id": lid, "returned_qty": 1, "written_off_qty": 2},
		{"line_id": lid, "returned_qty": -1},
		{"line_id": uuid.New().String(), "returned_qty": 1},
	} {
		if code, _ := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/receive", map[string]any{"lines": []map[string]any{bad}}); code != http.StatusBadRequest {
			t.Fatalf("bad receive %v = %d, want 400", bad, code)
		}
	}
	if e.onHand(phone) != 8 {
		t.Fatal("a rejected receive moved stock")
	}

	// final receive: 1 repaired + 1 unrepairable
	code, final := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/receive", map[string]any{
		"lines": []map[string]any{{"line_id": lid, "returned_qty": 1, "written_off_qty": 1}},
	})
	if code != http.StatusOK || final["service_status"] != "returned" || final["outstanding_quantity"].(float64) != 0 {
		t.Fatalf("final receive = %d %v", code, final)
	}
	// 7 + 1 + (1 back + 1 restocked then written off) - 1 written off = 9
	if got := e.onHand(phone); got != 9 {
		t.Fatalf("on hand = %v, want 9", got)
	}
	l := e.client.PurchaseReturnLine.Query().Where(entprline.PurchaseReturnID(uuid.MustParse(id))).OnlyX(ctx)
	if l.ReceivedBackQty != 2 || l.WrittenOffQty != 1 {
		t.Fatalf("line = received %v written off %v, want 2 and 1", l.ReceivedBackQty, l.WrittenOffQty)
	}
	if e.adjustments(phone, entstockadj.ReasonServiceIn) != 2 || e.adjustments(phone, entstockadj.ReasonDamaged) != 1 {
		t.Fatalf("expected 2 service_in and 1 damaged adjustments")
	}
	// the write-off is the ONLY thing that reaches the GL, valued at the layer cost
	adj := e.events("stock.adjusted")
	if len(adj) != 1 || adj[0]["reason"] != "damaged" || adj[0]["cost_value"].(float64) != 100 {
		t.Fatalf("stock.adjusted = %v, want one damaged event worth 100", adj)
	}
	if len(e.events("service_order.returned")) != 2 {
		t.Fatalf("each receive publishes service_order.returned, got %d", len(e.events("service_order.returned")))
	}
	// the returning units re-entered as their own cost layer at the snapshot cost
	lots := e.client.InventoryLot.Query().Where(entlot.TenantID(e.tenant), entlot.LotNumberHasPrefix("SVC-")).AllX(ctx)
	if len(lots) != 2 {
		t.Fatalf("expected a cost layer per receive, got %d", len(lots))
	}
	for _, lot := range lots {
		if lot.CostPrice == nil || *lot.CostPrice != 100 || !lot.IsCostLayer {
			t.Fatalf("cost layer = %+v", lot)
		}
	}

	// closed: no further receive
	if code, _ := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/receive", map[string]any{
		"lines": []map[string]any{{"line_id": lid, "returned_qty": 1}},
	}); code != http.StatusConflict {
		t.Fatalf("receive on a closed order = %d, want 409", code)
	}
	if e.onHand(phone) != 9 {
		t.Fatal("a refused receive moved stock")
	}

	// the item's stock history reads it as repair, not sale / adjustment, with the provider
	hist, err := stock.NewService(e.client, zap.NewNop()).ItemStockHistory(ctx, e.tenant, phone.Sku, stock.StockHistoryFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if hist.Summary.SentForRepair != 3 || hist.Summary.ReturnedFromRepair != 3 || hist.Summary.TotalAdjusted != -1 {
		t.Fatalf("history summary = %+v", hist.Summary)
	}
	var sawOut bool
	for _, m := range hist.Movements {
		if m.Type == "service_out" {
			sawOut = true
			if m.Label != "Sent for Repair" || m.Counterparty != "Calcare Centre" || m.Reference == "" {
				t.Fatalf("service_out row = %+v", m)
			}
		}
	}
	if !sawOut {
		t.Fatal("history is missing the send-out")
	}
}

// The classic supplier return keeps working exactly as before: credit-note event, purchase_return
// adjustment, no service fields.
func TestSupplierReturn_StillPublishesCreditEvent(t *testing.T) {
	e := newRepairEnv(t)
	ctx := context.Background()
	phone := e.item("PH-2", 5)
	sup := e.client.Supplier.Create().SetTenantID(e.tenant).SetName("ELECTROMATE").SetCode("ELEC").SaveX(ctx)

	code, created := e.call(http.MethodPost, "/inventory/purchase-returns", map[string]any{
		"supplier_id": sup.ID.String(), "reason": "dead on arrival",
		"lines": []map[string]any{{"item_id": phone.ID.String(), "quantity": 2, "sub_total": 250}},
	})
	if code != http.StatusCreated || created["destination"] != "supplier" {
		t.Fatalf("create = %d %v", code, created)
	}
	id := created["id"].(string)
	if created["return_amount"].(float64) != 250 || created["supplier_name"] != "ELECTROMATE" {
		t.Fatalf("supplier return = %v", created)
	}
	code, approved := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/approve", nil)
	if code != http.StatusOK || approved["payment_status"] != "paid" {
		t.Fatalf("approve = %d %v", code, approved)
	}
	if _, has := approved["service_status"]; has {
		t.Fatalf("a supplier return has no service status: %v", approved)
	}
	if e.onHand(phone) != 3 || e.adjustments(phone, entstockadj.ReasonPurchaseReturn) != 1 {
		t.Fatal("supplier approve must take 2 out as purchase_return")
	}
	ev := e.events("purchase_return.approved")
	if len(ev) != 1 || ev[0]["supplier_name"] != "ELECTROMATE" || ev[0]["return_amount"].(float64) != 250 {
		t.Fatalf("approved event = %v", ev)
	}
	if len(e.events("service_order.sent")) != 0 {
		t.Fatal("a supplier return must not publish a service event")
	}
	// a supplier return cannot be received back
	if code, _ := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/receive", map[string]any{
		"lines": []map[string]any{{"line_id": lineIDs(t, approved)[0], "returned_qty": 1}},
	}); code != http.StatusBadRequest {
		t.Fatalf("receive on a supplier return = %d, want 400", code)
	}
}

// Audit fix: a return is never left approved with only some items out. A SERVICE item cannot be
// stock-adjusted, so the second line fails; the first line's stock-out must be put back and the
// approval undone.
func TestApprove_AllOrNothing(t *testing.T) {
	e := newRepairEnv(t)
	ctx := context.Background()
	good := e.item("PH-3", 5)
	svcItem := e.client.Item.Create().SetTenantID(e.tenant).SetSku("SVC-FEE").SetName("Repair fee").
		SetType(entitem.TypeSERVICE).SaveX(ctx)

	code, created := e.call(http.MethodPost, "/inventory/purchase-returns", map[string]any{
		"destination": "service", "service_provider": "Care Centre",
		"lines": []map[string]any{
			{"item_id": good.ID.String(), "quantity": 1, "sub_total": 100},
			{"item_id": svcItem.ID.String(), "quantity": 1, "sub_total": 10},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, created)
	}
	id := created["id"].(string)
	code, body := e.call(http.MethodPost, "/inventory/purchase-returns/"+id+"/approve", nil)
	if code != http.StatusConflict || body["code"] != "STOCK_OUT_FAILED" {
		t.Fatalf("approve = %d %v, want 409 STOCK_OUT_FAILED", code, body)
	}
	if e.onHand(good) != 5 {
		t.Fatalf("the first item must be put back: on hand %v", e.onHand(good))
	}
	row := e.client.PurchaseReturn.GetX(ctx, uuid.MustParse(id))
	if row.PaymentStatus != entpr.PaymentStatusPending || row.ServiceStatus != nil {
		t.Fatalf("the approval must be undone: %s %v", row.PaymentStatus, row.ServiceStatus)
	}
	if len(e.events("service_order.sent")) != 0 {
		t.Fatal("no sent event for a failed approval")
	}
}

// Validation of the create request and the list filters, including the location filter that
// replaced the unbounded goods-receipt id list.
func TestServiceReturn_ValidationFiltersAndSummary(t *testing.T) {
	e := newRepairEnv(t)
	ctx := context.Background()
	phone := e.item("PH-4", 20)
	tablet := e.item("TB-1", 20)

	create := func(body map[string]any) (int, map[string]any) {
		return e.call(http.MethodPost, "/inventory/purchase-returns", body)
	}
	line := func(it *ent.Item, qty float64) []map[string]any {
		return []map[string]any{{"item_id": it.ID.String(), "quantity": qty, "sub_total": qty * 100}}
	}
	for name, body := range map[string]map[string]any{
		"no provider":     {"destination": "service", "lines": line(phone, 1)},
		"bad destination": {"destination": "moon", "service_provider": "X", "lines": line(phone, 1)},
		"bad expected":    {"destination": "service", "service_provider": "X", "expected_back_date": "soon", "lines": line(phone, 1)},
		"no lines":        {"destination": "service", "service_provider": "X", "lines": []map[string]any{}},
		"zero quantity":   {"destination": "service", "service_provider": "X", "lines": line(phone, 0)},
	} {
		if code, _ := create(body); code != http.StatusBadRequest {
			t.Fatalf("%s: create = %d, want 400", name, code)
		}
	}

	// one overdue order (Calcare, 4 phones), one on time (Juma, 2 tablets), one supplier return
	_, a := create(map[string]any{"destination": "service", "service_provider": "Calcare", "expected_back_date": "2026-10-01", "lines": line(phone, 4)})
	_, b := create(map[string]any{"destination": "service", "service_provider": "Juma", "expected_back_date": "2026-12-01", "lines": line(tablet, 2)})
	_, c := create(map[string]any{"lines": line(phone, 1)})
	for _, o := range []map[string]any{a, b} {
		if code, _ := e.call(http.MethodPost, "/inventory/purchase-returns/"+o["id"].(string)+"/approve", nil); code != http.StatusOK {
			t.Fatalf("approve = %d", code)
		}
	}

	list := func(q string) (int, []any) {
		code, body := e.call(http.MethodGet, "/inventory/purchase-returns?"+q, nil)
		data, _ := body["data"].([]any)
		return code, data
	}
	if code, d := list(""); code != http.StatusOK || len(d) != 3 {
		t.Fatalf("all = %d rows %d", code, len(d))
	}
	if _, d := list("destination=service"); len(d) != 2 {
		t.Fatalf("service only = %d", len(d))
	}
	if _, d := list("destination=supplier"); len(d) != 1 || d[0].(map[string]any)["id"] != c["id"] {
		t.Fatalf("supplier only = %v", d)
	}
	if _, d := list("service_status=out"); len(d) != 2 {
		t.Fatalf("status out = %d", len(d))
	}
	if _, d := list("service_status=returned,written_off"); len(d) != 0 {
		t.Fatalf("closed = %d", len(d))
	}
	if _, d := list("overdue=true&today=2026-10-21"); len(d) != 1 || d[0].(map[string]any)["id"] != a["id"] {
		t.Fatalf("overdue = %v", d)
	}
	if _, d := list("overdue=true&today=2026-09-01"); len(d) != 0 {
		t.Fatalf("nothing is overdue before the dates: %d", len(d))
	}
	if code, _ := list("destination=moon"); code != http.StatusBadRequest {
		t.Fatalf("bad destination filter = %d", code)
	}
	if code, _ := list("service_status=bogus"); code != http.StatusBadRequest {
		t.Fatalf("bad status filter = %d", code)
	}
	// location filter: the warehouse matches, an unknown one does not (subselect SQL is valid)
	if _, d := list("warehouse_id=" + e.wh.String()); len(d) != 3 {
		t.Fatalf("by warehouse = %d", len(d))
	}
	if _, d := list("warehouse_id=" + uuid.NewString()); len(d) != 0 {
		t.Fatalf("other warehouse = %d", len(d))
	}
	if _, d := list("outlet_id=" + uuid.NewString()); len(d) != 0 {
		t.Fatalf("outlet without warehouses = %d", len(d))
	}
	if _, d := list("search=" + strings.ToUpper(a["return_number"].(string)[:3])); len(d) == 0 {
		t.Fatal("search by return number prefix found nothing")
	}

	// summary: 6 units out worth 600, one overdue order of 4 units
	code, sum := e.call(http.MethodGet, "/inventory/purchase-returns/service-summary?today=2026-10-21", nil)
	if code != http.StatusOK {
		t.Fatalf("summary = %d %v", code, sum)
	}
	if sum["open_orders"].(float64) != 2 || sum["units_outstanding"].(float64) != 6 || sum["cost_outstanding"].(float64) != 600 ||
		sum["overdue_orders"].(float64) != 1 || sum["overdue_units"].(float64) != 4 {
		t.Fatalf("summary = %v", sum)
	}
	if provs := sum["providers"].([]any); len(provs) != 2 || provs[0].(map[string]any)["provider"] != "Calcare" {
		t.Fatalf("providers = %v", provs)
	}

	// receiving one order fully drops it from the summary
	_, ap := e.call(http.MethodGet, "/inventory/purchase-returns/"+a["id"].(string), nil)
	if code, _ := e.call(http.MethodPost, "/inventory/purchase-returns/"+a["id"].(string)+"/receive", map[string]any{
		"lines": []map[string]any{{"line_id": lineIDs(t, ap)[0], "returned_qty": 4}},
	}); code != http.StatusOK {
		t.Fatalf("receive = %d", code)
	}
	_, sum = e.call(http.MethodGet, "/inventory/purchase-returns/service-summary?today=2026-10-21", nil)
	if sum["open_orders"].(float64) != 1 || sum["overdue_orders"].(float64) != 0 || sum["units_outstanding"].(float64) != 2 {
		t.Fatalf("summary after receive = %v", sum)
	}
	if e.client.PurchaseReturn.GetX(ctx, uuid.MustParse(a["id"].(string))).ServiceStatus.String() != "returned" {
		t.Fatal("order a should be returned")
	}
}
