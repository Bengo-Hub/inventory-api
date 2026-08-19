package render

// Generic, document-type-agnostic layout blocks shared by every document this package renders.
//
// The purchase-order (render.go/header.go/table.go) and stock-transfer (transfer.go) renderers
// predate these and keep their own bespoke drawing functions, because each hardcodes something
// document-specific (drawHeader's literal "PURCHASE ORDER" title, drawItems' currency-suffixed
// AMOUNT column, drawTransferItems' conditional RECEIVED/VARIANCE pair). Everything added after
// them — goods receipts, requisitions, RFQs, purchase returns, stock adjustments, stock counts,
// bundle specs — composes THESE instead, so a layout fix lands once rather than nine times.
//
// The item table below (drawDocTable) implements the SAME measure-before-draw pagination
// contract as table.go/drawTransferItems: every row's height is measured with rowHeight before
// any of its cells are drawn, so a row that wouldn't fit starts a fresh page (header bar
// repeated) atomically. See primitives.go's pageBottomSafe doc comment for why a page-break-blind
// table silently corrupts long documents.

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
)

// drawDocHeader renders the logo (left), a caller-supplied document title + subtitle (right) and
// the gradient rule beneath. The transfer/PO-specific headers hardcode their title; this one
// takes it, so every new document type reuses the identical masthead.
func (p *painter) drawDocHeader(b Branding, logo []byte, logoType, title, subtitle string) float64 {
	topY := 12.0
	logoH := 15.0
	if len(logo) > 0 {
		if logoType == "" {
			logoType = imgType(logo)
		}
		info := p.pdf.RegisterImageOptionsReader("logo",
			fpdf.ImageOptions{ImageType: logoType}, bytes.NewReader(logo))
		if info != nil && info.Height() > 0 {
			logoW := logoH * info.Width() / info.Height()
			p.pdf.ImageOptions("logo", leftX, topY, logoW, logoH, false,
				fpdf.ImageOptions{ImageType: logoType}, 0, "")
		}
	}

	// Title size steps down for long titles ("STOCK ADJUSTMENT NOTE") so they never collide with
	// the logo on the left.
	p.textR(rightX, topY+0.5, strings.ToUpper(ifEmpty(title, "DOCUMENT")), "B", headerTitleSize(title), p.pal.navy)
	if strings.TrimSpace(subtitle) != "" {
		p.textR(rightX, topY+11.0, strings.ToUpper(subtitle), "", 7.5, p.pal.blue)
	}

	ruleY := topY + logoH + 2.0
	p.gradient(leftX, ruleY, contentW, 1.4, 0.7, p.pal.sky, p.pal.navy)
	return ruleY
}

// headerTitleSize keeps long document titles inside the right half of the masthead.
func headerTitleSize(title string) float64 {
	switch n := len(strings.TrimSpace(title)); {
	case n > 24:
		return 16
	case n > 18:
		return 19
	default:
		return 22
	}
}

// drawDocIdentity renders the tenant identity block (left) and the meta key/value box (right),
// returning the y below whichever column is taller. rows are drawn in order; empty values are the
// caller's business (use orDash to show an em-dash placeholder).
func (p *painter) drawDocIdentity(b Branding, ruleY float64, rows [][2]string) float64 {
	y := ruleY + 6.0

	p.text(leftX, y, ifEmpty(b.CompanyName, "—"), "B", 15, p.pal.navy)
	if b.Tagline != "" {
		p.text(leftX, y+5.6, b.Tagline, "I", 9, p.pal.blue)
	}
	ly := y + 10.5
	for _, ln := range companyAddressLines(b) {
		p.text(leftX, ly, ln, "", 9, p.pal.grey)
		ly += 4.0
	}
	if b.KRAPIN != "" {
		p.text(leftX, ly, "KRA PIN: "+b.KRAPIN, "", 9, p.pal.grey)
		ly += 4.0
	}

	if len(rows) == 0 {
		return ly
	}
	mbX := 117.0
	mbW := rightX - mbX
	rowH := 6.4
	mbH := rowH * float64(len(rows))
	keyW := mbW * 0.46
	for i, r := range rows {
		ry := y + float64(i)*rowH
		p.setFill(p.pal.lightBlue)
		p.pdf.Rect(mbX, ry, keyW, rowH, "F")
		if i > 0 {
			p.hline(mbX, ry, mbX+mbW)
		}
		p.text(mbX+2.5, ry+2.1, strings.ToUpper(r[0]), "B", 7.5, p.pal.blue)
		// Auto-shrink so a long value (document number, warehouse name) never overflows the cell.
		p.textFit(mbX+keyW+2.5, ry+2.1, mbW-keyW-4.5, r[1], "B", 8.0, 5.5, p.pal.navy)
	}
	p.box(mbX, y, mbW, mbH)

	return maxF(y+mbH, ly)
}

