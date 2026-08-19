package render

import (
	"bytes"
	"encoding/csv"
	"testing"
)

// TestRenderPurchaseReturnCSV_NumberedColumnsAlignWithCells is a regression test for the same
// off-by-one bug covered on the XLSX side (see simpledoc_xlsx_test.go's
// TestRenderGoodsReceiptXLSX_StructureAndTotals): a numbered table's "#" column used to shift
// every data column over by one, so DESCRIPTION showed the QTY value, QTY showed UNIT PRICE, and
// AMOUNT read past the end of the row and came back blank. Uses all-distinct values per line so a
// shift can't hide behind two columns happening to hold the same number.
func TestRenderPurchaseReturnCSV_NumberedColumnsAlignWithCells(t *testing.T) {
	doc := &PurchaseReturnDoc{
		Branding:     Branding{CompanyName: "Urban Loft Cafe"},
		ReturnNumber: "PR-0007",
		Currency:     "KES",
		SupplierName: "ACME Supplies",
		Items: []PurchaseReturnDocLine{
			{Desc: "Broken Blender", SubDesc: "SKU-777", Qty: "3", UnitPrice: "1500.00", Amount: "4500.00"},
		},
		ReturnAmount: "4,500.00",
	}
	b, err := RenderPurchaseReturnCSV(doc)
	if err != nil {
		t.Fatalf("RenderPurchaseReturnCSV: %v", err)
	}
	rows, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatalf("re-parse generated csv: %v", err)
	}

	var itemRow []string
	for _, row := range rows {
		if len(row) > 0 && row[0] == "1" {
			itemRow = row
			break
		}
	}
	if itemRow == nil {
		t.Fatalf("did not find the item row (starting with \"1\") in %v", rows)
	}
	want := []string{"1", "Broken Blender  -  SKU-777", "3", "1500.00", "4500.00"}
	if len(itemRow) != len(want) {
		t.Fatalf("item row = %v, want %v", itemRow, want)
	}
	for i, w := range want {
		if itemRow[i] != w {
			t.Fatalf("item row column %d = %q, want %q — full row: %v", i, itemRow[i], w, itemRow)
		}
	}
}
