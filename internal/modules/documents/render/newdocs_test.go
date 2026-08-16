package render

import (
	"fmt"
	"testing"
)

// Smoke + pagination coverage for the six documents added alongside the goods receipt
// (requisition, RFQ, purchase return, stock adjustment, stock count, bundle spec). All six go
// through the same renderSimpleDoc pipeline, so the contract worth asserting per document is:
// a short one fits a single page, and a long one paginates instead of silently corrupting
// mid-row (see primitives.go's pageBottomSafe doc comment).

func testBranding() Branding {
	return Branding{
		CompanyName:           "BOI Enterprises",
		Tagline:               "Tangible Solutions for Businesses",
		Address:               []string{"2nd Floor, Ramis Centre, Mombasa Road", "P.O Box 57933-00200  ·  Nairobi", "Kenya"},
		Email:                 "info@boi.co.ke",
		Website:               "https://boi.co.ke",
		KRAPIN:                "P051565369U",
		ProviderFooterEnabled: true,
	}
}

// pageCounter returns a helper that turns a render call's (bytes, error) result straight into a
// page count. It takes exactly those two values, so `pages(RenderX(...))` reads inline — Go
// won't splice a multi-value call into a longer argument list.
func pageCounter(t *testing.T) func([]byte, error) int {
	return func(b []byte, err error) int {
		t.Helper()
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return len(pageObjRe.FindAll(b, -1))
	}
}

func requisitionDoc(n int) *RequisitionDoc {
	items := make([]RequisitionDocLine, n)
	for i := range items {
		items[i] = RequisitionDocLine{
			Desc:         fmt.Sprintf("Assorted Office Supplies Bulk Pack %d", i+1),
			SubDesc:      fmt.Sprintf("SKU-%05d", i+1),
			ItemType:     "inventory",
			Qty:          "10",
			ApprovedQty:  "8",
			UnitEstimate: "1,200.00",
			LineEstimate: "9,600.00",
			Urgent:       i%7 == 0,
		}
	}
	return &RequisitionDoc{
		Branding: testBranding(), ReferenceNumber: "REQ-000021", Date: "16 August 2026",
		Status: "pending_approval", RequestType: "inventory", Priority: "high",
		RequiredByDate: "30 August 2026", Currency: "KES",
		Purpose: "Quarterly restock for the Juja branch", RequesterName: "Jane Wanjiru",
		OutletName: "Juja Branch", Items: items, EstimatedTotal: "96,000.00",
		Notes: []string{"Estimates taken from the last approved supplier price list."},
	}
}

func TestRequisition_OnePageAndPaginates(t *testing.T) {
	pages := pageCounter(t)
	if p := pages(RenderRequisition(requisitionDoc(3), nil, "")); p != 1 {
		t.Fatalf("short requisition: expected 1 page, got %d", p)
	}
	if p := pages(RenderRequisition(requisitionDoc(40), nil, "")); p < 2 {
		t.Fatalf("long requisition: expected multiple pages, got %d", p)
	}
}

func rfqDoc(n, quotes int) *RFQDoc {
	items := make([]RFQDocLine, n)
	for i := range items {
		items[i] = RFQDocLine{
			Desc:    fmt.Sprintf("K0%d 128/4 SPARK 50 PRO TECNO SMARTPHONE", i+1),
			SubDesc: fmt.Sprintf("SMA-GDS-%03d", i+1),
			Uom:     "PCS", Qty: "25",
		}
	}
	qs := make([]RFQDocQuote, quotes)
	for i := range qs {
		qs[i] = RFQDocQuote{
			SupplierName: fmt.Sprintf("Supplier %d Ltd", i+1), Status: "submitted",
			Currency: "KES", QuotedTotal: "312,500.00", LeadTime: "14 days", Awarded: i == 0,
		}
	}
	return &RFQDoc{
		Branding: testBranding(), RFQNumber: "RFQ-000007", Title: "Q3 Handset Restock",
		Date: "16 August 2026", DueDate: "26 August 2026", Status: "open",
		WarehouseName: "Juja Warehouse", Items: items, Quotes: qs,
		Notes:      []string{"Prices must be quoted VAT-inclusive and remain valid for 30 days."},
		PreparedBy: "procurement@boi.co.ke",
	}
}

// The quotation appendix is supplementary: its absence must not change whether the base RFQ
// renders, and its presence must not corrupt the document.
func TestRFQ_RendersWithAndWithoutQuotationAppendix(t *testing.T) {
	pages := pageCounter(t)
	if p := pages(RenderRFQ(rfqDoc(4, 0), nil, "")); p != 1 {
		t.Fatalf("short RFQ without quotes: expected 1 page, got %d", p)
	}
	if p := pages(RenderRFQ(rfqDoc(4, 3), nil, "")); p < 1 {
		t.Fatalf("RFQ with quotes: expected at least 1 page, got %d", p)
	}
	if p := pages(RenderRFQ(rfqDoc(40, 6), nil, "")); p < 2 {
		t.Fatalf("long RFQ: expected multiple pages, got %d", p)
	}
}

