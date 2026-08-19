package render

// XLSX (and CSV) export for the simpleDoc pipeline (simpledoc.go) — the Excel counterpart of
// renderSimpleDoc's PDF, reused by every document type that composes through simpleDoc (goods
// receipts, requisitions, RFQs, purchase returns, stock adjustments, stock counts, bundle specs).
// One generic renderer here means a layout or legibility fix lands for all seven at once, exactly
// like simpledoc.go's own doc comment describes for the PDF side — building a SECOND generic
// pipeline for Excel rather than seven bespoke ones keeps that property for this export format too.
//
// Shares newPalette(...) (and its print-safe TextSafeDarken floor) with the PDF renderer, so brand
// colors read identically and stay identically legible in both formats — see transfer_xlsx.go and
// pdfcolor's doc comment for the same reasoning applied to the stock-transfer documents.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/bengobox/inventory-service/internal/pdfcolor"
)

// simpleDocStyles bundles every style ID used across the sheet so the (many) drawing helpers below
// don't each need a long parameter list.
type simpleDocStyles struct {
	title, tagline, addr                             int
	docTitle, docSub                                 int
	metaLabel, metaValue                             int
	partyTitle, partyName, partyLine                 int
	lead                                             int
	header                                           int
	cell, cellAlt                                    int
	num, numAlt                                      int
	emphasisCell, emphasisNum                        int
	sectionHead                                      int
	totalsLabel, totalsValue, grandLabel, grandValue int
	noteHead, noteLine                               int
	sigLabel, sigValue                               int
	footer                                           int
}

func newSimpleDocStyles(f *excelize.File, pal palette) simpleDocStyles {
	hex := func(c rgb) string { return pdfcolor.ToHex(c.r, c.g, c.b) }
	hexNavy, hexBlue, hexGrey, hexInk, hexLine, hexLightBlue :=
		hex(pal.navy), hex(pal.blue), hex(pal.grey), hex(pal.ink), hex(pal.line), hex(pal.lightBlue)
	const hexZebra = "F7F9FC"

	borderAll := []excelize.Border{
		{Type: "left", Color: hexLine, Style: 1}, {Type: "top", Color: hexLine, Style: 1},
		{Type: "right", Color: hexLine, Style: 1}, {Type: "bottom", Color: hexLine, Style: 1},
	}
	must := func(id int, _ error) int { return id }

	var s simpleDocStyles
	s.title = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 16, Color: hexNavy}}))
	s.tagline = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Italic: true, Size: 9, Color: hexGrey}}))
	s.addr = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9, Color: hexGrey}}))
	s.docTitle = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14, Color: hexNavy}, Alignment: &excelize.Alignment{Horizontal: "right"}}))
	s.docSub = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9, Color: hexGrey}, Alignment: &excelize.Alignment{Horizontal: "right"}}))
	s.metaLabel = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 8.5, Color: hexBlue}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexLightBlue}}, Border: borderAll, Alignment: &excelize.Alignment{Vertical: "center"}}))
	s.metaValue = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 9, Color: hexNavy}, Border: borderAll, Alignment: &excelize.Alignment{Vertical: "center"}}))
	s.partyTitle = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 9, Color: hexBlue}}))
	s.partyName = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 10.5, Color: hexNavy}}))
	s.partyLine = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9, Color: hexGrey}}))
	s.lead = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 10, Color: hexNavy}, Alignment: &excelize.Alignment{WrapText: true}}))
	s.header = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 9, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexNavy}}, Border: borderAll, Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true}}))
	s.cell = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9.5, Color: hexInk}, Border: borderAll, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}}))
	s.cellAlt = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9.5, Color: hexInk}, Border: borderAll, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexZebra}}, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}}))
	s.num = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9.5, Color: hexInk}, Border: borderAll, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}}))
	s.numAlt = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9.5, Color: hexInk}, Border: borderAll, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexZebra}}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}}))
	s.emphasisCell = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 9.5, Color: hexNavy}, Border: borderAll, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexLightBlue}}, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}}))
	s.emphasisNum = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 9.5, Color: hexNavy}, Border: borderAll, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexLightBlue}}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}}))
	s.sectionHead = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 8.5, Color: hexBlue}}))
	s.totalsLabel = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 10, Color: hexGrey}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexLightBlue}}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}}))
	s.totalsValue = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 10, Color: hexNavy}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexLightBlue}}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}}))
	s.grandLabel = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 12, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexNavy}}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}}))
	s.grandValue = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 12.5, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexNavy}}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}}))
	s.noteHead = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 8.5, Color: hexBlue}}))
	s.noteLine = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 8.5, Color: hexGrey}}))
	s.sigLabel = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 8.5, Color: hexGrey}, Border: []excelize.Border{{Type: "top", Color: hexLine, Style: 1}}}))
	s.sigValue = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 8.5, Color: hexGrey}}))
	s.footer = must(f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 7.5, Italic: true, Color: hexGrey}}))
	return s
}

