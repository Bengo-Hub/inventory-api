package render

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

// csvFromColumnsAndRows renders a uniform-width CSV (every row padded to len(cols) — see
// RenderTransferCSV's doc comment for why) for any column-driven table: title, meta label/value
// pairs, the item table header+rows (with SubDesc folded into the flex/description column, since
// CSV has no rich-text cells to keep it on its own visual line), and a trailing totals block.
// numbered says whether cols[0] is an auto "#" column the caller already prepended.
func csvFromColumnsAndRows(title string, metaRows [][2]string, numbered bool, cols []docColumn, rows []docRow, totals []totalRow, notes []string) ([]byte, error) {
	flexIdx := -1
	for i, c := range cols {
		if c.Width <= 0 {
			flexIdx = i
			break
		}
	}
	width := len(cols)
	if width == 0 {
		width = 1
	}
	// Every caller builds docRow.Cells positionally against its OWN column list — Cells[0] is
	// always the first real (non-"#") column; cols here already has "#" prepended when numbered,
	// so column index ci is one AHEAD of its matching Cells index. See common.go's drawDocTable
	// doc comment for the exact bug this mirrors and fixes.
	cellOffset := 0
	if numbered {
		cellOffset = 1
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	pad := func(fields ...string) []string {
		row := make([]string, width)
		copy(row, fields)
		return row
	}

	_ = w.Write(pad(title))
	for _, m := range metaRows {
		_ = w.Write(pad(m[0], m[1]))
	}
	_ = w.Write(pad())

	header := make([]string, width)
	for i, c := range cols {
		header[i] = c.Title
	}
	_ = w.Write(header)

	for i, r := range rows {
		fields := make([]string, width)
		for ci := range cols {
			switch {
			case numbered && ci == 0:
				fields[ci] = strconv.Itoa(i + 1)
			default:
				if j := ci - cellOffset; j >= 0 && j < len(r.Cells) {
					fields[ci] = r.Cells[j]
				}
			}
		}
		if flexIdx >= 0 && flexIdx < width && strings.TrimSpace(r.SubDesc) != "" {
			fields[flexIdx] = strings.TrimSpace(fields[flexIdx] + "  -  " + r.SubDesc)
		}
		_ = w.Write(fields)
	}

	if len(totals) > 0 {
		_ = w.Write(pad())
		for _, t := range totals {
			if strings.TrimSpace(t.Value) == "" {
				continue
			}
			if width < 2 {
				_ = w.Write(pad(t.Label + ": " + t.Value))
				continue
			}
			row := make([]string, width)
			row[0] = t.Label
			row[width-1] = t.Value
			_ = w.Write(row)
		}
	}

	if ns := nonEmpty(notes); len(ns) > 0 {
		_ = w.Write(pad())
		_ = w.Write(pad("Notes"))
		for _, n := range ns {
			_ = w.Write(pad(n))
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("render document csv: %w", err)
	}
	return buf.Bytes(), nil
}
