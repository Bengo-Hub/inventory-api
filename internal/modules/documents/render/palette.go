package render

import "github.com/bengobox/inventory-service/internal/pdfcolor"

// rgb is an 8-bit RGB color used by the fpdf renderer.
type rgb struct{ r, g, b int }

// palette is the full set of colors used to render a document. The branded
// shades (sky / blue / navy / lightBlue / line) are derived from the tenant's
// primary color; the neutral ink/grey/muted tones and white are fixed so body
// text stays legible regardless of brand color.
type palette struct {
	sky       rgb // lightest brand tone (gradient start)
	blue      rgb // mid brand tone (label text + gradient accent) — print-safe floored
	navy      rgb // darkest brand tone (headings, descriptions) — print-safe floored
	ink       rgb // body text
	grey      rgb // secondary text
	muted     rgb // tertiary text
	line      rgb // borders / hairlines
	lightBlue rgb // light brand tint (key cells, subtotal bg)
	white     rgb
	bannerSub rgb // banner sub-label tone (light tint over gradient)
	warn      rgb // fixed (not brand-derived) — flags a non-zero transfer variance
}

// defaultBrand is the house blue used when a tenant has no primary color set.
var defaultBrand = rgb{31, 111, 178}

// navyTextFloor/blueTextFloor are the brightest a channel of pal.navy/pal.blue may be — see
// pdfcolor.TextSafeDarken. navy carries the document's primary readable text (titles, item
// descriptions, party names, meta values), so it gets the darker floor; blue only ever carries
// secondary label text (field labels, subtitles, taglines) alongside a couple of pure decorative
// uses (gradient accents), so it keeps more of the brand hue.
const (
	navyTextFloor = 90
	blueTextFloor = 150
)

// newPalette builds a cohesive palette from the tenant primary color (hex).
// Falls back to defaultBrand on empty/invalid input.
func newPalette(primaryHex string) palette {
	base := defaultBrand
	if r, g, b, ok := pdfcolor.HexToRGB(primaryHex); ok {
		base = rgb{r, g, b}
	}
	sr, sg, sb := pdfcolor.Lighten(base.r, base.g, base.b, 0.30)
	nr, ng, nb := pdfcolor.TextSafeDarken(base.r, base.g, base.b, 0.45, navyTextFloor)
	br, bg, bb := pdfcolor.TextSafeDarken(base.r, base.g, base.b, 0, blueTextFloor)
	lr, lg, lb := pdfcolor.Lighten(base.r, base.g, base.b, 0.80)
	lbr, lbg, lbb := pdfcolor.Lighten(base.r, base.g, base.b, 0.92)
	bsr, bsg, bsb := pdfcolor.Lighten(base.r, base.g, base.b, 0.78)
	return palette{
		sky:       rgb{sr, sg, sb},
		blue:      rgb{br, bg, bb},
		navy:      rgb{nr, ng, nb},
		ink:       rgb{34, 48, 63},
		grey:      rgb{82, 97, 122},
		muted:     rgb{107, 122, 144},
		line:      rgb{lr, lg, lb},
		lightBlue: rgb{lbr, lbg, lbb},
		white:     rgb{255, 255, 255},
		bannerSub: rgb{bsr, bsg, bsb},
		warn:      rgb{158, 30, 30},
	}
}