// renderSimpleDocXLSX builds an Excel workbook for any document that composes through simpleDoc —
// see this file's doc comment. Laid out to double as a print-ready document (bordered/colored
// table, repeating header row, A4 page setup) rather than a bare data dump, same as
// RenderTransferXLSX.
func renderSimpleDocXLSX(d simpleDoc) ([]byte, error) {
	pal := newPalette(d.Branding.PrimaryColor)
	f := excelize.NewFile()
	defer f.Close()
	sheet := xlsxSheetName(ifEmpty(d.Title, "Document"))
	f.SetSheetName("Sheet1", sheet)
	_ = f.SetSheetView(sheet, 0, &excelize.ViewOptions{ShowGridLines: boolPtr(false)})
	st := newSimpleDocStyles(f, pal)

	cols := d.Columns
	if d.Numbered {
		cols = append([]docColumn{{Title: "#", Width: 9}}, cols...)
	}
	if len(cols) == 0 {
		cols = []docColumn{{Title: "DESCRIPTION"}}
	}
	letters := colLetters(len(cols))
	lastCol := letters[len(letters)-1]

	row := 1
	set := func(col string, r int, v interface{}, style int) {
		ref := col + strconv.Itoa(r)
		_ = f.SetCellValue(sheet, ref, v)
		_ = f.SetCellStyle(sheet, ref, ref, style)
	}
	merge := func(fromCol, toCol string, r int) {
		if fromCol != toCol {
			_ = f.MergeCell(sheet, fromCol+strconv.Itoa(r), toCol+strconv.Itoa(r))
		}
	}
	midCol := letters[len(letters)/2]

	// ── Letterhead ──
	b := d.Branding
	merge("A", midCol, row)
	set("A", row, ifEmpty(b.CompanyName, "—"), st.title)
	if midCol != lastCol {
		merge(nextCol(midCol), lastCol, row)
		set(nextCol(midCol), row, strings.ToUpper(ifEmpty(d.Title, "DOCUMENT")), st.docTitle)
	}
	row++
	if b.Tagline != "" {
		merge("A", midCol, row)
		set("A", row, b.Tagline, st.tagline)
	}
	if d.Subtitle != "" && midCol != lastCol {
		merge(nextCol(midCol), lastCol, row)
		set(nextCol(midCol), row, strings.ToUpper(d.Subtitle), st.docSub)
	}
	row++
	for _, ln := range companyAddressLines(b) {
		merge("A", midCol, row)
		set("A", row, ln, st.addr)
		row++
	}
	row++ // spacer

	// ── Meta rows (document number, date, status, …) ──
	for _, m := range d.MetaRows {
		set("A", row, strings.ToUpper(m[0]), st.metaLabel)
		merge("B", lastCol, row)
		set("B", row, m[1], st.metaValue)
		row++
	}
	if len(d.MetaRows) > 0 {
		row++
	}

	// ── Party cards ──
	if d.Parties != nil {
		half := maxInt(1, len(letters)/2)
		leftEnd, rightStart := letters[half-1], letters[half%len(letters)]
		if half >= len(letters) {
			rightStart = lastCol
		}
		draw := func(fromCol, toCol string, c partyCard) {
			merge(fromCol, toCol, row)
			set(fromCol, row, strings.ToUpper(c.Title), st.partyTitle)
		}
		draw("A", leftEnd, d.Parties[0])
		draw(rightStart, lastCol, d.Parties[1])
		row++
		drawVal := func(fromCol, toCol string, c partyCard) {
			merge(fromCol, toCol, row)
			set(fromCol, row, orDash(c.Name), st.partyName)
		}
		drawVal("A", leftEnd, d.Parties[0])
		drawVal(rightStart, lastCol, d.Parties[1])
		row++
		maxLines := maxInt(len(nonEmpty(d.Parties[0].Lines)), len(nonEmpty(d.Parties[1].Lines)))
		leftLines, rightLines := nonEmpty(d.Parties[0].Lines), nonEmpty(d.Parties[1].Lines)
		for i := 0; i < maxLines; i++ {
			if i < len(leftLines) {
				merge("A", leftEnd, row)
				set("A", row, leftLines[i], st.partyLine)
			}
			if i < len(rightLines) {
				merge(rightStart, lastCol, row)
				set(rightStart, row, rightLines[i], st.partyLine)
			}
			row++
		}
		row++
	}

	// ── Lead line ──
	if strings.TrimSpace(d.Lead) != "" {
		merge("A", lastCol, row)
		set("A", row, d.Lead, st.lead)
		row += 2
	}

	// ── Item table ──
	tableHeaderRow := row
	row = xlsxDrawTable(f, sheet, set, letters, row, d.Numbered, cols, d.Rows, st)

	// ── Appendix (e.g. an RFQ's supplier-quotation summary) — never numbered; its own column set. ──
	if len(d.AppendixRows) > 0 {
		row++
		aCols := d.AppendixColumns
		if len(aCols) == 0 {
			aCols = cols
		}
		aLetters := colLetters(len(aCols))
		if strings.TrimSpace(d.AppendixTitle) != "" {
			merge("A", lastCol, row)
			set("A", row, strings.ToUpper(d.AppendixTitle), st.sectionHead)
			row++
		}
		row = xlsxDrawTable(f, sheet, set, aLetters, row, false, aCols, d.AppendixRows, st)
	}

	// ── Totals ──
	if len(d.Totals) > 0 {
		row++
		labelCol := letters[maxInt(0, len(letters)-2)]
		for _, t := range d.Totals {
			if strings.TrimSpace(t.Value) == "" {
				continue
			}
			labelStyle, valueStyle := st.totalsLabel, st.totalsValue
			if t.Grand {
				labelStyle, valueStyle = st.grandLabel, st.grandValue
			}
			merge("A", labelCol, row)
			set("A", row, ifEmpty(t.Label, "Total"), labelStyle)
			set(lastCol, row, t.Value, valueStyle)
			row++
		}
		row++
	}

	// ── Notes ──
	if notes := nonEmpty(d.Notes); len(notes) > 0 {
		merge("A", lastCol, row)
		set("A", row, strings.ToUpper(ifEmpty(d.NotesTitle, "NOTES")), st.noteHead)
		row++
		for _, n := range notes {
			merge("A", lastCol, row)
			set("A", row, "-  "+n, st.noteLine)
			row++
		}
		row++
	}

	// ── Sign-off ──
	if strings.TrimSpace(d.LeftSigLabel) != "" || strings.TrimSpace(d.RightSigLabel) != "" {
		row++
		if strings.TrimSpace(d.LeftSigLabel) != "" {
			merge("A", midCol, row)
			set("A", row, ifEmpty(d.LeftSigName, " "), st.sigLabel)
			merge("A", midCol, row+1)
			set("A", row+1, d.LeftSigLabel, st.sigValue)
		}
		if strings.TrimSpace(d.RightSigLabel) != "" && midCol != lastCol {
			merge(nextCol(midCol), lastCol, row)
			set(nextCol(midCol), row, ifEmpty(d.RightSigName, " "), st.sigLabel)
			merge(nextCol(midCol), lastCol, row+1)
			set(nextCol(midCol), row+1, d.RightSigLabel, st.sigValue)
		}
		row += 3
	}

	// ── Footer ──
	if meta := footerMeta(b); meta != "" {
		merge("A", lastCol, row)
		set("A", row, meta, st.footer)
		row++
	}

	// ── Column widths ──
	for i, c := range cols {
		w := 40.0
		if c.Width > 0 {
			w = excelColWidth(c.Width)
		}
		_ = f.SetColWidth(sheet, letters[i], letters[i], w)
	}
	_ = f.SetRowHeight(sheet, tableHeaderRow, 16)

	xlsxSetPrintSetup(f, sheet, lastCol, tableHeaderRow, row, letters[0]+strconv.Itoa(tableHeaderRow+1))

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, fmt.Errorf("render %s xlsx: %w", ifEmpty(d.ErrLabel, "document"), err)
	}
	return buf.Bytes(), nil
}

