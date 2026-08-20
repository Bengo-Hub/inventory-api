package stock

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidateSetItemOutletMembershipRequest(t *testing.T) {
	qty := func(v float64) *float64 { return &v }
	oneWarehouse := []uuid.UUID{uuid.New()}

	cases := []struct {
		name    string
		req     SetItemOutletMembershipRequest
		wantErr bool
	}{
		{"default hide, no flags", SetItemOutletMembershipRequest{}, false},
		{"zero-stock mode alone", SetItemOutletMembershipRequest{ZeroStockMode: true}, false},
		{"move-with-stock with a destination", SetItemOutletMembershipRequest{MoveWithStock: true, TargetWarehouseIDs: oneWarehouse}, false},
		{"move-with-stock with no destinations at all", SetItemOutletMembershipRequest{MoveWithStock: true}, true},
		{"move-with-stock AND zero-stock together", SetItemOutletMembershipRequest{MoveWithStock: true, ZeroStockMode: true, TargetWarehouseIDs: oneWarehouse}, true},
		{"move_quantity without move-with-stock", SetItemOutletMembershipRequest{MoveQuantity: qty(5)}, true},
		{"move_quantity with move-with-stock and a destination", SetItemOutletMembershipRequest{MoveWithStock: true, TargetWarehouseIDs: oneWarehouse, MoveQuantity: qty(5)}, false},
		{"move_quantity zero", SetItemOutletMembershipRequest{MoveWithStock: true, TargetWarehouseIDs: oneWarehouse, MoveQuantity: qty(0)}, true},
		{"move_quantity negative", SetItemOutletMembershipRequest{MoveWithStock: true, TargetWarehouseIDs: oneWarehouse, MoveQuantity: qty(-1)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateSetItemOutletMembershipRequest(c.req)
			if (err != nil) != c.wantErr {
				t.Errorf("ValidateSetItemOutletMembershipRequest(%+v) error = %v, wantErr %v", c.req, err, c.wantErr)
			}
		})
	}
}

func TestPlanMembershipChange(t *testing.T) {
	cases := []struct {
		name                       string
		mode                       membershipMode
		toRemoveCount, toAddCount  int
		pooledOnHand, pooledAvail  float64
		wantSourceZeroed           bool
		wantStrategy               reactivateStrategy
		wantFallback               bool
		wantPooledOnHand           float64
	}{
		{
			name: "hide default, pure uncheck", mode: modeHide,
			toRemoveCount: 1, toAddCount: 0,
			wantSourceZeroed: false, wantStrategy: reactivatePreserve, wantFallback: false,
		},
		{
			name: "hide default, pure add", mode: modeHide,
			toRemoveCount: 0, toAddCount: 1,
			wantSourceZeroed: false, wantStrategy: reactivatePreserve, wantFallback: false,
		},
		{
			name: "hide default, swap A for B", mode: modeHide,
			toRemoveCount: 1, toAddCount: 1,
			wantSourceZeroed: false, wantStrategy: reactivatePreserve, wantFallback: false,
		},
		{
			name: "zero-stock, pure uncheck discards", mode: modeZeroStock,
			toRemoveCount: 1, toAddCount: 0,
			wantSourceZeroed: true, wantStrategy: reactivateReset, wantFallback: false,
		},
		{
			name: "zero-stock, swap discards source and zeroes dest", mode: modeZeroStock,
			toRemoveCount: 1, toAddCount: 1,
			wantSourceZeroed: true, wantStrategy: reactivateReset, wantFallback: false,
		},
		{
			name: "move-with-stock, real destination pools", mode: modeMoveWithStock,
			toRemoveCount: 2, toAddCount: 1, pooledOnHand: 30, pooledAvail: 30,
			wantSourceZeroed: false, wantStrategy: reactivateAdd, wantFallback: false, wantPooledOnHand: 30,
		},
		{
			name: "move-with-stock, no destination for THIS item falls back to hide, never discards", mode: modeMoveWithStock,
			toRemoveCount: 1, toAddCount: 0, pooledOnHand: 30,
			wantSourceZeroed: false, wantStrategy: reactivatePreserve, wantFallback: true, wantPooledOnHand: 0,
		},
		{
			name: "move-with-stock, pure add (nothing dropped) behaves like a normal add", mode: modeMoveWithStock,
			toRemoveCount: 0, toAddCount: 1,
			wantSourceZeroed: false, wantStrategy: reactivateAdd, wantFallback: false, wantPooledOnHand: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planMembershipChange(c.mode, c.toRemoveCount, c.toAddCount, c.pooledOnHand, c.pooledAvail)
			if got.SourceZeroed != c.wantSourceZeroed {
				t.Errorf("SourceZeroed = %v, want %v", got.SourceZeroed, c.wantSourceZeroed)
			}
			if got.TargetStrategy != c.wantStrategy {
				t.Errorf("TargetStrategy = %v, want %v", got.TargetStrategy, c.wantStrategy)
			}
			if got.Fallback != c.wantFallback {
				t.Errorf("Fallback = %v, want %v", got.Fallback, c.wantFallback)
			}
			if got.PooledOnHand != c.wantPooledOnHand {
				t.Errorf("PooledOnHand = %v, want %v", got.PooledOnHand, c.wantPooledOnHand)
			}
		})
	}
}

func TestJoinWarehouseNames(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	names := map[uuid.UUID]string{a: "BOI ENTERPRISES", b: "JUNIOR WHOLESALERS"}

	if got := joinWarehouseNames([]uuid.UUID{a, b}, names); got != "BOI ENTERPRISES, JUNIOR WHOLESALERS" {
		t.Errorf("joinWarehouseNames = %q", got)
	}
	if got := joinWarehouseNames(nil, names); got != "another outlet" {
		t.Errorf("joinWarehouseNames(nil) = %q, want fallback phrase", got)
	}
	unknown := uuid.New()
	if got := joinWarehouseNames([]uuid.UUID{unknown}, names); got != "another outlet" {
		t.Errorf("joinWarehouseNames(unknown) = %q, want fallback phrase", got)
	}
}
