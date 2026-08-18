package render

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

// RenderTransferCSV flattens a TransferDoc into a plain CSV — the lightweight sibling of
// RenderTransferXLSX for callers that just want the item data (spreadsheet import, quick diff)
// without the styled workbook. Every row (title, metadata, item table, totals, notes) is padded
// to the SAME column count as the item table header: a CSV mixing row widths reads fine in Excel
// but breaks strict readers (Go's encoding/csv included, with its default FieldsPerRecord check),
// so this keeps a single uniform table shape throughout instead of the "flattened memo" style
// modules/docs' report CSV uses (that one is fine be looser about it — it's read by people opening
// it in Excel, never re-ingested — but a document export should hold up under either use).
func RenderTransferCSV(d *TransferDoc) ([]byte, error) {
	showReceived := false
	for _, it := range d.Items {
		if it.ReceivedQty != "" {
			showReceived = true
			break
		}
	}
	header := []string{"#", "Description", "SKU", "Unit", "Shipped"}
	if showReceived {
		header = append(header, "Received", "Variance", "Notes")
	}
	width := len(header)

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	pad := func(fields ...string) []string {
		row := make([]string, width)
		copy(row, fields)
		return row
	}

	_ = w.Write(pad(strings.ToUpper(ifEmpty(d.DocTitle, "STOCK TRANSFER"))))
	if d.DocSubtitle != "" {
		_ = w.Write(pad(d.DocSubtitle))
	}
	_ = w.Write(pad("Transfer No.", d.TransferNumber))
	_ = w.Write(pad("Date", d.Date))
	if d.Reference != "" {
		_ = w.Write(pad("Reference", d.Reference))
	}
	if d.Carrier != "" {
		_ = w.Write(pad("Carrier", d.Carrier))
	}
	_ = w.Write(pad("From Warehouse", d.FromWarehouseName))
	_ = w.Write(pad("To Warehouse", d.ToWarehouseName))
	_ = w.Write(pad())

	_ = w.Write(header)

	var sumShipped, sumReceived, sumVariance float64
	for i, it := range d.Items {
		qty, _ := strconv.ParseFloat(strings.TrimSpace(it.Qty), 64)
		sumShipped += qty
		fields := []string{strconv.Itoa(i + 1), it.Desc, it.SubDesc, it.Unit, it.Qty}
		if showReceived {
			receivedText := ifEmpty(it.ReceivedQty, it.Qty)
			fields = append(fields, receivedText, varianceQtyText(it.Qty, receivedText), it.VarianceReason)
			if received, err := strconv.ParseFloat(strings.TrimSpace(receivedText), 64); err == nil {
				sumReceived += received
				sumVariance += received - qty
			}
		}
		_ = w.Write(pad(fields...))
	}

	totals := []string{"", "TOTALS", "", "", formatQtyDelta(sumShipped)}
	if showReceived {
		totals = append(totals, formatQtyDelta(sumReceived), formatQtyDelta(sumVariance))
	}
	_ = w.Write(pad(totals...))

	if notes := nonEmpty(d.Notes); len(notes) > 0 {
		_ = w.Write(pad())
		_ = w.Write(pad("Notes"))
		for _, n := range notes {
			_ = w.Write(pad(n))
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("render transfer csv: %w", err)
	}
	return buf.Bytes(), nil
}
