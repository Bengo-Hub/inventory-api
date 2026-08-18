package render

// XLSX export for the stock-transfer documents (Dispatch/Transit Note + Goods-Received Note) —
// the Excel counterpart of transfer.go's PDF renderer. Shares the SAME newPalette(...) derivation
// (and therefore the SAME print-safe TextSafeDarken floor — see pdfcolor's doc comment) so a
// tenant's brand color reads identically, and stays identically legible, in both export formats.
//
// Laid out to double as a print-ready document rather than a bare data dump: a lettterhead block,
// a bordered/colored item table with a repeating header row, and a totals footer row — with the
// sheet's page setup (A4, fit-to-width, repeated header rows, no default gridlines) pre-configured
// so "File > Print" (or Excel's own Save-as-PDF) produces something that looks like the PDF
// sibling document, not a spreadsheet printout.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/bengobox/inventory-service/internal/pdfcolor"
)

// RenderTransferXLSX builds an Excel workbook for a TransferDoc (Delivery Note or Goods-Received
// Note, mirroring RenderTransfer's PDF) and returns the .xlsx bytes.
func RenderTransferXLSX(d *TransferDoc) ([]byte, error) {
	pal := newPalette(d.Branding.PrimaryColor)
	hexNavy := pdfcolor.ToHex(pal.navy.r, pal.navy.g, pal.navy.b)
	hexLightBlue := pdfcolor.ToHex(pal.lightBlue.r, pal.lightBlue.g, pal.lightBlue.b)
	hexLine := pdfcolor.ToHex(pal.line.r, pal.line.g, pal.line.b)
	hexGrey := pdfcolor.ToHex(pal.grey.r, pal.grey.g, pal.grey.b)
	hexInk := pdfcolor.ToHex(pal.ink.r, pal.ink.g, pal.ink.b)
	hexWarn := pdfcolor.ToHex(pal.warn.r, pal.warn.g, pal.warn.b)
	hexZebra := "F7F9FC" // a fixed, very light neutral tint — independent of brand color so
	// zebra striping never gets brand-tinted enough to blur into the header/border lines.

	showReceived := false
	for _, it := range d.Items {
		if it.ReceivedQty != "" {
			showReceived = true
			break
		}
	}
	// Column layout: # | Description | SKU | Unit | Shipped [| Received | Variance] [| Notes]
	lastCol := "E"
	if showReceived {
		lastCol = "H"
	}

	f := excelize.NewFile()
	defer f.Close()
	sheet := xlsxSheetName(ifEmpty(d.DocTitle, "Transfer Document"))
	f.SetSheetName("Sheet1", sheet)
	_ = f.SetSheetView(sheet, 0, &excelize.ViewOptions{ShowGridLines: boolPtr(false)})

	borderAll := []excelize.Border{
		{Type: "left", Color: hexLine, Style: 1},
		{Type: "top", Color: hexLine, Style: 1},
		{Type: "right", Color: hexLine, Style: 1},
		{Type: "bottom", Color: hexLine, Style: 1},
	}

	titleStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 16, Color: hexNavy}})
	taglineStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Italic: true, Size: 9, Color: hexGrey}})
	addrStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9, Color: hexGrey}})
	docTitleStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 14, Color: hexNavy},
		Alignment: &excelize.Alignment{Horizontal: "right"},
	})
	docSubStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 9, Color: hexGrey},
		Alignment: &excelize.Alignment{Horizontal: "right"},
	})
	metaLabelStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 8.5, Color: pdfcolor.ToHex(pal.blue.r, pal.blue.g, pal.blue.b)},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexLightBlue}},
		Border:    borderAll,
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
	})
	metaValueStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 9, Color: hexNavy},
		Border:    borderAll,
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
	})
	partyTitleStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 9, Color: pdfcolor.ToHex(pal.blue.r, pal.blue.g, pal.blue.b)}})
	partyNameStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 10.5, Color: hexNavy}})
	partyLineStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9, Color: hexGrey}})
	ackStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 10, Color: hexNavy}})
	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 9, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexNavy}},
		Border:    borderAll,
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
	})
	cellStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9.5, Color: hexInk}, Border: borderAll, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}})
	cellAltStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9.5, Color: hexInk}, Border: borderAll, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexZebra}}, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}})
	numStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9.5, Color: hexInk}, Border: borderAll, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}, CustomNumFmt: strPtr("0.####")})
	numAltStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 9.5, Color: hexInk}, Border: borderAll, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexZebra}}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}, CustomNumFmt: strPtr("0.####")})
	varStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 9.5, Color: hexWarn}, Border: borderAll, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}, CustomNumFmt: strPtr("0.####;-0.####;\"0\"")})
	varAltStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 9.5, Color: hexWarn}, Border: borderAll, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexZebra}}, Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"}, CustomNumFmt: strPtr("0.####;-0.####;\"0\"")})
	notesCellStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 8.5, Italic: true, Color: hexGrey}, Border: borderAll, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}})
	notesCellAltStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 8.5, Italic: true, Color: hexGrey}, Border: borderAll, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexZebra}}, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}})
	totalsLabelStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 9.5, Color: hexNavy},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexLightBlue}},
		Border:    borderAll,
		Alignment: &excelize.Alignment{Horizontal: "right", Vertical: "center"},
	})
	totalsValStyle, _ := f.NewStyle(&excelize.Style{
		Font:         &excelize.Font{Bold: true, Size: 9.5, Color: hexNavy},
		Fill:         excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hexLightBlue}},
		Border:       borderAll,
		Alignment:    &excelize.Alignment{Horizontal: "right", Vertical: "center"},
		CustomNumFmt: strPtr("0.####"),
	})
	sectionHeadStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 8.5, Color: pdfcolor.ToHex(pal.blue.r, pal.blue.g, pal.blue.b)}})
	noteLineStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 8.5, Color: hexGrey}})
	sigLabelStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 8.5, Color: hexGrey}, Border: []excelize.Border{{Type: "top", Color: hexLine, Style: 1}}})
	footerStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 7.5, Italic: true, Color: hexGrey}})

	row := 1
	set := func(col string, r int, v interface{}, style int) {
		ref := col + strconv.Itoa(r)
		_ = f.SetCellValue(sheet, ref, v)
		_ = f.SetCellStyle(sheet, ref, ref, style)
	}
	merge := func(fromCol, toCol string, r int) {
		_ = f.MergeCell(sheet, fromCol+strconv.Itoa(r), toCol+strconv.Itoa(r))
	}

	// ── Letterhead: company identity (left) + doc title/transfer no. (right) ──
	b := d.Branding
	merge("A", "D", row)
	set("A", row, ifEmpty(b.CompanyName, "—"), titleStyle)
	merge("E", lastCol, row)
	set("E", row, strings.ToUpper(ifEmpty(d.DocTitle, "STOCK TRANSFER")), docTitleStyle)
	row++
	if b.Tagline != "" {
		merge("A", "D", row)
		set("A", row, b.Tagline, taglineStyle)
	}
	merge("E", lastCol, row)
	set("E", row, strings.ToUpper(d.DocSubtitle), docSubStyle)
	row++
	for _, ln := range companyAddressLines(b) {
		merge("A", "D", row)
		set("A", row, ln, addrStyle)
		row++
	}
	if b.KRAPIN != "" {
		merge("A", "D", row)
		set("A", row, "KRA PIN: "+b.KRAPIN, addrStyle)
		row++
	}
	row++ // spacer

	// ── Meta strip: Transfer No. / Date / Reference / Carrier as label:value cell pairs ──
	metaRow := row
	metaCols := [][3]string{{"A", "B", "Transfer No."}, {"C", "D", "Date"}}
	if strings.TrimSpace(d.Reference) != "" {
		metaCols = append(metaCols, [3]string{"E", "F", "Reference"})
	}
	if strings.TrimSpace(d.Carrier) != "" {
		metaCols = append(metaCols, [3]string{"G", "H", "Carrier"})
	}
	metaVals := map[string]string{"Transfer No.": orDash(d.TransferNumber), "Date": orDash(d.Date), "Reference": d.Reference, "Carrier": d.Carrier}
	for _, mc := range metaCols {
		if mc[0] > lastCol {
			continue
		}
		set(mc[0], metaRow, strings.ToUpper(mc[2]), metaLabelStyle)
		set(mc[1], metaRow, metaVals[mc[2]], metaValueStyle)
	}
	row = metaRow + 2

	// ── Party cards: FROM WAREHOUSE (left) / TO WAREHOUSE (right) ──
	partyRow := row
	merge("A", "D", partyRow)
	set("A", partyRow, "FROM WAREHOUSE", partyTitleStyle)
	merge("E", lastCol, partyRow)
	set("E", partyRow, "TO WAREHOUSE", partyTitleStyle)
	partyRow++
	merge("A", "D", partyRow)
	set("A", partyRow, orDash(d.FromWarehouseName), partyNameStyle)
	merge("E", lastCol, partyRow)
	set("E", partyRow, orDash(d.ToWarehouseName), partyNameStyle)
	partyRow++
	for i := 0; i < maxInt(len(d.FromWarehouseAddr), len(d.ToWarehouseAddr)); i++ {
		if i < len(d.FromWarehouseAddr) && strings.TrimSpace(d.FromWarehouseAddr[i]) != "" {
			merge("A", "D", partyRow)
			set("A", partyRow, d.FromWarehouseAddr[i], partyLineStyle)
		}
		if i < len(d.ToWarehouseAddr) && strings.TrimSpace(d.ToWarehouseAddr[i]) != "" {
			merge("E", lastCol, partyRow)
			set("E", partyRow, d.ToWarehouseAddr[i], partyLineStyle)
		}
		partyRow++
	}
	row = partyRow + 1

	if strings.TrimSpace(d.AcknowledgementText) != "" {
		merge("A", lastCol, row)
		set("A", row, d.AcknowledgementText, ackStyle)
		row += 2
	}

	// ── Item table ──
	headers := []string{"#", "DESCRIPTION", "SKU", "UNIT", "SHIPPED"}
	if showReceived {
		headers = append(headers, "RECEIVED", "VARIANCE", "NOTES")
	}
	tableHeaderRow := row
	cols := colLetters(len(headers))
	for i, h := range headers {
		set(cols[i], row, h, headerStyle)
	}
	row++

	var sumShipped, sumReceived, sumVariance float64
	var anyReceived bool
	for i, it := range d.Items {
		alt := i%2 == 1
		cs, ns, vs, os := cellStyle, numStyle, varStyle, notesCellStyle
		if alt {
			cs, ns, vs, os = cellAltStyle, numAltStyle, varAltStyle, notesCellAltStyle
		}
		set(cols[0], row, i+1, cs)
		set(cols[1], row, it.Desc, cs)
		set(cols[2], row, it.SubDesc, cs)
		set(cols[3], row, it.Unit, cs)
		qty, _ := strconv.ParseFloat(strings.TrimSpace(it.Qty), 64)
		set(cols[4], row, qty, ns)
		sumShipped += qty
		if showReceived {
			receivedText := ifEmpty(it.ReceivedQty, it.Qty)
			received, err := strconv.ParseFloat(strings.TrimSpace(receivedText), 64)
			if err == nil {
				anyReceived = true
				set(cols[5], row, received, ns)
				variance := received - qty
				sumReceived += received
				sumVariance += variance
				set(cols[6], row, variance, vs)
			}
			set(cols[7], row, it.VarianceReason, os)
		}
		row++
	}

	// ── Totals footer row ──
	merge(cols[0], cols[3], row)
	set(cols[0], row, "TOTALS", totalsLabelStyle)
	set(cols[4], row, sumShipped, totalsValStyle)
	if showReceived {
		if anyReceived {
			set(cols[5], row, sumReceived, totalsValStyle)
			set(cols[6], row, sumVariance, totalsValStyle)
		} else {
			set(cols[5], row, "", totalsLabelStyle)
			set(cols[6], row, "", totalsLabelStyle)
		}
		set(cols[7], row, "", totalsLabelStyle)
	}
	row += 2

	// ── Notes ──
	notes := nonEmpty(d.Notes)
	if len(notes) > 0 {
		merge("A", lastCol, row)
		set("A", row, "NOTES", sectionHeadStyle)
		row++
		for _, n := range notes {
			merge("A", lastCol, row)
			set("A", row, "-  "+n, noteLineStyle)
			row++
		}
		row++
	}

	// ── Sign-off ──
	if strings.TrimSpace(d.LeftSigLabel) != "" || strings.TrimSpace(d.RightSigLabel) != "" {
		row++
		if strings.TrimSpace(d.LeftSigLabel) != "" {
			merge("A", "D", row)
			set("A", row, ifEmpty(d.LeftSigName, " "), sigLabelStyle)
			merge("A", "D", row+1)
			set("A", row+1, d.LeftSigLabel, noteLineStyle)
		}
		if strings.TrimSpace(d.RightSigLabel) != "" {
			merge("E", lastCol, row)
			set("E", row, ifEmpty(d.RightSigName, " "), sigLabelStyle)
			merge("E", lastCol, row+1)
			set("E", row+1, d.RightSigLabel, noteLineStyle)
		}
		row += 3
	}

	// ── Footer: tenant contact + platform note ──
	if meta := footerMeta(b); meta != "" {
		merge("A", lastCol, row)
		set("A", row, meta, footerStyle)
		row++
	}

	// ── Column widths ──
	_ = f.SetColWidth(sheet, "A", "A", 5)
	_ = f.SetColWidth(sheet, "B", "B", 34)
	_ = f.SetColWidth(sheet, "C", "C", 16)
	_ = f.SetColWidth(sheet, "D", "D", 9)
	_ = f.SetColWidth(sheet, "E", "E", 11)
	if showReceived {
		_ = f.SetColWidth(sheet, "F", "G", 11)
		_ = f.SetColWidth(sheet, "H", "H", 22)
	}
	_ = f.SetRowHeight(sheet, tableHeaderRow, 16)

	// ── Print setup: A4, fit-to-width, letterhead+column-header repeats on every printed page,
	// no on-screen gridline clutter (the table's own borders carry that job) — so opening this
	// file and choosing Print (or Save As PDF) produces a document, not a spreadsheet printout.
	_ = f.SetPageLayout(sheet, &excelize.PageLayoutOptions{
		Size:        intPtr(9), // A4
		Orientation: strPtr("portrait"),
		FitToWidth:  intPtr(1),
		FitToHeight: intPtr(0),
	})
	_ = f.SetPageMargins(sheet, &excelize.PageLayoutMarginsOptions{
		Top: f64Ptr(0.6), Bottom: f64Ptr(0.6), Left: f64Ptr(0.4), Right: f64Ptr(0.4),
		Header: f64Ptr(0.2), Footer: f64Ptr(0.2),
	})
	_ = f.SetHeaderFooter(sheet, &excelize.HeaderFooterOptions{
		OddFooter: fmt.Sprintf("&L&8%s&C&8Page &P of &N&R&8%s", d.TransferNumber, ifEmpty(b.CompanyName, "")),
	})
	_ = f.SetDefinedName(&excelize.DefinedName{
		Name: "_xlnm.Print_Titles", Scope: sheet,
		RefersTo: fmt.Sprintf("'%s'!$1:$%d", sheet, tableHeaderRow),
	})
	_ = f.SetDefinedName(&excelize.DefinedName{
		Name: "_xlnm.Print_Area", Scope: sheet,
		RefersTo: fmt.Sprintf("'%s'!$A$1:$%s$%d", sheet, lastCol, row),
	})
	_ = f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, XSplit: 0, YSplit: tableHeaderRow,
		TopLeftCell: cols[0] + strconv.Itoa(tableHeaderRow+1), ActivePane: "bottomLeft",
	})
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, fmt.Errorf("render transfer xlsx: %w", err)
	}
	return buf.Bytes(), nil
}

// xlsxSheetName sanitizes a document title into a valid Excel sheet name: <=31 chars, none of
// the characters Excel rejects in a sheet name ( : \ / ? * [ ] ).
func xlsxSheetName(title string) string {
	r := strings.NewReplacer(":", "-", "\\", "-", "/", "-", "?", "", "*", "", "[", "(", "]", ")")
	s := strings.TrimSpace(r.Replace(title))
	if s == "" {
		s = "Document"
	}
	if len(s) > 31 {
		s = s[:31]
	}
	return s
}

// colLetters returns the first n excel column letters starting at "A".
func colLetters(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		name, _ := excelize.ColumnNumberToName(i + 1)
		out[i] = name
	}
	return out
}

func boolPtr(b bool) *bool      { return &b }
func intPtr(i int) *int         { return &i }
func f64Ptr(f float64) *float64 { return &f }
func strPtr(s string) *string   { return &s }
