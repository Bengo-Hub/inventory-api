package tickets

import (
	"bytes"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/go-pdf/fpdf"
)

// TicketPDFInput is the data needed to render a branded event-ticket PDF (no external service deps).
type TicketPDFInput struct {
	// Branding (from documents.GetBranding — tenant logo/name/colors)
	CompanyName  string
	LogoURL      string
	PrimaryColor string // hex e.g. "#1F6FB2"; empty = default

	// Document
	TicketNumber string // sequenced, e.g. TKT-260604-00000042
	Code         string // unique redemption code (manual-entry fallback + QR target if no VerifyURL)
	VerifyURL    string // check-in deep link encoded in the QR so a phone camera opens it directly

	// Event
	EventName string
	Venue     string
	EventDate string // formatted

	// Buyer / tier
	BuyerName  string
	BuyerEmail string
	TierName   string
	Quantity   int
	Currency   string
	TotalPrice float64
}

// hexToRGB parses "#RRGGBB" to ints; returns the brand default on failure.
func hexToRGB(hex string) (int, int, int) {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) == 6 {
		if r, err := strconv.ParseInt(hex[0:2], 16, 0); err == nil {
			if g, err := strconv.ParseInt(hex[2:4], 16, 0); err == nil {
				if b, err := strconv.ParseInt(hex[4:6], 16, 0); err == nil {
					return int(r), int(g), int(b)
				}
			}
		}
	}
	return 31, 111, 178 // #1F6FB2 default
}

// qrPNG encodes the given content as a QR PNG of size px.
func qrPNG(content string, size int) ([]byte, error) {
	code, err := qr.Encode(content, qr.M, qr.Auto)
	if err != nil {
		return nil, err
	}
	scaled, err := barcode.Scale(code, size, size)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fetchLogo best-effort downloads a logo image; returns nil on any failure (PDF renders without it).
func fetchLogo(url string) ([]byte, string) {
	if url == "" {
		return nil, ""
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url) //nolint:noctx
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ""
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(&io.LimitedReader{R: resp.Body, N: 5 << 20}); err != nil {
		return nil, ""
	}
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "png"):
		return buf.Bytes(), "PNG"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return buf.Bytes(), "JPG"
	default:
		return nil, ""
	}
}

// RenderTicketPDF renders a branded A5 event ticket with an embedded QR of the ticket code.
func RenderTicketPDF(in TicketPDFInput) ([]byte, error) {
	r, g, b := hexToRGB(in.PrimaryColor)

	pdf := fpdf.New("P", "mm", "A5", "") // 148 x 210mm
	pdf.SetMargins(12, 14, 12)
	pdf.SetAutoPageBreak(true, 12)
	pdf.AddPage()
	pageW, _ := pdf.GetPageSize()
	contentW := pageW - 24

	// Header band
	pdf.SetFillColor(r, g, b)
	pdf.Rect(0, 0, pageW, 8, "F")

	if logo, lt := fetchLogo(in.LogoURL); logo != nil {
		opt := fpdf.ImageOptions{ImageType: lt, ReadDpi: true}
		pdf.RegisterImageOptionsReader("logo", opt, bytes.NewReader(logo))
		pdf.ImageOptions("logo", 12, 14, 22, 0, false, opt, 0, "")
		pdf.SetXY(38, 16)
	} else {
		pdf.SetXY(12, 16)
	}
	pdf.SetTextColor(r, g, b)
	pdf.SetFont("Helvetica", "B", 15)
	company := in.CompanyName
	if company == "" {
		company = "Event Ticket"
	}
	pdf.CellFormat(contentW-26, 8, company, "", 2, "L", false, 0, "")

	pdf.Ln(6)
	pdf.SetTextColor(34, 48, 63)
	pdf.SetFont("Helvetica", "B", 18)
	pdf.MultiCell(contentW, 8, in.EventName, "", "L", false)

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(90, 100, 110)
	if in.EventDate != "" {
		pdf.CellFormat(contentW, 6, in.EventDate, "", 2, "L", false, 0, "")
	}
	if in.Venue != "" {
		pdf.CellFormat(contentW, 6, in.Venue, "", 2, "L", false, 0, "")
	}

	pdf.Ln(4)
	pdf.SetDrawColor(r, g, b)
	pdf.SetLineWidth(0.3)
	y := pdf.GetY()
	pdf.Line(12, y, pageW-12, y)
	pdf.Ln(4)

	// QR (centered) — encode the check-in deep link when available so a phone camera opens the
	// verification page directly; otherwise the raw code (still scannable + manually enterable).
	qrContent := in.Code
	if in.VerifyURL != "" {
		qrContent = in.VerifyURL
	}
	if qrBytes, err := qrPNG(qrContent, 480); err == nil {
		pdf.RegisterImageOptionsReader("qr", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(qrBytes))
		const qrW = 55.0
		pdf.ImageOptions("qr", (pageW-qrW)/2, pdf.GetY(), qrW, qrW, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")
		pdf.SetY(pdf.GetY() + qrW + 2)
	}
	pdf.SetFont("Courier", "B", 14)
	pdf.SetTextColor(34, 48, 63)
	pdf.CellFormat(contentW, 8, in.Code, "", 1, "C", false, 0, "")
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(120, 130, 140)
	pdf.CellFormat(contentW, 5, "Present this code at the entrance", "", 1, "C", false, 0, "")

	pdf.Ln(4)
	// Details rows
	row := func(label, value string) {
		if value == "" {
			return
		}
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetTextColor(120, 130, 140)
		pdf.CellFormat(contentW*0.4, 7, label, "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "B", 10)
		pdf.SetTextColor(34, 48, 63)
		pdf.CellFormat(contentW*0.6, 7, value, "", 1, "R", false, 0, "")
	}
	row("Ticket No.", in.TicketNumber)
	if in.TierName != "" {
		row("Type", in.TierName)
	}
	row("Quantity", strconv.Itoa(in.Quantity))
	row("Holder", in.BuyerName)
	if in.TotalPrice > 0 {
		cur := in.Currency
		if cur == "" {
			cur = "KES"
		}
		row("Paid", fmt.Sprintf("%s %0.2f", cur, in.TotalPrice))
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("render ticket pdf: %w", err)
	}
	return out.Bytes(), nil
}
