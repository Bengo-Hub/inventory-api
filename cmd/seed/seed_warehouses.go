package main

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entwarehouse "github.com/bengobox/inventory-service/internal/ent/warehouse"
)

// seedWarehouses ensures a default "Main Warehouse" exists for the tenant.
func seedWarehouses(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	warehouseID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:warehouse:main:"+tenantID.String()))

	exists, _ := client.Warehouse.Query().Where(entwarehouse.ID(warehouseID)).Exist(ctx)
	if exists {
		return nil
	}

	if _, err := client.Warehouse.Create().
		SetID(warehouseID).
		SetTenantID(tenantID).
		SetName("Main Warehouse").
		SetCode("MAIN").
		SetIsDefault(true).
		SetIsActive(true).
		Save(ctx); err != nil {
		return err
	}
	log.Printf("warehouse created: MAIN for tenant %s", tenantID)
	return nil
}
