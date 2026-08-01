package barcode

import "strings"

// LabelTemplate describes a physical label roll/stock: one lane's size, how many lanes sit
// side-by-side across the roll's width, gaps, DPI, and whether content must be rotated 90° to
// read correctly given how the roll is mounted in the printer.
//
// This replaces the old single-lane ThermalSpec. The rotation/orientation bug this module used
// to have (labels printing sideways on an Xprinter XP-330B) came from assuming every roll is
// mounted with its "width" (LabelWIn) running across the printhead and its "height" (LabelHIn)
// running along the feed — true for some rolls, false for others. Rotate makes that an explicit,
// per-template fact instead of something inferred from comparing W vs H. See docs/barcode-labels.md.
type LabelTemplate struct {
	Name string

	LabelWIn float64 // one lane's label width, inches (content's natural/unrotated orientation)
	LabelHIn float64 // label height along the feed direction, inches
	DPI      int

	Lanes  int     // 1-4 labels side by side across the roll's width
	GapXIn float64 // gutter BETWEEN lanes, inches (0 when Lanes==1)
	GapYIn float64 // gap between labels along the feed direction, inches (die-cut gap)

	// Rotate: true when the physical media is mounted so content must print turned 90° to read
	// correctly along the feed direction (e.g. a narrow roll where the label's long edge feeds
	// first). Each renderer (PDF/ZPL/TSPL) implements this the same way conceptually but via its
	// own mechanism — see renderZPL's ^FWR emission, renderTSPL's per-command rotation param, and
	// RenderSingleLabelPDF's page-dim swap + fpdf transform.
	Rotate bool

	Custom bool // true when built from caller-supplied dims via CustomLabelTemplate, not a named preset
}

// RollWidthIn returns the full roll width in inches: all lanes plus the gutters between them.
func (t LabelTemplate) RollWidthIn() float64 {
	lanes := t.Lanes
	if lanes < 1 {
		lanes = 1
	}
	return float64(lanes)*t.LabelWIn + float64(lanes-1)*t.GapXIn
}

// WidthDots / HeightDots convert one lane's physical size to printer dots at the template's DPI.
func (t LabelTemplate) WidthDots() int  { return int(t.LabelWIn * float64(t.DPI)) }
func (t LabelTemplate) HeightDots() int { return int(t.LabelHIn * float64(t.DPI)) }

// RollWidthDots is the full roll width (all lanes + gutters) in dots.
func (t LabelTemplate) RollWidthDots() int { return int(t.RollWidthIn() * float64(t.DPI)) }

// GapXDots is the gutter between lanes in dots.
func (t LabelTemplate) GapXDots() int { return int(t.GapXIn * float64(t.DPI)) }

// GapYDots is the gap between labels along the feed, in dots.
func (t LabelTemplate) GapYDots() int { return int(t.GapYIn * float64(t.DPI)) }

// LaneXOffsetDots returns the x-offset (in dots, from the roll's left edge) at which the given
// zero-based lane index starts — used by renderZPL/renderTSPL to tile labels side-by-side across
// the roll width instead of one at a time.
func (t LabelTemplate) LaneXOffsetDots(lane int) int {
	return lane * (t.WidthDots() + t.GapXDots())
}

// laneCount normalizes Lanes to a valid 1-4 range.
func (t LabelTemplate) laneCount() int {
	switch {
	case t.Lanes < 1:
		return 1
	case t.Lanes > 4:
		return 4
	default:
		return t.Lanes
	}
}

