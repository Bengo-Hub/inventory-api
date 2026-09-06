package items

import (
	"testing"

	"github.com/google/uuid"
)

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

// effectivePrice: active clearance markdown > recipe price > default-tier price > merchant's
// max_selling_price ceiling > cost+margin SuggestedPrice (last resort). Regression coverage for
// the live incident this session diagnosed (boi-enterprises SKU 17606, "2160 Itel Copy" not
// reflecting a price change) — the item is a GOODS item (no recipe), so its correct source is the
// default-tier price, which is exactly what a duplicate/stale tierPrice entry would corrupt.
func TestEffectivePrice(t *testing.T) {
	id := uuid.New()
	maxPrice := 1000.0
	suggested := 500.0

	tests := []struct {
		name      string
		item      *ItemDTO
		clearance map[uuid.UUID]float64
		recipe    map[uuid.UUID]float64
		tier      map[uuid.UUID]float64
		want      float64
	}{
		{
			name:      "an active clearance markdown wins over everything, including recipe",
			item:      &ItemDTO{ID: id, MaxSellingPrice: &maxPrice, SuggestedPrice: &suggested},
			clearance: map[uuid.UUID]float64{id: 300},
			recipe:    map[uuid.UUID]float64{id: 1200},
			tier:      map[uuid.UUID]float64{id: 700},
			want:      300,
		},
		{
			name:   "recipe price wins over everything else",
			item:   &ItemDTO{ID: id, MaxSellingPrice: &maxPrice, SuggestedPrice: &suggested},
			recipe: map[uuid.UUID]float64{id: 1200},
			tier:   map[uuid.UUID]float64{id: 700},
			want:   1200,
		},
		{
			name: "default-tier price wins over the max_selling_price guardrail and cooked suggestion",
			item: &ItemDTO{ID: id, MaxSellingPrice: &maxPrice, SuggestedPrice: &suggested},
			tier: map[uuid.UUID]float64{id: 700},
			want: 700,
		},
		{
			name: "falls back to max_selling_price when no tier price resolved",
			item: &ItemDTO{ID: id, MaxSellingPrice: &maxPrice, SuggestedPrice: &suggested},
			want: 1000,
		},
		{
			name: "cost+margin SuggestedPrice is the last resort only",
			item: &ItemDTO{ID: id, SuggestedPrice: &suggested},
			want: 500,
		},
		{
			name: "zero when nothing prices the item",
			item: &ItemDTO{ID: id},
			want: 0,
		},
		{
			name:      "a zero/negative clearance, recipe or tier price is ignored, not treated as authoritative",
			item:      &ItemDTO{ID: id, MaxSellingPrice: &maxPrice},
			clearance: map[uuid.UUID]float64{id: -1},
			recipe:    map[uuid.UUID]float64{id: 0},
			tier:      map[uuid.UUID]float64{id: -5},
			want:      1000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectivePrice(tt.item, tt.clearance, tt.recipe, tt.tier); got != tt.want {
				t.Errorf("effectivePrice() = %v, want %v", got, tt.want)
			}
		})
	}
}
