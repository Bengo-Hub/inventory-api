package render

import "strings"

// drawDocFooter is the SHARED footer primitive for every document type in this package (purchase
// order, transfer/GRN, goods receipt, requisition, RFQ, purchase return, stock adjustment, stock
// count, bundle spec). It draws the separator rule, the caller-supplied closing disclaimer, the
// tenant contact/meta line, and the platform-owner advertisement — so a footer change lands in
// ONE place rather than being copy-pasted per doc struct.
//
// The provider block prefers the LIVE strings resolved onto Branding by
// documents.Service.ResolveProviderFooterText and falls back to this package's compiled-in
// constants when they're empty, so a Branding built without that resolve still renders fully.
func (p *painter) drawDocFooter(b Branding, fY float64, disclaimer string) {
	// Pin the footer near the page bottom when content is short.
	if fY < 282.0 {
		fY = 282.0
	}
	p.setDraw(p.pal.line)
	p.pdf.SetLineWidth(0.2)
	p.pdf.Line(leftX, fY, rightX, fY)

	// Footnote-sized so the body keeps maximum room for the item listing. Every line uses a centered
	// MultiCell so a long contact/meta line wraps within the content width instead of overflowing
	// (and clipping) the page edges. Auto page break is already disabled before the footer, so a
	// wrapped second line stays on the last page.
	p.pdf.SetFont("Helvetica", "", 6.6)
	p.setText(p.pal.muted)
	p.pdf.SetXY(leftX, fY+1.6)
	p.pdf.MultiCell(contentW, 3.2, p.tr(disclaimer), "", "C", false)
	if meta := footerMeta(b); meta != "" {
		p.pdf.SetX(leftX)
		p.pdf.MultiCell(contentW, 3.2, p.tr(meta), "", "C", false)
	}
	// Platform-owner (Codevertex) advertisement — platform default ON, per-tenant opt-out.
	if b.ProviderFooterEnabled {
		lead, contact := providerFooterLines(b)
		p.pdf.SetX(leftX)
		p.pdf.SetFont("Helvetica", "B", 6.4)
		p.pdf.MultiCell(contentW, 3.0, p.tr(lead), "", "C", false)
		p.pdf.SetX(leftX)
		p.pdf.SetFont("Helvetica", "", 6.0)
		p.pdf.MultiCell(contentW, 3.0, p.tr(contact), "", "C", false)
	}
}

// drawFooter renders the purchase order's closing disclaimer line plus the shared footer block.
func (p *painter) drawFooter(d *PurchaseOrderDoc, fY float64) {
	name := ifEmpty(d.Branding.CompanyName, "the issuer")
	p.drawDocFooter(d.Branding, fY,
		"This purchase order is issued by "+name+" and is subject to the terms stated above.")
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
