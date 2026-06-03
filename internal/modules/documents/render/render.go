package render

import (
	"bytes"
	"fmt"

	"github.com/go-pdf/fpdf"
)

// Render builds a premium, tenant-branded A4 purchase-order PDF for the given
// document. logo holds the (already-fetched) tenant logo bytes; logoType is the
// fpdf image type ("PNG"/"JPG"/"GIF"). Both may be empty — the logo is then
// omitted. The whole visual identity is driven by Branding.PrimaryColor.
func Render(doc *PurchaseOrderDoc, logo []byte, logoType string) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(margin, 12, margin)
	pdf.SetAutoPageBreak(true, 14)
	pdf.AddPage()

	p := newPainter(pdf, newPalette(doc.Branding.PrimaryColor))

	y := p.drawHeader(doc, logo, logoType)
	y = p.drawMetaAndCompany(doc, y)
	y = p.drawParties(doc, y+6.0)
	y = p.drawBanner(doc, y+6.0)
	y = p.drawItems(doc, y+6.0)
	y = p.drawTotals(doc, y+4.0)
	y = p.drawLowerBlocks(doc, y+6.0)
	y = p.drawSignatures(doc, y+16.0)
	p.drawFooter(doc, y+10.0)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render PO pdf: %w", err)
	}
	return buf.Bytes(), nil
}
