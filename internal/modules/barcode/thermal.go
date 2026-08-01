package barcode

import (
	"fmt"
	"strings"
)

// zplEscape removes characters that would break a ZPL field (^ and ~ are command introducers).
func zplEscape(s string) string {
	r := strings.NewReplacer("^", " ", "~", " ", "\n", " ", "\r", " ")
	return r.Replace(s)
}

// renderZPL emits Zebra ZPL II — one ^XA…^XZ block per feed-row, sized to the given
// LabelTemplate so the print width/length actually matches whatever roll is loaded and however
// many lanes it carries side-by-side (see docs/barcode-labels.md).
//
// Orientation: ^PW/^LL always describe the roll AS MOUNTED (never swapped to "fix" rotation —
// see template.go's Rotate doc). When tmpl.Rotate is set, ^FWR rotates every field in the block
// 90° so content reads correctly along the feed direction on rolls mounted that way; this was
// previously impossible to express (no ^FWR anywhere in this module), which was the root cause
// of labels printing sideways on rolls not mounted the way the code implicitly assumed.
//
// Multi-lane: when tmpl.Lanes > 1, each feed-row draws up to `lanes` consecutive labels from the
// batch side-by-side (at x-offsets from tmpl.LaneXOffsetDots), then advances to the next row —
// the same left-to-right tiling idea renderAveryPDF already uses for its sheet grid, applied to
// a continuous roll instead of a fixed page.
//
// Barcode commands by symbology:
//
//	EAN13   → ^BE (EAN-13)
//	CODE128 → ^BC (Code 128, auto subset)
//	GS1-128 → ^BC with ^FD>;… GS1 data via ^FH and the FNC1 (>8) escape
func renderZPL(labels []Label, tmpl LabelTemplate) string {
	lanes := tmpl.laneCount()
	rollW := tmpl.RollWidthDots()
	h := tmpl.HeightDots()

	var sb strings.Builder
	for i := 0; i < len(labels); i += lanes {
		sb.WriteString("^XA\n")
		sb.WriteString("^CI28\n") // UTF-8 input
		sb.WriteString(fmt.Sprintf("^PW%d\n", rollW))
		sb.WriteString(fmt.Sprintf("^LL%d\n", h))
		sb.WriteString("^LH0,0\n")
		if tmpl.Rotate {
			sb.WriteString("^FWR\n") // rotate every field in this block 90°: see template.go Rotate doc
		}

		for lane := 0; lane < lanes && i+lane < len(labels); lane++ {
			writeZPLLabel(&sb, labels[i+lane], tmpl, tmpl.LaneXOffsetDots(lane))
		}

		sb.WriteString("^XZ\n")
	}
	return sb.String()
}

