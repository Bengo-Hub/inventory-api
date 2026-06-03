package render

import (
	"strconv"
	"strings"
)

// rgb is an 8-bit RGB color used by the fpdf renderer.
type rgb struct{ r, g, b int }

// palette is the full set of colors used to render a document. The branded
// shades (sky / blue / navy / lightBlue / line) are derived from the tenant's
// primary color; the neutral ink/grey/muted tones and white are fixed so body
// text stays legible regardless of brand color.
type palette struct {
	sky       rgb // lightest brand tone (gradient start)
	blue      rgb // mid brand tone (= tenant primary)
	navy      rgb // darkest brand tone (gradient end, headings)
	ink       rgb // body text
	grey      rgb // secondary text
	muted     rgb // tertiary text
	line      rgb // borders / hairlines
	lightBlue rgb // light brand tint (key cells, subtotal bg)
	white     rgb
	bannerSub rgb // banner sub-label tone (light tint over gradient)
}

// defaultBrand is the house blue used when a tenant has no primary color set.
var defaultBrand = rgb{31, 111, 178}

// newPalette builds a cohesive palette from the tenant primary color (hex).
// Falls back to defaultBrand on empty/invalid input.
func newPalette(primaryHex string) palette {
	base := defaultBrand
	if c, ok := hexToRGB(primaryHex); ok {
		base = c
	}
	return palette{
		sky:       lighten(base, 0.30),
		blue:      base,
		navy:      darken(base, 0.45),
		ink:       rgb{34, 48, 63},
		grey:      rgb{82, 97, 122},
		muted:     rgb{107, 122, 144},
		line:      lighten(base, 0.80),
		lightBlue: lighten(base, 0.92),
		white:     rgb{255, 255, 255},
		bannerSub: lighten(base, 0.78),
	}
}

// hexToRGB parses a "#RRGGBB" (or "RRGGBB") hex string. ok is false on bad input.
func hexToRGB(hex string) (rgb, bool) {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return rgb{}, false
	}
	r, e1 := strconv.ParseInt(hex[0:2], 16, 0)
	g, e2 := strconv.ParseInt(hex[2:4], 16, 0)
	b, e3 := strconv.ParseInt(hex[4:6], 16, 0)
	if e1 != nil || e2 != nil || e3 != nil {
		return rgb{}, false
	}
	return rgb{int(r), int(g), int(b)}, true
}

// darken scales each channel toward black by factor f (0..1).
func darken(c rgb, f float64) rgb {
	return rgb{
		r: clamp(int(float64(c.r) * (1 - f))),
		g: clamp(int(float64(c.g) * (1 - f))),
		b: clamp(int(float64(c.b) * (1 - f))),
	}
}

// lighten scales each channel toward white by factor f (0..1).
func lighten(c rgb, f float64) rgb {
	return rgb{
		r: clamp(c.r + int(float64(255-c.r)*f)),
		g: clamp(c.g + int(float64(255-c.g)*f)),
		b: clamp(c.b + int(float64(255-c.b)*f)),
	}
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
