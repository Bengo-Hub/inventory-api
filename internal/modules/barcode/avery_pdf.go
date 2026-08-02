package barcode

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
)

// cutDash is the dash pattern (mm) used for the "cut here" guide around each label cell —
// short dashes with a wider gap, matching the perforation-style lines printers/scissors
// guides conventionally use so it doesn't read as a solid border users might mistake for
// part of the label's own design.
var cutDash = []float64{0.8, 0.8}

// renderAveryPDF lays out labels onto A4 sheets per the given Avery grid, embedding a
// barcode PNG in each cell plus a dashed cut-guide border so users know exactly where to
// cut once the sheet is printed (real label stock is either pre-perforated or, when printed
// on plain paper, needs a visible guide — see docs/barcode-labels.md). Mirrors the fpdf +
// boombuler PNG-embed pattern used by the ticket PDF renderer.
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

		drawCutGuide(pdf, x, y, spec.LabelW, spec.LabelH)
		drawLabelCell(pdf, tr, lbl, company, x, y, spec.LabelW, spec.LabelH, &imgSeq)
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("barcode: render avery pdf: %w", err)
	}
	return out.Bytes(), nil
}

// RenderSingleLabelPDF renders ONE label as a standalone one-page PDF sized to the given
// template's real physical label dimensions, reusing the exact same drawLabelCell the bulk Avery
// sheet uses so a quick single-item print (e.g. inventory-ui's item-detail "Barcode" action)
// never diverges into its own bare-barcode-image-only rendering with no title/SKU/price — every
// barcode print in this service goes through one card layout. No cut-guide is drawn (there is
// nothing to cut around on a single label).
//
// The page is ALWAYS sized to match tmpl's real physical W×H — this used to be hardcoded to a
// fixed 50.8mm×25.4mm landscape page regardless of what paper/roll was actually loaded, which was
// the root cause of labels printing rotated: a downstream PDF viewer/driver would rotate a
// landscape-shaped page to fit a portrait-mounted roll. When tmpl.Rotate is set (the roll is
// mounted so content must read along the feed direction), the page dimensions are swapped
// (H×W instead of W×H) and drawLabelCell is invoked inside a 90° PDF transform so the card layout
// still renders correctly inside the now-portrait page — the page itself never needs manual
// Landscape/Portrait selection or scaling in the OS print dialog. See docs/barcode-labels.md.
func RenderSingleLabelPDF(lbl Label, company string, tmpl LabelTemplate) ([]byte, error) {
	w, h := tmpl.LabelWIn*25.4, tmpl.LabelHIn*25.4 // mm
	pageW, pageH := w, h
	if tmpl.Rotate {
		pageW, pageH = h, w
	}
	pdf := fpdf.NewCustom(&fpdf.InitType{UnitStr: "mm", Size: fpdf.SizeType{Wd: pageW, Ht: pageH}})
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	imgSeq := 0

	if tmpl.Rotate {
		// Center the unrotated w×h card rect on the page's center point, then rotate 90° about
		// that same point: a rectangle centered on its rotation center stays centered after any
		// 90°-multiple rotation (only its bounding box's W/H swap), so this exactly fills the
		// swapped pageW×pageH page regardless of rotation direction.
		pdf.TransformBegin()
		pdf.TransformRotate(90, pageW/2, pageH/2)
		drawLabelCell(pdf, tr, lbl, company, (pageW-w)/2, (pageH-h)/2, w, h, &imgSeq)
		pdf.TransformEnd()
	} else {
		drawLabelCell(pdf, tr, lbl, company, 0, 0, w, h, &imgSeq)
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("barcode: render single label pdf: %w", err)
	}
	return out.Bytes(), nil
}

