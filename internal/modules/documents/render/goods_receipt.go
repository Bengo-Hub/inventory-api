package render

// Goods-Receipt Note (GRN) rendering for PO RECEIVING — the supplier-facing receipt that records
// what arrived against a purchase order, what was accepted, what was rejected, and at what cost.
//
// Deliberately its own model rather than a retrofit of either existing document:
//   - PurchaseOrderDoc's header hardcodes the literal "PURCHASE ORDER" title, and its totals block
//     is fixed to Subtotal/Tax/Grand.
//   - TransferDoc covers the already-shipped WAREHOUSE-TO-WAREHOUSE transfer GRN and deliberately
//     carries no financial fields at all ("a stock transfer moves goods between the tenant's own
//     warehouses, not a sale") — bolting cost/accepted/rejected/lot columns onto it would risk
//     regressing that shipped feature.
//
// Layout composes the generic blocks in common.go through the shared pipeline in simpledoc.go,
// so its item table inherits the same measure-before-draw pagination contract as every other
// table in this package.

import "strings"

// GoodsReceiptDoc is the canonical input for rendering a PO goods-receipt (GRN) PDF. Every
// numeric field is a PRE-FORMATTED string (matching DocLine/TransferDocLine) — the renderer never
// formats money or quantities itself.
type GoodsReceiptDoc struct {
	Branding Branding

	GrnNumber string
	Date      string
	Status    string
	Currency  string
	// PurchaseOrderNumber is the originating PO's human number, shown in the meta box.
	PurchaseOrderNumber string

	SupplierName string
	SupplierAddr []string

	WarehouseName string

	Items []GoodsReceiptDocLine

	// TotalReceivedValue is the pre-formatted sum of the lines' Amount. Empty omits the totals
	// block entirely (a receipt captured without unit costs is still a valid document).
	TotalReceivedValue string

	Notes      []string
	PreparedBy string
	ApprovedBy string
}

// GoodsReceiptDocLine is a single received line. Unit, when set, is appended to the muted
// sub-line beside the SKU rather than claiming its own column — the accepted/rejected/cost
// columns are what a receiving clerk actually reconciles against.
type GoodsReceiptDocLine struct {
	Desc    string
	SubDesc string // SKU / lot number / rejection reason
	Unit    string
	// OrderedQty is the originating PO line's quantity, blank when the receipt has no PO line.
	OrderedQty  string
	ReceivedQty string
	AcceptedQty string
	RejectedQty string
	UnitCost    string
	Amount      string
}

// RenderGoodsReceipt builds a premium, tenant-branded A4 goods-receipt note and returns PDF bytes.
func RenderGoodsReceipt(doc *GoodsReceiptDoc, logo []byte, logoType string) ([]byte, error) {
	cur := currencyCode(doc.Currency)
	amountTitle := "AMOUNT"
	if cur != "" {
		amountTitle = "AMOUNT (" + cur + ")"
	}

	rows := make([]docRow, 0, len(doc.Items))
	for _, it := range doc.Items {
		sub := it.SubDesc
		if u := strings.TrimSpace(it.Unit); u != "" {
			sub = strings.TrimSpace(strings.Trim(sub+"  ·  "+u, " ·"))
		}
		rows = append(rows, docRow{
			SubDesc: sub,
			Cells: []string{
				it.Desc, it.OrderedQty, it.ReceivedQty, it.AcceptedQty,
				it.RejectedQty, it.UnitCost, it.Amount,
			},
		})
	}

	meta := [][2]string{
		{"GRN No.", orDash(doc.GrnNumber)},
		{"Date", orDash(doc.Date)},
	}
	if strings.TrimSpace(doc.PurchaseOrderNumber) != "" {
		meta = append(meta, [2]string{"PO No.", doc.PurchaseOrderNumber})
	}
	if strings.TrimSpace(doc.Status) != "" {
		meta = append(meta, [2]string{"Status", strings.ToUpper(doc.Status)})
	}
	if strings.TrimSpace(doc.Currency) != "" {
		meta = append(meta, [2]string{"Currency", doc.Currency})
	}

	approvedLabel := ""
	if doc.ApprovedBy != "" {
		approvedLabel = "Approved By"
	}

	return renderSimpleDoc(simpleDoc{
		Branding: doc.Branding,
		Title:    "Goods Received Note",
		Subtitle: "Receipt Against Purchase Order",
		MetaRows: meta,
		Parties: &[2]partyCard{
			{Title: "SUPPLIER", Name: doc.SupplierName, Lines: doc.SupplierAddr},
			{Title: "RECEIVED INTO", Name: doc.WarehouseName},
		},
		Lead: "Received the following goods into " +
			ifEmpty(doc.WarehouseName, "the receiving warehouse") + " and inspected as recorded below:",
		Numbered: true,
		Columns: []docColumn{
			{Title: "DESCRIPTION"},
			{Title: "ORDERED", Width: 16, Right: true},
			{Title: "RECEIVED", Width: 16, Right: true},
			{Title: "ACCEPTED", Width: 16, Right: true},
			{Title: "REJECTED", Width: 16, Right: true},
			{Title: "UNIT COST", Width: 24, Right: true},
			{Title: amountTitle, Width: 28, Right: true},
		},
		Rows: rows,
		Totals: []totalRow{
			{Label: "Total Received Value", Value: moneyOrEmpty(doc.Currency, doc.TotalReceivedValue), Grand: true},
		},
		NotesTitle:    "NOTES",
		Notes:         doc.Notes,
		LeftSigLabel:  "Received By",
		LeftSigName:   doc.PreparedBy,
		RightSigLabel: approvedLabel,
		RightSigName:  doc.ApprovedBy,
		Disclaimer: "This goods received note records the physical receipt and inspection of the goods listed above by " +
			ifEmpty(doc.Branding.CompanyName, "the issuer") + ".",
		ErrLabel: "goods receipt",
	}, logo, logoType)
}