// namedLabelTemplates are the built-in presets. The four single-lane sizes (2x1/3x2/4x2/4x6)
// are exact aliases of the old ThermalSpecByName presets so existing callers/requests keep
// working unchanged. The "Nrow_*" presets are new multi-lane templates for wider rolls that carry
// several labels side-by-side across the web width — what the user calls "1 row / 2 rows / 3 or 4
// rows" label stock (the roll in hand today, a single narrow lane feeding one label at a time
// down its length, is "1 row"; a wider roll die-cut into parallel lanes is "2/3/4 rows") — their
// dimensions are engineering estimates sized to fit within the Xprinter XP-330B's confirmed
// ≤80mm media width, NOT vendor-confirmed exact Xprinter SKUs. A tenant with different real stock
// should use the "custom" template (CustomLabelTemplate) instead of assuming one of these matches
// their roll exactly. GapYIn on every preset is the physical gap/perforation between labels along
// the feed — SIZE/GAP (TSPL) and ^LL (ZPL) are always set from ONE label's real height plus that
// gap, never guessed, so a single barcode's content can never bleed across the boundary into the
// next physical label (the "one barcode printing across several label sheets" failure mode).
func namedLabelTemplates() map[string]LabelTemplate {
	return map[string]LabelTemplate{
		// Back-compat single-lane aliases of the old ThermalSpecByName presets.
		"2x1": {Name: "2\"x1\" — 1 row @203dpi", LabelWIn: 2, LabelHIn: 1, DPI: 203, Lanes: 1, GapYIn: 2.0 / 25.4},
		"3x2": {Name: "3\"x2\" — 1 row @203dpi", LabelWIn: 3, LabelHIn: 2, DPI: 203, Lanes: 1, GapYIn: 2.0 / 25.4},
		"4x2": {Name: "4\"x2\" — 1 row @203dpi", LabelWIn: 4, LabelHIn: 2, DPI: 203, Lanes: 1, GapYIn: 2.0 / 25.4},
		"4x6": {Name: "4\"x6\" — 1 row @203dpi", LabelWIn: 4, LabelHIn: 6, DPI: 203, Lanes: 1, GapYIn: 3.0 / 25.4},

		// New multi-row (multi-lane) presets: N labels side-by-side per feed-row on one roll.
		"1row_40x30": {
			Name: "1 row — 40x30mm @203dpi", LabelWIn: 40.0 / 25.4, LabelHIn: 30.0 / 25.4,
			DPI: 203, Lanes: 1, GapYIn: 2.0 / 25.4,
		},
		"2row_38x30": {
			Name: "2 rows — 38x30mm each @203dpi", LabelWIn: 38.0 / 25.4, LabelHIn: 30.0 / 25.4,
			DPI: 203, Lanes: 2, GapXIn: 2.0 / 25.4, GapYIn: 2.0 / 25.4,
		},
		"3row_25x40": {
			Name: "3 rows — 25x40mm each @203dpi", LabelWIn: 25.0 / 25.4, LabelHIn: 40.0 / 25.4,
			DPI: 203, Lanes: 3, GapXIn: 1.5 / 25.4, GapYIn: 2.0 / 25.4,
		},
		"4row_18x30": {
			Name: "4 rows — 18x30mm each @203dpi", LabelWIn: 18.0 / 25.4, LabelHIn: 30.0 / 25.4,
			DPI: 203, Lanes: 4, GapXIn: 1.0 / 25.4, GapYIn: 2.0 / 25.4,
		},
	}
}

// LabelTemplateByName resolves a request-supplied template/preset name; unknown/empty falls back
// to the pre-existing 4x2 single-lane default so old callers with no opinion keep working.
func LabelTemplateByName(name string) LabelTemplate {
	key := strings.ToLower(strings.TrimSpace(name))
	if t, ok := namedLabelTemplates()[key]; ok {
		if t.Lanes < 1 {
			t.Lanes = 1
		}
		return t
	}
	return namedLabelTemplates()["4x2"]
}

// CustomLabelTemplate builds a template from caller-supplied physical dimensions (a real label
// roll that doesn't match any built-in preset). lanes is clamped to 1-4.
func CustomLabelTemplate(wIn, hIn float64, lanes int, gapXIn, gapYIn float64, rotate bool) LabelTemplate {
	if lanes < 1 {
		lanes = 1
	}
	if lanes > 4 {
		lanes = 4
	}
	if wIn <= 0 {
		wIn = 4
	}
	if hIn <= 0 {
		hIn = 2
	}
	return LabelTemplate{
		Name:     "Custom",
		LabelWIn: wIn, LabelHIn: hIn, DPI: 203,
		Lanes: lanes, GapXIn: gapXIn, GapYIn: gapYIn,
		Rotate: rotate, Custom: true,
	}
}
