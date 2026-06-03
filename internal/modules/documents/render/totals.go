package render

import "strings"

// drawTotals renders the subtotal/tax stack and the gradient grand total,
// followed by the amount-in-words band (when present). Returns the y below the
// last rendered element.
func (p *painter) drawTotals(d *PurchaseOrderDoc, top float64) float64 {
	tlW := 92.0
	tlX := rightX - tlW

	// Subtotal box.
	y := top
	p.setFill(p.pal.lightBlue)
	p.pdf.RoundedRect(tlX, y, tlW, 7.5, 1.2, "1234", "F")
	p.text(tlX+4, y+2.6, "Subtotal", "", 10, p.pal.grey)
	p.textR(rightX-4, y+2.6, money(d.Currency, d.Subtotal), "B", 10, p.pal.navy)
	y += 8.3

	if strings.TrimSpace(d.TaxAmount) != "" {
		p.text(tlX+4, y+1.0, ifEmpty(d.TaxLabel, "Tax"), "", 10, p.pal.grey)
		p.textR(rightX-4, y+1.0, money(d.Currency, d.TaxAmount), "B", 10, p.pal.navy)
		y += 6.5
	}

	// Gradient grand total.
	gtY := y + 1.0
	p.gradient(tlX, gtY, tlW, 9.0, 1.4, p.pal.blue, p.pal.navy)
	p.text(tlX+4, gtY+3.2, "Grand Total", "B", 12, p.pal.white)
	p.textR(rightX-4, gtY+3.2, money(d.Currency, d.Grand), "B", 12.5, p.pal.white)
	y = gtY + 9.0

	// Amount-in-words band (full width), only when supplied.
	if strings.TrimSpace(d.AmountInWords) != "" {
		wY := y + 5.0
		p.setFill(p.pal.lightBlue)
		p.pdf.RoundedRect(leftX, wY, contentW, 8.0, 1.2, "1234", "F")
		p.text(leftX+4, wY+2.6, "Amount in Words:", "B", 8, p.pal.muted)
		p.multiCell(leftX+34, wY+1.6, contentW-34-4, 3.6, d.AmountInWords, "B", 8, p.pal.navy)
		return wY + 8.0
	}
	return y
}
