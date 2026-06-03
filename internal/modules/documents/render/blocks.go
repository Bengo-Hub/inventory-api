package render

// drawLowerBlocks renders the Notes & Terms box (only when there is content).
// Returns the y below the box (or unchanged when nothing is rendered).
func (p *painter) drawLowerBlocks(d *PurchaseOrderDoc, lY float64) float64 {
	notes := nonEmpty(d.Notes)
	if len(notes) == 0 {
		return lY
	}
	lowH := 28.0
	p.box(leftX, lY, contentW, lowH)
	p.text(leftX+3, lY+3.0, "NOTES & TERMS", "B", 8, p.pal.blue)
	y := lY + 7.5
	for _, n := range notes {
		y = p.multiCell(leftX+3, y, contentW-6, 3.8, "•  "+n, "", 8.6, p.pal.grey) + 1.0
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
