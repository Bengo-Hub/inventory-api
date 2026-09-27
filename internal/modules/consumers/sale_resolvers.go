package consumers

import (
	"context"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entwh "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// Shared by the treasury sale consumers (goods committed, sale goods).

// resolveSaleItem resolves a treasury line to an inventory item: by id first, then by SKU within
// the tenant. nil when neither matches (a free-typed good with no stock record).
func resolveSaleItem(ctx context.Context, orm *ent.Client, tenantID uuid.UUID, itemIDRaw, sku string) *ent.Item {
	if id, err := uuid.Parse(itemIDRaw); err == nil {
		if itm, err := orm.Item.Query().Where(entitem.ID(id), entitem.TenantID(tenantID)).Only(ctx); err == nil {
			return itm
		}
	}
	if sku != "" {
		if itm, err := orm.Item.Query().Where(entitem.TenantID(tenantID), entitem.Sku(sku)).Only(ctx); err == nil {
			return itm
		}
	}
	return nil
}

// resolveSaleWarehouse picks the warehouse a sale moves stock through: the selling outlet's active
// warehouse, else the tenant's default active warehouse, else uuid.Nil.
func resolveSaleWarehouse(ctx context.Context, orm *ent.Client, tenantID uuid.UUID, outletIDRaw string) uuid.UUID {
	if outletID, err := uuid.Parse(outletIDRaw); err == nil {
		if wh, werr := orm.Warehouse.Query().
			Where(entwh.TenantID(tenantID), entwh.OutletID(outletID), entwh.IsActive(true)).First(ctx); werr == nil {
			return wh.ID
		}
	}
	if wh, werr := orm.Warehouse.Query().
		Where(entwh.TenantID(tenantID), entwh.IsDefault(true), entwh.IsActive(true)).First(ctx); werr == nil {
		return wh.ID
	}
	return uuid.Nil
}
