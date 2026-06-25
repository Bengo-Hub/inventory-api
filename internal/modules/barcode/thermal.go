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

// renderZPL emits Zebra ZPL II — one ^XA…^XZ label per Label. Layout targets a 4×2 inch
// (~812×406 dots @ 203dpi) label; the printer driver/firmware handles the actual feed.
//
// Barcode commands by symbology:
//   - EAN13   → ^BE (EAN-13)
//   - CODE128 → ^BC (Code 128, auto subset)
//   - GS1-128 → ^BC with ^FD>;… GS1 data via ^FH and the FNC1 (>8) escape
func renderZPL(labels []Label) string {
	var sb strings.Builder
	for _, l := range labels {
		sb.WriteString("^XA\n")
		sb.WriteString("^CI28\n") // UTF-8 input
		sb.WriteString("^PW812\n")
		sb.WriteString("^LH0,0\n")

		// Title.
		if l.Title != "" {
			sb.WriteString("^FO20,20^A0N,34,34^FD")
			sb.WriteString(zplEscape(truncate(l.Title, 38)))
			sb.WriteString("^FS\n")
		}
		// Sub-title (SKU).
		if l.SubTitle != "" {
			sb.WriteString("^FO20,60^A0N,26,26^FD")
			sb.WriteString(zplEscape(truncate(l.SubTitle, 42)))
			sb.WriteString("^FS\n")
		}

		// Barcode.
		switch l.Symbology {
		case SymEAN13, SymUPCA:
			code, err := NormalizeEAN13(l.Content)
			if err == nil {
				sb.WriteString("^BY3\n^FO40,100^BEN,120,Y,N\n^FD")
				sb.WriteString(code[:12]) // ^BE takes 12 digits; printer adds check digit
				sb.WriteString("^FS\n")
			}
		case SymGS1128:
			// GS1-128: ^BC with field-hex on; >8 is the FNC1 escape inside ^FD.
			sb.WriteString("^BY3\n^FO40,100^BCN,120,Y,N,N\n^FH^FD")
			sb.WriteString(zplGS1Data(l.Content))
			sb.WriteString("^FS\n")
		default: // CODE128
			sb.WriteString("^BY3\n^FO40,100^BCN,120,Y,N,N\n^FD")
			sb.WriteString(zplEscape(l.Content))
			sb.WriteString("^FS\n")
		}

		// Price.
		if l.Price != "" {
			sb.WriteString("^FO20,250^A0N,40,40^FD")
			sb.WriteString(zplEscape(l.Price))
			sb.WriteString("^FS\n")
		}

		sb.WriteString("^XZ\n")
	}
	return sb.String()
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
		if l.SubTitle != "" {
			sb.WriteString("SKU: " + sanitizeLine(l.SubTitle) + "\n")
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
