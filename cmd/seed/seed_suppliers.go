package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvbal "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
)

func strPtr(s string) *string { return &s }

// seedSuppliers creates a demo supplier for the given tenant and returns its ID.
func seedSuppliers(ctx context.Context, client *ent.Client, tenantID uuid.UUID) (uuid.UUID, error) {
	const demoSupplierName = "Demo Distributor Co."

	existing, err := client.Supplier.Query().
		Where(entsupplier.TenantID(tenantID), entsupplier.NameEQ(demoSupplierName)).
		Only(ctx)
	if err == nil {
		log.Printf("supplier already exists: %s", existing.Name)
		return existing.ID, nil
	}
	if !ent.IsNotFound(err) {
		return uuid.Nil, fmt.Errorf("query supplier: %w", err)
	}

	phone := "254700000001"
	email := "orders@demo-distributor.com"
	pmType := entsupplier.PaymentMethodTypeMpesa

	sup, err := client.Supplier.Create().
		SetTenantID(tenantID).
		SetName(demoSupplierName).
		SetCode("DEMO-DIST-001").
		SetNillableContactName(strPtr("John Distributor")).
		SetNillableContactEmail(&email).
		SetNillableContactPhone(&phone).
		SetNillablePaymentMethodType(&pmType).
		SetNillableMpesaPhone(&phone).
		SetIsActive(true).
		Save(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create supplier: %w", err)
	}
	log.Printf("created demo supplier: %s (%s)", sup.Name, sup.ID)
	return sup.ID, nil
}

// seedReorderConfig wires preferred_supplier_id + auto_reorder_enabled on key
// INGREDIENT balances so the stock-low → auto-PO workflow works in demo.
func seedReorderConfig(ctx context.Context, client *ent.Client, tenantID, supplierID uuid.UUID) error {
	autoreorderSKUs := []string{"RAW-ESP-001", "RAW-MLK-001", "RAW-CKN-001"}

	for _, sku := range autoreorderSKUs {
		item, err := client.Item.Query().
			Where(entitem.TenantID(tenantID), entitem.SkuEQ(sku)).
			Only(ctx)
		if err != nil {
			log.Printf("[WARN] reorder config: item %s not found: %v", sku, err)
			continue
		}

		n, err := client.InventoryBalance.Update().
			Where(
				entinvbal.TenantID(tenantID),
				entinvbal.ItemID(item.ID),
			).
			SetPreferredSupplierID(supplierID).
			SetAutoReorderEnabled(true).
			SetReorderLevel(10).
			SetReorderQuantity(50).
			Save(ctx)
		if err != nil {
			log.Printf("[WARN] reorder config update for %s: %v", sku, err)
			continue
		}
		log.Printf("reorder config set for %s (rows updated: %d)", sku, n)
	}
	return nil
}
