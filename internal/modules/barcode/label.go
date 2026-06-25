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

// Label is one printable label: a title (item name), the human/SKU line, an optional price,
// and the barcode payload (already-resolved content + symbology). Lot/serial labels carry
// the GS1 element list so the renderer can show the bracketed AI text.
type Label struct {
	Title    string // item name (top line)
	SubTitle string // SKU or secondary identifier
	Price    string // pre-formatted price string, e.g. "KES 250.00" (optional)

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

// AverySpec describes an A4 label-sheet grid (mm). Defaults model Avery L7160 (3×7, 63.5×38.1mm).
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

// DefaultAvery returns the Avery L7160 layout (21 labels per A4 sheet) used when no spec is given.
func DefaultAvery() AverySpec {
	return AverySpec{
		Name:    "Avery L7160",
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

// PerPage returns how many labels fit on one sheet.
func (s AverySpec) PerPage() int { return s.Cols * s.Rows }

// Batch is a full label-print job: the resolved labels (already expanded by quantity) plus
// branding for the sheet header.
type Batch struct {
	Labels      []Label
	CompanyName string
	Format      LabelFormat
	Avery       AverySpec
	GeneratedAt time.Time
}

// Render produces the output bytes for the batch's format: an Avery A4 PDF, or ZPL/Dymo text.
func (b Batch) Render() ([]byte, error) {
	switch b.Format {
	case FormatAveryA4:
		spec := b.Avery
		if spec.Cols == 0 || spec.Rows == 0 {
			spec = DefaultAvery()
		}
		return renderAveryPDF(b.Labels, spec, b.CompanyName)
	case FormatThermalZPL:
		return []byte(renderZPL(b.Labels)), nil
	case FormatDymo:
		return []byte(renderDymo(b.Labels)), nil
	default:
		return nil, fmt.Errorf("barcode: unsupported label format %q", b.Format)
	}
}
