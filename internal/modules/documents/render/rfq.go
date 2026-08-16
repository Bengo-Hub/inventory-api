package render

// Request-for-Quotation rendering — the document sent to invited suppliers asking them to price
// a list of items, optionally followed by an appendix summarising the quotations received.
// Composes the generic blocks in common.go through the shared pipeline in simpledoc.go.

import "strings"

// RFQDoc is the canonical input for rendering an RFQ PDF.
type RFQDoc struct {
	Branding Branding

	RFQNumber string
	Title     string
	Date      string
	DueDate   string
	Status    string

	WarehouseName string
	// RequisitionNumber, when the RFQ was raised from a requisition.
	RequisitionNumber string

	Items []RFQDocLine

	// Quotes is the OPTIONAL supplier-response appendix. Empty (the normal case while the RFQ is
	// still out for quotation) simply omits the appendix — it never blocks the base document.
	Quotes []RFQDocQuote

	Notes      []string
	PreparedBy string
}

// RFQDocLine is a single item being quoted on.
type RFQDocLine struct {
	Desc    string
	SubDesc string // SKU / specification
	Uom     string
	Qty     string
}

// RFQDocQuote is one supplier's response, rendered in the appendix table. Awarded marks the
// supplier that won the line(s), so the appendix doubles as the award record once decided.
type RFQDocQuote struct {
	SupplierName string
	Status       string // invited / submitted / declined
	Currency     string
	QuotedTotal  string
	LeadTime     string
	Awarded      bool
	Notes        string
}

// RenderRFQ builds a premium, tenant-branded A4 request-for-quotation and returns PDF bytes.
func RenderRFQ(doc *RFQDoc, logo []byte, logoType string) ([]byte, error) {
	rows := make([]docRow, 0, len(doc.Items))
	for _, it := range doc.Items {
		rows = append(rows, docRow{
			SubDesc: it.SubDesc,
			Cells:   []string{it.Desc, it.Uom, it.Qty, "", ""},
		})
	}

	quoteRows := make([]docRow, 0, len(doc.Quotes))
	for _, q := range doc.Quotes {
		name := q.SupplierName
		if q.Awarded {
			name += "  [AWARDED]"
		}
		quoteRows = append(quoteRows, docRow{
			SubDesc:  q.Notes,
			Emphasis: q.Awarded,
			Cells: []string{
				name, strings.ToUpper(q.Status), q.LeadTime,
				strings.TrimSpace(currencyCode(q.Currency) + " " + q.QuotedTotal),
			},
		})
	}

	meta := [][2]string{
		{"RFQ No.", orDash(doc.RFQNumber)},
		{"Issued", orDash(doc.Date)},
	}
	if strings.TrimSpace(doc.DueDate) != "" {
		meta = append(meta, [2]string{"Responses By", doc.DueDate})
	}
	if strings.TrimSpace(doc.Status) != "" {
		meta = append(meta, [2]string{"Status", strings.ToUpper(doc.Status)})
	}
	if strings.TrimSpace(doc.RequisitionNumber) != "" {
		meta = append(meta, [2]string{"Requisition", doc.RequisitionNumber})
	}

	return renderSimpleDoc(simpleDoc{
		Branding: doc.Branding,
		Title:    "Request for Quotation",
		Subtitle: doc.Title,
		MetaRows: meta,
		Parties: &[2]partyCard{
			{Title: "ISSUED BY", Name: doc.Branding.CompanyName, Lines: companyAddressLines(doc.Branding)},
			{Title: "DELIVER TO", Name: doc.WarehouseName},
		},
		Lead: "You are invited to quote your best price and lead time for the items listed below. " +
			"Please complete the UNIT PRICE and LEAD TIME columns and return this document by the date shown above.",
		Numbered: true,
		Columns: []docColumn{
			{Title: "DESCRIPTION"},
			{Title: "UOM", Width: 20},
			{Title: "QTY", Width: 20, Right: true},
			// Deliberately blank columns: this document is sent OUT to be filled in by hand, so
			// the supplier has a ruled place to write. Quotes captured back in the system are
			// summarised in the appendix instead.
			{Title: "UNIT PRICE", Width: 32, Right: true},
			{Title: "LEAD TIME", Width: 28, Right: true},
		},
		Rows:          rows,
		AppendixTitle: "Quotations Received",
		AppendixColumns: []docColumn{
			{Title: "SUPPLIER"},
			{Title: "STATUS", Width: 26},
			{Title: "LEAD TIME", Width: 28, Right: true},
			{Title: "QUOTED TOTAL", Width: 40, Right: true},
		},
		AppendixRows: quoteRows,
		NotesTitle:   "TERMS & NOTES",
		Notes:        doc.Notes,
		LeftSigLabel: "Issued By",
		LeftSigName:  doc.PreparedBy,
		// Right side is where the responding supplier signs their quotation.
		RightSigLabel: "Supplier / Quoted By",
		Disclaimer: "This request for quotation is not an order and places no obligation on " +
			ifEmpty(doc.Branding.CompanyName, "the issuer") + " to purchase.",
		ErrLabel: "rfq",
	}, logo, logoType)
}
