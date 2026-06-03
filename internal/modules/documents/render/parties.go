package render

// drawParties renders the supplier (left) and deliver-to (right) boxes.
// Returns the y below the boxes.
func (p *painter) drawParties(d *PurchaseOrderDoc, py float64) float64 {
	boxH := 27.5
	bw := (contentW - 6.0) / 2
	bx2 := leftX + bw + 6.0

	// Left: supplier.
	p.box(leftX, py, bw, boxH)
	p.text(leftX+3, py+3.0, "SUPPLIER", "B", 8, p.pal.blue)
	endY := p.multiCell(leftX+3, py+6.5, bw-6, 4.0, orDash(d.SupplierName), "B", 10.5, p.pal.navy)
	cy := endY + 1.0
	for _, ln := range d.SupplierAddr {
		if ln == "" {
			continue
		}
		p.text(leftX+3, cy, ln, "", 9, p.pal.grey)
		cy += 4.0
	}

	// Right: deliver-to warehouse.
	p.box(bx2, py, bw, boxH)
	p.text(bx2+3, py+3.0, "DELIVER TO", "B", 8, p.pal.blue)
	p.multiCell(bx2+3, py+6.5, bw-6, 4.0, orDash(d.WarehouseName), "B", 10.5, p.pal.navy)
	if d.ExpectedDate != "" {
		p.text(bx2+3, py+16.0, "Expected: "+d.ExpectedDate, "", 9, p.pal.grey)
	}

	return py + boxH
}
