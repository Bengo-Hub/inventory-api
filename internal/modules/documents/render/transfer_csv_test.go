package render

import (
	"bytes"
	"encoding/csv"
	"testing"
)

func TestRenderTransferCSV_GRN_HeaderAndTotals(t *testing.T) {
	doc := sampleGRNDoc()
	b, err := RenderTransferCSV(doc)
	if err != nil {
		t.Fatalf("RenderTransferCSV: %v", err)
	}
	rows, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatalf("re-parse generated csv: %v", err)
	}

	var headerRow, totalsRow []string
	for _, row := range rows {
		if len(row) > 0 && row[0] == "#" {
			headerRow = row
		}
		if len(row) > 1 && row[1] == "TOTALS" {
			totalsRow = row
		}
	}
	want := []string{"#", "Description", "SKU", "Unit", "Shipped", "Received", "Variance", "Notes"}
	if len(headerRow) != len(want) {
		t.Fatalf("header row = %v, want %v", headerRow, want)
	}
	for i, w := range want {
		if headerRow[i] != w {
			t.Fatalf("header[%d] = %q, want %q", i, headerRow[i], w)
		}
	}
	if totalsRow == nil {
		t.Fatalf("did not find a TOTALS row in %v", rows)
	}
	// See TestRenderTransferXLSX_GRN_StructureAndTotals for the shipped/received/variance math.
	if totalsRow[4] != "30" || totalsRow[5] != "28" || totalsRow[6] != "-2" {
		t.Fatalf("totals row = %v, want shipped=30 received=28 variance=-2", totalsRow)
	}
}

func TestRenderTransferCSV_DeliveryNote_NoReceivedColumns(t *testing.T) {
	doc := &TransferDoc{
		Branding:          Branding{CompanyName: "BOI Enterprises"},
		DocTitle:          "Delivery Note",
		TransferNumber:    "TRF-260817-000079",
		FromWarehouseName: "BOI Enterprises",
		ToWarehouseName:   "Junior Wholesalers",
		Items:             longTransferItems(3, false),
	}
	b, err := RenderTransferCSV(doc)
	if err != nil {
		t.Fatalf("RenderTransferCSV: %v", err)
	}
	rows, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatalf("re-parse generated csv: %v", err)
	}
	for _, row := range rows {
		if len(row) > 0 && row[0] == "#" {
			if len(row) != 5 {
				t.Fatalf("delivery note header row = %v, want exactly 5 columns", row)
			}
			return
		}
	}
	t.Fatalf("did not find the item table header row in %v", rows)
}
