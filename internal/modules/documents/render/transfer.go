package render

// Stock-transfer document rendering (Dispatch/Transit Note + Goods-Received Note). Reuses the
// SAME low-level painter/palette primitives, tenant-branding footer, and page geometry as the
// purchase-order renderer (render.go/primitives.go/palette.go/footer.go's footerMeta helper) —
// but with its own layout functions, since a transfer has no financial totals and different
// party/sign-off semantics (mirrors erp-api's own precedent of giving a structurally different
// document its own drawing functions rather than retrofitting the PO-specific ones).

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
)

// RenderTransfer builds a premium, tenant-branded A4 stock-transfer document (Dispatch/Transit
// Note or Goods-Received Note, depending on what the caller populated on doc) and returns PDF
// bytes.
func RenderTransfer(doc *TransferDoc, logo []byte, logoType string) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(margin, 12, margin)
	pdf.SetAutoPageBreak(true, 10)
	pdf.AddPage()

	p := newPainter(pdf, newPalette(doc.Branding.PrimaryColor))

	y := p.drawTransferHeader(doc, logo, logoType)
	y = p.drawTransferMetaAndCompany(doc, y)
	y = p.drawTransferParties(doc, y+5.0)
	if strings.TrimSpace(doc.AcknowledgementText) != "" {
		y = p.drawTransferAck(doc.AcknowledgementText, y+4.0)
	}
	y = p.drawTransferItems(doc, y+4.0)
	y = p.drawTransferLowerBlocks(doc, y+5.0)
	pdf.SetAutoPageBreak(false, 0)
	y = p.drawTransferSignatures(doc, y+10.0)
	p.drawTransferFooter(doc, y+6.0)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render transfer doc pdf: %w", err)
	}
	return buf.Bytes(), nil
}

func (p *painter) drawTransferHeader(d *TransferDoc, logo []byte, logoType string) float64 {
	topY := 12.0
	logoH := 15.0
	if len(logo) > 0 {
		if logoType == "" {
			logoType = imgType(logo)
		}
		info := p.pdf.RegisterImageOptionsReader("logo",
			fpdf.ImageOptions{ImageType: logoType}, bytes.NewReader(logo))
		if info != nil && info.Height() > 0 {
			logoW := logoH * info.Width() / info.Height()
			p.pdf.ImageOptions("logo", leftX, topY, logoW, logoH, false,
				fpdf.ImageOptions{ImageType: logoType}, 0, "")
		}
	}

	p.textR(rightX, topY+0.5, strings.ToUpper(ifEmpty(d.DocTitle, "STOCK TRANSFER")), "B", 22, p.pal.navy)
	if d.DocSubtitle != "" {
		p.textR(rightX, topY+11.0, strings.ToUpper(d.DocSubtitle), "", 7.5, p.pal.blue)
	}

	ruleY := topY + logoH + 2.0
	p.gradient(leftX, ruleY, contentW, 1.4, 0.7, p.pal.sky, p.pal.navy)
	return ruleY
}

func (p *painter) drawTransferMetaAndCompany(d *TransferDoc, ruleY float64) float64 {
	y := ruleY + 6.0
	b := d.Branding

	p.text(leftX, y, ifEmpty(b.CompanyName, "—"), "B", 15, p.pal.navy)
	if b.Tagline != "" {
		p.text(leftX, y+5.6, b.Tagline, "I", 9, p.pal.blue)
	}
	ly := y + 10.5
	for _, ln := range companyAddressLines(b) {
		p.text(leftX, ly, ln, "", 9, p.pal.grey)
		ly += 4.0
	}
	if b.KRAPIN != "" {
		p.text(leftX, ly, "KRA PIN: "+b.KRAPIN, "", 9, p.pal.grey)
		ly += 4.0
	}

	rows := [][2]string{
		{"Transfer No.", orDash(d.TransferNumber)},
		{"Date", orDash(d.Date)},
	}
	if strings.TrimSpace(d.Reference) != "" {
		rows = append(rows, [2]string{"Reference", d.Reference})
	}
	if strings.TrimSpace(d.Carrier) != "" {
		rows = append(rows, [2]string{"Carrier", d.Carrier})
	}
	mbX := 117.0
	mbW := rightX - mbX
	rowH := 6.4
	mbH := rowH * float64(len(rows))
	keyW := mbW * 0.46
	for i, r := range rows {
		ry := y + float64(i)*rowH
		p.setFill(p.pal.lightBlue)
		p.pdf.Rect(mbX, ry, keyW, rowH, "F")
		if i > 0 {
			p.hline(mbX, ry, mbX+mbW)
		}
		p.text(mbX+2.5, ry+2.1, strings.ToUpper(r[0]), "B", 7.5, p.pal.blue)
		p.textFit(mbX+keyW+2.5, ry+2.1, mbW-keyW-4.5, r[1], "B", 8.0, 5.5, p.pal.navy)
	}
	p.box(mbX, y, mbW, mbH)

	return maxF(y+mbH, ly)
}

