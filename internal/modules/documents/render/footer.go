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

	// Footnote-sized so the body keeps maximum room for the item listing. Both lines use a centered
	// MultiCell so a long contact/meta line wraps within the content width instead of overflowing
	// (and clipping) the page edges. Auto page break is already disabled before the footer, so a
	// wrapped second line stays on page one.
	name := ifEmpty(d.Branding.CompanyName, "the issuer")
	p.pdf.SetFont("Helvetica", "", 6.6)
	p.setText(p.pal.muted)
	p.pdf.SetXY(leftX, fY+1.6)
	p.pdf.MultiCell(contentW, 3.2,
		p.tr("This purchase order is issued by "+name+" and is subject to the terms stated above."),
		"", "C", false)
	if meta := footerMeta(d.Branding); meta != "" {
		p.pdf.SetX(leftX)
		p.pdf.MultiCell(contentW, 3.2, p.tr(meta), "", "C", false)
	}
	// Platform-owner (Codevertex) advertisement — shown on every inventory document.
	p.pdf.SetX(leftX)
	p.pdf.SetFont("Helvetica", "B", 6.4)
	p.pdf.MultiCell(contentW, 3.0, p.tr(providerFooterLead), "", "C", false)
	p.pdf.SetX(leftX)
	p.pdf.SetFont("Helvetica", "", 6.0)
	p.pdf.MultiCell(contentW, 3.0, p.tr(providerFooterContact), "", "C", false)
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
	if b.Website != "" {
		parts = append(parts, b.Website)
	}
	if b.Tagline != "" {
		parts = append(parts, b.Tagline)
	}
	return strings.Join(parts, "  |  ")
}
