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
	margin   = 13.0
	leftX    = margin
	rightX   = pageW - margin
	contentW = pageW - 2*margin
)

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