// xlsxDrawTable renders one column-driven table (the item table, or an appendix table) starting
// at ty and returns the row just below its last line. Mirrors drawDocTable's flex-column and
// numbered-column handling so the same docColumn/docRow values used by the PDF renderer produce
// the equivalent Excel table. numbered must say whether cols[0] is the auto "#" column the caller
// prepended — an appendix table (e.g. an RFQ's supplier-quotation summary) is never numbered, and
// treating its real first column as an auto-index would silently overwrite its content.
func xlsxDrawTable(f *excelize.File, sheet string, set func(string, int, interface{}, int), letters []string, ty int, numbered bool, cols []docColumn, rows []docRow, st simpleDocStyles) int {
	flexIdx := -1
	for i, c := range cols {
		if c.Width <= 0 {
			flexIdx = i
			break
		}
	}
	// Every caller builds docRow.Cells positionally against its OWN column list — Cells[0] is
	// always the first real (non-"#") column. cols here already has "#" prepended when numbered,
	// so a column index ci is one AHEAD of its matching Cells index; cellAt corrects for that. See
	// common.go's drawDocTable doc comment for the exact bug this mirrors and fixes.
	cellOffset := 0
	if numbered {
		cellOffset = 1
	}
	cellAt := func(r docRow, ci int) string {
		j := ci - cellOffset
		if j >= 0 && j < len(r.Cells) {
			return r.Cells[j]
		}
		return ""
	}
	row := ty
	for i, c := range cols {
		set(letters[i], row, c.Title, st.header)
	}
	row++
	for ri, r := range rows {
		alt := ri%2 == 1
		cellStyle, numStyle := st.cell, st.num
		if alt {
			cellStyle, numStyle = st.cellAlt, st.numAlt
		}
		if r.Emphasis {
			cellStyle, numStyle = st.emphasisCell, st.emphasisNum
		}
		for ci, c := range cols {
			ref := letters[ci]
			switch {
			case ci == flexIdx:
				desc := cellAt(r, ci)
				setRichDescCell(f, sheet, ref+strconv.Itoa(row), desc, r.SubDesc, r.Emphasis, st)
				_ = f.SetCellStyle(sheet, ref+strconv.Itoa(row), ref+strconv.Itoa(row), cellStyle)
			default:
				v := ""
				switch {
				case numbered && ci == 0:
					v = strconv.Itoa(ri + 1)
				default:
					v = cellAt(r, ci)
				}
				if c.Right {
					if n, err := strconv.ParseFloat(strings.TrimSpace(stripThousands(v)), 64); err == nil {
						set(ref, row, n, numStyle)
					} else {
						set(ref, row, v, numStyle)
					}
				} else {
					set(ref, row, v, cellStyle)
				}
			}
		}
		row++
	}
	return row
}

