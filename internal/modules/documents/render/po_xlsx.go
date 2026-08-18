package render

// XLSX/CSV export for PurchaseOrderDoc. render.go's PDF renderer (Render) predates simpledoc.go
// and keeps its own bespoke drawing functions for document-specific PDF chrome (the gradient
// amount banner, the fixed Subtotal/Tax/Grand block) — but that chrome doesn't have, or need, an
// Excel equivalent: a purchase order's DATA (supplier/deliver-to, item table, totals, sign-off)
// is the exact shape simpledoc.go's pipeline already models. Rather than hand-roll a third
// generic-table implementation just for this one document, this file maps PurchaseOrderDoc into
// the SAME simpleDoc shape (independent of, not derived from, render.go's PDF path — that path is
// untouched) and reuses renderSimpleDocXLSX/renderSimpleDocCSV.
import "strings"

// RenderPurchaseOrderXLSX renders a purchase order as a styled, print-ready Excel workbook — see
// simpledoc_xlsx.go's doc comment.
func RenderPurchaseOrderXLSX(doc *PurchaseOrderDoc) ([]byte, error) {
	return renderSimpleDocXLSX(purchaseOrderSimpleDoc(doc))
}

// RenderPurchaseOrderCSV renders the purchase order's data as plain CSV.
func RenderPurchaseOrderCSV(doc *PurchaseOrderDoc) ([]byte, error) {
	return renderSimpleDocCSV(purchaseOrderSimpleDoc(doc))
}

func purchaseOrderSimpleDoc(doc *PurchaseOrderDoc) simpleDoc {
	cur := currencyCode(doc.Currency)
	amountTitle := "AMOUNT"
	if cur != "" {
		amountTitle = "AMOUNT (" + cur + ")"
	}

	rows := make([]docRow, 0, len(doc.Items))
	for _, it := range doc.Items {
		rows = append(rows, docRow{
			SubDesc: it.SubDesc,
			Cells:   []string{it.Desc, it.Unit, it.Qty, it.Rate, it.Amount},
		})
	}

	right := partyCard{Title: "DELIVER TO", Name: doc.WarehouseName}
	if doc.ExpectedDate != "" {
		right.Lines = []string{"Expected: " + doc.ExpectedDate}
	}

	totals := []totalRow{{Label: "Subtotal", Value: money(doc.Currency, doc.Subtotal)}}
	if strings.TrimSpace(doc.TaxAmount) != "" {
		totals = append(totals, totalRow{Label: ifEmpty(doc.TaxLabel, "Tax"), Value: money(doc.Currency, doc.TaxAmount)})
	}
	totals = append(totals, totalRow{Label: "Grand Total", Value: money(doc.Currency, doc.Grand), Grand: true})

	approvedLabel := ""
	if doc.ApprovedBy != "" {
		approvedLabel = "Approved By"
	}

	return simpleDoc{
		Branding: doc.Branding,
		Title:    "Purchase Order",
		Subtitle: "Order For Procurement",
		MetaRows: metaRows(doc),
		Parties: &[2]partyCard{
			{Title: "SUPPLIER", Name: doc.SupplierName, Lines: doc.SupplierAddr},
			right,
		},
		Numbered: true,
		Columns: []docColumn{
			{Title: "DESCRIPTION"},
			{Title: "UNIT", Width: 16, Right: true},
			{Title: "QTY", Width: 14, Right: true},
			{Title: "RATE", Width: 28, Right: true},
			{Title: amountTitle, Width: 30, Right: true},
		},
		Rows:          rows,
		Totals:        totals,
		NotesTitle:    "NOTES",
		Notes:         doc.Notes,
		LeftSigLabel:  "Prepared By",
		LeftSigName:   doc.PreparedBy,
		RightSigLabel: approvedLabel,
		RightSigName:  doc.ApprovedBy,
		Disclaimer: "This purchase order is issued by " + ifEmpty(doc.Branding.CompanyName, "the issuer") +
			" and is subject to the terms stated above.",
		ErrLabel: "purchase order",
	}
}
