package items

import (
	"context"
	"errors"
)

// ErrActivationRefused is returned when an activation guard refuses to bring items back into the
// active catalog (the plan's inventory_max_sku is reached). A single-request handler has already
// written the 402 response when it sees this error.
var ErrActivationRefused = errors.New("items: plan SKU limit reached")

// ActivationGuard decides whether n items may join the active catalog, either new items or items
// moving in from outside it (is_active false, End of Life included). It returns nil to allow the
// change.
type ActivationGuard func(ctx context.Context, n int) error

type activationGuardKey struct{}

// WithActivationGuard installs g for the service calls made with the returned context. Every path
// that turns an inactive item active (UpdateItem, RestoreItemEOL, the bulk activate action) asks it
// before writing, and the variant matrix asks once for its new and reactivated children together.
// A nil g removes any guard already installed.
func WithActivationGuard(ctx context.Context, g ActivationGuard) context.Context {
	return context.WithValue(ctx, activationGuardKey{}, g)
}

// GuardActivation asks the guard in ctx whether n items may join the active catalog. No guard
// (service-to-service and internal callers) or n <= 0 always allows.
func GuardActivation(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	g, _ := ctx.Value(activationGuardKey{}).(ActivationGuard)
	if g == nil {
		return nil
	}
	return g(ctx, n)
}
