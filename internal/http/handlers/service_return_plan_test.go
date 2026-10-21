package handlers

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	entpr "github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	entstockadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

func ptr[T any](v T) *T { return &v }

func TestParseServiceInput(t *testing.T) {
	cases := []struct {
		name    string
		req     purchaseReturnPayload
		wantErr string
	}{
		{"provider required", purchaseReturnPayload{}, "SERVICE_PROVIDER_REQUIRED"},
		{"blank provider", purchaseReturnPayload{ServiceProvider: "   "}, "SERVICE_PROVIDER_REQUIRED"},
		{"valid minimal", purchaseReturnPayload{ServiceProvider: "Calcare Centre"}, ""},
		{"valid with date", purchaseReturnPayload{ServiceProvider: "Juma Electronics", ExpectedBackDate: ptr("2026-11-05")}, ""},
		{"bad date", purchaseReturnPayload{ServiceProvider: "X", ExpectedBackDate: ptr("05/11/2026")}, "INVALID_EXPECTED_DATE"},
		{"blank date ignored", purchaseReturnPayload{ServiceProvider: "X", ExpectedBackDate: ptr("  ")}, ""},
	}
	for _, c := range cases {
		in, err := parseServiceInput(c.req)
		if c.wantErr != "" {
			if err == nil || err.code != c.wantErr {
				t.Fatalf("%s: err = %+v, want %s", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || in == nil {
			t.Fatalf("%s: unexpected error %+v", c.name, err)
		}
	}

	long := make([]rune, maxServiceFaultLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := parseServiceInput(purchaseReturnPayload{ServiceProvider: "X", Fault: string(long)}); err == nil || err.code != "SERVICE_TEXT_TOO_LONG" {
		t.Fatalf("over-long fault must be rejected, got %+v", err)
	}

	in, _ := parseServiceInput(purchaseReturnPayload{ServiceProvider: " Calcare ", ServiceContact: " 0700 ", Fault: " swollen battery ", ExpectedBackDate: ptr("2026-11-05")})
	if in.Provider != "Calcare" || in.Contact != "0700" || in.Fault != "swollen battery" {
		t.Fatalf("fields must be trimmed: %+v", in)
	}
	if in.Expected == nil || in.Expected.Format("2006-01-02") != "2026-11-05" {
		t.Fatalf("expected date lost: %+v", in.Expected)
	}
	m := in.metadata()
	if m["service_provider"] != "Calcare" || m["service_contact"] != "0700" || m["fault"] != "swollen battery" {
		t.Fatalf("metadata = %v", m)
	}
	if only := (serviceInput{Provider: "P"}).metadata(); len(only) != 1 {
		t.Fatalf("empty contact/fault must not be stored: %v", only)
	}
	if serviceProviderOf(m) != "Calcare" || serviceProviderOf(nil) != "" {
		t.Fatal("serviceProviderOf mismatch")
	}
}

func lineStates(qty ...float64) []serviceLineState {
	out := make([]serviceLineState, len(qty))
	for i, q := range qty {
		out[i] = serviceLineState{ID: uuid.New(), Quantity: q}
	}
	return out
}

func TestPlanServiceReceive(t *testing.T) {
	st := lineStates(5, 2)
	a, b := st[0].ID, st[1].ID

	// happy path: partial on one line, blank row ignored
	entries, err := planServiceReceive(st, []serviceReceiveLine{
		{LineID: a, ReturnedQty: 3, WrittenOffQty: 1},
		{LineID: b},
	})
	if err != nil || len(entries) != 1 || entries[0].Returned != 3 || entries[0].WrittenOff != 1 {
		t.Fatalf("entries = %+v err = %+v", entries, err)
	}

	bad := []struct {
		name string
		in   []serviceReceiveLine
		code string
	}{
		{"unknown line", []serviceReceiveLine{{LineID: uuid.New(), ReturnedQty: 1}}, "UNKNOWN_LINE"},
		{"duplicate line", []serviceReceiveLine{{LineID: a, ReturnedQty: 1}, {LineID: a, ReturnedQty: 1}}, "DUPLICATE_LINE"},
		{"negative", []serviceReceiveLine{{LineID: a, ReturnedQty: -1}}, "INVALID_QUANTITY"},
		{"negative write-off", []serviceReceiveLine{{LineID: a, WrittenOffQty: -0.5}}, "INVALID_QUANTITY"},
		{"over receive", []serviceReceiveLine{{LineID: a, ReturnedQty: 5, WrittenOffQty: 1}}, "OVER_RECEIVE"},
		{"over receive on small line", []serviceReceiveLine{{LineID: b, ReturnedQty: 2.5}}, "OVER_RECEIVE"},
		{"nothing", []serviceReceiveLine{{LineID: a}}, "NOTHING_TO_RECEIVE"},
		{"empty", nil, "NOTHING_TO_RECEIVE"},
	}
	for _, c := range bad {
		if _, e := planServiceReceive(st, c.in); e == nil || e.code != c.code {
			t.Fatalf("%s: err = %+v, want %s", c.name, e, c.code)
		}
	}

	// a line that already has units settled can only take what is outstanding
	st[0].Received, st[0].WrittenOff = 3, 1
	if _, e := planServiceReceive(st, []serviceReceiveLine{{LineID: a, ReturnedQty: 2}}); e == nil || e.code != "OVER_RECEIVE" {
		t.Fatalf("only 1 unit is outstanding, got %+v", e)
	}
	if _, e := planServiceReceive(st, []serviceReceiveLine{{LineID: a, ReturnedQty: 1}}); e != nil {
		t.Fatalf("the last outstanding unit must be receivable: %+v", e)
	}
	// fractional quantities (weighed items) settle exactly despite float noise
	frac := []serviceLineState{{ID: a, Quantity: 0.3}}
	if _, e := planServiceReceive(frac, []serviceReceiveLine{{LineID: a, ReturnedQty: 0.1}}); e != nil {
		t.Fatalf("0.1 of 0.3: %+v", e)
	}
	frac[0].Received = 0.2
	if _, e := planServiceReceive(frac, []serviceReceiveLine{{LineID: a, ReturnedQty: 0.1}}); e != nil {
		t.Fatalf("0.2+0.1 must equal 0.3 exactly after rounding: %+v", e)
	}
}

func TestServiceStatusFor(t *testing.T) {
	mk := func(q, r, w float64) serviceLineState { return serviceLineState{ID: uuid.New(), Quantity: q, Received: r, WrittenOff: w} }
	cases := []struct {
		name   string
		states []serviceLineState
		want   entpr.ServiceStatus
	}{
		{"nothing back", []serviceLineState{mk(3, 0, 0), mk(2, 0, 0)}, entpr.ServiceStatusOut},
		{"some back", []serviceLineState{mk(3, 1, 0), mk(2, 0, 0)}, entpr.ServiceStatusPartial},
		{"some written off only", []serviceLineState{mk(3, 0, 1)}, entpr.ServiceStatusPartial},
		{"all back", []serviceLineState{mk(3, 3, 0), mk(2, 2, 0)}, entpr.ServiceStatusReturned},
		{"all settled mixed", []serviceLineState{mk(3, 2, 1)}, entpr.ServiceStatusReturned},
		{"none repaired", []serviceLineState{mk(3, 0, 3), mk(2, 0, 2)}, entpr.ServiceStatusWrittenOff},
		{"fraction noise", []serviceLineState{mk(0.3, 0.1+0.2, 0)}, entpr.ServiceStatusReturned},
	}
	for _, c := range cases {
		if got := serviceStatusFor(c.states); got != c.want {
			t.Fatalf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestApplyReceive(t *testing.T) {
	st := lineStates(5, 2)
	entries, _ := planServiceReceive(st, []serviceReceiveLine{{LineID: st[0].ID, ReturnedQty: 2, WrittenOffQty: 1}})
	after := applyReceive(st, entries)
	if after[0].Received != 2 || after[0].WrittenOff != 1 || after[0].outstanding() != 2 {
		t.Fatalf("line 0 = %+v", after[0])
	}
	if after[1] != st[1] {
		t.Fatalf("untouched line changed: %+v", after[1])
	}
	if serviceStatusFor(after) != entpr.ServiceStatusPartial {
		t.Fatal("status after partial receive must be partial")
	}
}

func TestOverdueAndToday(t *testing.T) {
	today := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	tomorrow := today.AddDate(0, 0, 1)
	out, partial, returned := entpr.ServiceStatusOut, entpr.ServiceStatusPartial, entpr.ServiceStatusReturned

	if !isOverdue(&out, &yesterday, today) || !isOverdue(&partial, &yesterday, today) {
		t.Fatal("an open order past its date is overdue")
	}
	if isOverdue(&out, &today, today) {
		t.Fatal("due today is not overdue yet")
	}
	if isOverdue(&out, &tomorrow, today) || isOverdue(&out, nil, today) || isOverdue(nil, &yesterday, today) {
		t.Fatal("future date, no date or no status is never overdue")
	}
	if isOverdue(&returned, &yesterday, today) {
		t.Fatal("a closed order is never overdue")
	}

	now := time.Date(2026, 10, 21, 23, 30, 0, 0, time.UTC)
	if got := resolveToday("2026-10-22", now); got.Format("2006-01-02") != "2026-10-22" {
		t.Fatalf("explicit local day ignored: %s", got)
	}
	if got := resolveToday("", now); got.Format("2006-01-02") != "2026-10-21" || got.Hour() != 0 {
		t.Fatalf("default must be the UTC midnight of now: %s", got)
	}
	if got := resolveToday("garbage", now); got.Format("2006-01-02") != "2026-10-21" {
		t.Fatalf("garbage must fall back: %s", got)
	}
}

func TestReturnKindPredicates(t *testing.T) {
	today := time.Now()
	q := func(kv ...string) url.Values {
		v := url.Values{}
		for i := 0; i < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	if p, e := returnKindPredicates(q(), today); e != nil || len(p) != 0 {
		t.Fatalf("no filters = no predicates: %v %v", p, e)
	}
	if p, e := returnKindPredicates(q("destination", "service"), today); e != nil || len(p) != 1 {
		t.Fatalf("destination: %v %v", p, e)
	}
	if p, e := returnKindPredicates(q("service_status", "out, partial"), today); e != nil || len(p) != 1 {
		t.Fatalf("status list: %v %v", p, e)
	}
	if p, e := returnKindPredicates(q("overdue", "true"), today); e != nil || len(p) != 3 {
		t.Fatalf("overdue: %v %v", p, e)
	}
	if _, e := returnKindPredicates(q("destination", "moon"), today); e == nil || e.code != "INVALID_DESTINATION" {
		t.Fatalf("bad destination: %v", e)
	}
	if _, e := returnKindPredicates(q("service_status", "out,bogus"), today); e == nil || e.code != "INVALID_SERVICE_STATUS" {
		t.Fatalf("bad status: %v", e)
	}
}

func TestInverseMove(t *testing.T) {
	base := stock.AdjustStockRequest{SKU: "S1", Notes: "n", Reference: "PRET-1", WarehouseID: uuid.New(), AdjustedBy: uuid.New(), AdjustmentDate: "2026-10-20"}
	mk := func(reason entstockadj.Reason, qty float64) stockMove {
		r := base
		r.Reason, r.Adjustment = string(reason), qty
		return stockMove{Req: r, UnitCost: 120}
	}
	cases := []struct {
		name       string
		m          stockMove
		wantReason entstockadj.Reason
		wantQty    float64
		wantLayer  float64
	}{
		{"supplier return reverses as itself", mk(entstockadj.ReasonPurchaseReturn, -3), entstockadj.ReasonPurchaseReturn, 3, 0},
		{"send out reverses as receive with cost", mk(entstockadj.ReasonServiceOut, -3), entstockadj.ReasonServiceIn, 3, 120},
		{"receive reverses as send out", mk(entstockadj.ReasonServiceIn, 2), entstockadj.ReasonServiceOut, -2, 0},
		{"write-off reverses as found", mk(entstockadj.ReasonDamaged, -1), entstockadj.ReasonFound, 1, 0},
		{"anything else is a correction", mk(entstockadj.ReasonOther, -1), entstockadj.ReasonCorrection, 1, 0},
	}
	for _, c := range cases {
		got := inverseMove(c.m)
		if got.Reason != string(c.wantReason) || got.Adjustment != c.wantQty || got.CostLayerUnitCost != c.wantLayer {
			t.Fatalf("%s: got %+v", c.name, got)
		}
		if got.SKU != "S1" || got.Reference != "PRET-1" || got.WarehouseID != base.WarehouseID || got.AdjustmentDate != "2026-10-20" {
			t.Fatalf("%s: identity lost: %+v", c.name, got)
		}
		if got.Notes != "Reversal: n" {
			t.Fatalf("%s: notes = %q", c.name, got.Notes)
		}
	}
	// the reversal of a reversal returns to the original movement
	orig := mk(entstockadj.ReasonServiceOut, -3)
	back := inverseMove(stockMove{Req: inverseMove(orig), UnitCost: 120})
	if back.Reason != string(entstockadj.ReasonServiceOut) || back.Adjustment != -3 {
		t.Fatalf("double inverse = %+v", back)
	}
}

func TestRunStockMoves_CompensatesOnFailure(t *testing.T) {
	tenant := uuid.New()
	moves := []stockMove{
		{Req: stock.AdjustStockRequest{SKU: "A", Adjustment: -2, Reason: "service_out"}, UnitCost: 10},
		{Req: stock.AdjustStockRequest{SKU: "B", Adjustment: -1, Reason: "service_out"}, UnitCost: 20},
		{Req: stock.AdjustStockRequest{SKU: "C", Adjustment: -4, Reason: "service_out"}, UnitCost: 30},
	}

	// all succeed: nothing reversed
	var calls []stock.AdjustStockRequest
	ok := func(_ context.Context, tid uuid.UUID, r stock.AdjustStockRequest) (*stock.AdjustStockResponse, error) {
		if tid != tenant {
			t.Fatalf("wrong tenant %s", tid)
		}
		calls = append(calls, r)
		return &stock.AdjustStockResponse{}, nil
	}
	failed, err, undoErr := runStockMoves(context.Background(), tenant, moves, ok)
	if failed != -1 || err != nil || undoErr != nil || len(calls) != 3 {
		t.Fatalf("clean run: failed=%d err=%v undo=%v calls=%d", failed, err, undoErr, len(calls))
	}

	// third move fails: the first two are reversed, most recent first
	calls = nil
	boom := errors.New("item is a service")
	failing := func(_ context.Context, _ uuid.UUID, r stock.AdjustStockRequest) (*stock.AdjustStockResponse, error) {
		if r.SKU == "C" {
			return nil, boom
		}
		calls = append(calls, r)
		return &stock.AdjustStockResponse{}, nil
	}
	failed, err, undoErr = runStockMoves(context.Background(), tenant, moves, failing)
	if failed != 2 || !errors.Is(err, boom) || undoErr != nil {
		t.Fatalf("failed=%d err=%v undo=%v", failed, err, undoErr)
	}
	if len(calls) != 4 {
		t.Fatalf("2 applied + 2 reversals expected, got %d: %+v", len(calls), calls)
	}
	if calls[2].SKU != "B" || calls[2].Adjustment != 1 || calls[2].Reason != "service_in" || calls[2].CostLayerUnitCost != 20 {
		t.Fatalf("first reversal must undo B with its cost: %+v", calls[2])
	}
	if calls[3].SKU != "A" || calls[3].Adjustment != 2 {
		t.Fatalf("second reversal must undo A: %+v", calls[3])
	}

	// first move fails: nothing to reverse
	calls = nil
	first := func(_ context.Context, _ uuid.UUID, r stock.AdjustStockRequest) (*stock.AdjustStockResponse, error) {
		calls = append(calls, r)
		return nil, boom
	}
	failed, err, undoErr = runStockMoves(context.Background(), tenant, moves, first)
	if failed != 0 || err == nil || undoErr != nil || len(calls) != 1 {
		t.Fatalf("first-move failure: failed=%d err=%v undo=%v calls=%d", failed, err, undoErr, len(calls))
	}

	// a reversal that fails is reported, and the remaining reversals still run
	calls = nil
	brokenUndo := func(_ context.Context, _ uuid.UUID, r stock.AdjustStockRequest) (*stock.AdjustStockResponse, error) {
		calls = append(calls, r)
		switch {
		case r.SKU == "C":
			return nil, boom
		case r.SKU == "B" && r.Adjustment > 0:
			return nil, errors.New("reverse failed")
		}
		return &stock.AdjustStockResponse{}, nil
	}
	_, _, undoErr = runStockMoves(context.Background(), tenant, moves, brokenUndo)
	if undoErr == nil {
		t.Fatal("a failed reversal must be reported")
	}
	if calls[len(calls)-1].SKU != "A" || calls[len(calls)-1].Adjustment != 2 {
		t.Fatalf("A must still be reversed after B's reversal failed: %+v", calls)
	}
}

func TestRepairReasonRejection(t *testing.T) {
	for _, r := range []string{"service_out", "service_in"} {
		if e := repairReasonRejection(r); e == nil || e.code != "RESERVED_REASON" {
			t.Fatalf("%s must be rejected on manual adjustments: %+v", r, e)
		}
	}
	for _, r := range []string{"", "damaged", "found", "purchase_return", "other", "correction"} {
		if e := repairReasonRejection(r); e != nil {
			t.Fatalf("%q must stay allowed: %+v", r, e)
		}
	}
}

func TestBuildServiceSummary(t *testing.T) {
	today := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	past, future := today.AddDate(0, 0, -3), today.AddDate(0, 0, 5)
	o1, o2, o3, o4 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	orders := []serviceOrderRow{
		{ID: o1, Expected: &past, Status: entpr.ServiceStatusOut, Provider: "Calcare"},
		{ID: o2, Expected: &future, Status: entpr.ServiceStatusPartial, Provider: "Calcare"},
		{ID: o3, Expected: &past, Status: entpr.ServiceStatusOut, Provider: ""},
		{ID: o4, Expected: &past, Status: entpr.ServiceStatusOut, Provider: "Juma"}, // fully settled lines: not open
	}
	lines := []serviceLineRow{
		{ReturnID: o1, Quantity: 4, Received: 0, WrittenOff: 0, SubTotal: 400},  // 4 out, 400
		{ReturnID: o2, Quantity: 2, Received: 1, WrittenOff: 0, SubTotal: 300},  // 1 out, 150
		{ReturnID: o3, Quantity: 1, SubTotal: 50},                               // 1 out, 50
		{ReturnID: o4, Quantity: 2, Received: 1, WrittenOff: 1, SubTotal: 1000}, // nothing out
	}
	s := buildServiceSummary(orders, lines, today)
	if s.OpenOrders != 3 || s.UnitsOutstanding != 6 || s.CostOutstanding != 600 {
		t.Fatalf("totals = %+v", s)
	}
	if s.OverdueOrders != 2 || s.OverdueUnits != 5 {
		t.Fatalf("overdue = %+v", s)
	}
	if len(s.Providers) != 2 || s.Providers[0].Provider != "Calcare" || s.Providers[0].Units != 5 || s.Providers[0].Orders != 2 || s.Providers[1].Provider != "Unspecified" {
		t.Fatalf("providers = %+v", s.Providers)
	}
	if empty := buildServiceSummary(nil, nil, today); empty.OpenOrders != 0 || empty.Providers == nil {
		t.Fatalf("empty summary must be zero with a non-nil provider list: %+v", empty)
	}
}

func TestServiceStatusLabel(t *testing.T) {
	out := entpr.ServiceStatusOut
	if serviceStatusLabel(nil) != "Awaiting approval" || serviceStatusLabel(&out) != "Out for repair" {
		t.Fatal("labels")
	}
}
