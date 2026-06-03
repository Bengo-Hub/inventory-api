package render

import (
	"bytes"
	"strings"

	"github.com/go-pdf/fpdf"
)

// drawHeader renders the logo (left), document title + status caption (right) and
// the gradient rule beneath. Returns the y just below the rule.
func (p *painter) drawHeader(d *PurchaseOrderDoc, logo []byte, logoType string) float64 {
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

	p.textR(rightX, topY+0.5, "PURCHASE ORDER", "B", 24, p.pal.navy)
	p.textR(rightX, topY+11.0, "ORDER FOR PROCUREMENT", "", 7.5, p.pal.blue)

	ruleY := topY + logoH + 2.0
	p.gradient(leftX, ruleY, contentW, 1.4, 0.7, p.pal.sky, p.pal.navy)
	return ruleY
}

// drawMetaAndCompany renders the company identity (left) and the meta key/value
// box (right). Returns the y below whichever column is taller.
func (p *painter) drawMetaAndCompany(d *PurchaseOrderDoc, ruleY float64) float64 {
	y := ruleY + 6.0
	b := d.Branding

	// Left: company identity.
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

	// Right: meta box.
	rows := metaRows(d)
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
		p.text(mbX+keyW+2.5, ry+2.1, r[1], "B", 8.0, p.pal.navy)
	}
	p.box(mbX, y, mbW, mbH)

	return maxF(y+mbH, ly)
}

// companyAddressLines returns the branding address lines (capped at three).
func companyAddressLines(b Branding) []string {
	out := make([]string, 0, len(b.Address))
	for _, ln := range b.Address {
		if t := strings.TrimSpace(ln); t != "" {
			out = append(out, t)
		}
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}
