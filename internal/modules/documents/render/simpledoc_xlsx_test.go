package render

import (
	"bytes"
	"encoding/csv"
	"testing"

	"github.com/xuri/excelize/v2"
)

func sampleGoodsReceiptDoc() *GoodsReceiptDoc {
	return &GoodsReceiptDoc{
		Branding:            Branding{CompanyName: "Urban Loft Cafe", Address: []string{"Nairobi"}},
		GrnNumber:           "GRN-0001",
		Date:                "17 August 2026",
		Status:              "completed",
		Currency:            "KES",
		PurchaseOrderNumber: "PO-0099",
		SupplierName:        "ACME Supplies",
		WarehouseName:       "Main Store",
		Items: []GoodsReceiptDocLine{
			{Desc: "Coffee Beans 1kg", SubDesc: "SKU-001", OrderedQty: "10", ReceivedQty: "10", AcceptedQty: "10", RejectedQty: "0", UnitCost: "1,200.00", Amount: "12,000.00"},
			{Desc: "Milk 1L", SubDesc: "SKU-002", OrderedQty: "20", ReceivedQty: "18", AcceptedQty: "17", RejectedQty: "1", UnitCost: "150.00", Amount: "2,550.00"},
		},
		TotalReceivedValue: "14,550.00",
		Notes:              []string{"One crate of milk arrived damaged."},
		PreparedBy:         "J. Otieno",
	}
}

