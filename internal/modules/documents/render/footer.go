package render

import "strings"

// drawFooter renders the closing disclaimer line plus a company contact line.
func (p *painter) drawFooter(d *PurchaseOrderDoc, fY float64) {
	// Pin the footer near the page bottom when content is short.
	if fY < 282.0 {
		fY = 282.0
	}
	p.setDraw(p.pal.line)
	p.pdf.SetLineWidth(0.2)
	p.pdf.Line(leftX, fY, rightX, fY)

	name := ifEmpty(d.Branding.CompanyName, "the issuer")
	p.pdf.SetFont("Helvetica", "", 8.3)
	p.setText(p.pal.muted)
	p.pdf.SetXY(leftX, fY+2.0)
	p.pdf.CellFormat(contentW, 4,
		p.tr("This purchase order is issued by "+name+" and is subject to the terms stated above."),
		"", 1, "C", false, 0, "")
	if meta := footerMeta(d.Branding); meta != "" {
		p.pdf.SetX(leftX)
		p.pdf.CellFormat(contentW, 4, p.tr(meta), "", 1, "C", false, 0, "")
	}
}

func footerMeta(b Branding) string {
	parts := []string{}
	if addr := strings.Join(companyAddressLines(b), ", "); addr != "" {
		parts = append(parts, addr)
	}
	if b.KRAPIN != "" {
		parts = append(parts, "KRA PIN: "+b.KRAPIN)
	}
	if b.Email != "" {
		parts = append(parts, b.Email)
	}
	if b.Phone != "" {
		parts = append(parts, b.Phone)
	}
	if b.Website != "" {
		parts = append(parts, b.Website)
	}
	return strings.Join(parts, "  |  ")
}
