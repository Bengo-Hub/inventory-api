package render

import (
	"bytes"
	"testing"
)

func TestPO_FitsOnePage(t *testing.T) {
	doc := &PurchaseOrderDoc{
		Branding: Branding{
			CompanyName: "Masterspace Solutions Limited",
			Address:     []string{"2nd Floor, Suite A, Ramis Centre, Mombasa Road", "P.O Box 57933-00200  ·  Nairobi", "Kenya"},
			Email:       "info@masterspace.co.ke",
			Website:     "https://masterspace.co.ke",
		},
		PONumber:      "PO-EFE00F15-20260624223224",
		Date:          "24 June 2026",
		Currency:      "KES",
		Status:        "DRAFT",
		SupplierName:  "A. Mulu & Company Advocates",
		SupplierAddr:  []string{"Global Trade Centre (GTC) Office Tower, 14th Floor, Chiromo Lane, Westlands, P.O Box 26849-00100, Nairobi, Kenya"},
		WarehouseName: "Masterspace Solutions HQ",
		ExpectedDate:  "30 June 2026",
		Items: []DocLine{
			{Desc: "Legal Fees — PPARB Appeal No 84 of 2026 (MSS v IEBC)", Qty: "1", Rate: "200,000.00", Amount: "200,000.00"},
			{Desc: "Disbursements — PPARB Appeal No 84 of 2026", Qty: "1", Rate: "50,000.00", Amount: "50,000.00"},
		},
		Grand:         "250,000.00",
		AmountInWords: "Two Hundred Fifty Thousand Kenya Shillings Only",
	}
	b, err := Render(doc, nil, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	pages := bytes.Count(b, []byte("/MediaBox"))
	t.Logf("pdf bytes=%d pages=%d", len(b), pages)
	if pages != 1 {
		t.Fatalf("expected 1 page, got %d", pages)
	}
}