func TestRenderGoodsReceiptXLSX_StructureAndTotals(t *testing.T) {
	doc := sampleGoodsReceiptDoc()
	b, err := RenderGoodsReceiptXLSX(doc)
	if err != nil {
		t.Fatalf("RenderGoodsReceiptXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("re-open generated xlsx: %v", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) != 1 || sheets[0] != "Goods Received Note" {
		t.Fatalf("sheets = %v, want exactly [Goods Received Note]", sheets)
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}

	var headerRow, grandRow []string
	var itemRows [][]string
	headerSeen := false
	for _, row := range rows {
		if len(row) > 0 && row[0] == "#" {
			headerRow = row
			headerSeen = true
			continue
		}
		if len(row) > 0 && row[0] == "Total Received Value" {
			grandRow = row
			continue
		}
		if headerSeen && grandRow == nil && len(row) > 1 && (row[0] == "1" || row[0] == "2") {
			itemRows = append(itemRows, row)
		}
	}
	wantHeader := []string{"#", "DESCRIPTION", "ORDERED", "RECEIVED", "ACCEPTED", "REJECTED", "UNIT COST", "AMOUNT (KES)"}
	if len(headerRow) != len(wantHeader) {
		t.Fatalf("header row = %v, want %v", headerRow, wantHeader)
	}
	for i, w := range wantHeader {
		if headerRow[i] != w {
			t.Fatalf("header[%d] = %q, want %q", i, headerRow[i], w)
		}
	}
	if grandRow == nil {
		t.Fatalf("did not find the 'Total Received Value' grand-total row in %v", rows)
	}
	if got := grandRow[len(grandRow)-1]; got != "KES 14,550.00" {
		t.Fatalf("grand total value = %q, want %q", got, "KES 14,550.00")
	}

	// Regression test for a real off-by-one bug: the numbered "#" column shifted every data
	// column over by one, so DESCRIPTION showed the ORDERED quantity, ORDERED showed RECEIVED,
	// and so on — invisible on line 1 (10/10/10/0) where every shifted-in value happens to match,
	// but not on line 2, whose six numbers (20/18/17/1/150.00/2,550.00) are all distinct.
	if len(itemRows) != 2 {
		t.Fatalf("expected 2 item rows, got %d: %v", len(itemRows), itemRows)
	}
	row2 := itemRows[1]
	wantRow2 := []string{"2", "Milk 1L\r\nSKU-002", "20", "18", "17", "1", "150", "2550"}
	for i, want := range wantRow2 {
		if row2[i] != want {
			t.Fatalf("item row 2, column %d (%s) = %q, want %q — full row: %v", i, wantHeader[i], row2[i], want, row2)
		}
	}
}

func sampleRFQDoc() *RFQDoc {
	return &RFQDoc{
		Branding:  Branding{CompanyName: "Urban Loft Cafe"},
		RFQNumber: "RFQ-0007",
		Title:     "Kitchen Equipment",
		Date:      "17 August 2026",
		Items: []RFQDocLine{
			{Desc: "Commercial Blender", SubDesc: "2000W", Uom: "PCS", Qty: "2"},
		},
		Quotes: []RFQDocQuote{
			{SupplierName: "Kitchen Pro Ltd", Status: "submitted", Currency: "KES", QuotedTotal: "45,000.00", LeadTime: "5 days", Awarded: true},
			{SupplierName: "Catering World", Status: "submitted", Currency: "KES", QuotedTotal: "48,000.00", LeadTime: "7 days"},
		},
		PreparedBy: "M. Wanjiru",
	}
}

// TestRenderRFQXLSX_AppendixDoesNotClobberFirstColumn is a regression test for the bug the
// generic table renderer could have here: the appendix table (supplier quotations) is NOT
// numbered, so its first column ("SUPPLIER") must keep the real supplier names — not get
// overwritten with an auto row-index the way the numbered main item table's "#" column is.
func TestRenderRFQXLSX_AppendixDoesNotClobberFirstColumn(t *testing.T) {
	doc := sampleRFQDoc()
	b, err := RenderRFQXLSX(doc)
	if err != nil {
		t.Fatalf("RenderRFQXLSX: %v", err)
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

	var appendixHeader []string
	var supplierRows [][]string
	appendixIdx := -1
	for i, row := range rows {
		if len(row) > 0 && row[0] == "SUPPLIER" {
			appendixHeader = row
			appendixIdx = i
			break
		}
	}
	if appendixHeader == nil {
		t.Fatalf("did not find the appendix header row (SUPPLIER ...) in %v", rows)
	}
	// Exactly len(doc.Quotes) rows follow the appendix header — grab no more than that so a
	// loosely-bounded scan can't wander into the notes/signature rows below and produce a
	// confusing false failure.
	for i := appendixIdx + 1; i < len(rows) && len(supplierRows) < len(doc.Quotes); i++ {
		supplierRows = append(supplierRows, rows[i])
	}
	if len(supplierRows) != 2 {
		t.Fatalf("expected 2 supplier rows in the appendix, got %d: %v", len(supplierRows), supplierRows)
	}
	got := map[string]bool{supplierRows[0][0]: true, supplierRows[1][0]: true}
	if !got["Kitchen Pro Ltd  [AWARDED]"] || !got["Catering World"] {
		t.Fatalf("appendix supplier names = %v, want both supplier names intact (not row indices)", supplierRows)
	}
}

func TestRenderRFQCSV_UniformWidth(t *testing.T) {
	doc := sampleRFQDoc()
	b, err := RenderRFQCSV(doc)
	if err != nil {
		t.Fatalf("RenderRFQCSV: %v", err)
	}
	if _, err := csv.NewReader(bytes.NewReader(b)).ReadAll(); err != nil {
		t.Fatalf("re-parse generated csv with strict reader: %v", err)
	}
}

// TestRenderBundleSpecXLSX_SingleSignatureOnly checks a doc with only LeftSigLabel set (no
// RightSigLabel) doesn't panic or draw a stray right-hand block.
func TestRenderBundleSpecXLSX_SingleSignatureOnly(t *testing.T) {
	doc := &BundleSpecDoc{
		Branding:   Branding{CompanyName: "Urban Loft Cafe"},
		BundleName: "Breakfast Combo",
		ItemName:   "Breakfast Combo",
		Components: []BundleSpecDocLine{
			{Desc: "Coffee", Kind: "ITEM", Qty: "1", Unit: "CUP"},
			{Desc: "Croissant", Kind: "ITEM", Qty: "1", Unit: "PCS"},
		},
	}
	if _, err := RenderBundleSpecXLSX(doc); err != nil {
		t.Fatalf("RenderBundleSpecXLSX: %v", err)
	}
	if _, err := RenderBundleSpecCSV(doc); err != nil {
		t.Fatalf("RenderBundleSpecCSV: %v", err)
	}
}

// TestAllSimpleDocXLSXRenderersProduceValidWorkbooks is a broad smoke test: every one of the
// 7 simpleDoc-based document types renders to a workbook that re-opens cleanly.
func TestAllSimpleDocXLSXRenderersProduceValidWorkbooks(t *testing.T) {
	cases := []struct {
		name string
		gen  func() ([]byte, error)
	}{
		{"GoodsReceipt", func() ([]byte, error) { return RenderGoodsReceiptXLSX(sampleGoodsReceiptDoc()) }},
		{"Requisition", func() ([]byte, error) {
			return RenderRequisitionXLSX(&RequisitionDoc{
				Branding: Branding{CompanyName: "Urban Loft Cafe"}, ReferenceNumber: "REQ-01",
				Items: []RequisitionDocLine{{Desc: "Napkins", Qty: "50"}},
			})
		}},
		{"RFQ", func() ([]byte, error) { return RenderRFQXLSX(sampleRFQDoc()) }},
		{"PurchaseReturn", func() ([]byte, error) {
			return RenderPurchaseReturnXLSX(&PurchaseReturnDoc{
				Branding: Branding{CompanyName: "Urban Loft Cafe"}, ReturnNumber: "PR-01",
				Items:        []PurchaseReturnDocLine{{Desc: "Broken Blender", Qty: "1", UnitPrice: "5,000.00", Amount: "5,000.00"}},
				ReturnAmount: "5,000.00",
			})
		}},
		{"StockAdjustment", func() ([]byte, error) {
			return RenderStockAdjustmentXLSX(&StockAdjustmentDoc{
				Branding: Branding{CompanyName: "Urban Loft Cafe"}, Reference: "ADJ-01",
				Items: []StockAdjustmentDocLine{{Desc: "Sugar 1kg", Before: "20", Change: "-2", After: "18", Reason: "damaged"}},
			})
		}},
		{"StockCount", func() ([]byte, error) {
			return RenderStockCountXLSX(&StockCountDoc{
				Branding: Branding{CompanyName: "Urban Loft Cafe"}, Reference: "SC-01", Mode: StockCountModeVariance,
				Items: []StockCountDocLine{{Desc: "Sugar 1kg", SystemQty: "20", CountedQty: "18", Variance: "-2"}},
			})
		}},
		{"BundleSpec", func() ([]byte, error) {
			return RenderBundleSpecXLSX(&BundleSpecDoc{
				Branding: Branding{CompanyName: "Urban Loft Cafe"}, BundleName: "Combo",
				Components: []BundleSpecDocLine{{Desc: "Coffee", Qty: "1"}},
			})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := c.gen()
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			f, err := excelize.OpenReader(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("%s: re-open generated xlsx: %v", c.name, err)
			}
			defer f.Close()
			if len(f.GetSheetList()) != 1 {
				t.Fatalf("%s: expected exactly 1 sheet, got %v", c.name, f.GetSheetList())
			}
		})
	}
}
