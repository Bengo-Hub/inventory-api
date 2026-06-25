package barcode

import (
	"bytes"
	"fmt"

	"github.com/go-pdf/fpdf"
)

// renderAveryPDF lays out labels onto A4 sheets per the given Avery grid, embedding a
// barcode PNG in each cell. It mirrors the fpdf + boombuler PNG-embed pattern used by the
// ticket PDF renderer (RegisterImageOptionsReader → ImageOptions).
func renderAveryPDF(labels []Label, spec AverySpec, company string) ([]byte, error) {
	if len(labels) == 0 {
		return nil, fmt.Errorf("barcode: no labels to render")
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(false, 0)
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	perPage := spec.PerPage()
	imgSeq := 0

	for i, lbl := range labels {
		posOnPage := i % perPage
		if posOnPage == 0 {
			pdf.AddPage()
		}
		col := posOnPage % spec.Cols
		row := posOnPage / spec.Cols

		x := spec.MarginX + float64(col)*(spec.LabelW+spec.GutterX)
		y := spec.MarginY + float64(row)*(spec.LabelH+spec.GutterY)

		drawLabelCell(pdf, tr, lbl, x, y, spec.LabelW, spec.LabelH, &imgSeq)
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("barcode: render avery pdf: %w", err)
	}
	return out.Bytes(), nil
}

// drawLabelCell renders a single label inside the rectangle (x,y,w,h).
// Layout (top→bottom): title, sub-title (SKU), barcode image, human-readable text, optional price.
func drawLabelCell(pdf *fpdf.Fpdf, tr func(string) string, lbl Label, x, y, w, h float64, imgSeq *int) {
	const pad = 2.0
	innerW := w - 2*pad
	cursorY := y + pad

	pdf.SetTextColor(20, 28, 38)

	// Title (item name) — bold, one line, truncated to width.
	if lbl.Title != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "B", 8)
		pdf.CellFormat(innerW, 3.6, tr(fitText(pdf, lbl.Title, innerW)), "", 0, "L", false, 0, "")
		cursorY += 3.8
	}

	// Sub-title (SKU) — small muted.
	if lbl.SubTitle != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "", 6.5)
		pdf.SetTextColor(110, 120, 130)
		pdf.CellFormat(innerW, 3.0, tr(fitText(pdf, lbl.SubTitle, innerW)), "", 0, "L", false, 0, "")
		pdf.SetTextColor(20, 28, 38)
		cursorY += 3.4
	}

	// Barcode image — fills the central band of the cell.
	barH := h - (cursorY - y) - pad - 5.5 // leave room for human text + price line
	if barH < 6 {
		barH = 6
	}
	if pngBytes, err := RenderPNG(lbl.Content, lbl.Symbology, 800, 240); err == nil {
		name := fmt.Sprintf("bc%d", *imgSeq)
		*imgSeq++
		opt := fpdf.ImageOptions{ImageType: "PNG"}
		pdf.RegisterImageOptionsReader(name, opt, bytes.NewReader(pngBytes))
		pdf.ImageOptions(name, x+pad, cursorY, innerW, barH, false, opt, 0, "")
		cursorY += barH + 0.5
	}

	// Human-readable barcode text (centered, monospace).
	pdf.SetXY(x+pad, cursorY)
	pdf.SetFont("Courier", "", 6)
	pdf.CellFormat(innerW, 2.8, tr(fitText(pdf, lbl.human(), innerW)), "", 0, "C", false, 0, "")
	cursorY += 3.0

	// Price (optional, bold, right-aligned).
	if lbl.Price != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "B", 8)
		pdf.CellFormat(innerW, 3.0, tr(lbl.Price), "", 0, "R", false, 0, "")
	}
}

// fitText truncates s with an ellipsis so it fits within maxW at the pdf's current font.
func fitText(pdf *fpdf.Fpdf, s string, maxW float64) string {
	if pdf.GetStringWidth(s) <= maxW {
		return s
	}
	for len(s) > 1 {
		s = s[:len(s)-1]
		if pdf.GetStringWidth(s+"...") <= maxW {
			return s + "..."
		}
	}
	return s
}
