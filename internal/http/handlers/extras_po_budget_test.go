package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/platform/treasury"
)

type fakeBudgetChecker struct {
	res  *treasury.BudgetCheck
	err  error
	seen treasury.PurchaseBudgetInput
}

func (f *fakeBudgetChecker) CheckPurchaseBudget(_ context.Context, _ uuid.UUID, in treasury.PurchaseBudgetInput) (*treasury.BudgetCheck, error) {
	f.seen = in
	return f.res, f.err
}

func testPO() *ent.PurchaseOrder {
	od := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	proj := uuid.New()
	return &ent.PurchaseOrder{ID: uuid.New(), PoNumber: "PO-7", TotalAmount: 5000, Currency: "KES", OrderDate: &od, ProjectID: &proj}
}

func TestCheckPOBudget(t *testing.T) {
	tenant := uuid.New()
	cases := []struct {
		name     string
		checker  *fakeBudgetChecker
		wantOK   bool
		wantCode int
	}{
		{"no checker wired", nil, true, 0},
		{"ok", &fakeBudgetChecker{res: &treasury.BudgetCheck{Action: "ok"}}, true, 0},
		{"warn lets it through", &fakeBudgetChecker{res: &treasury.BudgetCheck{Action: "warn"}}, true, 0},
		{"stop blocks with 409", &fakeBudgetChecker{res: &treasury.BudgetCheck{Action: "stop"}}, false, http.StatusConflict},
		{"treasury down fails open", &fakeBudgetChecker{err: errors.New("timeout")}, true, 0},
	}
	for _, tc := range cases {
		h := &InventoryExtrasHandler{log: zap.NewNop()}
		if tc.checker != nil {
			h.SetBudgetChecker(tc.checker)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/po/send", nil)
		po := testPO()
		_, ok := h.checkPOBudget(w, r, tenant, po)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
		}
		if tc.wantCode != 0 && w.Code != tc.wantCode {
			t.Errorf("%s: status = %d, want %d", tc.name, w.Code, tc.wantCode)
		}
		if tc.checker != nil && tc.checker.err == nil {
			if tc.checker.seen.POID != po.ID || tc.checker.seen.NetAmount != 5000 || !tc.checker.seen.Date.Equal(*po.OrderDate) {
				t.Errorf("%s: checker got %+v", tc.name, tc.checker.seen)
			}
		}
	}
}

func TestStopWithoutPermissionIsNotOverridden(t *testing.T) {
	h := &InventoryExtrasHandler{log: zap.NewNop()}
	h.SetBudgetChecker(&fakeBudgetChecker{res: &treasury.BudgetCheck{Action: "stop"}})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/po/send?override_budget=true", nil) // no claims
	if _, ok := h.checkPOBudget(w, r, uuid.New(), testPO()); ok || w.Code != http.StatusConflict {
		t.Fatalf("an override request without permission must still stop, got ok=%v code=%d", ok, w.Code)
	}
}

func TestPOBudgetPayload(t *testing.T) {
	po := testPO()
	p := poBudgetPayload(uuid.New(), po)
	if p["order_date"] != "2026-03-04" || p["project_id"] != po.ProjectID.String() || p["subtotal"] != 5000.0 || p["currency"] != "KES" {
		t.Fatalf("payload: %+v", p)
	}
	po.OrderDate, po.ProjectID = nil, nil
	p = poBudgetPayload(uuid.New(), po)
	if _, has := p["order_date"]; has {
		t.Error("no order date, no order_date key")
	}
	if _, has := p["project_id"]; has {
		t.Error("no project, no project_id key")
	}
}
