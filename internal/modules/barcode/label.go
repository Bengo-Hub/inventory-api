package barcode

import (
	"fmt"
	"strings"
	"time"
)

// LabelFormat selects the output transport / sheet layout for a label batch.
type LabelFormat string

const (
	// FormatAveryA4 renders an A4 PDF sheet of labels (multiple labels per page).
	FormatAveryA4 LabelFormat = "avery_a4"
	// FormatThermalZPL emits Zebra ZPL II text (one ^XA..^XZ block per label).
	FormatThermalZPL LabelFormat = "thermal_zpl"
	// FormatDymo emits DYMO label text (a simple line-based command stream per label).
	FormatDymo LabelFormat = "dymo"
)

// ParseLabelFormat normalizes a client-supplied format string.
func ParseLabelFormat(s string) (LabelFormat, error) {
	switch LabelFormat(strings.ToLower(strings.TrimSpace(s))) {
	case FormatAveryA4:
		return FormatAveryA4, nil
	case FormatThermalZPL:
		return FormatThermalZPL, nil
	case FormatDymo:
		return FormatDymo, nil
	default:
		return "", fmt.Errorf("barcode: unknown label format %q", s)
	}
}

// IsPDF reports whether the format produces a PDF (vs. printer command text).
func (f LabelFormat) IsPDF() bool { return f == FormatAveryA4 }

// ContentType returns the HTTP content type for the format's output.
func (f LabelFormat) ContentType() string {
	if f.IsPDF() {
		return "application/pdf"
	}
	return "text/plain; charset=utf-8"
}

// Label is one printable label: a title (item name), SKU, an optional lot/serial+expiry
// detail line, an optional price, and the barcode payload (already-resolved content +
// symbology). Each field renders as its own row on the card (see drawLabelCell) so a label
// with a lot number and expiry reads as distinct lines rather than one crammed string.
type Label struct {
	Title      string // item name (top line)
	Sku        string // SKU / product code — its own row
	DetailLine string // lot/serial + expiry, e.g. "LOT L45 · EXP 2026-01-01" — its own row
	Price      string // pre-formatted price string, e.g. "KES 250.00" (optional)

	// SubTitle is deprecated in favor of Sku/DetailLine; kept for any caller still setting
	// it directly. If both SubTitle and Sku/DetailLine are set, all render as separate rows.
	SubTitle string

	// Barcode content. For EAN13/CODE128, Content is the code. For GS1-128, Content is the
	// FNC1-prefixed payload and HumanText is the bracketed-AI string under the bars.
	Symbology Symbology
	Content   string
	HumanText string // printed under the bars; defaults to Content when empty
}

// human returns the text printed under the bars.
func (l Label) human() string {
	if l.HumanText != "" {
		return l.HumanText
	}
	return l.Content
}

// AverySpec describes a label-sheet grid (mm) on a single physical sheet (A4 or US Letter).
// See docs/barcode-labels.md for how these dimensions were sourced and how to add another
// real-world preset.
type AverySpec struct {
	Name    string
	Cols    int
	Rows    int
	LabelW  float64 // mm
	LabelH  float64 // mm
	MarginX float64 // mm — left/right page margin to first column
	MarginY float64 // mm — top page margin to first row
	GutterX float64 // mm — horizontal gap between columns
	GutterY float64 // mm — vertical gap between rows
}

// AveryL7160 is the standard A4 sheet-label layout: 3×7 = 21 labels/sheet, 63.5×38.1mm each.
func AveryL7160() AverySpec {
	return AverySpec{
		Name:    "Avery L7160 (A4, 21/sheet)",
		Cols:    3,
		Rows:    7,
		LabelW:  63.5,
		LabelH:  38.1,
		MarginX: 7.0,
		MarginY: 15.0,
		GutterX: 2.5,
		GutterY: 0,
	}
}

// Avery5160 is the standard US-Letter sheet-label layout (shared by 5160/8160/5260/8460):
// 3×10 = 30 labels/sheet, 2-5/8"×1" (66.7×25.4mm) each.
func Avery5160() AverySpec {
	return AverySpec{
		Name:    "Avery 5160 (US Letter, 30/sheet)",
		Cols:    3,
		Rows:    10,
		LabelW:  66.7,
		LabelH:  25.4,
		MarginX: 4.8,
		MarginY: 12.7,
		GutterX: 3.2,
		GutterY: 0,
	}
}

// AverySpecByName resolves a request-supplied sheet-preset name; unknown/empty falls back
// to AveryL7160 (the pre-existing default, so old callers with no opinion keep working).
func AverySpecByName(name string) AverySpec {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "5160", "avery_5160", "avery5160", "us_letter":
		return Avery5160()
	default:
		return AveryL7160()
	}
}

// DefaultAvery is kept as an alias of AveryL7160 for existing callers.
func DefaultAvery() AverySpec { return AveryL7160() }

// PerPage returns how many labels fit on one sheet.
func (s AverySpec) PerPage() int { return s.Cols * s.Rows }

// Batch is a full label-print job: the resolved labels (already expanded by quantity) plus
// branding and the physical sheet/roll preset to render onto.
type Batch struct {
	Labels      []Label
	CompanyName string
	Format      LabelFormat
	Avery       AverySpec   // used when Format == FormatAveryA4; zero-value falls back to AveryL7160
	Thermal     ThermalSpec // used when Format == FormatThermalZPL; zero-value falls back to 4x2in@203dpi
	GeneratedAt time.Time
}

// Render produces the output bytes for the batch's format: an Avery sheet PDF, or ZPL/Dymo text.
func (b Batch) Render() ([]byte, error) {
	switch b.Format {
	case FormatAveryA4:
		spec := b.Avery
		if spec.Cols == 0 || spec.Rows == 0 {
			spec = DefaultAvery()
		}
		return renderAveryPDF(b.Labels, spec, b.CompanyName)
	case FormatThermalZPL:
		spec := b.Thermal
		if spec.WdIn == 0 || spec.HtIn == 0 {
			spec = ThermalSpecByName("")
		}
		return []byte(renderZPL(b.Labels, spec)), nil
	case FormatDymo:
		return []byte(renderDymo(b.Labels)), nil
	default:
		return nil, fmt.Errorf("barcode: unsupported label format %q", b.Format)
	}
}