// drawTransferParties renders the source (left) and destination (right) warehouse cards —
// the transfer analog of drawParties' SUPPLIER/DELIVER TO layout.
func (p *painter) drawTransferParties(d *TransferDoc, py float64) float64 {
	bw := (contentW - 6.0) / 2
	bx2 := leftX + bw + 6.0
	innerW := bw - 6.0
	const nameH, lineH = 4.4, 4.0

	leftH := float64(p.measureLines(orDash(d.FromWarehouseName), "B", 10.5, innerW)) * nameH
	for _, ln := range d.FromWarehouseAddr {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		leftH += float64(p.measureLines(ln, "", 9, innerW)) * lineH
	}
	rightH := float64(p.measureLines(orDash(d.ToWarehouseName), "B", 10.5, innerW)) * nameH
	for _, ln := range d.ToWarehouseAddr {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		rightH += float64(p.measureLines(ln, "", 9, innerW)) * lineH
	}
	boxH := maxF(maxF(leftH, rightH)+9.0, 22.0)

	p.box(leftX, py, bw, boxH)
	p.text(leftX+3, py+3.0, "FROM WAREHOUSE", "B", 8, p.pal.blue)
	cy := p.multiCell(leftX+3, py+5.6, innerW, nameH, orDash(d.FromWarehouseName), "B", 10.5, p.pal.navy) + 0.6
	for _, ln := range d.FromWarehouseAddr {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		cy = p.multiCell(leftX+3, cy, innerW, lineH, ln, "", 9, p.pal.grey)
	}

	p.box(bx2, py, bw, boxH)
	p.text(bx2+3, py+3.0, "TO WAREHOUSE", "B", 8, p.pal.blue)
	ry := p.multiCell(bx2+3, py+5.6, innerW, nameH, orDash(d.ToWarehouseName), "B", 10.5, p.pal.navy) + 0.6
	for _, ln := range d.ToWarehouseAddr {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		ry = p.multiCell(bx2+3, ry, innerW, lineH, ln, "", 9, p.pal.grey)
	}

	return py + boxH
}

// drawTransferAck renders a bold acknowledgement lead line above the item table (mirrors
// treasury's delivery_challan profile's acknowledgement text).
func (p *painter) drawTransferAck(text string, y float64) float64 {
	return p.multiCell(leftX, y, contentW, 4.2, text, "B", 9.5, p.pal.navy) + 2.0
}

// drawTransferItems renders the item table. Gains RECEIVED + VARIANCE columns only when at
// least one line carries a ReceivedQty (i.e. this is a Goods-Received Note render) — the
// dispatch note keeps the simpler # / Description / Unit / Qty layout.
func (p *painter) drawTransferItems(d *TransferDoc, ty float64) float64 {
	showReceived := false
	for _, it := range d.Items {
		if it.ReceivedQty != "" {
			showReceived = true
			break
		}
	}

	colNum := 9.0
	colUnit := 16.0
	colQty := 22.0
	colRecv := 22.0
	colVar := 34.0
	if !showReceived {
		colRecv, colVar = 0, 0
	}
	colDesc := contentW - colNum - colUnit - colQty - colRecv - colVar

	xNum := leftX
	xDesc := xNum + colNum
	xUnitR := xDesc + colDesc + colUnit
	xQtyR := xUnitR + colQty
	xRecvR := xQtyR + colRecv
	xVarR := rightX

	p.setFill(p.pal.navy)
	p.pdf.Rect(leftX, ty, contentW, 8.0, "F")
	p.text(xNum+2, ty+2.7, "#", "B", 8, p.pal.white)
	p.text(xDesc+1, ty+2.7, "DESCRIPTION", "B", 8, p.pal.white)
	p.textR(xUnitR-1, ty+2.7, "UNIT", "B", 8, p.pal.white)
	p.textR(xQtyR-1, ty+2.7, "QTY", "B", 8, p.pal.white)
	if showReceived {
		p.textR(xRecvR-1, ty+2.7, "RECEIVED", "B", 8, p.pal.white)
		p.textR(xVarR-2, ty+2.7, "VARIANCE", "B", 8, p.pal.white)
	}

	ry := ty + 8.0
	for i, it := range d.Items {
		rowTop := ry + 2.5
		endY := p.multiCell(xDesc+1, rowTop, colDesc-2, 4.0, it.Desc, "B", 9.3, p.pal.navy)
		if it.SubDesc != "" {
			endY = p.multiCell(xDesc+1, endY+0.4, colDesc-2, 4.0, it.SubDesc, "", 8.6, p.pal.muted)
		}
		p.text(xNum+2, rowTop, fmt.Sprintf("%d", i+1), "", 9.3, p.pal.ink)
		p.textR(xUnitR-1, rowTop, it.Unit, "", 9.3, p.pal.ink)
		p.textR(xQtyR-1, rowTop, it.Qty, "", 9.3, p.pal.ink)
		if showReceived {
			p.textR(xRecvR-1, rowTop, ifEmpty(it.ReceivedQty, it.Qty), "", 9.3, p.pal.ink)
			p.textR(xVarR-2, rowTop, it.VarianceReason, "", 8.6, p.pal.muted)
		}
		ry = endY + 2.0
		p.hline(leftX, ry, rightX)
	}
	return ry
}

