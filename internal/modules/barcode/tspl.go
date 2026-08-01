package barcode

import (
	"fmt"
	"strings"
)

// renderTSPL emits TSC/TSPL2 commands — the command language spoken by TSC-compatible desktop
// thermal label printers (including the Xprinter XP-330B this module targets; confirmed via
// Xprinter's own TSPL-emulation spec, not Zebra ZPL, which this module previously assumed for
// every thermal printer — see docs/barcode-labels.md).
//
// SIZE/GAP describe the physical media AS MOUNTED (mm, never swapped to "fix" rotation — same
// convention as renderZPL's ^PW/^LL, see template.go's Rotate doc). When tmpl.Rotate is set, each
// TEXT/BARCODE command's own rotation parameter is 90 instead of 0, so content reads correctly
// along the feed direction on rolls mounted that way.
//
// Multi-lane: identical tiling to renderZPL — each feed-row draws up to `lanes` consecutive
// labels side-by-side at x-offsets from tmpl.LaneXOffsetDots, then CLS/PRINT advances to the
// next row.
//
// GS1-128 is intentionally NOT supported here (see ParseLabelFormat/Batch.Render's guard in
// label.go / the handler's UNSUPPORTED_TSPL_GS1 check) — the exact FNC1 escape convention inside
// a TSPL BARCODE command's content string is a TSC-firmware detail this module has not been able
// to confirm against Xprinter's specific TSPL clone from public sources, and guessing risks
// silently printing a barcode that scans as plain Code128 instead of GS1-128.
func renderTSPL(labels []Label, tmpl LabelTemplate) string {
	lanes := tmpl.laneCount()
	rollWmm := tmpl.RollWidthIn() * 25.4
	hMm := tmpl.LabelHIn * 25.4
	gapMm := tmpl.GapYIn * 25.4

	var sb strings.Builder
	for i := 0; i < len(labels); i += lanes {
		fmt.Fprintf(&sb, "SIZE %.2f mm,%.2f mm\n", rollWmm, hMm)
		fmt.Fprintf(&sb, "GAP %.2f mm,0 mm\n", gapMm)
		sb.WriteString("DIRECTION 0\n")
		sb.WriteString("REFERENCE 0,0\n")
		sb.WriteString("CLS\n")

		for lane := 0; lane < lanes && i+lane < len(labels); lane++ {
			writeTSPLLabel(&sb, labels[i+lane], tmpl, tmpl.LaneXOffsetDots(lane))
		}

		sb.WriteString("PRINT 1,1\n")
	}
	return sb.String()
}

// tsplRotation returns the per-command rotation parameter for the template's mount orientation.
func tsplRotation(tmpl LabelTemplate) int {
	if tmpl.Rotate {
		return 90
	}
	return 0
}

// writeTSPLLabel draws one label's fields at the given x-offset (dots) within the current
// SIZE/GAP/CLS block, mirroring writeZPLLabel's row layout (title/SKU/detail/barcode/price) so
// TSPL output stays visually consistent with the ZPL and PDF renderers.
func writeTSPLLabel(sb *strings.Builder, l Label, tmpl LabelTemplate, xOffset int) {
	w, h := tmpl.WidthDots(), tmpl.HeightDots()
	rot := tsplRotation(tmpl)
	marginX := xOffset + w/20
	// TSPL built-in font "3" is a fixed ~24-dot bitmap font; x/y-mult scales it. Clamp 1-10 per
	// the TSPL2 manual's valid multiplier range.
	titleMult := clampMult(h / 10 / 24)
	subMult := clampMult(h / 16 / 24)
	lineGap := h / 40

	y := h / 20

	if l.Title != "" {
		fmt.Fprintf(sb, "TEXT %d,%d,\"3\",%d,%d,%d,\"%s\"\n", marginX, y, rot, titleMult, titleMult, tsplEscape(truncate(l.Title, 38)))
		y += (24 * titleMult) + lineGap
	}
	if sku := l.Sku; sku != "" || l.SubTitle != "" {
		if sku == "" {
			sku = l.SubTitle
		}
		fmt.Fprintf(sb, "TEXT %d,%d,\"3\",%d,%d,%d,\"%s\"\n", marginX, y, rot, subMult, subMult, tsplEscape(truncate(sku, 42)))
		y += (24 * subMult) + lineGap
	}
	if l.DetailLine != "" {
		fmt.Fprintf(sb, "TEXT %d,%d,\"3\",%d,%d,%d,\"%s\"\n", marginX, y, rot, subMult, subMult, tsplEscape(truncate(l.DetailLine, 42)))
		y += (24 * subMult) + lineGap
	}

	barY := y
	barH := maxInt((h-y)/2, 60)
	barcodeType := "128"
	content := l.Content
	switch l.Symbology {
	case SymEAN13, SymUPCA:
		barcodeType = "EAN13"
		if code, err := NormalizeEAN13(l.Content); err == nil {
			content = code[:12] // TSPL's EAN13 type takes 12 digits; printer computes the check digit
		}
	case SymGS1128:
		// Not supported (see doc comment above) — callers must not reach here (guarded upstream),
		// but fall back to plain Code128 of the human-readable text rather than emitting FNC1
		// bytes we can't verify are escaped correctly for this firmware.
		barcodeType = "128"
		content = stripFNC1(l.human())
	}
	fmt.Fprintf(sb, "BARCODE %d,%d,\"%s\",%d,1,%d,2,2,\"%s\"\n",
		marginX+w/40, barY, barcodeType, barH, rot, tsplEscape(content))
	y = barY + barH + lineGap*2

	if l.Price != "" {
		priceMult := clampMult(h / 10 / 24)
		fmt.Fprintf(sb, "TEXT %d,%d,\"3\",%d,%d,%d,\"%s\"\n", marginX, y, rot, priceMult, priceMult, tsplEscape(l.Price))
	}
}

// clampMult keeps a TEXT command's x/y multiplier within TSPL2's valid 1-10 range.
func clampMult(n int) int {
	if n < 1 {
		return 1
	}
	if n > 10 {
		return 10
	}
	return n
}

// tsplEscape removes characters that would break a quoted TSPL string field.
func tsplEscape(s string) string {
	r := strings.NewReplacer("\"", "'", "\n", " ", "\r", " ")
	return r.Replace(s)
}