// setRichDescCell writes the flex/description column as bold text plus an italic muted sub-line
// (SKU, lot number, rejection reason, …) in the SAME cell — matching the PDF's stacked
// description+subDesc layout instead of pushing the sub-line into its own column or row.
func setRichDescCell(f *excelize.File, sheet, cellRef, desc, subDesc string, emphasis bool, st simpleDocStyles) {
	descColor := "22303F" // pal.ink is fixed; a literal here avoids threading the palette into this leaf helper
	if emphasis {
		descColor = "1F3B57"
	}
	if strings.TrimSpace(subDesc) == "" {
		_ = f.SetCellValue(sheet, cellRef, desc)
		return
	}
	_ = f.SetCellRichText(sheet, cellRef, []excelize.RichTextRun{
		{Text: desc, Font: &excelize.Font{Bold: true, Size: 9.5, Color: descColor}},
		{Text: "\r\n" + subDesc, Font: &excelize.Font{Italic: true, Size: 8.5, Color: "6B7A90"}},
	})
}

// stripThousands removes thousands-separator commas so a pre-formatted money string like
// "12,345.67" still parses as a real Excel number instead of falling back to text.
func stripThousands(s string) string { return strings.ReplaceAll(s, ",", "") }

// excelColWidth converts a PDF column width in millimetres to an approximate Excel column-width
// unit (roughly 1.7mm per unit at the default Calibri 11 font), with a floor so narrow numeric
// columns (e.g. a 9mm "#" column) never clip their header text.
func excelColWidth(mm float64) float64 {
	w := mm / 1.7
	if w < 6 {
		return 6
	}
	return w
}

