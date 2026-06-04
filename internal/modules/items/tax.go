package items

import (
	"context"
	"math"

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
		return TaxSplit{Net: round2(price), Tax: 0, Gross: round2(price), Rate: ratePct}
	}
	r := ratePct / 100.0
	if inclusive {
		net := price / (1 + r)
		return TaxSplit{Net: round2(net), Tax: round2(price - net), Gross: round2(price), Rate: ratePct}
	}
	tax := price * r
	return TaxSplit{Net: round2(price), Tax: round2(tax), Gross: round2(price + tax), Rate: ratePct}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
