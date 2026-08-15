package render

import (
	"fmt"
	"testing"
)

// Regression coverage for the 2026-08-15 "goods-received note prints halfway" bug: a long item
// table (drawItems for Purchase Orders, drawTransferItems for Delivery Notes/GRNs) used to mix
// page-break-aware multiCell calls with page-break-blind text/textR calls at a single stale
// rowTop, so a row that crossed the page boundary got its description drawn on a fresh page
// while its numeric columns stayed on the old one — the exact "content stops/degrades partway
// through the list" symptom reported for a 16-line BOI Enterprises GRN. These tests assert a
// long list now spans real, cleanly-paginated pages instead of silently corrupting/clipping.

func longPOItems(n int) []DocLine {
	items := make([]DocLine, n)
	for i := range items {
		items[i] = DocLine{
			Desc:    fmt.Sprintf("Item %d — Assorted Office Supplies Bulk Pack", i+1),
			SubDesc: fmt.Sprintf("SKU-%05d", i+1),
			Unit:    "PCS",
			Qty:     "10",
			Rate:    "1,200.00",
			Amount:  "12,000.00",
		}
	}
	return items
}

func TestPO_MultiPage_LongItemList(t *testing.T) {
	doc := &PurchaseOrderDoc{
		Branding: Branding{
			CompanyName: "Masterspace Solutions Limited",
			Address:     []string{"2nd Floor, Suite A, Ramis Centre, Mombasa Road", "Kenya"},
			KRAPIN:      "P051565369U",
		},
		PONumber:     "PO-TEST-0001",
		Date:         "15 August 2026",
		Currency:     "KES",
		Status:       "DRAFT",
		SupplierName: "Test Supplier Ltd",
		Items:        longPOItems(40),
		Subtotal:     "480,000.00",
		Grand:        "480,000.00",
		PreparedBy:   "test@masterspace.co.ke",
	}
	b, err := Render(doc, nil, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	pages := len(pageObjRe.FindAll(b, -1))
	t.Logf("pdf bytes=%d pages=%d", len(b), pages)
	if pages < 2 {
		t.Fatalf("expected a 40-line PO to span multiple pages, got %d", pages)
	}
}

func longTransferItems(n int, receivedVariance bool) []TransferDocLine {
	items := make([]TransferDocLine, n)
	for i := range items {
		items[i] = TransferDocLine{
			Desc:    fmt.Sprintf("K0%d 128/4 SPARK 50 PRO TECNO SMARTPHONE", i+1),
			SubDesc: fmt.Sprintf("SMA-GDS-%03d", i+1),
			Unit:    "PCS",
			Qty:     "5",
		}
		if receivedVariance {
			items[i].ReceivedQty = "5"
			if i%5 == 0 {
				items[i].ReceivedQty = "4"
				items[i].VarianceReason = "Damaged in transit"
			}
		}
	}
	return items
}

func TestTransferDoc_MultiPage_GoodsReceivedNote(t *testing.T) {
	doc := &TransferDoc{
		Branding: Branding{
			CompanyName: "BOI Enterprises",
			Address:     []string{"Nairobi", "Kenya"},
		},
		DocTitle:            "GOODS RECEIVED NOTE",
		TransferNumber:      "TRF-TEST-0001",
		Date:                "15 August 2026",
		FromWarehouseName:   "BOI Enterprises",
		ToWarehouseName:     "Juja Warehouse",
		AcknowledgementText: "Received the following goods into Juja Warehouse in good order and condition:",
		Items:               longTransferItems(30, true),
		LeftSigLabel:        "Delivered By",
		RightSigLabel:       "Received By",
	}
	b, err := RenderTransfer(doc, nil, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	pages := len(pageObjRe.FindAll(b, -1))
	t.Logf("pdf bytes=%d pages=%d", len(b), pages)
	if pages < 2 {
		t.Fatalf("expected a 30-line GRN to span multiple pages, got %d", pages)
	}
}

func TestTransferDoc_FitsOnePage_ShortDeliveryNote(t *testing.T) {
	doc := &TransferDoc{
		Branding: Branding{
			CompanyName: "BOI Enterprises",
			Address:     []string{"Nairobi", "Kenya"},
		},
		DocTitle:            "DELIVERY NOTE",
		TransferNumber:      "TRF-TEST-0002",
		Date:                "15 August 2026",
		FromWarehouseName:   "BOI Enterprises",
		ToWarehouseName:     "Juja Warehouse",
		AcknowledgementText: "Received the following goods into Juja Warehouse in good order and condition:",
		Items:               longTransferItems(3, false),
		LeftSigLabel:        "Dispatched By",
		RightSigLabel:       "Received By",
	}
	b, err := RenderTransfer(doc, nil, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	pages := len(pageObjRe.FindAll(b, -1))
	t.Logf("pdf bytes=%d pages=%d", len(b), pages)
	if pages != 1 {
		t.Fatalf("expected a 3-line delivery note to fit 1 page, got %d", pages)
	}
}
