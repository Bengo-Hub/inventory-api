package documents

import (
	"testing"
	"time"

	"github.com/bengobox/inventory-service/internal/ent"
)

var testNow = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

// Numeric-by-default: empty prefix + empty date_format ⇒ just the zero-padded counter.
func TestFormatNumber_NumericDefault(t *testing.T) {
	row := &ent.DocumentSequence{Separator: "-", Padding: 6}
	cases := map[int64]string{1: "000001", 42: "000042", 123456: "123456"}
	for val, want := range cases {
		if got := formatNumber(row, val, testNow); got != want {
			t.Errorf("formatNumber(numeric, %d) = %q, want %q", val, got, want)
		}
	}
}

// Prefixed/dated format when a tenant opts in.
func TestFormatNumber_Prefixed(t *testing.T) {
	row := &ent.DocumentSequence{Prefix: "PO", Separator: "-", DateFormat: "YYMMDD", Padding: 6}
	if got := formatNumber(row, 1, testNow); got != "PO-260102-000001" {
		t.Errorf("formatNumber(prefixed) = %q, want %q", got, "PO-260102-000001")
	}
}

// The platform default for every inventory doc type must be pure numeric (no prefix, no date).
func TestSeqDefaults_AreNumeric(t *testing.T) {
	if len(seqDefaults) == 0 {
		t.Fatal("seqDefaults is empty")
	}
	for docType, cfg := range seqDefaults {
		if cfg.Prefix != "" {
			t.Errorf("seqDefaults[%s].Prefix = %q, want empty (numeric default)", docType, cfg.Prefix)
		}
		if cfg.DateFormat != "" {
			t.Errorf("seqDefaults[%s].DateFormat = %q, want empty (numeric default)", docType, cfg.DateFormat)
		}
	}
}