// partyCard is one side of the two-up party block (SUPPLIER / DELIVER TO, FROM / TO, …).
type partyCard struct {
	Title string
	Name  string
	Lines []string
}

// drawPartyCards renders two content-fitting, height-matched cards side by side. This is the
// generic form of drawParties (purchase order) and drawTransferParties (stock transfer) — both
// of which predate it and keep their own typed wrappers.
func (p *painter) drawPartyCards(py float64, left, right partyCard) float64 {
	bw := (contentW - 6.0) / 2
	bx2 := leftX + bw + 6.0
	innerW := bw - 6.0
	const nameH, lineH = 4.4, 4.0

	measure := func(c partyCard) float64 {
		h := float64(p.measureLines(orDash(c.Name), "B", 10.5, innerW)) * nameH
		for _, ln := range c.Lines {
			if strings.TrimSpace(ln) == "" {
				continue
			}
			h += float64(p.measureLines(ln, "", 9, innerW)) * lineH
		}
		return h
	}
	boxH := maxF(maxF(measure(left), measure(right))+9.0, 22.0)

	draw := func(x float64, c partyCard) {
		p.box(x, py, bw, boxH)
		p.text(x+3, py+3.0, strings.ToUpper(c.Title), "B", 8, p.pal.blue)
		cy := p.multiCell(x+3, py+5.6, innerW, nameH, orDash(c.Name), "B", 10.5, p.pal.navy) + 0.6
		for _, ln := range c.Lines {
			if strings.TrimSpace(ln) == "" {
				continue
			}
			cy = p.multiCell(x+3, cy, innerW, lineH, ln, "", 9, p.pal.grey)
		}
	}
	draw(leftX, left)
	draw(bx2, right)

	return py + boxH
}

// drawLeadLine renders a bold acknowledgement/purpose line above the item table (e.g. "Received
// the following goods…"). Empty text draws nothing.
func (p *painter) drawLeadLine(text string, y float64) float64 {
	if strings.TrimSpace(text) == "" {
		return y
	}
	return p.multiCell(leftX, y, contentW, 4.2, text, "B", 9.5, p.pal.navy) + 2.0
}

// docColumn describes one column of a generic item table. Exactly one column should carry
// Width == 0 — it flexes to consume whatever width the fixed columns leave, and is the column
// that renders the wrapped bold description (plus the row's optional muted SubDesc line).
type docColumn struct {
	Title string
	Width float64
	Right bool // right-align (numeric/amount columns)
}

// docRow is a single table row. Cells align positionally with the columns passed alongside;
// short Cells slices simply leave the trailing columns blank. SubDesc renders as a muted second
// line under the flex column (SKU, lot number, reason, …).
type docRow struct {
	Cells   []string
	SubDesc string
	// Emphasis renders every fixed-column cell in bold navy (used for totals-ish/summary rows).
	Emphasis bool
}