// drawTransferLowerBlocks mirrors drawLowerBlocks' Notes & Terms card (freight notes / general
// notes), only rendered when there's content.
func (p *painter) drawTransferLowerBlocks(d *TransferDoc, lY float64) float64 {
	notes := nonEmpty(d.Notes)
	if len(notes) == 0 {
		return lY
	}
	innerW := contentW - 6.0
	const lineH = 3.4
	lines := 0
	for _, n := range notes {
		lines += p.measureLines("-  "+n, "", 7.3, innerW)
	}
	lowH := maxF(float64(lines)*lineH+8.0, 14.0)
	p.box(leftX, lY, contentW, lowH)
	p.text(leftX+3, lY+2.6, "NOTES", "B", 6.8, p.pal.blue)
	y := lY + 6.0
	for _, n := range notes {
		y = p.multiCell(leftX+3, y, innerW, lineH, "-  "+n, "", 7.3, p.pal.grey)
	}
	return lY + lowH
}

// drawTransferSignatures reuses drawSig directly (it's already generic — role/name only) with
// the caller-supplied labels, since "Dispatched By"/"Received By" (transit note) and "Delivered
// By"/"Received By" (GRN) both differ from the PO renderer's "Prepared By"/"Approved By".
func (p *painter) drawTransferSignatures(d *TransferDoc, sY float64) float64 {
	sigW := 80.0
	p.drawSig(leftX, sY, sigW, ifEmpty(d.LeftSigLabel, "Dispatched By"), d.LeftSigName)
	p.drawSig(rightX-sigW, sY, sigW, ifEmpty(d.RightSigLabel, "Received By"), d.RightSigName)
	return sY
}

func (p *painter) drawTransferFooter(d *TransferDoc, fY float64) {
	if fY < 282.0 {
		fY = 282.0
	}
	p.setDraw(p.pal.line)
	p.pdf.SetLineWidth(0.2)
	p.pdf.Line(leftX, fY, rightX, fY)

	name := ifEmpty(d.Branding.CompanyName, "the issuer")
	p.pdf.SetFont("Helvetica", "", 6.6)
	p.setText(p.pal.muted)
	p.pdf.SetXY(leftX, fY+1.6)
	p.pdf.MultiCell(contentW, 3.2,
		p.tr("This document records an internal stock movement between "+name+"'s own warehouses — it has no monetary value."),
		"", "C", false)
	if meta := footerMeta(d.Branding); meta != "" {
		p.pdf.SetX(leftX)
		p.pdf.MultiCell(contentW, 3.2, p.tr(meta), "", "C", false)
	}
	if d.Branding.ProviderFooterEnabled {
		p.pdf.SetX(leftX)
		p.pdf.SetFont("Helvetica", "B", 6.4)
		p.pdf.MultiCell(contentW, 3.0, p.tr(providerFooterLead), "", "C", false)
		p.pdf.SetX(leftX)
		p.pdf.SetFont("Helvetica", "", 6.0)
		p.pdf.MultiCell(contentW, 3.0, p.tr(providerFooterContact), "", "C", false)
	}
}
