package items

import (
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/modules/stockcalc"
)

// TestAvailabilityForTracksStockByKind pins which items report a stock figure and which never
// run out: a menu recipe with no BOM, a service and a voucher must come back with
// tracks_stock=false, never as a 0 that reads as out of stock.
func TestAvailabilityForTracksStockByKind(t *testing.T) {
	itemOf := func(typ item.Type) *ent.Item { return &ent.Item{ID: uuid.New(), Sku: string(typ), Type: typ} }
	zero := &balanceTotal{available: 0, rows: 1}
	five := &balanceTotal{onHand: 5, available: 5, rows: 1}

	cases := []struct {
		name       string
		itm        *ent.Item
		rs         *recipeStock
		own        *balanceTotal
		wantTracks bool
		wantAvail  float64
	}{
		{"menu recipe with no recipe", itemOf(item.TypeRECIPE), nil, zero, false, 0},
		{"menu recipe with an empty BOM", itemOf(item.TypeRECIPE), &recipeStock{}, zero, false, 0},
		{"menu recipe nothing constrains", itemOf(item.TypeRECIPE), &recipeStock{hasBOM: true, portions: stockcalc.Unlimited}, nil, false, 0},
		{"menu recipe with portions", itemOf(item.TypeRECIPE), &recipeStock{hasBOM: true, portions: 4}, zero, true, 4},
		{"menu recipe out of ingredients", itemOf(item.TypeRECIPE), &recipeStock{hasBOM: true, portions: 0}, nil, true, 0},
		{"production recipe reads its balance", itemOf(item.TypeRECIPE), &recipeStock{production: true}, five, true, 5},
		{"service with a stray zero row", itemOf(item.TypeSERVICE), nil, zero, false, 0},
		{"voucher", itemOf(item.TypeVOUCHER), nil, nil, false, 0},
		{"goods with no row", itemOf(item.TypeGOODS), nil, nil, true, 0},
		{"ingredient in stock", itemOf(item.TypeINGREDIENT), nil, five, true, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := availabilityFor(tc.itm, tc.rs, tc.own)
			if got.TracksStock != tc.wantTracks || got.Available != tc.wantAvail {
				t.Fatalf("got tracks_stock=%v available=%v, want %v and %v", got.TracksStock, got.Available, tc.wantTracks, tc.wantAvail)
			}
		})
	}
}

// TestHoldsOwnStock is the rule the inventory summary's out-of-stock count applies: services,
// vouchers and menu recipes are never counted, production recipes are.
func TestHoldsOwnStock(t *testing.T) {
	cases := []struct {
		typ        item.Type
		production bool
		want       bool
	}{
		{item.TypeGOODS, false, true},
		{item.TypeINGREDIENT, false, true},
		{item.TypeEQUIPMENT, false, true},
		{item.TypeSERVICE, false, false},
		{item.TypeVOUCHER, false, false},
		{item.TypeRECIPE, false, false},
		{item.TypeRECIPE, true, true},
	}
	for _, tc := range cases {
		if got := holdsOwnStock(tc.typ, tc.production); got != tc.want {
			t.Errorf("holdsOwnStock(%s, production=%v) = %v, want %v", tc.typ, tc.production, got, tc.want)
		}
	}
}

// TestBalanceTotalSumsWarehouses: an item in two warehouses reports the sum, with no single
// warehouse id.
func TestBalanceTotalSumsWarehouses(t *testing.T) {
	var b balanceTotal
	b.add(&ent.InventoryBalance{WarehouseID: uuid.New(), OnHand: 4, Available: 3, Reserved: 1})
	if b.warehouseID == uuid.Nil {
		t.Fatal("one row: want its warehouse id")
	}
	b.add(&ent.InventoryBalance{WarehouseID: uuid.New(), OnHand: 2, Available: 2})
	if b.available != 5 || b.onHand != 6 || b.reserved != 1 || b.warehouseID != uuid.Nil {
		t.Fatalf("got %+v, want available 5, on hand 6, reserved 1, no warehouse id", b)
	}
}
