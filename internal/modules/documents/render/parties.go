package render

import "strings"

// drawParties renders the supplier (left) and deliver-to (right) cards. The cards flex to fit
// their wrapped content (long supplier addresses wrap inside the card instead of overflowing) and
// share a common height so the two columns stay aligned, matching the invoice layout.
func (p *painter) drawParties(d *PurchaseOrderDoc, py float64) float64 {
	bw := (contentW - 6.0) / 2
	bx2 := leftX + bw + 6.0
	innerW := bw - 6.0
	const nameH, lineH = 4.4, 4.0

	// Measure both columns to compute a shared, content-fitting card height.
	leftH := float64(p.measureLines(orDash(d.SupplierName), "B", 10.5, innerW)) * nameH
	for _, ln := range d.SupplierAddr {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		leftH += float64(p.measureLines(ln, "", 9, innerW)) * lineH
	}
	rightH := float64(p.measureLines(orDash(d.WarehouseName), "B", 10.5, innerW)) * nameH
	if d.ExpectedDate != "" {
		rightH += lineH
	}
	boxH := maxF(maxF(leftH, rightH)+9.0, 22.0)

	// Left: supplier.
	p.box(leftX, py, bw, boxH)
	p.text(leftX+3, py+3.0, "SUPPLIER", "B", 8, p.pal.blue)
	cy := p.multiCell(leftX+3, py+5.6, innerW, nameH, orDash(d.SupplierName), "B", 10.5, p.pal.navy) + 0.6
	for _, ln := range d.SupplierAddr {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		cy = p.multiCell(leftX+3, cy, innerW, lineH, ln, "", 9, p.pal.grey)
	}

	// Right: deliver-to warehouse.
	p.box(bx2, py, bw, boxH)
	p.text(bx2+3, py+3.0, "DELIVER TO", "B", 8, p.pal.blue)
	ry := p.multiCell(bx2+3, py+5.6, innerW, nameH, orDash(d.WarehouseName), "B", 10.5, p.pal.navy) + 0.6
	if d.ExpectedDate != "" {
		p.multiCell(bx2+3, ry, innerW, lineH, "Expected: "+d.ExpectedDate, "", 9, p.pal.grey)
	}

	return py + boxH
}
