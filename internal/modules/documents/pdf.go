package documents

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// rgb is a small color helper.
type rgb struct{ r, g, b int }

// House palette (adapted from finance-service/go-pdf-generation-sample).
var (
	colSky    = rgb{61, 155, 214}
	colBlue   = rgb{31, 111, 178}
	colNavy   = rgb{20, 58, 115}
	colInk    = rgb{34, 48, 63}
	colGrey   = rgb{82, 97, 122}
	colMuted  = rgb{107, 122, 144}
	colLine   = rgb{214, 227, 240}
	colLightB = rgb{238, 245, 251}
	colWhite  = rgb{255, 255, 255}
)

const (
	pageW   = 210.0
	margin  = 13.0
	leftX   = margin
	rightX  = pageW - margin
	centerW = pageW - 2*margin
)

// fetchLogoBytes downloads a logo from the (trusted auth-api) URL. Graceful: returns
// nil on any failure so the document still renders without a logo.
func fetchLogoBytes(url string) ([]byte, string) {
	if strings.TrimSpace(url) == "" {
		return nil, ""
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil || len(b) == 0 {
		return nil, ""
	}
	return b, imgType(b)
}

func imgType(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "JPG"
	case len(b) >= 4 && b[0] == 0x47 && b[1] == 0x49 && b[2] == 0x46:
		return "GIF"
	default:
		return "PNG"
	}
}

func hexToRGB(hex string, fallback rgb) rgb {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return fallback
	}
	r, e1 := strconv.ParseInt(hex[0:2], 16, 0)
	g, e2 := strconv.ParseInt(hex[2:4], 16, 0)
	b, e3 := strconv.ParseInt(hex[4:6], 16, 0)
	if e1 != nil || e2 != nil || e3 != nil {
		return fallback
	}
	return rgb{int(r), int(g), int(b)}
}

