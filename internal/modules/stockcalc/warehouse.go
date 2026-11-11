package stockcalc

import (
	"context"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// OutletWarehouse is the warehouse that stock for an operating outlet lands in: the outlet's own
// active warehouse, else the tenant's active default warehouse. Outlet-scoped screens (the POS
// till, the catalogue and the treasury eTIMS Items page with an outlet picked) list only items
// stocked in the outlet's own warehouses, so stock written to the tenant default (often HQ or the
// hotel outlet) is invisible at every other outlet. A nil outletID, or an outlet with no
// warehouse of its own, gives the tenant default. Returns an ent NotFound error when the tenant
// has neither.
//
// Shared by stock movements and item creation (stock imports items, so neither can hold it).
func OutletWarehouse(ctx context.Context, client *ent.Client, tenantID, outletID uuid.UUID) (*ent.Warehouse, error) {
	if outletID != uuid.Nil {
		wh, err := client.Warehouse.Query().
			Where(warehouse.TenantID(tenantID), warehouse.OutletIDEQ(outletID), warehouse.IsActive(true)).
			First(ctx)
		if err == nil {
			return wh, nil
		}
		if !ent.IsNotFound(err) {
			return nil, err
		}
	}
	return client.Warehouse.Query().
		Where(warehouse.TenantID(tenantID), warehouse.IsDefault(true), warehouse.IsActive(true)).
		First(ctx)
}
