package render

// Purchase-return (supplier RTV / debit note) rendering — the document sent to a supplier for
// goods returned to them, claiming credit for the returned value. Composes the generic blocks in
// common.go through the shared pipeline in simpledoc.go.

import "strings"

// PurchaseReturnDoc is the canonical input for rendering a supplier purchase-return debit note.
type PurchaseReturnDoc struct {
	Branding Branding

	ReturnNumber string
	Date         string
	Currency     string
	// PaymentStatus is the credit's settlement state (pending / due / partial / paid).
	PaymentStatus string
	Reason        string

	PurchaseOrderNumber string
	GrnNumber           string

	SupplierName string
	SupplierAddr []string

	Items []PurchaseReturnDocLine

	// ReturnAmount is the total credit claimed; AmountDue is what remains unsettled. Both are
	// pre-formatted; a blank value drops that row from the totals stack.
	ReturnAmount string
	AmountDue    string

	Notes      []string
	PreparedBy string
	ApprovedBy string
}

// PurchaseReturnDocLine is a single returned line.
type PurchaseReturnDocLine struct {
	Desc    string
	SubDesc string // SKU / lot number
	Qty     string
	// UnitPrice is derived from the line's stored sub-total and quantity (the entity records
	// only the sub-total), and is blank when the quantity is zero.
	UnitPrice string
	Amount    string
}

// RenderPurchaseReturn builds a premium, tenant-branded A4 supplier return (debit note) and
// returns PDF bytes.
func RenderPurchaseReturn(doc *PurchaseReturnDoc, logo []byte, logoType string) ([]byte, error) {
	return renderSimpleDoc(purchaseReturnSimpleDoc(doc), logo, logoType)
}

// RenderPurchaseReturnXLSX renders the same purchase return as a styled, print-ready Excel
// workbook — see simpledoc_xlsx.go's doc comment.
func RenderPurchaseReturnXLSX(doc *PurchaseReturnDoc) ([]byte, error) {
	return renderSimpleDocXLSX(purchaseReturnSimpleDoc(doc))
}

// RenderPurchaseReturnCSV renders the purchase return's data as plain CSV.
func RenderPurchaseReturnCSV(doc *PurchaseReturnDoc) ([]byte, error) {
	return renderSimpleDocCSV(purchaseReturnSimpleDoc(doc))
}

// purchaseReturnSimpleDoc maps a PurchaseReturnDoc into the shared simpleDoc pipeline — the one
// definition of this document's shape shared by every export format (PDF/XLSX/CSV above).
func purchaseReturnSimpleDoc(doc *PurchaseReturnDoc) simpleDoc {
	cur := currencyCode(doc.Currency)
	amountTitle := "AMOUNT"
	if cur != "" {
		amountTitle = "AMOUNT (" + cur + ")"
	}

	rows := make([]docRow, 0, len(doc.Items))
	for _, it := range doc.Items {
		rows = append(rows, docRow{
			SubDesc: it.SubDesc,
			Cells:   []string{it.Desc, it.Qty, it.UnitPrice, it.Amount},
		})
	}

	meta := [][2]string{
		{"Return No.", orDash(doc.ReturnNumber)},
		{"Date", orDash(doc.Date)},
	}
	if strings.TrimSpace(doc.PurchaseOrderNumber) != "" {
		meta = append(meta, [2]string{"PO No.", doc.PurchaseOrderNumber})
	}
	if strings.TrimSpace(doc.GrnNumber) != "" {
		meta = append(meta, [2]string{"GRN No.", doc.GrnNumber})
	}
	if strings.TrimSpace(doc.PaymentStatus) != "" {
		meta = append(meta, [2]string{"Credit", strings.ToUpper(doc.PaymentStatus)})
	}
	if strings.TrimSpace(doc.Currency) != "" {
		meta = append(meta, [2]string{"Currency", doc.Currency})
	}

	notes := doc.Notes
	if r := strings.TrimSpace(doc.Reason); r != "" {
		notes = append([]string{"Reason for return: " + r}, notes...)
	}

	approvedLabel := ""
	if doc.ApprovedBy != "" {
		approvedLabel = "Approved By"
	}

	return simpleDoc{
		Branding: doc.Branding,
		Title:    "Purchase Return",
		Subtitle: "Debit Note - Return to Supplier",
		MetaRows: meta,
		Parties: &[2]partyCard{
			{Title: "SUPPLIER", Name: doc.SupplierName, Lines: doc.SupplierAddr},
			{Title: "RETURNED BY", Name: doc.Branding.CompanyName, Lines: companyAddressLines(doc.Branding)},
		},
		Lead:     "The goods listed below have been returned to you. Please raise a credit note for the value claimed.",
		Numbered: true,
		Columns: []docColumn{
			{Title: "DESCRIPTION"},
			{Title: "QTY", Width: 20, Right: true},
			{Title: "UNIT PRICE", Width: 30, Right: true},
			{Title: amountTitle, Width: 34, Right: true},
		},
		Rows: rows,
		Totals: []totalRow{
			{Label: "Outstanding Credit", Value: moneyOrEmpty(doc.Currency, doc.AmountDue)},
			{Label: "Return Amount", Value: moneyOrEmpty(doc.Currency, doc.ReturnAmount), Grand: true},
		},
		NotesTitle:    "NOTES",
		Notes:         notes,
		LeftSigLabel:  "Returned By",
		LeftSigName:   doc.PreparedBy,
		RightSigLabel: approvedLabel,
		RightSigName:  doc.ApprovedBy,
		Disclaimer: "This debit note claims credit for goods returned to the supplier named above by " +
			ifEmpty(doc.Branding.CompanyName, "the issuer") + ".",
		ErrLabel: "purchase return",
	}
}