// drawDocTable renders a paginated, column-driven item table. numbered prepends an automatic "#"
// column. Returns the y below the final row hairline.
//
// Pagination contract (identical to table.go/drawTransferItems): each row's height is measured
// BEFORE any cell is drawn, and a row that would cross pageBottomSafe starts a fresh page with
// the header bar repeated — never a row split across two pages.
func (p *painter) drawDocTable(ty float64, numbered bool, cols []docColumn, rows []docRow) float64 {
	if numbered {
		cols = append([]docColumn{{Title: "#", Width: 9.0}}, cols...)
	}
	// Every caller builds docRow.Cells positionally against ITS OWN cols slice — Cells[0] is
	// always the first REAL (non-"#") column, never against the "#"-prepended cols above. So once
	// "#" is injected, a numbered table's column index i is one AHEAD of the matching Cells index;
	// cellOffset (used by cell() below) corrects for that. Getting this wrong silently shifted
	// every column over by one on every numbered document (goods receipt, requisition, RFQ,
	// purchase return, stock adjustment, stock count, bundle spec): DESCRIPTION showed the first
	// numeric column's value instead of the item name, every other numeric column showed its
	// NEXT neighbor's value, and the last column read past the end of Cells and rendered blank.

	// Resolve the flex column and per-column widths/x-offsets.
	flexIdx, fixed := -1, 0.0
	for i, c := range cols {
		if c.Width <= 0 {
			flexIdx = i
		} else {
			fixed += c.Width
		}
	}
	widths := make([]float64, len(cols))
	for i, c := range cols {
		widths[i] = c.Width
	}
	if flexIdx >= 0 {
		widths[flexIdx] = maxF(contentW-fixed, 20.0)
	}
	xs := make([]float64, len(cols)+1)
	xs[0] = leftX
	for i := range cols {
		xs[i+1] = xs[i] + widths[i]
	}

	drawHeaderBar := func(y float64) float64 {
		p.setFill(p.pal.navy)
		p.pdf.Rect(leftX, y, contentW, 8.0, "F")
		for i, c := range cols {
			if c.Right {
				p.textR(xs[i+1]-1.5, y+2.7, c.Title, "B", 8, p.pal.white)
			} else {
				p.text(xs[i]+1.5, y+2.7, c.Title, "B", 8, p.pal.white)
			}
		}
		return y + 8.0
	}

	cellOffset := 0
	if numbered {
		cellOffset = 1
	}
	cell := func(row docRow, i int) string {
		j := i - cellOffset
		if j >= 0 && j < len(row.Cells) {
			return row.Cells[j]
		}
		return ""
	}

	ry := drawHeaderBar(ty)
	for n, row := range rows {
		desc, flexW := "", 0.0
		if flexIdx >= 0 {
			desc, flexW = cell(row, flexIdx), widths[flexIdx]-2
		}
		rowH := 4.0
		if flexIdx >= 0 {
			rowH = p.rowHeight(desc, row.SubDesc, flexW)
		}
		if ry+2.5+rowH > pageBottomSafe {
			p.pdf.AddPage()
			ry = drawHeaderBar(contTopY)
		}

		rowTop := ry + 2.5
		endY := rowTop + rowH
		if flexIdx >= 0 {
			endY = p.multiCell(xs[flexIdx]+1, rowTop, flexW, 4.0, desc, "B", 9.3, p.pal.navy)
			if row.SubDesc != "" {
				endY = p.multiCell(xs[flexIdx]+1, endY+0.4, flexW, 4.0, row.SubDesc, "", 8.6, p.pal.muted)
			}
		}
		font, color := "", p.pal.ink
		if row.Emphasis {
			font, color = "B", p.pal.navy
		}
		for i, c := range cols {
			if i == flexIdx {
				continue
			}
			v := cell(row, i)
			if numbered && i == 0 {
				v = fmt.Sprintf("%d", n+1)
			}
			if c.Right {
				p.textR(xs[i+1]-1.5, rowTop, v, font, 9.3, color)
			} else {
				p.text(xs[i]+1.5, rowTop, v, font, 9.3, color)
			}
		}
		ry = endY + 2.0
		p.hline(leftX, ry, rightX)
	}
	return ry
}

// drawSectionHeading renders a small brand-colored heading above a secondary block (e.g. an
// RFQ's supplier-quotation appendix), with a hairline rule beneath. Empty text draws nothing.
func (p *painter) drawSectionHeading(y float64, text string) float64 {
	if strings.TrimSpace(text) == "" {
		return y
	}
	p.text(leftX, y, strings.ToUpper(text), "B", 9, p.pal.blue)
	p.hline(leftX, y+5.2, rightX)
	return y + 6.0
}

// drawNotesBlock renders a content-fitting Notes card under a caller-supplied heading. Draws
// nothing (and returns lY unchanged) when there are no non-empty notes.
func (p *painter) drawNotesBlock(lY float64, title string, notes []string) float64 {
	notes = nonEmpty(notes)
	if len(notes) == 0 {
		return lY
	}
	innerW := contentW - 6.0
	const lineH = 3.4
	lines := 0
	for _, n := range notes {
		lines += p.measureLines("-  "+n, "", 7.3, innerW)
	}
	lowH := maxF(float64(lines)*lineH+8.0, 14.0)
	p.box(leftX, lY, contentW, lowH)
	p.text(leftX+3, lY+2.6, strings.ToUpper(ifEmpty(title, "NOTES")), "B", 6.8, p.pal.blue)
	y := lY + 6.0
	for _, n := range notes {
		y = p.multiCell(leftX+3, y, innerW, lineH, "-  "+n, "", 7.3, p.pal.grey)
	}
	return lY + lowH
}

// drawSigPair renders the two sign-off lines with caller-supplied labels. A blank label omits
// that side entirely (so a document with a single signatory doesn't show an empty slot).
func (p *painter) drawSigPair(sY float64, leftLabel, leftName, rightLabel, rightName string) float64 {
	sigW := 80.0
	if strings.TrimSpace(leftLabel) != "" {
		p.drawSig(leftX, sY, sigW, leftLabel, leftName)
	}
	if strings.TrimSpace(rightLabel) != "" {
		p.drawSig(rightX-sigW, sY, sigW, rightLabel, rightName)
	}
	return sY
}
