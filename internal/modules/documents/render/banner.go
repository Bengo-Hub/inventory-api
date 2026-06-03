package render

import "strings"

// drawBanner renders the gradient amount banner with the status badge.
// Returns the y below the banner.
func (p *painter) drawBanner(d *PurchaseOrderDoc, by float64) float64 {
	banH := 18.0
	p.gradient(leftX, by, contentW, banH, 2.0, p.pal.sky, p.pal.navy)
	p.text(leftX+8, by+4.5, strings.ToUpper(amountLabel(d)), "", 9, p.pal.bannerSub)
	p.text(leftX+8, by+8.5, money(d.Currency, d.Grand), "B", 24, p.pal.white)

	badge := badgeText(d)
	if badge != "" {
		badgeW := 36.0
		bgx := rightX - 8 - badgeW
		bgy := by + 4.0
		p.pdf.SetDrawColor(255, 255, 255)
		p.pdf.SetLineWidth(0.8)
		p.pdf.RoundedRect(bgx, bgy, badgeW, 10.0, 1.4, "1234", "D")
		p.pdf.SetFont("Helvetica", "B", 12)
		p.setText(p.pal.white)
		p.pdf.SetXY(bgx, bgy+1.2)
		p.pdf.CellFormat(badgeW, 8.0, p.tr(badge), "", 0, "C", false, 0, "")
	}
	return by + banH
}
