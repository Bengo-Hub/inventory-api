// Package barcode generates item barcodes and printable label sheets for inventory.
//
// Symbologies:
//   - EAN-13           — retail item barcodes. Manufacturer GTINs are stored verbatim;
//     in-house items get an INTERNAL EAN-13 in the GS1 restricted-circulation range
//     (prefix 20–29). 02 is reserved here for variable-measure / store-weighed items.
//   - Code 128         — alphanumeric SKUs that don't fit the numeric-only EAN-13.
//   - GS1-128          — Code128 + Application Identifiers (AIs) for weighed/lot/serial
//     labels: (01) GTIN, (3103) net weight kg, (392n) price, (10) batch/lot,
//     (17) expiry YYMMDD, (21) serial.
//
// Rendering goes to PNG (boombuler/barcode), which the label engine embeds in Avery
// A4 PDF sheets (go-pdf/fpdf) or streams directly for the single-item endpoint.
package barcode

import (
	"bytes"
	"fmt"
	"image/png"
	"strconv"
	"strings"

	bb "github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	"github.com/boombuler/barcode/ean"
)

// Symbology identifies the encoding used for a rendered barcode image.
type Symbology string

const (
	SymEAN13   Symbology = "EAN13"
	SymUPCA    Symbology = "UPCA"
	SymCode128 Symbology = "CODE128"
	SymGS1128  Symbology = "GS1-128"
)

// fnc1 is the Code128 special character that, as the first symbol, makes the
// barcode a GS1-128 (FNC1 also separates variable-length AI fields).
const fnc1 = string(code128.FNC1)

// EAN13CheckDigit computes the GS1 modulo-10 check digit for the first 12 digits.
// digits12 must be exactly 12 numeric characters.
func EAN13CheckDigit(digits12 string) (int, error) {
	if len(digits12) != 12 {
		return 0, fmt.Errorf("barcode: EAN-13 needs 12 digits, got %d", len(digits12))
	}
	sum := 0
	for i, r := range digits12 {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("barcode: non-digit %q in EAN body", r)
		}
		d := int(r - '0')
		// Odd positions (1-indexed) weight 1, even weight 3.
		if i%2 == 0 {
			sum += d
		} else {
			sum += d * 3
		}
	}
	return (10 - (sum % 10)) % 10, nil
}

// CompleteEAN13 appends the check digit to a 12-digit body, returning the full 13-digit code.
func CompleteEAN13(digits12 string) (string, error) {
	cd, err := EAN13CheckDigit(digits12)
	if err != nil {
		return "", err
	}
	return digits12 + strconv.Itoa(cd), nil
}

// GenerateInternalEAN13 builds a stable internal EAN-13 in the GS1 restricted-circulation
// range (prefix 20–29). The prefix MUST be two digits in "20".."29". The remaining 10 body
// digits are derived from `seed` (typically a per-item counter or a hash of the SKU); only the
// low 10 digits are used. The check digit is appended. These codes are valid for in-store
// scanning only and are never published to the global GS1 registry.
//
// Convention: prefix "02" is NOT a valid restricted prefix on its own — variable-measure
// in-store items use prefix "02" only inside POS price-embedded barcodes; for a plain internal
// item code use 20–29. Callers that want a weighed-item code should use BuildGS1128 instead.
func GenerateInternalEAN13(prefix string, seed uint64) (string, error) {
	if len(prefix) != 2 || prefix[0] != '2' {
		return "", fmt.Errorf("barcode: internal EAN-13 prefix must be 20-29, got %q", prefix)
	}
	// 10 body digits from the seed, zero-padded.
	body := fmt.Sprintf("%010d", seed%10000000000)
	return CompleteEAN13(prefix + body)
}

// NormalizeEAN13 validates/repairs a numeric code for EAN-13 use. It accepts:
//   - 13 digits: validated against its check digit (error if the check digit is wrong).
//   - 12 digits: treated as a body, check digit appended.
//   - otherwise: error.
func NormalizeEAN13(code string) (string, error) {
	code = strings.TrimSpace(code)
	switch len(code) {
	case 13:
		cd, err := EAN13CheckDigit(code[:12])
		if err != nil {
			return "", err
		}
		if int(code[12]-'0') != cd {
			return "", fmt.Errorf("barcode: EAN-13 check digit mismatch for %s", code)
		}
		return code, nil
	case 12:
		return CompleteEAN13(code)
	default:
		return "", fmt.Errorf("barcode: %q is not a 12/13-digit EAN body", code)
	}
}

// isAllDigits reports whether s is non-empty and contains only 0-9.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// encodeBarcode returns a boombuler barcode for the given content + symbology.
func encodeBarcode(content string, sym Symbology) (bb.Barcode, error) {
	switch sym {
	case SymEAN13, SymUPCA:
		norm, err := NormalizeEAN13(content)
		if err != nil {
			return nil, err
		}
		return ean.Encode(norm)
	case SymCode128, SymGS1128:
		return code128.Encode(content)
	default:
		return nil, fmt.Errorf("barcode: unsupported symbology %q", sym)
	}
}

// RenderPNG renders the content as a 1D barcode PNG at the requested pixel size.
// width/height are the target pixel dimensions; the underlying bars are scaled to fit.
// For EAN/UPC, content may be a 12 or 13 digit code. For GS1-128, pass the string from
// BuildGS1128 (it already carries the leading FNC1).
func RenderPNG(content string, sym Symbology, width, height int) ([]byte, error) {
	bc, err := encodeBarcode(content, sym)
	if err != nil {
		return nil, err
	}
	if width <= 0 {
		width = 600
	}
	if height <= 0 {
		height = 200
	}
	scaled, err := bb.Scale(bc, width, height)
	if err != nil {
		return nil, fmt.Errorf("barcode: scale: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, fmt.Errorf("barcode: png encode: %w", err)
	}
	return buf.Bytes(), nil
}

// ChooseSymbology picks the best symbology for a raw item code:
//   - 12/13 numeric digits → EAN13
//   - anything else        → CODE128
func ChooseSymbology(code string) Symbology {
	c := strings.TrimSpace(code)
	if (len(c) == 12 || len(c) == 13) && isAllDigits(c) {
		return SymEAN13
	}
	return SymCode128
}
