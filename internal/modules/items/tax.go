package items

import (
	"context"

	vat "github.com/Bengo-Hub/httpware/tax"
	"github.com/google/uuid"
)

// TaxResolver resolves the VAT rate for a tenant from treasury-api, which owns every tax rate.
// Implemented by internal/platform/treasury.Client. ok=false means no rate is known, and the item
// then carries no VAT split; a rate is never assumed.
type TaxResolver interface {
	ResolveVATRate(ctx context.Context, tenantID uuid.UUID, preferredCode string) (rate float64, code string, ok bool)
}

// TaxSplit holds the net, tax, gross and rate components of a price.
type TaxSplit struct {
	Net   float64
	Tax   float64
	Gross float64
	Rate  float64
}

// ComputeTaxSplit splits a price into net and tax at ratePct, through the platform's shared VAT
// calculator (github.com/Bengo-Hub/httpware/tax) so the catalog, the till and treasury agree to the
// cent. inclusive means the price already holds the VAT.
func ComputeTaxSplit(price, ratePct float64, inclusive bool) TaxSplit {
	if price <= 0 || ratePct <= 0 {
		net, _, gross := vat.Split(price, 0, inclusive)
		return TaxSplit{Net: net, Gross: gross, Rate: ratePct}
	}
	net, tax, gross := vat.Split(price, ratePct, inclusive)
	return TaxSplit{Net: net, Tax: tax, Gross: gross, Rate: ratePct}
}
