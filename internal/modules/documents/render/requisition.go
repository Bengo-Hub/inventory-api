package render

// Purchase-requisition rendering — the internal "please buy/issue this" request that precedes an
// RFQ or purchase order. Composes the generic blocks in common.go through the shared pipeline in
// simpledoc.go, so its item table inherits the same measure-before-draw pagination contract as
// every other table in this package.

import "strings"

// RequisitionDoc is the canonical input for rendering a requisition PDF. Numeric fields are
// PRE-FORMATTED strings, matching the DocLine convention.
type RequisitionDoc struct {
	Branding Branding

	ReferenceNumber string
	Date            string
	Status          string
	RequestType     string // inventory / external_item / service
	Priority        string // low / medium / high / critical
	RequiredByDate  string
	Currency        string

	Purpose string

	RequesterName string
	OutletName    string

	Items []RequisitionDocLine

	// EstimatedTotal is the pre-formatted sum of the lines' estimated value. Empty omits the
	// totals block — a requisition raised without price estimates is still a valid document.
	EstimatedTotal string

	Notes      []string
	PreparedBy string
	ApprovedBy string
}

// RequisitionDocLine is a single requested line. ApprovedQty stays blank until the requisition
// has actually been through approval, so a draft prints no misleading approved column values.
type RequisitionDocLine struct {
	Desc     string
	SubDesc  string // specifications / SKU
	ItemType string // inventory / external / service
	Qty      string
	// ApprovedQty is the quantity the approver authorised (blank when not yet decided).
	ApprovedQty string
	// UnitEstimate is the per-unit estimated price; LineEstimate is qty x estimate.
	UnitEstimate string
	LineEstimate string
	Urgent       bool
}

// RenderRequisition builds a premium, tenant-branded A4 requisition and returns PDF bytes.
func RenderRequisition(doc *RequisitionDoc, logo []byte, logoType string) ([]byte, error) {
	return renderSimpleDoc(requisitionSimpleDoc(doc), logo, logoType)
}

// RenderRequisitionXLSX renders the same requisition as a styled, print-ready Excel workbook —
// see simpledoc_xlsx.go's doc comment.
func RenderRequisitionXLSX(doc *RequisitionDoc) ([]byte, error) {
	return renderSimpleDocXLSX(requisitionSimpleDoc(doc))
}

// RenderRequisitionCSV renders the requisition's data as plain CSV.
func RenderRequisitionCSV(doc *RequisitionDoc) ([]byte, error) {
	return renderSimpleDocCSV(requisitionSimpleDoc(doc))
}

// requisitionSimpleDoc maps a RequisitionDoc into the shared simpleDoc pipeline — the one
// definition of this document's shape shared by every export format (PDF/XLSX/CSV above).
func requisitionSimpleDoc(doc *RequisitionDoc) simpleDoc {
	cur := currencyCode(doc.Currency)
	estTitle := "EST. TOTAL"
	if cur != "" {
		estTitle = "EST. TOTAL (" + cur + ")"
	}

	rows := make([]docRow, 0, len(doc.Items))
	for _, it := range doc.Items {
		desc := it.Desc
		if it.Urgent {
			desc += "  [URGENT]"
		}
		rows = append(rows, docRow{
			SubDesc: it.SubDesc,
			Cells: []string{
				desc, strings.ToUpper(it.ItemType), it.Qty, it.ApprovedQty,
				it.UnitEstimate, it.LineEstimate,
			},
		})
	}

	meta := [][2]string{
		{"Requisition No.", orDash(doc.ReferenceNumber)},
		{"Date", orDash(doc.Date)},
	}
	if strings.TrimSpace(doc.RequiredByDate) != "" {
		meta = append(meta, [2]string{"Required By", doc.RequiredByDate})
	}
	if strings.TrimSpace(doc.Priority) != "" {
		meta = append(meta, [2]string{"Priority", strings.ToUpper(doc.Priority)})
	}
	if strings.TrimSpace(doc.Status) != "" {
		meta = append(meta, [2]string{"Status", strings.ToUpper(strings.ReplaceAll(doc.Status, "_", " "))})
	}

	requester := partyCard{Title: "REQUESTED BY", Name: doc.RequesterName}
	if strings.TrimSpace(doc.Purpose) != "" {
		requester.Lines = []string{doc.Purpose}
	}

	approvedLabel := ""
	if doc.ApprovedBy != "" {
		approvedLabel = "Approved By"
	}

	return simpleDoc{
		Branding: doc.Branding,
		Title:    "Requisition",
		Subtitle: requisitionSubtitle(doc.RequestType),
		MetaRows: meta,
		Parties: &[2]partyCard{
			requester,
			{Title: "FOR", Name: doc.OutletName},
		},
		Numbered: true,
		Columns: []docColumn{
			{Title: "DESCRIPTION"},
			{Title: "TYPE", Width: 24},
			{Title: "QTY", Width: 18, Right: true},
			{Title: "APPROVED", Width: 22, Right: true},
			{Title: "EST. UNIT", Width: 24, Right: true},
			{Title: estTitle, Width: 30, Right: true},
		},
		Rows: rows,
		Totals: []totalRow{
			{Label: "Estimated Total", Value: moneyOrEmpty(doc.Currency, doc.EstimatedTotal), Grand: true},
		},
		NotesTitle:    "NOTES",
		Notes:         doc.Notes,
		LeftSigLabel:  "Requested By",
		LeftSigName:   ifEmpty(doc.PreparedBy, doc.RequesterName),
		RightSigLabel: approvedLabel,
		RightSigName:  doc.ApprovedBy,
		Disclaimer: "Estimated prices are indicative only and do not commit " +
			ifEmpty(doc.Branding.CompanyName, "the issuer") + " to a purchase.",
		ErrLabel: "requisition",
	}
}

// requisitionSubtitle maps the stored request_type enum to a human masthead caption.
func requisitionSubtitle(requestType string) string {
	switch strings.ToLower(strings.TrimSpace(requestType)) {
	case "service":
		return "Service Request"
	case "external_item":
		return "External Item Request"
	case "inventory":
		return "Internal Purchase Request"
	default:
		return "Internal Purchase Request"
	}
}