// writeZPLLabel draws one label's fields at the given x-offset (dots) within the current ^XA
// block. Every offset below is a fraction of the label's own height in dots, so smaller presets
// scale down instead of overflowing.
func writeZPLLabel(sb *strings.Builder, l Label, tmpl LabelTemplate, xOffset int) {
	w, h := tmpl.WidthDots(), tmpl.HeightDots()
	marginX := xOffset + w/20
	titleSize := maxInt(h/10, 18)
	subSize := maxInt(h/14, 14)
	lineGap := h / 40

	y := h / 20

	// Title.
	if l.Title != "" {
		sb.WriteString(fmt.Sprintf("^FO%d,%d^A0N,%d,%d^FD", marginX, y, titleSize, titleSize))
		sb.WriteString(zplEscape(truncate(l.Title, 38)))
		sb.WriteString("^FS\n")
		y += titleSize + lineGap
	}
	// SKU (own line — was previously the only "sub-title" slot).
	if sku := l.Sku; sku != "" || l.SubTitle != "" {
		if sku == "" {
			sku = l.SubTitle
		}
		sb.WriteString(fmt.Sprintf("^FO%d,%d^A0N,%d,%d^FD", marginX, y, subSize, subSize))
		sb.WriteString(zplEscape(truncate(sku, 42)))
		sb.WriteString("^FS\n")
		y += subSize + lineGap
	}
	// Lot/serial + expiry detail line.
	if l.DetailLine != "" {
		sb.WriteString(fmt.Sprintf("^FO%d,%d^A0N,%d,%d^FD", marginX, y, subSize, subSize))
		sb.WriteString(zplEscape(truncate(l.DetailLine, 42)))
		sb.WriteString("^FS\n")
		y += subSize + lineGap
	}

	// Barcode — fills roughly a third of the remaining height.
	barY := y
	barH := maxInt((h-y)/2, 60)
	switch l.Symbology {
	case SymEAN13, SymUPCA:
		code, err := NormalizeEAN13(l.Content)
		if err == nil {
			sb.WriteString(fmt.Sprintf("^BY3\n^FO%d,%d^BEN,%d,Y,N\n^FD", marginX+w/40, barY, barH))
			sb.WriteString(code[:12]) // ^BE takes 12 digits; printer adds check digit
			sb.WriteString("^FS\n")
		}
	case SymGS1128:
		// GS1-128: ^BC with field-hex on; >8 is the FNC1 escape inside ^FD.
		sb.WriteString(fmt.Sprintf("^BY3\n^FO%d,%d^BCN,%d,Y,N,N\n^FH^FD", marginX+w/40, barY, barH))
		sb.WriteString(zplGS1Data(l.Content))
		sb.WriteString("^FS\n")
	default: // CODE128
		sb.WriteString(fmt.Sprintf("^BY3\n^FO%d,%d^BCN,%d,Y,N,N\n^FD", marginX+w/40, barY, barH))
		sb.WriteString(zplEscape(l.Content))
		sb.WriteString("^FS\n")
	}
	y = barY + barH + lineGap*2

	// Price.
	if l.Price != "" {
		priceSize := maxInt(h/10, 18)
		sb.WriteString(fmt.Sprintf("^FO%d,%d^A0N,%d,%d^FD", marginX, y, priceSize, priceSize))
		sb.WriteString(zplEscape(l.Price))
		sb.WriteString("^FS\n")
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// zplGS1Data converts a GS1 payload (leading + embedded FNC1 = the 'ñ' rune) into ZPL ^FD
// data using the >8 FNC1 escape and >; start. We strip the literal FNC1 runes and represent
// each as the ZPL escape sequence.
func zplGS1Data(content string) string {
	// Drop the leading FNC1, then replace internal FNC1 separators with the >8 escape.
	content = strings.TrimPrefix(content, fnc1)
	content = strings.ReplaceAll(content, fnc1, ">8")
	return ">;>8" + zplEscape(content)
}

// renderDymo emits a simple line-based DYMO label stream. DYMO's native format is XML
// (.label), but most integrations drive the printer through a host SDK; here we emit a
// compact, deterministic text block per label that a DYMO host bridge can consume.
func renderDymo(labels []Label) string {
	var sb strings.Builder
	for i, l := range labels {
		sb.WriteString(fmt.Sprintf("DYMO LABEL %d\n", i+1))
		if l.Title != "" {
			sb.WriteString("TITLE: " + sanitizeLine(l.Title) + "\n")
		}
		if sku := l.Sku; sku != "" || l.SubTitle != "" {
			if sku == "" {
				sku = l.SubTitle
			}
			sb.WriteString("SKU: " + sanitizeLine(sku) + "\n")
		}
		if l.DetailLine != "" {
			sb.WriteString("DETAIL: " + sanitizeLine(l.DetailLine) + "\n")
		}
		sb.WriteString("SYMBOLOGY: " + string(l.Symbology) + "\n")
		sb.WriteString("BARCODE: " + sanitizeLine(stripFNC1(l.Content)) + "\n")
		sb.WriteString("HUMAN: " + sanitizeLine(l.human()) + "\n")
		if l.Price != "" {
			sb.WriteString("PRICE: " + sanitizeLine(l.Price) + "\n")
		}
		sb.WriteString("END\n")
	}
	return sb.String()
}

func stripFNC1(s string) string { return strings.ReplaceAll(s, fnc1, "") }

func sanitizeLine(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
