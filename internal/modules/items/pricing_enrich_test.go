package items

import "testing"

// preferredTaxCode: an item's own tax code must win over the tenant default — a zero-rated/
// exempt item must never resolve against the tenant's default (usually standard VAT) code.
func TestPreferredTaxCode(t *testing.T) {
	tests := []struct {
		name           string
		item           *ItemDTO
		defaultTaxCode string
		want           string
	}{
		{
			name:           "item's own tax code wins over tenant default",
			item:           &ItemDTO{TaxCodeID: "VAT-0"},
			defaultTaxCode: "VAT-16",
			want:           "VAT-0",
		},
		{
			name:           "falls back to tenant default when item has no tax code",
			item:           &ItemDTO{TaxCodeID: ""},
			defaultTaxCode: "VAT-16",
			want:           "VAT-16",
		},
		{
			name:           "empty when neither item nor tenant has a code",
			item:           &ItemDTO{TaxCodeID: ""},
			defaultTaxCode: "",
			want:           "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := preferredTaxCode(tt.item, tt.defaultTaxCode); got != tt.want {
				t.Errorf("preferredTaxCode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// applyItemTax: the previously-outer resolveVATRate stamp, now applied per item. Confirms the
// per-item rate/code actually lands on the DTO, VAT suppression still zeroes tax, and an item
// that already carries its own TaxCodeID is never overwritten by the resolved rateCode.
func TestApplyItemTax(t *testing.T) {
	t.Run("stamps resolved rate/net/tax for an exclusive-priced item", func(t *testing.T) {
		d := &ItemDTO{}
		applyItemTax(d, 100, 16, "VAT-16", "VAT-16", false, false)
		if d.TaxRate == nil || *d.TaxRate != 16 {
			t.Fatalf("TaxRate = %v, want 16", d.TaxRate)
		}
		if d.TaxCodeID != "VAT-16" {
			t.Errorf("TaxCodeID = %q, want VAT-16 (filled from rateCode since item had none)", d.TaxCodeID)
		}
		if d.NetPrice == nil || d.TaxAmount == nil {
			t.Fatal("NetPrice/TaxAmount should be set for a taxable item")
		}
	})

	t.Run("never overwrites an item's own already-set tax code", func(t *testing.T) {
		d := &ItemDTO{TaxCodeID: "VAT-0"}
		applyItemTax(d, 100, 0, "VAT-0", "VAT-16", false, false)
		if d.TaxCodeID != "VAT-0" {
			t.Errorf("TaxCodeID = %q, want VAT-0 (must not be replaced)", d.TaxCodeID)
		}
		if d.TaxRate != nil {
			t.Errorf("TaxRate = %v, want nil (zero-rated item, no VAT to split out)", d.TaxRate)
		}
	})

	t.Run("VAT suppression zeroes tax even when a rate resolved", func(t *testing.T) {
		d := &ItemDTO{}
		applyItemTax(d, 100, 16, "VAT-16", "VAT-16", false, true)
		if d.TaxRate != nil {
			t.Errorf("TaxRate = %v, want nil (VAT suppressed for a non-VAT-eligible tenant)", d.TaxRate)
		}
	})

	t.Run("inclusive pricing backfills DefaultVATRate when treasury unreachable", func(t *testing.T) {
		d := &ItemDTO{TaxInclusive: true}
		applyItemTax(d, 100, 0, "", "", true, false)
		if d.TaxRate == nil || *d.TaxRate != DefaultVATRate {
			t.Errorf("TaxRate = %v, want DefaultVATRate (%v)", d.TaxRate, DefaultVATRate)
		}
	})
}
