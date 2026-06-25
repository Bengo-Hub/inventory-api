package barcode

import (
	"fmt"
	"strings"
	"time"
)

// GS1Element is a single Application Identifier (AI) + its value, used to build a GS1-128
// barcode for lot/serial/weighed-item labels.
type GS1Element struct {
	AI    string // e.g. "01", "10", "17", "21", "3103", "3922"
	Value string
}

// fixedLenAIs lists AIs whose data length is fixed by the GS1 spec; these do NOT need an
// FNC1 separator after them. Everything else is variable-length and is followed by FNC1
// (except when it's the last element in the string).
var fixedLenAIs = map[string]bool{
	"00": true, "01": true, "02": true,
	"11": true, "12": true, "13": true, "15": true, "16": true, "17": true,
	"20": true,
	// (3nnn) decimal measures are fixed 6-digit values.
	"3100": true, "3101": true, "3102": true, "3103": true, "3104": true, "3105": true,
	"3200": true, "3201": true, "3202": true, "3203": true, "3204": true, "3205": true,
}

// GS1Builder accumulates AI elements and renders the human-readable and barcode strings.
type GS1Builder struct {
	elements []GS1Element
}

// NewGS1 starts a new GS1-128 element list.
func NewGS1() *GS1Builder { return &GS1Builder{} }

// Add appends a raw AI element.
func (b *GS1Builder) Add(ai, value string) *GS1Builder {
	if value == "" {
		return b
	}
	b.elements = append(b.elements, GS1Element{AI: ai, Value: value})
	return b
}

// GTIN adds (01) — the 14-digit GTIN. A 13-digit EAN is left-padded to 14.
func (b *GS1Builder) GTIN(gtin string) *GS1Builder {
	gtin = strings.TrimSpace(gtin)
	if gtin == "" {
		return b
	}
	for len(gtin) < 14 {
		gtin = "0" + gtin
	}
	return b.Add("01", gtin)
}

// NetWeightKg adds (3103) — net weight in kilograms with 3 implied decimals (e.g. 1.250 kg → "001250").
func (b *GS1Builder) NetWeightKg(kg float64) *GS1Builder {
	if kg <= 0 {
		return b
	}
	v := int64(kg*1000 + 0.5)
	return b.Add("3103", fmt.Sprintf("%06d", v))
}

// PriceMinor adds (392n) — amount payable, single monetary area, with `decimals` implied decimal
// places. minorUnits is the integer value already scaled (e.g. KES 125.00 with 2 decimals → 12500).
func (b *GS1Builder) PriceMinor(minorUnits int64, decimals int) *GS1Builder {
	if minorUnits <= 0 {
		return b
	}
	if decimals < 0 || decimals > 9 {
		decimals = 2
	}
	return b.Add(fmt.Sprintf("392%d", decimals), fmt.Sprintf("%d", minorUnits))
}

// Batch adds (10) — batch / lot number (variable length, alphanumeric).
func (b *GS1Builder) Batch(lot string) *GS1Builder { return b.Add("10", sanitizeAlnum(lot)) }

// Serial adds (21) — serial number (variable length, alphanumeric).
func (b *GS1Builder) Serial(serial string) *GS1Builder { return b.Add("21", sanitizeAlnum(serial)) }

// Expiry adds (17) — expiration date as YYMMDD.
func (b *GS1Builder) Expiry(t time.Time) *GS1Builder {
	if t.IsZero() {
		return b
	}
	return b.Add("17", t.Format("060102"))
}

// sanitizeAlnum keeps GS1 AI-82 safe characters (drops the FNC1 sentinel and control chars).
func sanitizeAlnum(s string) string {
	s = strings.TrimSpace(s)
	return strings.Map(func(r rune) rune {
		if r == 'ñ' || r < 0x20 { // strip FNC1 / control chars that would corrupt the stream
			return -1
		}
		return r
	}, s)
}

// HumanReadable returns the bracketed AI text printed under the bars, e.g.
// "(01)05012345678900(10)LOT42(17)261231".
func (b *GS1Builder) HumanReadable() string {
	var sb strings.Builder
	for _, e := range b.elements {
		sb.WriteString("(")
		sb.WriteString(e.AI)
		sb.WriteString(")")
		sb.WriteString(e.Value)
	}
	return sb.String()
}

// Barcode returns the raw string to feed Code128 encoding: a leading FNC1 marks it as GS1-128,
// AI+value are concatenated, and a trailing FNC1 separates each variable-length field from the next.
func (b *GS1Builder) Barcode() string {
	var sb strings.Builder
	sb.WriteString(fnc1) // leading FNC1 → GS1-128
	for i, e := range b.elements {
		sb.WriteString(e.AI)
		sb.WriteString(e.Value)
		// Variable-length fields need a trailing FNC1 unless they're the final element.
		if !fixedLenAIs[e.AI] && i < len(b.elements)-1 {
			sb.WriteString(fnc1)
		}
	}
	return sb.String()
}

// Empty reports whether no elements were added.
func (b *GS1Builder) Empty() bool { return len(b.elements) == 0 }

// RenderPNG renders this GS1-128 element list as a barcode PNG.
func (b *GS1Builder) RenderPNG(width, height int) ([]byte, error) {
	if b.Empty() {
		return nil, fmt.Errorf("barcode: empty GS1-128 element list")
	}
	return RenderPNG(b.Barcode(), SymGS1128, width, height)
}
