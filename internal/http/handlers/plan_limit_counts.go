package handlers

import (
	"context"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// Plan structural caps (inventory_max_warehouses, max_suppliers, inventory_max_sku) count only
// the rows a tenant is actually using. Archived suppliers and deactivated warehouses stay in the
// database for history, so counting them would lock a downgraded tenant out of creating the one
// record they still need.

// countActiveWarehouses counts the tenant's active warehouses for the max_warehouses cap.
func countActiveWarehouses(ctx context.Context, orm *ent.Client, tenantID uuid.UUID) (int, error) {
	return orm.Warehouse.Query().
		Where(entwarehouse.TenantID(tenantID), entwarehouse.IsActive(true)).
		Count(ctx)
}

// countActiveSuppliers counts the tenant's active (not archived) suppliers for the max_suppliers cap.
func countActiveSuppliers(ctx context.Context, orm *ent.Client, tenantID uuid.UUID) (int, error) {
	return orm.Supplier.Query().
		Where(entsupplier.TenantID(tenantID), entsupplier.IsActive(true)).
		Count(ctx)
}
