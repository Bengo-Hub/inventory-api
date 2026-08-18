package render

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"
)

func sampleGRNDoc() *TransferDoc {
	return &TransferDoc{
		Branding: Branding{
			CompanyName: "BOI Enterprises",
			Address:     []string{"Nairobi", "Kenya"},
			KRAPIN:      "P051565369U",
		},
		DocTitle:            "Goods Received Note",
		DocSubtitle:         "Receipt Confirmation",
		TransferNumber:      "TRF-260817-000078",
		Date:                "17 August 2026",
		Reference:           "WB-0099",
		Carrier:             "Own Fleet",
		FromWarehouseName:   "BOI Enterprises",
		FromWarehouseAddr:   []string{"Malaba HQ"},
		ToWarehouseName:     "Junior Wholesalers",
		ToWarehouseAddr:     []string{"Bungoma"},
		AcknowledgementText: "Received the following goods into Junior Wholesalers in good order and condition:",
		Items:               longTransferItems(6, true),
		Notes:               []string{"Two cartons arrived with damaged packaging."},
		LeftSigLabel:        "Delivered By",
		LeftSigName:         "J. Otieno",
		RightSigLabel:       "Received By",
	}
}

func TestRenderTransferXLSX_GRN_StructureAndTotals(t *testing.T) {
	doc := sampleGRNDoc()
	b, err := RenderTransferXLSX(doc)
	if err != nil {
		t.Fatalf("RenderTransferXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("re-open generated xlsx: %v", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) != 1 {
		t.Fatalf("expected 1 sheet, got %d: %v", len(sheets), sheets)
	}
	sheet := sheets[0]
	if sheet != "Goods Received Note" {
		t.Fatalf("sheet name = %q, want %q", sheet, "Goods Received Note")
	}

	// The item table header row should exist with the SHIPPED/RECEIVED/VARIANCE/NOTES columns
	// (mirrors the PDF renderer's drawTransferItems column set for a GRN).
	rows, err := f.GetRows(sheet)
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}
	var headerRow []string
	for _, row := range rows {
		if len(row) > 0 && row[0] == "#" {
			headerRow = row
			break
		}
	}
	if headerRow == nil {
		t.Fatalf("did not find the item table header row (# ...) in %v", rows)
	}
	want := []string{"#", "DESCRIPTION", "SKU", "UNIT", "SHIPPED", "RECEIVED", "VARIANCE", "NOTES"}
	if len(headerRow) != len(want) {
		t.Fatalf("header row = %v, want %v", headerRow, want)
	}
	for i, w := range want {
		if headerRow[i] != w {
			t.Fatalf("header[%d] = %q, want %q (full row %v)", i, headerRow[i], w, headerRow)
		}
	}

	// A TOTALS row summing shipped/received/variance across all 6 lines must be present.
	// longTransferItems(6, true) shorts every line where i%5==0 (i.e. i=0 and i=5) by 1, so
	// 2 of the 6 lines receive 4 instead of 5: shipped 6*5=30, received (4*5)+(2*4)=28, variance -2.
	var totalsRow []string
	for _, row := range rows {
		if len(row) > 0 && row[0] == "TOTALS" {
			totalsRow = row
			break
		}
	}
	if totalsRow == nil {
		t.Fatalf("did not find a TOTALS row in %v", rows)
	}
	if totalsRow[4] != "30" {
		t.Fatalf("totals SHIPPED = %q, want %q", totalsRow[4], "30")
	}
	if totalsRow[5] != "28" {
		t.Fatalf("totals RECEIVED = %q, want %q", totalsRow[5], "28")
	}
	if totalsRow[6] != "-2" {
		t.Fatalf("totals VARIANCE = %q, want %q", totalsRow[6], "-2")
	}
}

func TestRenderTransferXLSX_DeliveryNote_NoReceivedColumns(t *testing.T) {
	doc := &TransferDoc{
		Branding:          Branding{CompanyName: "BOI Enterprises"},
		DocTitle:          "Delivery Note",
		TransferNumber:    "TRF-260817-000079",
		Date:              "17 August 2026",
		FromWarehouseName: "BOI Enterprises",
		ToWarehouseName:   "Junior Wholesalers",
		Items:             longTransferItems(3, false),
		LeftSigLabel:      "Dispatched By",
		RightSigLabel:     "Received By",
	}
	b, err := RenderTransferXLSX(doc)
	if err != nil {
		t.Fatalf("RenderTransferXLSX: %v", err)
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
	for _, row := range rows {
		if len(row) > 0 && row[0] == "#" {
			if len(row) != 5 {
				t.Fatalf("delivery note header row = %v, want exactly 5 columns (no RECEIVED/VARIANCE/NOTES)", row)
			}
			if row[4] != "SHIPPED" {
				t.Fatalf("delivery note qty column header = %q, want %q (goods have actually shipped at this stage)", row[4], "SHIPPED")
			}
			return
		}
	}
	t.Fatalf("did not find the item table header row in %v", rows)
}

// TestRenderTransferXLSX_PendingOrder_QtyColumnNotLabeledShipped is a regression test: a
// not-yet-dispatched Transfer Order used to inherit the delivery note's "SHIPPED" header even
// though nothing has shipped yet, and never carried the item's unit-of-measure either. Both must
// be fixed via QtyColumnLabel and TransferDocLine.Unit respectively.
func TestRenderTransferXLSX_PendingOrder_QtyColumnNotLabeledShipped(t *testing.T) {
	doc := &TransferDoc{
		Branding:          Branding{CompanyName: "BOI Enterprises"},
		DocTitle:          "Transfer Order",
		DocSubtitle:       "Pending Dispatch",
		QtyColumnLabel:    "QTY",
		TransferNumber:    "TRF-260818-000098",
		FromWarehouseName: "BOI Enterprises",
		ToWarehouseName:   "Eldoret Enterprises",
		Items: []TransferDocLine{
			{Desc: "X6840 64/4 SMART 20 INFINIX", SubDesc: "10625", Unit: "PCS", Qty: "10"},
		},
		LeftSigLabel:  "Prepared By",
		RightSigLabel: "Authorized By",
	}
	b, err := RenderTransferXLSX(doc)
	if err != nil {
		t.Fatalf("RenderTransferXLSX: %v", err)
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
	var headerRow, dataRow []string
	for i, row := range rows {
		if len(row) > 0 && row[0] == "#" {
			headerRow = row
			if i+1 < len(rows) {
				dataRow = rows[i+1]
			}
			break
		}
	}
	if headerRow == nil {
		t.Fatalf("did not find the item table header row in %v", rows)
	}
	if headerRow[4] != "QTY" {
		t.Fatalf("pending transfer order qty column header = %q, want %q (nothing has shipped yet)", headerRow[4], "QTY")
	}
	for _, h := range headerRow {
		if h == "SHIPPED" {
			t.Fatalf("pending transfer order header row = %v must not say SHIPPED anywhere", headerRow)
		}
	}
	if dataRow == nil || dataRow[3] != "PCS" {
		t.Fatalf("pending transfer order UNIT cell = %v, want \"PCS\" in the UNIT column", dataRow)
	}
}

func TestRenderTransferXLSX_EmptyPrimaryColorDoesNotError(t *testing.T) {
	doc := sampleGRNDoc()
	doc.Branding.PrimaryColor = "not-a-color"
	if _, err := RenderTransferXLSX(doc); err != nil {
		t.Fatalf("RenderTransferXLSX with invalid brand color: %v", err)
	}
}
