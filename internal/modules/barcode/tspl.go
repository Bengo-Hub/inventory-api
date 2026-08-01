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

// textFont is TSPL's smallest built-in bitmap font ("1", documented as 8×12 dots at multiplier
// 1) — switched from the larger "3" font (16×24) after a bench print showed title/SKU/detail
// text at "3" dominating a narrow label, leaving little room to balance against the barcode.
const textFont = "1"
const fontCharWDots = 8
const fontCharHDots = 12

// writeTSPLLabel draws one label's fields at the given x-offset (dots) within the current
// SIZE/GAP/CLS block, mirroring writeZPLLabel's row layout (title/SKU/detail/barcode/price) so
// TSPL output stays visually consistent with the ZPL and PDF renderers.
func writeTSPLLabel(sb *strings.Builder, l Label, tmpl LabelTemplate, xOffset int) {
	w, h := tmpl.WidthDots(), tmpl.HeightDots()
	rot := tsplRotation(tmpl)
	marginX := xOffset + w/20
	availW := w - 2*(w/20)
	if availW < fontCharWDots {
		availW = fontCharWDots
	}

	// Font multiplier is sized off the label's SMALLER dimension, not height alone — a tall-but-
	// narrow label has plenty of height to spare but almost no width, so a height-only formula
	// inflated text well past what the width could actually hold, dominating the label and
	// leaving little room to balance against the barcode. Clamp 1-10 per the TSPL2 manual's
	// valid multiplier range, THEN clamp down further until a reasonably short (~12-char) string
	// would fit the label's width.
	minDim := h
	if w < minDim {
		minDim = w
	}
	titleMult := clampMult(minDim / 10 / fontCharHDots)
	for titleMult > 1 && titleMult*fontCharWDots*12 > availW {
		titleMult--
	}
	subMult := clampMult(minDim / 16 / fontCharHDots)
	for subMult > 1 && subMult*fontCharWDots*12 > availW {
		subMult--
	}
	lineGap := h / 40

	// Start with more clearance than a bare margin (h/20) — on this printer/roll combination
	// low-y content lands closest to the physical edge the operator tears along, so a first line
	// placed flush against y=h/20 was getting clipped by the tear rather than sitting fully
	// inside the printable label.
	y := h / 10

	if l.Title != "" {
		maxChars := maxInt(availW/(fontCharWDots*titleMult), 4)
		text := truncate(l.Title, maxChars)
		x := marginX + centerOffsetDots(availW, len([]rune(text))*fontCharWDots*titleMult)
		fmt.Fprintf(sb, "TEXT %d,%d,\"%s\",%d,%d,%d,\"%s\"\n", x, y, textFont, rot, titleMult, titleMult, tsplEscape(text))
		y += (fontCharHDots * titleMult) + lineGap
	}
	if sku := l.Sku; sku != "" || l.SubTitle != "" {
		if sku == "" {
			sku = l.SubTitle
		}
		maxChars := maxInt(availW/(fontCharWDots*subMult), 4)
		text := truncate(sku, maxChars)
		x := marginX + centerOffsetDots(availW, len([]rune(text))*fontCharWDots*subMult)
		fmt.Fprintf(sb, "TEXT %d,%d,\"%s\",%d,%d,%d,\"%s\"\n", x, y, textFont, rot, subMult, subMult, tsplEscape(text))
		y += (fontCharHDots * subMult) + lineGap
	}
	if l.DetailLine != "" {
		maxChars := maxInt(availW/(fontCharWDots*subMult), 4)
		text := truncate(l.DetailLine, maxChars)
		x := marginX + centerOffsetDots(availW, len([]rune(text))*fontCharWDots*subMult)
		fmt.Fprintf(sb, "TEXT %d,%d,\"%s\",%d,%d,%d,\"%s\"\n", x, y, textFont, rot, subMult, subMult, tsplEscape(text))
		y += (fontCharHDots * subMult) + lineGap
	}

	// Barcode height: capped relative to the label's WIDTH (not just "half the remaining
	// height") — on a tall/narrow label, sizing off height alone produced bars taller than the
	// barcode's own natural width, reading as a dense vertical column instead of the normal
	// short-and-wide look a barcode symbol should have. Scaling the cap with width keeps bigger
	// wide presets (e.g. 4x6in shipping labels) free to use a taller, more legible barcode while
	// still constraining narrow ones.
	barY := y
	barH := maxInt((h-y)/2, 60)
	if maxBarH := w * 2 / 5; barH > maxBarH {
		barH = maxBarH
	}
	if barH < 60 {
		barH = 60
	}
	if remaining := h - y; barH > remaining {
		barH = remaining
	}
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
	const narrowBarDots = 2
	estWidth := estimateEAN13WidthDots(narrowBarDots)
	if barcodeType == "128" {
		estWidth = estimateCode128WidthDots(content, narrowBarDots)
	}
	barX := marginX + centerOffsetDots(availW, estWidth)
	fmt.Fprintf(sb, "BARCODE %d,%d,\"%s\",%d,1,%d,%d,%d,\"%s\"\n",
		barX, barY, barcodeType, barH, rot, narrowBarDots, narrowBarDots, tsplEscape(content))
	y = barY + barH + lineGap*2

	if l.Price != "" {
		priceMult := titleMult
		maxChars := maxInt(availW/(fontCharWDots*priceMult), 4)
		text := truncate(l.Price, maxChars)
		x := marginX + centerOffsetDots(availW, len([]rune(text))*fontCharWDots*priceMult)
		fmt.Fprintf(sb, "TEXT %d,%d,\"%s\",%d,%d,%d,\"%s\"\n", x, y, textFont, rot, priceMult, priceMult, tsplEscape(text))
	}
}

// estimateCode128WidthDots approximates a Code128 symbol's printed width in dots: (start + data-
// and-check symbol characters, 11 modules each + stop, 13 modules) × the narrow-bar module width.
// This treats every input byte as one symbol character (Code128's Set-C digit-pairing can make
// real barcodes narrower than this estimate for numeric-heavy content) — an intentional
// over-estimate so the centering computed from it never pushes the symbol's actual (narrower)
// render past the label's right edge.
func estimateCode128WidthDots(content string, narrowBarDots int) int {
	modules := (len(content)+2)*11 + 13
	return modules * narrowBarDots
}

// estimateEAN13WidthDots is EAN-13's fixed symbol width (95 modules — 3 guard bars ×2 + 2×6 data
// digit groups ×7 modules each — a standard, unlike Code128's variable-length encoding).
func estimateEAN13WidthDots(narrowBarDots int) int { return 95 * narrowBarDots }

// centerOffsetDots returns how far to shift right from the left margin to center content of
// contentWidthDots within availWidthDots — 0 (flush-left, not negative) if the content is
// already at least as wide as the available space.
func centerOffsetDots(availWidthDots, contentWidthDots int) int {
	off := (availWidthDots - contentWidthDots) / 2
	if off < 0 {
		return 0
	}
	return off
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