// RenderThermalPreviewPDF renders a multi-page PDF preview matching EXACTLY what
// Batch.Render() with FormatThermalTSPL/FormatThermalZPL would print for the same labels/
// template — one page per label, each page sized (and rotated, if tmpl.Rotate) to the SAME
// physical LabelTemplate dimensions used for the real thermal job, so an operator can visually
// confirm placement/orientation BEFORE dispatching to the printer. Deliberately NOT the Avery
// grid (renderAveryPDF) — that's a different physical format (cut-sheet office paper laid out in
// columns), and silently substituting it as a "preview" for a thermal-roll job showed the
// operator a misleadingly different layout (a multi-column grid) than what would actually print.
func RenderThermalPreviewPDF(labels []Label, company string, tmpl LabelTemplate) ([]byte, error) {
	if len(labels) == 0 {
		return nil, fmt.Errorf("barcode: no labels to render")
	}
	w, h := tmpl.LabelWIn*25.4, tmpl.LabelHIn*25.4 // mm
	pageW, pageH := w, h
	if tmpl.Rotate {
		pageW, pageH = h, w
	}
	pdf := fpdf.NewCustom(&fpdf.InitType{UnitStr: "mm", Size: fpdf.SizeType{Wd: pageW, Ht: pageH}})
	pdf.SetAutoPageBreak(false, 0)
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	imgSeq := 0

	for _, lbl := range labels {
		pdf.AddPage()
		if tmpl.Rotate {
			pdf.TransformBegin()
			pdf.TransformRotate(90, pageW/2, pageH/2)
			drawLabelCell(pdf, tr, lbl, company, (pageW-w)/2, (pageH-h)/2, w, h, &imgSeq)
			pdf.TransformEnd()
		} else {
			drawLabelCell(pdf, tr, lbl, company, 0, 0, w, h, &imgSeq)
		}
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("barcode: render thermal preview pdf: %w", err)
	}
	return out.Bytes(), nil
}

// drawCutGuide draws a dashed rectangle around a label cell as a cutting guide, then
// restores a solid draw style so it never bleeds into the label content drawn after it.
func drawCutGuide(pdf *fpdf.Fpdf, x, y, w, h float64) {
	pdf.SetDrawColor(150, 150, 150)
	pdf.SetLineWidth(0.15)
	pdf.SetDashPattern(cutDash, 0)
	pdf.Rect(x, y, w, h, "D")
	pdf.SetDashPattern([]float64{}, 0) // back to solid for everything drawn after
	pdf.SetDrawColor(0, 0, 0)
}

// drawLabelCell renders a single label as ONE unified card inside the rectangle (x,y,w,h) —
// every identifying detail (tenant, title, SKU, lot/serial+expiry, barcode, human-readable
// code, price) stacks in one column of rows rather than separate disconnected blocks.
// Layout (top→bottom): tenant name (compact), title, SKU, lot/serial detail line, barcode
// image, human-readable text, optional price.
func drawLabelCell(pdf *fpdf.Fpdf, tr func(string) string, lbl Label, company string, x, y, w, h float64, imgSeq *int) {
	const pad = 2.0
	innerW := w - 2*pad
	cursorY := y + pad

	// Tenant/company name — one compact line, not a graphic banner: at label sizes this small
	// (down to 2"x1"), a colored header band would eat the space needed for the barcode's
	// quiet zone and force everything else to shrink below legibility.
	if company != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "B", 6)
		pdf.SetTextColor(90, 98, 110)
		pdf.CellFormat(innerW, 2.6, tr(fitText(pdf, strings.ToUpper(company), innerW)), "", 0, "L", false, 0, "")
		cursorY += 2.9
	}

	pdf.SetTextColor(20, 28, 38)

	// Title (item name) — bold, one line, truncated to width.
	if lbl.Title != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "B", 8)
		pdf.CellFormat(innerW, 3.6, tr(fitText(pdf, lbl.Title, innerW)), "", 0, "L", false, 0, "")
		cursorY += 3.8
	}

	// SKU — its own row (was previously crammed onto the same line as lot/expiry).
	if lbl.Sku != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "", 6.5)
		pdf.SetTextColor(110, 120, 130)
		pdf.CellFormat(innerW, 3.0, tr(fitText(pdf, lbl.Sku, innerW)), "", 0, "L", false, 0, "")
		pdf.SetTextColor(20, 28, 38)
		cursorY += 3.2
	}

	// Lot/serial + expiry detail line — its own row, right-aligned so it reads as metadata
	// distinct from the SKU line above it (mirrors a lot#/expiry line on a retail label).
	if lbl.DetailLine != "" {
		pdf.SetXY(x+pad, cursorY)
		pdf.SetFont("Helvetica", "", 6.5)
		pdf.SetTextColor(110, 120, 130)
		pdf.CellFormat(innerW, 3.0, tr(fitText(pdf, lbl.DetailLine, innerW)), "", 0, "R", false, 0, "")
		pdf.SetTextColor(20, 28, 38)
		cursorY += 3.2
	}

	// Backward-compat: a caller-supplied pre-joined SubTitle (if any code still sets it
	// directly instead of Sku/DetailLine) still renders as its own row.
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
