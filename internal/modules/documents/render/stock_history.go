package render

// Product stock-history export — the branded, printable snapshot of a single item's unified
// movement ledger (adjustments/purchases/sales/returns/transfers) for whatever warehouse/date/
// type filter the caller currently has applied on the Stock History page. Composes the shared
// simpleDoc pipeline like stock_adjustment.go; no financial totals since this is an audit trail,
// not an invoice.

// StockHistoryDoc is the canonical input for rendering a stock-history export.
type StockHistoryDoc struct {
	Branding Branding

	ItemName string
	ItemSKU  string
	// Scope describes the applied warehouse/date/type filters as a human-readable line, e.g.
	// "Main Warehouse · 01 Aug 2026 – 26 Aug 2026 · Sales only". Empty when unfiltered.
	Scope string

	// Summary feeds the meta box (Opening Stock / Total Purchased / Total Sold / Current Stock…)
	// as label/value pairs — reuses the same cards the on-screen page shows.
	Summary [][2]string

	Items []StockHistoryDocLine
}

// StockHistoryDocLine is a single ledger row.
type StockHistoryDocLine struct {
	Date           string
	Type           string
	Reference      string
	Location       string
	Counterparty   string // customer (sales) or supplier (purchases)
	User           string
	QuantityChange string // signed, e.g. "+3" / "-12"
}

// RenderStockHistory builds a premium, tenant-branded A4 stock-history export and returns PDF
// bytes.
func RenderStockHistory(doc *StockHistoryDoc, logo []byte, logoType string) ([]byte, error) {
	return renderSimpleDoc(stockHistorySimpleDoc(doc), logo, logoType)
}

// RenderStockHistoryXLSX renders the same stock-history export as a styled, print-ready Excel
// workbook.
func RenderStockHistoryXLSX(doc *StockHistoryDoc) ([]byte, error) {
	return renderSimpleDocXLSX(stockHistorySimpleDoc(doc))
}

// RenderStockHistoryCSV renders the stock-history export's data as plain CSV.
func RenderStockHistoryCSV(doc *StockHistoryDoc) ([]byte, error) {
	return renderSimpleDocCSV(stockHistorySimpleDoc(doc))
}

// stockHistorySimpleDoc maps a StockHistoryDoc into the shared simpleDoc pipeline — the one
// definition of this document's shape shared by every export format (PDF/XLSX/CSV above).
func stockHistorySimpleDoc(doc *StockHistoryDoc) simpleDoc {
	rows := make([]docRow, 0, len(doc.Items))
	for _, it := range doc.Items {
		rows = append(rows, docRow{
			Cells: []string{
				it.Date, it.Type, it.Reference, it.Location, it.Counterparty, it.User, it.QuantityChange,
			},
		})
	}

	meta := [][2]string{
		{"Item", orDash(doc.ItemName)},
		{"SKU", orDash(doc.ItemSKU)},
		{"Scope", ifEmpty(doc.Scope, "All locations · All time")},
		{"Lines", plural(len(doc.Items), "movement")},
	}

	return simpleDoc{
		Branding: doc.Branding,
		Title:    "Stock History",
		Subtitle: "Product Movement Ledger",
		MetaRows: meta,
		Lead:     "The following stock movements were recorded for " + ifEmpty(doc.ItemName, "this item") + ":",
		Numbered: true,
		Columns: []docColumn{
			{Title: "DATE", Width: 28},
			{Title: "TYPE", Width: 26},
			{Title: "REFERENCE"},
			{Title: "LOCATION", Width: 28},
			{Title: "CUSTOMER/SUPPLIER", Width: 36},
			{Title: "USER", Width: 26},
			{Title: "QTY CHANGE", Width: 22, Right: true},
		},
		Rows: rows,
		// Summary cards reused as the totals stack (label/value, right-aligned) beneath the table.
		Totals: func() []totalRow {
			t := make([]totalRow, 0, len(doc.Summary))
			for _, s := range doc.Summary {
				t = append(t, totalRow{Label: s[0], Value: s[1]})
			}
			return t
		}(),
		Disclaimer: issuedByDisclaimer(doc.Branding, "stock history export"),
		ErrLabel:   "stock history",
	}
}
