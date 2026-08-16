package render

import (
	"fmt"
	"testing"
)

// Coverage for the PO goods-receipt (GRN) renderer, mirroring onepage_test.go/pagination_test.go:
// a short receipt must stay on one page, and a long one must paginate cleanly rather than
// silently corrupting mid-row (see primitives.go's pageBottomSafe doc comment).

func grnItems(n int) []GoodsReceiptDocLine {
	items := make([]GoodsReceiptDocLine, n)
	for i := range items {
		items[i] = GoodsReceiptDocLine{
			Desc:        fmt.Sprintf("K0%d 128/4 SPARK 50 PRO TECNO SMARTPHONE", i+1),
			SubDesc:     fmt.Sprintf("SMA-GDS-%03d", i+1),
			Unit:        "PCS",
			OrderedQty:  "10",
			ReceivedQty: "10",
			AcceptedQty: "10",
			RejectedQty: "0",
			UnitCost:    "12,500.00",
			Amount:      "125,000.00",
		}
	}
	return items
}

func grnDoc(items []GoodsReceiptDocLine) *GoodsReceiptDoc {
	return &GoodsReceiptDoc{
		Branding: Branding{
			CompanyName:           "BOI Enterprises",
			Address:               []string{"2nd Floor, Ramis Centre, Mombasa Road", "Nairobi", "Kenya"},
			KRAPIN:                "P051565369U",
			ProviderFooterEnabled: true,
		},
		GrnNumber:           "GRN-000042",
		Date:                "16 August 2026",
		Status:              "posted",
		Currency:            "KES",
		PurchaseOrderNumber: "PO-000117",
		SupplierName:        "Tecno Mobile East Africa Ltd",
		SupplierAddr:        []string{"Sameer Business Park, Mombasa Road, P.O Box 12345-00100, Nairobi, Kenya"},
		WarehouseName:       "Juja Warehouse",
		Items:               items,
		TotalReceivedValue:  "125,000.00",
		Notes:               []string{"Inspected on arrival; rejected units to be returned to the supplier."},
		PreparedBy:          "stores@boi.co.ke",
	}
}

func TestGoodsReceipt_FitsOnePage(t *testing.T) {
	b, err := RenderGoodsReceipt(grnDoc(grnItems(3)), nil, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	pages := len(pageObjRe.FindAll(b, -1))
	t.Logf("pdf bytes=%d pages=%d", len(b), pages)
	if pages != 1 {
		t.Fatalf("expected a 3-line goods receipt to fit 1 page, got %d", pages)
	}
}

func TestGoodsReceipt_MultiPage_LongItemList(t *testing.T) {
	b, err := RenderGoodsReceipt(grnDoc(grnItems(35)), nil, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	pages := len(pageObjRe.FindAll(b, -1))
	t.Logf("pdf bytes=%d pages=%d", len(b), pages)
	if pages < 2 {
		t.Fatalf("expected a 35-line goods receipt to span multiple pages, got %d", pages)
	}
}

// A receipt captured without unit costs must still render — the totals stack simply drops out
// rather than printing a misleading empty/zero grand total bar.
func TestGoodsReceipt_NoCosts_OmitsTotals(t *testing.T) {
	items := grnItems(2)
	for i := range items {
		items[i].UnitCost, items[i].Amount = "", ""
	}
	doc := grnDoc(items)
	doc.TotalReceivedValue = ""
	b, err := RenderGoodsReceipt(doc, nil, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if pages := len(pageObjRe.FindAll(b, -1)); pages != 1 {
		t.Fatalf("expected 1 page, got %d", pages)
	}
}

// Part 1 contract: the LIVE provider-footer strings resolved onto Branding by
// documents.Service.ResolveProviderFooterText win over the compiled-in constants, and each line
// falls back INDEPENDENTLY — a live lead with an unresolvable contact still prints a usable
// contact line, so a document is never left with a half-empty footer.
func TestProviderFooterLines_LiveOverridesWithPerLineFallback(t *testing.T) {
	cases := []struct {
		name                  string
		in                    Branding
		wantLead, wantContact string
	}{
		{
			name:        "unset falls back to both constants",
			in:          Branding{},
			wantLead:    providerFooterLead,
			wantContact: providerFooterContact,
		},
		{
			name: "live strings win",
			in: Branding{
				ProviderFooterLead:    "Developed & maintained by Codevertex Africa Ltd",
				ProviderFooterContact: "live.example.com  ·  hi@example.com",
			},
			wantLead:    "Developed & maintained by Codevertex Africa Ltd",
			wantContact: "live.example.com  ·  hi@example.com",
		},
		{
			name:        "blank contact falls back on its own",
			in:          Branding{ProviderFooterLead: "Developed & maintained by Someone"},
			wantLead:    "Developed & maintained by Someone",
			wantContact: providerFooterContact,
		},
		{
			name:        "whitespace-only is treated as unset",
			in:          Branding{ProviderFooterLead: "   ", ProviderFooterContact: "\t"},
			wantLead:    providerFooterLead,
			wantContact: providerFooterContact,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lead, contact := providerFooterLines(tc.in)
			if lead != tc.wantLead {
				t.Errorf("lead = %q, want %q", lead, tc.wantLead)
			}
			if contact != tc.wantContact {
				t.Errorf("contact = %q, want %q", contact, tc.wantContact)
			}
		})
	}
}
