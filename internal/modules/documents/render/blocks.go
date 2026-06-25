package render

// drawLowerBlocks renders the Notes & Terms box (only when there is content).
// Returns the y below the box (or unchanged when nothing is rendered).
func (p *painter) drawLowerBlocks(d *PurchaseOrderDoc, lY float64) float64 {
	notes := nonEmpty(d.Notes)
	if len(notes) == 0 {
		return lY
	}
	// Compact, content-fitting block (smaller fonts) so the item listing keeps the bulk of the
	// page while the card height flexes to however many note lines wrap.
	innerW := contentW - 6.0
	const lineH = 3.4
	lines := 0
	for _, n := range notes {
		lines += p.measureLines("•  "+n, "", 7.3, innerW)
	}
	lowH := maxF(float64(lines)*lineH+8.0, 14.0)
	p.box(leftX, lY, contentW, lowH)
	p.text(leftX+3, lY+2.6, "NOTES & TERMS", "B", 6.8, p.pal.blue)
	y := lY + 6.0
	for _, n := range notes {
		y = p.multiCell(leftX+3, y, innerW, lineH, "•  "+n, "", 7.3, p.pal.grey)
	}
	return lY + lowH
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