// RenderPurchaseOrderPDF renders a branded A4 purchase order and returns PDF bytes.
func RenderPurchaseOrderPDF(d PurchaseOrderDoc) ([]byte, error) {
	brand := hexToRGB(d.Branding.PrimaryColor, colNavy)

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(margin, 12, margin)
	pdf.SetAutoPageBreak(true, 12)
	pdf.AddPage()
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	setFill := func(c rgb) { pdf.SetFillColor(c.r, c.g, c.b) }
	setText := func(c rgb) { pdf.SetTextColor(c.r, c.g, c.b) }
	setDraw := func(c rgb) { pdf.SetDrawColor(c.r, c.g, c.b) }
	gradient := func(x, y, w, h, r float64, c1, c2 rgb) {
		pdf.ClipRoundedRect(x, y, w, h, r, false)
		pdf.LinearGradient(x, y, w, h, c1.r, c1.g, c1.b, c2.r, c2.g, c2.b, 0, 0, 1, 0)
		pdf.ClipEnd()
	}
	box := func(x, y, w, h float64) {
		setDraw(colLine)
		setFill(colWhite)
		pdf.SetLineWidth(0.2)
		pdf.RoundedRect(x, y, w, h, 1.6, "1234", "D")
	}
	text := func(x, y float64, s, font string, sz float64, c rgb) {
		pdf.SetFont("Helvetica", font, sz)
		setText(c)
		pdf.Text(x, y+sz*0.3528*0.82, tr(s))
	}
	textR := func(rx, y float64, s, font string, sz float64, c rgb) {
		pdf.SetFont("Helvetica", font, sz)
		setText(c)
		t := tr(s)
		pdf.Text(rx-pdf.GetStringWidth(t), y+sz*0.3528*0.82, t)
	}

	// HEADER — logo (optional) + title
	topY := 12.0
	logoH := 15.0
	if raw, typ := fetchLogoBytes(d.Branding.LogoURL); raw != nil {
		info := pdf.RegisterImageOptionsReader("logo", fpdf.ImageOptions{ImageType: typ}, bytes.NewReader(raw))
		if info != nil && info.Height() > 0 {
			logoW := logoH * info.Width() / info.Height()
			pdf.ImageOptions("logo", leftX, topY, logoW, logoH, false, fpdf.ImageOptions{ImageType: typ}, 0, "")
		}
	}
	textR(rightX, topY+0.5, "PURCHASE ORDER", "B", 24, brand)
	if d.Status != "" {
		textR(rightX, topY+10.5, strings.ToUpper(d.Status), "", 7.5, colBlue)
	}
	ruleY := topY + logoH + 2.0
	gradient(leftX, ruleY, centerW, 1.4, 0.7, colSky, brand)

	// COMPANY + META STRIP
	y := ruleY + 6.0
	text(leftX, y, orDash(d.Branding.CompanyName), "B", 15, brand)
	if d.Branding.Tagline != "" {
		text(leftX, y+5.6, d.Branding.Tagline, "I", 9, colBlue)
	}
	ly := y + 10.5
	for _, ln := range d.Branding.Address {
		text(leftX, ly, ln, "", 9, colGrey)
		ly += 4.0
	}
	if d.Branding.KRAPIN != "" {
		text(leftX, ly, "KRA PIN: "+d.Branding.KRAPIN, "", 9, colGrey)
		ly += 4.0
	}

	mbX := 117.0
	mbW := rightX - mbX
	rows := [][2]string{
		{"PO No.", d.PONumber},
		{"Date", d.Date},
		{"Expected", orDash(d.ExpectedDate)},
		{"Currency", orDash(d.Currency)},
	}
	rowH := 6.4
	keyW := mbW * 0.46
	for i, rrow := range rows {
		ry := y + float64(i)*rowH
		setFill(colLightB)
		pdf.Rect(mbX, ry, keyW, rowH, "F")
		if i > 0 {
			setDraw(colLine)
			pdf.SetLineWidth(0.15)
			pdf.Line(mbX, ry, mbX+mbW, ry)
		}
		text(mbX+2.5, ry+2.1, strings.ToUpper(rrow[0]), "B", 7.5, colBlue)
		text(mbX+keyW+2.5, ry+2.1, rrow[1], "B", 8.0, brand)
	}
	box(mbX, y, mbW, rowH*float64(len(rows)))

	// PARTIES — supplier + warehouse
	py := maxF(ly+2.0, y+rowH*float64(len(rows))+2.0) + 4.0
	boxH := 26.0
	bw := (centerW - 6.0) / 2
	box(leftX, py, bw, boxH)
	text(leftX+3, py+3.0, "SUPPLIER", "B", 8, colBlue)
	pdf.SetFont("Helvetica", "B", 10.5)
	setText(brand)
	pdf.SetXY(leftX+3, py+6.5)
	pdf.MultiCell(bw-6, 4.0, tr(orDash(d.SupplierName)), "", "L", false)
	cny := pdf.GetY() + 1.0
	for _, ln := range d.SupplierAddr {
		text(leftX+3, cny, ln, "", 9, colGrey)
		cny += 4.0
	}
	bx2 := leftX + bw + 6.0
	box(bx2, py, bw, boxH)
	text(bx2+3, py+3.0, "DELIVER TO", "B", 8, colBlue)
	text(bx2+3, py+7.5, orDash(d.WarehouseName), "B", 10, brand)

	// BANNER — total
	by := py + boxH + 6.0
	banH := 18.0
	gradient(leftX, by, centerW, banH, 2.0, colSky, brand)
	text(leftX+8, by+4.5, "TOTAL ORDER VALUE", "", 9, rgb{214, 236, 251})
	text(leftX+8, by+8.5, strings.TrimSpace(d.Currency+" "+d.Grand), "B", 22, colWhite)

	// TABLE
	ty := by + banH + 6.0
	colNum := 9.0
	colAmt := 30.0
	colRate := 28.0
	colQty := 14.0
	colUnit := 16.0
	colDesc := centerW - colNum - colAmt - colRate - colQty - colUnit
	xNum := leftX
	xDesc := xNum + colNum
	xUnitR := xDesc + colDesc + colUnit
	xQtyR := xUnitR + colQty
	xRateR := xQtyR + colRate
	xAmtR := rightX

	setFill(brand)
	pdf.Rect(leftX, ty, centerW, 8.0, "F")
	text(xNum+2, ty+2.7, "#", "B", 8, colWhite)
	text(xDesc+1, ty+2.7, "DESCRIPTION", "B", 8, colWhite)
	textR(xUnitR-1, ty+2.7, "UNIT", "B", 8, colWhite)
	textR(xQtyR-1, ty+2.7, "QTY", "B", 8, colWhite)
	textR(xRateR-1, ty+2.7, "RATE", "B", 8, colWhite)
	textR(xAmtR-2, ty+2.7, "AMOUNT", "B", 8, colWhite)

	ry := ty + 8.0
	for i, it := range d.Items {
		rowTop := ry + 2.5
		text(xDesc+1, rowTop, it.Desc, "B", 9.5, brand)
		endY := rowTop
		if it.SubDesc != "" {
			pdf.SetFont("Helvetica", "", 9)
			setText(colMuted)
			pdf.SetXY(xDesc+1, rowTop+4.2)
			pdf.MultiCell(colDesc-2, 4.0, tr(it.SubDesc), "", "L", false)
			endY = pdf.GetY()
		} else {
			endY = rowTop + 3.0
		}
		text(xNum+2, rowTop, fmt.Sprintf("%d", i+1), "", 9.5, colInk)
		textR(xUnitR-1, rowTop, it.Unit, "", 9.6, colInk)
		textR(xQtyR-1, rowTop, it.Qty, "", 9.6, colInk)
		textR(xRateR-1, rowTop, it.Rate, "", 9.6, colInk)
		textR(xAmtR-2, rowTop, it.Amount, "", 9.6, colInk)
		ry = endY + 2.0
		setDraw(colLine)
		pdf.SetLineWidth(0.15)
		pdf.Line(leftX, ry, rightX, ry)
	}

	// TOTALS
	tlW := 92.0
	tlX := rightX - tlW
	tY := ry + 4.0
	setFill(colLightB)
	pdf.RoundedRect(tlX, tY, tlW, 7.5, 1.2, "1234", "F")
	text(tlX+4, tY+2.6, "Subtotal", "", 10, colGrey)
	textR(rightX-4, tY+2.6, strings.TrimSpace(d.Currency+" "+d.Subtotal), "B", 10, brand)
	t2 := tY + 8.3
	if d.TaxAmount != "" {
		text(tlX+4, t2+1.0, orDash(d.TaxLabel), "", 10, colGrey)
		textR(rightX-4, t2+1.0, strings.TrimSpace(d.Currency+" "+d.TaxAmount), "B", 10, brand)
		t2 += 6.0
	}
	gtY := t2 + 2.0
	gradient(tlX, gtY, tlW, 9.0, 1.4, colBlue, brand)
	text(tlX+4, gtY+3.2, "Grand Total", "B", 12, colWhite)
	textR(rightX-4, gtY+3.2, strings.TrimSpace(d.Currency+" "+d.Grand), "B", 12.5, colWhite)

	// NOTES
	lY := gtY + 9.0 + 6.0
	if len(d.Notes) > 0 {
		lowH := 26.0
		box(leftX, lY, centerW, lowH)
		text(leftX+3, lY+3.0, "NOTES & TERMS", "B", 8, colBlue)
		ny := lY + 7.5
		for _, n := range d.Notes {
			pdf.SetFont("Helvetica", "", 8.6)
			setText(colGrey)
			pdf.SetXY(leftX+3, ny)
			pdf.MultiCell(centerW-6, 3.8, tr("•  "+n), "", "L", false)
			ny = pdf.GetY() + 1.0
		}
		lY += lowH
	}

	// SIGNATURES
	sY := lY + 16.0
	sigW := 80.0
	drawSig := func(x float64, role, name string) {
		setDraw(brand)
		pdf.SetLineWidth(0.5)
		pdf.Line(x, sY, x+sigW, sY)
		text(x, sY+1.5, role+" — "+orDash(name), "", 9, colGrey)
	}
	drawSig(leftX, "Prepared By", d.PreparedBy)
	drawSig(rightX-sigW, "Approved By", d.ApprovedBy)

	// FOOTER
	setDraw(colLine)
	pdf.SetLineWidth(0.15)
	pdf.Line(leftX, 282, rightX, 282)
	pdf.SetFont("Helvetica", "", 7.5)
	setText(colMuted)
	pdf.SetXY(leftX, 284)
	footer := d.Branding.CompanyName
	if len(d.Branding.Address) > 0 {
		footer += " | " + strings.Join(d.Branding.Address, ", ")
	}
	pdf.MultiCell(centerW, 3.4, tr(footer), "", "C", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render PO pdf: %w", err)
	}
	return buf.Bytes(), nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
