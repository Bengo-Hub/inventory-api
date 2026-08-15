package render

import "fmt"

// drawItems renders the line-item table (# / Description / Unit / Qty / Rate / Amount). Each
// row's height is measured up front (rowHeight) so a row that wouldn't fit on the current page
// starts a new one — with the column header bar repeated — BEFORE any of its cells are drawn,
// never mid-row (see primitives.go's pageBottomSafe doc comment for why that matters). Returns
// the y below the final row hairline.
func (p *painter) drawItems(d *PurchaseOrderDoc, ty float64) float64 {
	colNum := 9.0
	colAmt := 30.0
	colRate := 28.0
	colQty := 14.0
	colUnit := 16.0
	colDesc := contentW - colNum - colAmt - colRate - colQty - colUnit

	xNum := leftX
	xDesc := xNum + colNum
	xUnitR := xDesc + colDesc + colUnit
	xQtyR := xUnitR + colQty
	xRateR := xQtyR + colRate
	xAmtR := rightX

	drawHeaderBar := func(y float64) float64 {
		p.setFill(p.pal.navy)
		p.pdf.Rect(leftX, y, contentW, 8.0, "F")
		p.text(xNum+2, y+2.7, "#", "B", 8, p.pal.white)
		p.text(xDesc+1, y+2.7, "DESCRIPTION", "B", 8, p.pal.white)
		p.textR(xUnitR-1, y+2.7, "UNIT", "B", 8, p.pal.white)
		p.textR(xQtyR-1, y+2.7, "QTY", "B", 8, p.pal.white)
		p.textR(xRateR-1, y+2.7, "RATE", "B", 8, p.pal.white)
		p.textR(xAmtR-2, y+2.7, "AMOUNT ("+currencyCode(d.Currency)+")", "B", 8, p.pal.white)
		return y + 8.0
	}

	ry := drawHeaderBar(ty)
	for i, it := range d.Items {
		if rowH := p.rowHeight(it.Desc, it.SubDesc, colDesc-2); ry+2.5+rowH > pageBottomSafe {
			p.pdf.AddPage()
			ry = drawHeaderBar(contTopY)
		}

		rowTop := ry + 2.5
		// Wrapped main description.
		endY := p.multiCell(xDesc+1, rowTop, colDesc-2, 4.0, it.Desc, "B", 9.3, p.pal.navy)
		if it.SubDesc != "" {
			endY = p.multiCell(xDesc+1, endY+0.4, colDesc-2, 4.0, it.SubDesc, "", 8.6, p.pal.muted)
		}
		// Numeric columns aligned to the first line.
		p.text(xNum+2, rowTop, fmt.Sprintf("%d", i+1), "", 9.3, p.pal.ink)
		p.textR(xUnitR-1, rowTop, it.Unit, "", 9.3, p.pal.ink)
		p.textR(xQtyR-1, rowTop, it.Qty, "", 9.3, p.pal.ink)
		p.textR(xRateR-1, rowTop, it.Rate, "", 9.3, p.pal.ink)
		p.textR(xAmtR-2, rowTop, it.Amount, "B", 9.3, p.pal.ink)
		ry = endY + 2.0
		p.hline(leftX, ry, rightX)
	}
	return ry
}
