// Package pdfcolor is the single source of the tenant-brand color math shared by every A4 PDF
// renderer in inventory-api ([modules/docs]'s tabular reports and [modules/documents/render]'s
// business documents — purchase orders, stock transfers/delivery notes, goods receipts, etc.).
// Both engines used to carry their own copy of this arithmetic (hexToRGB/darken/lighten/clamp),
// which is exactly the kind of copy-paste drift that let a legibility bug ship in one and not the
// other; centralizing it here means a fix (like TextSafeDarken below) lands for every document
// type at once instead of needing to be re-applied per engine.
package pdfcolor

import (
	"strconv"
	"strings"
)

// HexToRGB parses a "#RRGGBB" (or "RRGGBB") hex string. ok is false on bad input.
func HexToRGB(hex string) (r, g, b int, ok bool) {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	rv, e1 := strconv.ParseInt(hex[0:2], 16, 0)
	gv, e2 := strconv.ParseInt(hex[2:4], 16, 0)
	bv, e3 := strconv.ParseInt(hex[4:6], 16, 0)
	if e1 != nil || e2 != nil || e3 != nil {
		return 0, 0, 0, false
	}
	return int(rv), int(gv), int(bv), true
}

// Clamp restricts v to the valid 8-bit color range.
func Clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// Darken scales each channel toward black by factor f (0..1).
func Darken(r, g, b int, f float64) (int, int, int) {
	return Clamp(int(float64(r) * (1 - f))), Clamp(int(float64(g) * (1 - f))), Clamp(int(float64(b) * (1 - f)))
}

// Lighten scales each channel toward white by factor f (0..1).
func Lighten(r, g, b int, f float64) (int, int, int) {
	return Clamp(r + int(float64(255-r)*f)), Clamp(g + int(float64(255-g)*f)), Clamp(b + int(float64(255-b)*f))
}

// TextSafeDarken applies Darken(r, g, b, f) and then guarantees the result is dark enough to
// print legibly, by proportionally rescaling it toward black if its brightest channel still
// exceeds floor.
//
// Plain Darken alone isn't enough when the input is a tenant's UI brand color: those are picked
// for on-screen theming, where near-white and pastel colors are completely normal (a clean white
// dashboard accent, a light pastel UI theme). Fed into a flat multiplicative darken, a color like
// white (255,255,255) darkened by 45% is still (140,140,140) — a mid-gray that has enough contrast
// on a backlit screen but crushes toward invisible on a real printer (thermal, low-toner laser,
// or a phone photo of the printout), exactly the "text is not visible" symptom reported against a
// tenant-branded delivery note whose brand color was light. pos-api's printed receipts never hit
// this because they don't tint readable text with brand color at all — every line is fpdf's plain
// default black. TextSafeDarken keeps the branded look (readable text still carries a tint of the
// tenant's hue when that hue is already reasonably dark) while giving every document type the
// same print-safe floor a plain black-only design gets for free.
func TextSafeDarken(r, g, b int, f float64, floor int) (int, int, int) {
	dr, dg, db := Darken(r, g, b, f)
	m := dr
	if dg > m {
		m = dg
	}
	if db > m {
		m = db
	}
	if m <= floor || m == 0 {
		return dr, dg, db
	}
	scale := float64(floor) / float64(m)
	return int(float64(dr) * scale), int(float64(dg) * scale), int(float64(db) * scale)
}
