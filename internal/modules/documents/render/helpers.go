package render

import "strings"

// ifEmpty returns fallback when s is blank (after trimming), else s.
func ifEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// orDash returns an em-dash for blank input.
func orDash(s string) string { return ifEmpty(s, "—") }

// maxF returns the larger of two floats.
func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// maxInt returns the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// money joins a currency prefix with a pre-formatted amount, e.g. "KES 1,234.56".
// Both parts degrade gracefully when empty.
func money(currency, amount string) string {
	return strings.TrimSpace(currencyCode(currency) + " " + strings.TrimSpace(amount))
}

// currencyCode returns the leading token of a currency string (e.g. "USD" from "USD ($)").
func currencyCode(currency string) string {
	if f := strings.Fields(currency); len(f) > 0 {
		return f[0]
	}
	return currency
}

// amountLabel returns the banner caption (explicit override or default).
func amountLabel(d *PurchaseOrderDoc) string {
	return ifEmpty(d.AmountLabel, "Total Order Value")
}

// badgeText returns the banner status badge text (explicit override, else Status).
func badgeText(d *PurchaseOrderDoc) string {
	if strings.TrimSpace(d.Badge) != "" {
		return strings.ToUpper(d.Badge)
	}
	if strings.TrimSpace(d.Status) != "" {
		return strings.ToUpper(d.Status)
	}
	return ""
}

// metaRows builds the right-hand meta box rows, skipping empty optional values.
func metaRows(d *PurchaseOrderDoc) [][2]string {
	rows := [][2]string{
		{"PO No.", orDash(d.PONumber)},
		{"Date", orDash(d.Date)},
		{"Expected", orDash(d.ExpectedDate)},
	}
	if strings.TrimSpace(d.Reference) != "" {
		rows = append(rows, [2]string{"Reference", d.Reference})
	}
	rows = append(rows, [2]string{"Currency", orDash(d.Currency)})
	return rows
}

// imgType returns the fpdf image-type string for the given raw image bytes.
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
