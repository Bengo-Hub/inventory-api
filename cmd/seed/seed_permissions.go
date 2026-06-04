package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvperm "github.com/bengobox/inventory-service/internal/ent/inventorypermission"
)

type permDef struct {
	Code        string
	Name        string
	Module      string
	Action      string
	Description string
}

func permUUID(code string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("bengobox:inventory:permission:"+code))
}

func buildPermDefs() []permDef {
	modules := []struct {
		module string
		label  string
		note   string
	}{
		{"items", "Items", "catalog/SKU management"},
		{"variants", "Variants", "item variant management"},
		{"categories", "Categories", "item category management"},
		{"warehouses", "Warehouses", "warehouse and location management"},
		{"stock", "Stock", "stock adjustments, cycle counts, transfers"},
		{"recipes", "Recipes", "recipe/BOM management"},
		{"consumptions", "Consumptions", "stock consumption tracking"},
		{"reservations", "Reservations", "inventory reservations and allocation"},
		{"units", "Units", "unit of measure management (platform-only for manage operations)"},
		{"config", "Config", "service configuration management"},
		{"users", "Users", "user management"},
		{"tickets", "Tickets", "event ticket selling, issuance and check-in"},
		{"procurement", "Procurement", "requisitions, purchase orders, goods receipts, returns, contracts, suppliers"},
		{"manufacturing", "Manufacturing", "production batches, BOM, quality checks"},
		{"assets", "Assets", "fixed-asset register and lifecycle"},
	}

	actions := []struct {
		action string
		verb   string
	}{
		{"add", "Add"},
		{"view", "View"},
		{"view_own", "View own"},
		{"change", "Change"},
		{"change_own", "Change own"},
		{"delete", "Delete"},
		{"delete_own", "Delete own"},
		{"manage", "Manage"},
		{"manage_own", "Manage own"},
	}

	var defs []permDef
	for _, m := range modules {
		for _, a := range actions {
			code := fmt.Sprintf("inventory.%s.%s", m.module, a.action)
			name := fmt.Sprintf("%s %s", a.verb, m.label)
			desc := fmt.Sprintf("%s — %s", name, m.note)
			defs = append(defs, permDef{
				Code:        code,
				Name:        name,
				Module:      m.module,
				Action:      a.action,
				Description: desc,
			})
		}
	}
	return defs
}

func seedPermissions(ctx context.Context, client *ent.Client) error {
	defs := buildPermDefs()
	for _, d := range defs {
		id := permUUID(d.Code)
		exists, err := client.InventoryPermission.Query().
			Where(entinvperm.PermissionCode(d.Code)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("check permission %s: %w", d.Code, err)
		}
		if exists {
			continue
		}
		if _, err := client.InventoryPermission.Create().
			SetID(id).
			SetPermissionCode(d.Code).
			SetName(d.Name).
			SetModule(d.Module).
			SetAction(d.Action).
			SetResource(d.Module).
			SetDescription(d.Description).
			Save(ctx); err != nil {
			return fmt.Errorf("create permission %s: %w", d.Code, err)
		}
		log.Printf("permission created: %s", d.Code)
	}
	return nil
}
