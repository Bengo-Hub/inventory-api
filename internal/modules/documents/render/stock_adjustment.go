package render

// Stock-adjustment note rendering — the signed audit record for a batch of manual stock
// corrections (damage, shrinkage, found stock, count variance, …).
//
// StockAdjustment is a PER-LINE audit row with no natural document grouping, so a document here
// is a VIRTUAL batch: every adjustment row sharing one `reference` string. Deliberately carries
// NO financial totals — the entity records no cost, and inventing one from the item's current
// cost price would misstate the value of stock written off at an older layer cost.

import "strings"

// StockAdjustmentDoc is the canonical input for rendering a stock-adjustment note.
type StockAdjustmentDoc struct {
	Branding Branding

	Reference     string
	Date          string
	WarehouseName string
	AdjustedBy    string

	Items []StockAdjustmentDocLine

	Notes      []string
	ApprovedBy string
}

// StockAdjustmentDocLine is a single adjusted item: the balance before, the signed change, and
// the resulting balance — the three numbers an auditor reconciles.
type StockAdjustmentDocLine struct {
	Desc    string
	SubDesc string // SKU / per-line notes
	Before  string
	Change  string // signed, e.g. "-12" / "+3"
	After   string
	Reason  string
}

// RenderStockAdjustment builds a premium, tenant-branded A4 stock-adjustment note and returns
// PDF bytes.
func RenderStockAdjustment(doc *StockAdjustmentDoc, logo []byte, logoType string) ([]byte, error) {
	rows := make([]docRow, 0, len(doc.Items))
	for _, it := range doc.Items {
		rows = append(rows, docRow{
			SubDesc: it.SubDesc,
			Cells: []string{
				it.Desc, it.Before, it.Change, it.After,
				strings.ToUpper(strings.ReplaceAll(it.Reason, "_", " ")),
			},
		})
	}

	meta := [][2]string{
		{"Reference", orDash(doc.Reference)},
		{"Date", orDash(doc.Date)},
		{"Lines", plural(len(doc.Items), "item")},
	}

	return renderSimpleDoc(simpleDoc{
		Branding: doc.Branding,
		Title:    "Stock Adjustment",
		Subtitle: "Inventory Correction Note",
		MetaRows: meta,
		Parties: &[2]partyCard{
			{Title: "WAREHOUSE", Name: doc.WarehouseName},
			{Title: "ADJUSTED BY", Name: doc.AdjustedBy},
		},
		Lead:     "The following stock corrections were recorded against " + ifEmpty(doc.WarehouseName, "this warehouse") + ":",
		Numbered: true,
		Columns: []docColumn{
			{Title: "DESCRIPTION"},
			{Title: "BEFORE", Width: 22, Right: true},
			{Title: "CHANGE", Width: 22, Right: true},
			{Title: "AFTER", Width: 22, Right: true},
			{Title: "REASON", Width: 38},
		},
		Rows:          rows,
		NotesTitle:    "NOTES",
		Notes:         doc.Notes,
		LeftSigLabel:  "Adjusted By",
		LeftSigName:   doc.AdjustedBy,
		RightSigLabel: "Approved By",
		RightSigName:  doc.ApprovedBy,
		Disclaimer: "This note records manual inventory corrections made by " +
			ifEmpty(doc.Branding.CompanyName, "the issuer") + " and carries no monetary value.",
		ErrLabel: "stock adjustment",
	}, logo, logoType)
}
