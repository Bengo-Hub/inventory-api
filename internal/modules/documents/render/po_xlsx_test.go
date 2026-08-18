package render

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestRenderPurchaseOrderXLSX_StructureAndTotals(t *testing.T) {
	doc := &PurchaseOrderDoc{
		Branding:      Branding{CompanyName: "Urban Loft Cafe"},
		PONumber:      "PO-0042",
		Date:          "17 August 2026",
		Currency:      "KES",
		SupplierName:  "ACME Supplies",
		WarehouseName: "Main Store",
		Items: []DocLine{
			{Desc: "Coffee Beans 1kg", SubDesc: "SKU-001", Unit: "BAG", Qty: "10", Rate: "1,200.00", Amount: "12,000.00"},
		},
		Subtotal: "12,000.00",
		Grand:    "12,000.00",
	}
	b, err := RenderPurchaseOrderXLSX(doc)
	if err != nil {
		t.Fatalf("RenderPurchaseOrderXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("re-open generated xlsx: %v", err)
	}
	defer f.Close()
	rows, err := f.GetRows(f.GetSheetList()[0])
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}
	var headerRow, grandRow []string
	for _, row := range rows {
		if len(row) > 0 && row[0] == "#" {
			headerRow = row
		}
		if len(row) > 0 && row[0] == "Grand Total" {
			grandRow = row
		}
	}
	want := []string{"#", "DESCRIPTION", "UNIT", "QTY", "RATE", "AMOUNT (KES)"}
	if len(headerRow) != len(want) {
		t.Fatalf("header row = %v, want %v", headerRow, want)
	}
	if grandRow == nil {
		t.Fatalf("did not find the Grand Total row in %v", rows)
	}
	if got := grandRow[len(grandRow)-1]; got != "KES 12,000.00" {
		t.Fatalf("grand total = %q, want %q", got, "KES 12,000.00")
	}
}

func TestRenderPurchaseOrderCSV_Basic(t *testing.T) {
	doc := &PurchaseOrderDoc{
		Branding:     Branding{CompanyName: "Urban Loft Cafe"},
		PONumber:     "PO-0042",
		Currency:     "KES",
		SupplierName: "ACME Supplies",
		Items:        []DocLine{{Desc: "Coffee Beans 1kg", Qty: "10", Rate: "1,200.00", Amount: "12,000.00"}},
		Subtotal:     "12,000.00",
		Grand:        "12,000.00",
	}
	if _, err := RenderPurchaseOrderCSV(doc); err != nil {
		t.Fatalf("RenderPurchaseOrderCSV: %v", err)
	}
}
