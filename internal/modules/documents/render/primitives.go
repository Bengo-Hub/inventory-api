package render

import (
	"strings"

	"github.com/go-pdf/fpdf"
)

// smartPunct maps common Unicode punctuation (curly quotes, em/en dashes, bullets) to ASCII so
// the latin1 core-font translator renders them properly instead of dropping them to ".".
var smartPunct = strings.NewReplacer(
	"—", "-", // em dash —
	"–", "-", // en dash –
	"‘", "'", // left single quote ‘
	"’", "'", // right single quote ’
	"“", "\"", // left double quote “
	"”", "\"", // right double quote ”
	"•", "-", // bullet •
	"·", "-", // middle dot ·
	"…", "...", // ellipsis …
	" ", " ", // non-breaking space
)

// Page geometry (A4 portrait, mm).
const (
	pageW    = 210.0
	pageH    = 297.0
	margin   = 13.0
	leftX    = margin
	rightX   = pageW - margin
	contentW = pageW - 2*margin

	// pageBottomSafe is the lowest Y a new table row or fixed-height block may START at
	// before this package forces an explicit page break. Every drawing primitive below
	// (text/textR/textFit/box/hline/gradient) is a raw, absolute-position fpdf call and is
	// NOT page-break aware — only multiCell (which routes through fpdf's own MultiCell/
	// CellFormat) triggers fpdf's SetAutoPageBreak. Mixing the two in one row without an
	// explicit check made a long item table (e.g. a 16-line goods-received note) silently
	// break mid-row: the description's multiCell would jump to a new page while that same
	// row's numeric columns stayed on the old page at a stale Y, producing a corrupted,
	// visually "cut off" printout. Every multi-row/multi-block layout in this package must
	// call rowHeight/ensurePage explicitly instead of relying on SetAutoPageBreak.
	pageBottomSafe = 278.0
	// contTopY is where content resumes at the top of a continuation page.
	contTopY = 18.0
)

// newDoc creates the A4 page canvas every document in this package starts from (mm units,
// shared margins, auto page break on) so page geometry is declared in exactly one place.
func newDoc() *fpdf.Fpdf {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(margin, 12, margin)
	pdf.SetAutoPageBreak(true, 10)
	return pdf
}

// ensurePage starts a new page (returning contTopY) when y doesn't leave `need` mm of room
// before pageBottomSafe; otherwise it returns y unchanged. Use before drawing a block that
// can't itself split across a page break (totals, notes, signature/footer stacks) so a long
// item table spilling onto page 2 doesn't push those trailing sections past the physical page.
func (p *painter) ensurePage(y, need float64) float64 {
	if y+need > pageBottomSafe {
		p.pdf.AddPage()
		return contTopY
	}
	return y
}

// rowHeight measures the vertical space an item-table row (a bold description plus an optional
// muted sub-line, e.g. SKU) will occupy at the given width — called BEFORE drawing so a row that
// wouldn't fit on the current page can be moved to a fresh one atomically, rather than splitting
// its cells across two pages.
func (p *painter) rowHeight(desc, subDesc string, w float64) float64 {
	h := float64(p.measureLines(desc, "B", 9.3, w)) * 4.0
	if subDesc != "" {
		h += float64(p.measureLines(subDesc, "", 8.6, w))*4.0 + 0.4
	}
	return h
}

// painter wraps an fpdf document with the palette and the small set of drawing
// primitives used throughout the template.
type painter struct {
	pdf *fpdf.Fpdf
	tr  func(string) string
	pal palette
}

func newPainter(pdf *fpdf.Fpdf, pal palette) *painter {
	base := pdf.UnicodeTranslatorFromDescriptor("")
	// Sanitize smart punctuation before the latin1 translator so dashes/quotes/bullets render.
	tr := func(s string) string { return base(smartPunct.Replace(s)) }
	return &painter{pdf: pdf, tr: tr, pal: pal}
}

func (p *painter) setFill(c rgb) { p.pdf.SetFillColor(c.r, c.g, c.b) }
func (p *painter) setText(c rgb) { p.pdf.SetTextColor(c.r, c.g, c.b) }
func (p *painter) setDraw(c rgb) { p.pdf.SetDrawColor(c.r, c.g, c.b) }

// gradient fills a rounded rect with a horizontal linear gradient from c1 to c2.
func (p *painter) gradient(x, y, w, h, r float64, c1, c2 rgb) {
	p.pdf.ClipRoundedRect(x, y, w, h, r, false)
	p.pdf.LinearGradient(x, y, w, h, c1.r, c1.g, c1.b, c2.r, c2.g, c2.b, 0, 0, 1, 0)
	p.pdf.ClipEnd()
}

// box draws a white rounded rect with a hairline border.
func (p *painter) box(x, y, w, h float64) {
	p.setDraw(p.pal.line)
	p.setFill(p.pal.white)
	p.pdf.SetLineWidth(0.2)
	p.pdf.RoundedRect(x, y, w, h, 1.6, "1234", "D")
}

// text draws a string with its top at y (baseline computed for predictable placement).
func (p *painter) text(x, y float64, s, font string, sz float64, c rgb) {
	p.pdf.SetFont("Helvetica", font, sz)
	p.setText(c)
	p.pdf.Text(x, y+sz*0.3528*0.82, p.tr(s))
}

// textFit draws a string that auto-shrinks its font size (down to minSz) so it never overflows
// maxW. Used for variable-length values like the document number that must stay inside a cell.
func (p *painter) textFit(x, y, maxW float64, s, font string, sz, minSz float64, c rgb) {
	p.setText(c)
	size := sz
	for size > minSz {
		p.pdf.SetFont("Helvetica", font, size)
		if p.pdf.GetStringWidth(p.tr(s)) <= maxW {
			break
		}
		size -= 0.25
	}
	p.pdf.SetFont("Helvetica", font, size)
	p.pdf.Text(x, y+size*0.3528*0.82, p.tr(s))
}

// textR draws a right-aligned string ending at rx.
func (p *painter) textR(rx, y float64, s, font string, sz float64, c rgb) {
	p.pdf.SetFont("Helvetica", font, sz)
	p.setText(c)
	t := p.tr(s)
	p.pdf.Text(rx-p.pdf.GetStringWidth(t), y+sz*0.3528*0.82, t)
}

// hline draws a horizontal hairline in the line color.
func (p *painter) hline(x1, y, x2 float64) {
	p.setDraw(p.pal.line)
	p.pdf.SetLineWidth(0.15)
	p.pdf.Line(x1, y, x2, y)
}

// kv prints a label + bold value pair (label muted, value navy), value offset by 24mm.
func (p *painter) kv(x, y float64, k, v string) {
	p.text(x, y, k, "", 9, p.pal.muted)
	p.text(x+24, y, v, "B", 9, p.pal.navy)
}

// measureLines returns how many wrapped lines `s` occupies at width w for the given font/size,
// so callers can size a card to its content before drawing the (filled) box behind it.
func (p *painter) measureLines(s, font string, sz, w float64) int {
	p.pdf.SetFont("Helvetica", font, sz)
	n := len(p.pdf.SplitText(p.tr(s), w))
	if n < 1 {
		return 1
	}
	return n
}

// multiCell wraps text at the given x/width using the given font/size/color and
// returns the y just below the wrapped block.
func (p *painter) multiCell(x, y, w, lineH float64, s, font string, sz float64, c rgb) float64 {
	p.pdf.SetFont("Helvetica", font, sz)
	p.setText(c)
	p.pdf.SetXY(x, y)
	p.pdf.MultiCell(w, lineH, p.tr(s), "", "L", false)
	return p.pdf.GetY()
}
