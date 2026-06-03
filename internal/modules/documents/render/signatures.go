package render

// drawSignatures renders the Prepared By / Approved By signature lines.
func (p *painter) drawSignatures(d *PurchaseOrderDoc, sY float64) float64 {
	sigW := 80.0
	p.drawSig(leftX, sY, sigW, "Prepared By", d.PreparedBy)
	p.drawSig(rightX-sigW, sY, sigW, "Approved By", d.ApprovedBy)
	return sY
}

func (p *painter) drawSig(x, sY, sigW float64, role, name string) {
	p.setDraw(p.pal.navy)
	p.pdf.SetLineWidth(0.5)
	p.pdf.Line(x, sY, x+sigW, sY)
	label := role + " — "
	p.pdf.SetFont("Helvetica", "", 9)
	p.setText(p.pal.grey)
	p.pdf.SetXY(x, sY+1.5)
	p.pdf.CellFormat(0, 4, p.tr(label), "", 0, "L", false, 0, "")
	if name != "" {
		w := p.pdf.GetStringWidth(p.tr(label))
		p.pdf.SetFont("Helvetica", "B", 9.6)
		p.setText(p.pal.navy)
		p.pdf.SetXY(x+w, sY+1.5)
		p.pdf.CellFormat(0, 4, p.tr(name), "", 0, "L", false, 0, "")
	}
}