// nextCol returns the excel column letter immediately after col (e.g. "B" -> "C").
func nextCol(col string) string {
	n, _ := excelize.ColumnNameToNumber(col)
	name, _ := excelize.ColumnNumberToName(n + 1)
	return name
}

// xlsxSetPrintSetup applies the same print-ready page setup used by RenderTransferXLSX (A4,
// fit-to-width, repeating letterhead+column headers, no default gridlines, frozen header row) to
// any simpleDoc-based sheet.
func xlsxSetPrintSetup(f *excelize.File, sheet, lastCol string, tableHeaderRow, lastRow int, freezeTopLeft string) {
	_ = f.SetPageLayout(sheet, &excelize.PageLayoutOptions{
		Size: intPtr(9), Orientation: strPtr("portrait"), FitToWidth: intPtr(1), FitToHeight: intPtr(0),
	})
	_ = f.SetPageMargins(sheet, &excelize.PageLayoutMarginsOptions{
		Top: f64Ptr(0.6), Bottom: f64Ptr(0.6), Left: f64Ptr(0.4), Right: f64Ptr(0.4),
		Header: f64Ptr(0.2), Footer: f64Ptr(0.2),
	})
	_ = f.SetHeaderFooter(sheet, &excelize.HeaderFooterOptions{OddFooter: "&C&8Page &P of &N"})
	_ = f.SetDefinedName(&excelize.DefinedName{
		Name: "_xlnm.Print_Titles", Scope: sheet,
		RefersTo: fmt.Sprintf("'%s'!$1:$%d", sheet, tableHeaderRow),
	})
	_ = f.SetDefinedName(&excelize.DefinedName{
		Name: "_xlnm.Print_Area", Scope: sheet,
		RefersTo: fmt.Sprintf("'%s'!$A$1:$%s$%d", sheet, lastCol, lastRow),
	})
	_ = f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, XSplit: 0, YSplit: tableHeaderRow, TopLeftCell: freezeTopLeft, ActivePane: "bottomLeft",
	})
}

// renderSimpleDocCSV flattens a simpleDoc into a uniform-width CSV — see RenderTransferCSV's doc
// comment for why every row (not just the item table) is padded to the same column count.
func renderSimpleDocCSV(d simpleDoc) ([]byte, error) {
	cols := d.Columns
	if d.Numbered {
		// Width must be > 0 here — csvFromColumnsAndRows finds the flex/description column by
		// its FIRST Width<=0 entry, and the zero value of an unset Width field is 0, which would
		// otherwise make this synthetic "#" column look like the flex column instead of the real
		// DESCRIPTION column one slot over (see renderSimpleDocXLSX's matching "#" prepend, which
		// already sets Width: 9 for the same reason).
		cols = append([]docColumn{{Title: "#", Width: 9}}, cols...)
	}
	return csvFromColumnsAndRows(strings.ToUpper(ifEmpty(d.Title, "DOCUMENT")), d.MetaRows, d.Numbered, cols, d.Rows, d.Totals, d.Notes)
}
