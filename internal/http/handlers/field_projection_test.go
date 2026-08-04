package handlers

import "testing"

type projTestRow struct {
	ID          string  `json:"id"`
	SKU         string  `json:"sku"`
	Name        string  `json:"name"`
	CostPrice   float64 `json:"cost_price"`
	SellingPrice float64 `json:"selling_price"`
}

func TestProjectFields_EmptyFieldsReturnsNil(t *testing.T) {
	rows := []projTestRow{{ID: "1", SKU: "SKU1", Name: "Item 1"}}
	out, err := projectFields(rows, nil, "id", "sku")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil (caller should skip projection), got %v", out)
	}
}

func TestProjectFields_KeepsRequestedAndAlwaysKeepColumns(t *testing.T) {
	rows := []projTestRow{
		{ID: "1", SKU: "SKU1", Name: "Item 1", CostPrice: 100, SellingPrice: 150},
		{ID: "2", SKU: "SKU2", Name: "Item 2", CostPrice: 200, SellingPrice: 300},
	}
	out, err := projectFields(rows, []string{"name", "selling_price"}, "id", "sku")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(out))
	}
	row := out[0]
	for _, want := range []string{"id", "sku", "name", "selling_price"} {
		if _, ok := row[want]; !ok {
			t.Errorf("expected key %q to be present, row=%v", want, row)
		}
	}
	if _, ok := row["cost_price"]; ok {
		t.Errorf("expected cost_price to be pruned (not requested), row=%v", row)
	}
	if row["name"] != "Item 1" {
		t.Errorf("expected name=Item 1, got %v", row["name"])
	}
	if row["sku"] != "SKU1" {
		t.Errorf("expected always-keep sku=SKU1, got %v", row["sku"])
	}
}

func TestProjectFields_UnknownFieldIgnored(t *testing.T) {
	rows := []projTestRow{{ID: "1", SKU: "SKU1", Name: "Item 1"}}
	out, err := projectFields(rows, []string{"does_not_exist"}, "id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 row, got %d", len(out))
	}
	if len(out[0]) != 1 {
		t.Errorf("expected only the always-keep key to survive, got %v", out[0])
	}
}
