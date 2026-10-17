package items

import (
	"context"

	"github.com/Bengo-Hub/httpware/money"

	"github.com/google/uuid"
)

// DefaultVATRate is the Kenya standard VAT, used as a fallback when treasury-api
// (the source of truth for tax rates) is unavailable.
const DefaultVATRate = 16.0

// TaxResolver resolves the VAT rate for a tenant from treasury-api. Implemented by
// internal/platform/treasury.Client. ok=false → caller falls back to DefaultVATRate.
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

// ComputeTaxSplit splits a gross/net price into net + tax given a rate (%) and whether
// the price already includes tax.
//   - inclusive: net = price/(1+rate); tax = price - net; gross = price (tax computed backwards)
//   - exclusive: net = price; tax = price*rate; gross = net + tax
func ComputeTaxSplit(price, ratePct float64, inclusive bool) TaxSplit {
	if price <= 0 || ratePct <= 0 {
		return TaxSplit{Net: money.Round2(price), Tax: 0, Gross: money.Round2(price), Rate: ratePct}
	}
	r := ratePct / 100.0
	if inclusive {
		net := price / (1 + r)
		return TaxSplit{Net: money.Round2(net), Tax: money.Round2(price - net), Gross: money.Round2(price), Rate: ratePct}
	}
	tax := price * r
	return TaxSplit{Net: money.Round2(price), Tax: money.Round2(tax), Gross: money.Round2(price + tax), Rate: ratePct}
}