func purchaseReturnDoc(n int) *PurchaseReturnDoc {
	items := make([]PurchaseReturnDocLine, n)
	for i := range items {
		items[i] = PurchaseReturnDocLine{
			Desc:    fmt.Sprintf("Damaged Handset Unit %d", i+1),
			SubDesc: fmt.Sprintf("SMA-GDS-%03d  ·  Lot L-%03d", i+1, i+1),
			Qty:     "2", UnitPrice: "12,500.00", Amount: "25,000.00",
		}
	}
	return &PurchaseReturnDoc{
		Branding: testBranding(), ReturnNumber: "PRET-000003", Date: "16 August 2026",
		Currency: "KES", PaymentStatus: "pending", Reason: "Screen defects found on inspection",
		PurchaseOrderNumber: "PO-000117", GrnNumber: "GRN-000042",
		SupplierName: "Tecno Mobile East Africa Ltd",
		SupplierAddr: []string{"Sameer Business Park, Mombasa Road, Nairobi, Kenya"},
		Items:        items, ReturnAmount: "50,000.00", AmountDue: "50,000.00",
	}
}

func TestPurchaseReturn_OnePageAndPaginates(t *testing.T) {
	pages := pageCounter(t)
	if p := pages(RenderPurchaseReturn(purchaseReturnDoc(2), nil, "")); p != 1 {
		t.Fatalf("short return: expected 1 page, got %d", p)
	}
	if p := pages(RenderPurchaseReturn(purchaseReturnDoc(40), nil, "")); p < 2 {
		t.Fatalf("long return: expected multiple pages, got %d", p)
	}
}

func stockAdjustmentDoc(n int) *StockAdjustmentDoc {
	items := make([]StockAdjustmentDocLine, n)
	for i := range items {
		items[i] = StockAdjustmentDocLine{
			Desc:    fmt.Sprintf("Sugar Brown Refined %dkg", i+1),
			SubDesc: fmt.Sprintf("INGR-SUG-%03d", i+1),
			Before:  "120", Change: "-12", After: "108", Reason: "damaged_goods",
		}
	}
	return &StockAdjustmentDoc{
		Branding: testBranding(), Reference: "ADJ-000015", Date: "16 August 2026",
		WarehouseName: "Juja Warehouse", AdjustedBy: "Peter Kimani", Items: items,
		Notes: []string{"Water damage from the 14 August store-room leak."},
	}
}

func TestStockAdjustment_OnePageAndPaginates(t *testing.T) {
	pages := pageCounter(t)
	if p := pages(RenderStockAdjustment(stockAdjustmentDoc(4), nil, "")); p != 1 {
		t.Fatalf("short adjustment: expected 1 page, got %d", p)
	}
	if p := pages(RenderStockAdjustment(stockAdjustmentDoc(40), nil, "")); p < 2 {
		t.Fatalf("long adjustment: expected multiple pages, got %d", p)
	}
}

func stockCountDoc(n int, mode string) *StockCountDoc {
	items := make([]StockCountDocLine, n)
	for i := range items {
		items[i] = StockCountDocLine{
			Desc:    fmt.Sprintf("Sugar Brown Refined %dkg", i+1),
			SubDesc: fmt.Sprintf("INGR-SUG-%03d", i+1),
			Unit:    "kg", SystemQty: "120", CountedQty: "118", Variance: "-2",
			Reason: "shrinkage",
		}
	}
	return &StockCountDoc{
		Branding: testBranding(), Mode: mode, Reference: "CYCLE-2026-06",
		Date: "16 August 2026", Status: "review", WarehouseName: "Juja Warehouse",
		CountedBy: "Peter Kimani", ApprovedBy: "Jane Wanjiru", ApprovedAt: "17 August 2026",
		Items: items,
	}
}

// Both modes must render; blank mode simply suppresses the counted/variance/reason values so
// staff can write them in by hand.
func TestStockCount_BothModesRender(t *testing.T) {
	for _, mode := range []string{StockCountModeBlank, StockCountModeVariance} {
		t.Run(mode, func(t *testing.T) {
			pages := pageCounter(t)
			if p := pages(RenderStockCount(stockCountDoc(5, mode), nil, "")); p != 1 {
				t.Fatalf("short count: expected 1 page, got %d", p)
			}
			if p := pages(RenderStockCount(stockCountDoc(40, mode), nil, "")); p < 2 {
				t.Fatalf("long count: expected multiple pages, got %d", p)
			}
		})
	}
}

func TestBundleSpec_OnePageAndPaginates(t *testing.T) {
	pages := pageCounter(t)
	doc := func(n int) *BundleSpecDoc {
		comps := make([]BundleSpecDocLine, n)
		for i := range comps {
			comps[i] = BundleSpecDocLine{
				Desc: fmt.Sprintf("Conference Component %d", i+1), SubDesc: fmt.Sprintf("CMP-%03d", i+1),
				Kind: "MEAL_PERIOD", Qty: "1", Unit: "session", Metered: i%3 == 0, MealPeriod: "lunch",
			}
		}
		return &BundleSpecDoc{
			Branding: testBranding(), BundleName: "Full Day Delegate Package",
			ItemName: "DDR Package", ItemSKU: "PKG-DDR-001",
			PackageType: "DDR", PriceBasis: "per_delegate_per_day",
			Attributes: [][2]string{{"Min Delegates", "10"}, {"Sessions", "3"}},
			IsActive:   true, Components: comps,
		}
	}
	if p := pages(RenderBundleSpec(doc(5), nil, "")); p != 1 {
		t.Fatalf("short bundle spec: expected 1 page, got %d", p)
	}
	if p := pages(RenderBundleSpec(doc(45), nil, "")); p < 2 {
		t.Fatalf("long bundle spec: expected multiple pages, got %d", p)
	}
}
