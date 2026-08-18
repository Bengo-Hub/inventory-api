package render

// Stock-take rendering — ONE model covering both moments of a physical count, selected by Mode:
//
//	"blank"    — the count sheet handed to staff. Prints the system quantity and leaves ruled
//	             COUNTED / VARIANCE columns empty for them to fill in by hand.
//	"variance" — the post-count reconciliation report. Prints counted quantity, variance and the
//	             per-line reason, fully populated.
//
// One struct rather than two because the header, parties, columns and sign-off are identical —
// only whether the trailing cells carry values differs.

import "strings"

// Stock-count render modes.
const (
	StockCountModeBlank    = "blank"
	StockCountModeVariance = "variance"
)

// StockCountDoc is the canonical input for rendering a stock-take sheet or variance report.
type StockCountDoc struct {
	Branding Branding

	// Mode is "blank" or "variance" (anything else is treated as "variance").
	Mode string

	Reference     string
	Date          string
	Status        string
	WarehouseName string
	CountedBy     string
	ApprovedBy    string
	ApprovedAt    string

	Items []StockCountDocLine

	Notes []string
}

// StockCountDocLine is a single counted item. CountedQty/Variance/Reason are ignored in blank
// mode — the sheet is printed to be written on.
type StockCountDocLine struct {
	Desc       string
	SubDesc    string // SKU
	Unit       string
	SystemQty  string
	CountedQty string
	Variance   string
	Reason     string
}

// RenderStockCount builds a premium, tenant-branded A4 stock-take document and returns PDF bytes.
func RenderStockCount(doc *StockCountDoc, logo []byte, logoType string) ([]byte, error) {
	return renderSimpleDoc(stockCountSimpleDoc(doc), logo, logoType)
}

// RenderStockCountXLSX renders the same stock-take document as a styled, print-ready Excel
// workbook — see simpledoc_xlsx.go's doc comment.
func RenderStockCountXLSX(doc *StockCountDoc) ([]byte, error) {
	return renderSimpleDocXLSX(stockCountSimpleDoc(doc))
}

// RenderStockCountCSV renders the stock-take document's data as plain CSV.
func RenderStockCountCSV(doc *StockCountDoc) ([]byte, error) {
	return renderSimpleDocCSV(stockCountSimpleDoc(doc))
}

// stockCountSimpleDoc maps a StockCountDoc into the shared simpleDoc pipeline — the one
// definition of this document's shape shared by every export format (PDF/XLSX/CSV above).
func stockCountSimpleDoc(doc *StockCountDoc) simpleDoc {
	blank := strings.EqualFold(strings.TrimSpace(doc.Mode), StockCountModeBlank)

	rows := make([]docRow, 0, len(doc.Items))
	for _, it := range doc.Items {
		counted, variance, reason := it.CountedQty, it.Variance, it.Reason
		if blank {
			counted, variance, reason = "", "", ""
		}
		rows = append(rows, docRow{
			SubDesc: it.SubDesc,
			Cells: []string{
				it.Desc, it.Unit, it.SystemQty, counted, variance,
				strings.ToUpper(strings.ReplaceAll(reason, "_", " ")),
			},
		})
	}

	meta := [][2]string{
		{"Reference", orDash(doc.Reference)},
		{"Date", orDash(doc.Date)},
		{"Lines", plural(len(doc.Items), "item")},
	}
	if strings.TrimSpace(doc.Status) != "" {
		meta = append(meta, [2]string{"Status", strings.ToUpper(doc.Status)})
	}
	if !blank && strings.TrimSpace(doc.ApprovedAt) != "" {
		meta = append(meta, [2]string{"Approved", doc.ApprovedAt})
	}

	subtitle, lead, disclaimer := "Variance Report", "", ""
	if blank {
		subtitle = "Physical Count Sheet"
		lead = "Count the physical quantity of each item below and write it in the COUNTED column. " +
			"Record a reason for every line that differs from the system quantity."
		disclaimer = "Physical count sheet issued by " + ifEmpty(doc.Branding.CompanyName, "the issuer") +
			". Completed sheets must be signed and returned for reconciliation."
	} else {
		lead = "Reconciliation of the physical count against system quantities for " +
			ifEmpty(doc.WarehouseName, "this warehouse") + ":"
		disclaimer = "This variance report records the reconciliation of a physical stock count performed for " +
			ifEmpty(doc.Branding.CompanyName, "the issuer") + "."
	}

	approvedLabel := "Approved By"
	if blank {
		// Nothing has been approved yet on a sheet that hasn't been counted.
		approvedLabel = "Verified By"
	}

	return simpleDoc{
		Branding: doc.Branding,
		Title:    "Stock Take",
		Subtitle: subtitle,
		MetaRows: meta,
		Parties: &[2]partyCard{
			{Title: "WAREHOUSE", Name: doc.WarehouseName},
			{Title: "COUNTED BY", Name: doc.CountedBy},
		},
		Lead:     lead,
		Numbered: true,
		Columns: []docColumn{
			{Title: "DESCRIPTION"},
			{Title: "UNIT", Width: 16},
			{Title: "SYSTEM", Width: 22, Right: true},
			{Title: "COUNTED", Width: 24, Right: true},
			{Title: "VARIANCE", Width: 24, Right: true},
			{Title: "REASON", Width: 34},
		},
		Rows:          rows,
		NotesTitle:    "NOTES",
		Notes:         doc.Notes,
		LeftSigLabel:  "Counted By",
		LeftSigName:   doc.CountedBy,
		RightSigLabel: approvedLabel,
		RightSigName:  doc.ApprovedBy,
		Disclaimer:    disclaimer,
		ErrLabel:      "stock count",
	}
}
