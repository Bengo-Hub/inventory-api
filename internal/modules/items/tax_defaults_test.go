package items

import (
	"testing"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/platform/treasury"
)

func TestIndexTaxCodesPrefersTenantOwnCode(t *testing.T) {
	tenant := uuid.New()
	global := uuid.New().String()
	idx := indexTaxCodes(tenant, []treasury.TaxCode{
		{TenantID: global, Code: "VAT-16", KraCode: "B", IsDefault: true},
		{TenantID: global, Code: "D", KraCode: "D"},
		{TenantID: tenant.String(), Code: "B", KraCode: "B"},
		{TenantID: global, Code: "VAT-0", KraCode: "C"},
	})
	if got := idx.codeFor(KRABandVAT); got != "B" {
		t.Fatalf("band B: got %q, want the tenant's own code B", got)
	}
	if got := idx.codeFor(KRABandNonVAT); got != "D" {
		t.Fatalf("band D: got %q", got)
	}
	if idx.band["VAT-16"] != "B" || idx.band["VAT-0"] != "C" {
		t.Fatalf("band lookup wrong: %v", idx.band)
	}
}

func TestAutomaticTaxCode(t *testing.T) {
	idx := indexTaxCodes(uuid.New(), []treasury.TaxCode{
		{Code: "VAT-16", KraCode: "B"},
		{Code: "D", KraCode: "D"},
	})
	if got := automaticTaxCode(nil, idx, false); got != "D" {
		t.Fatalf("non-VAT tenant: got %q, want D", got)
	}
	if got := automaticTaxCode(nil, idx, true); got != "VAT-16" {
		t.Fatalf("VAT tenant: got %q, want VAT-16", got)
	}
	cfg := &ent.TenantInventoryConfig{DefaultTaxCode: "VAT-0"}
	if got := automaticTaxCode(cfg, idx, true); got != "VAT-0" {
		t.Fatalf("settings default must win: got %q", got)
	}
}

func TestStampTaxCodeSource(t *testing.T) {
	// Create with a code: a person chose it.
	d := ItemDTO{TaxCodeID: "B"}
	stampTaxCodeSource(nil, &d)
	if TaxCodeSource(d.Metadata) != TaxCodeSourceManual {
		t.Fatalf("create with code: got %v", d.Metadata)
	}
	// Create without a code: no marker (automatic).
	d = ItemDTO{}
	stampTaxCodeSource(nil, &d)
	if d.Metadata != nil {
		t.Fatalf("create without code must not add metadata: %v", d.Metadata)
	}
	// Variant child inherits an explicit auto marker.
	d = ItemDTO{TaxCodeID: "D", Metadata: map[string]any{MetaTaxCodeSource: TaxCodeSourceAuto}}
	stampTaxCodeSource(nil, &d)
	if TaxCodeSource(d.Metadata) != TaxCodeSourceAuto {
		t.Fatalf("child marker lost: %v", d.Metadata)
	}
	// Update resending the same code keeps the stored marker even if the client dropped it.
	prev := &ent.Item{TaxCodeID: "D", Metadata: map[string]any{MetaTaxCodeSource: TaxCodeSourceManual}}
	d = ItemDTO{TaxCodeID: "d", Metadata: map[string]any{"other": 1}}
	stampTaxCodeSource(prev, &d)
	if TaxCodeSource(d.Metadata) != TaxCodeSourceManual || d.Metadata["other"] != 1 {
		t.Fatalf("update same code: %v", d.Metadata)
	}
	// Update changing the code marks it manual.
	prev = &ent.Item{TaxCodeID: "D"}
	d = ItemDTO{TaxCodeID: "B", Metadata: map[string]any{}}
	stampTaxCodeSource(prev, &d)
	if TaxCodeSource(d.Metadata) != TaxCodeSourceManual {
		t.Fatalf("update changed code: %v", d.Metadata)
	}
	// A client cannot forge the marker on an unchanged code.
	prev = &ent.Item{TaxCodeID: "D"}
	d = ItemDTO{TaxCodeID: "D", Metadata: map[string]any{MetaTaxCodeSource: TaxCodeSourceManual}}
	stampTaxCodeSource(prev, &d)
	if TaxCodeSource(d.Metadata) != "" {
		t.Fatalf("forged marker kept: %v", d.Metadata)
	}
}
