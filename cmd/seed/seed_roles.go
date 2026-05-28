package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvrole "github.com/bengobox/inventory-service/internal/ent/inventoryrole"
	entrp "github.com/bengobox/inventory-service/internal/ent/rolepermission"
)

type roleDef struct {
	Code        string
	Name        string
	Description string
	IsSystem    bool
}

func roleUUID(tenantID uuid.UUID, code string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:role:%s:%s", tenantID, code)))
}

var roleDefs = []roleDef{
	{"inventory_admin", "Inventory Admin", "Full access to all inventory operations including config and user management", true},
	{"warehouse_manager", "Warehouse Manager", "Manage warehouses, stock, reservations, recipes, and consumptions", true},
	{"stock_clerk", "Stock Clerk", "View and change stock, view items and warehouses, manage own consumptions", true},
	{"viewer", "Viewer", "Read-only access to all inventory data", true},
}

var rolePermMap = map[string][]string{
	"inventory_admin": nil, // populated at seed time — gets ALL permissions
	"warehouse_manager": {
		"inventory.warehouses.add", "inventory.warehouses.view", "inventory.warehouses.change", "inventory.warehouses.delete", "inventory.warehouses.manage",
		"inventory.stock.add", "inventory.stock.view", "inventory.stock.change", "inventory.stock.delete", "inventory.stock.manage",
		"inventory.items.view", "inventory.items.change", "inventory.items.add",
		"inventory.variants.view", "inventory.variants.change", "inventory.variants.add",
		"inventory.categories.view", "inventory.categories.change", "inventory.categories.add",
		"inventory.recipes.add", "inventory.recipes.view", "inventory.recipes.change", "inventory.recipes.delete", "inventory.recipes.manage",
		"inventory.consumptions.add", "inventory.consumptions.view", "inventory.consumptions.change", "inventory.consumptions.delete", "inventory.consumptions.manage",
		"inventory.reservations.add", "inventory.reservations.view", "inventory.reservations.change", "inventory.reservations.delete", "inventory.reservations.manage",
		"inventory.units.view",
	},
	"stock_clerk": {
		"inventory.stock.view", "inventory.stock.change", "inventory.stock.add",
		"inventory.items.view",
		"inventory.variants.view",
		"inventory.categories.view",
		"inventory.warehouses.view",
		"inventory.consumptions.view_own", "inventory.consumptions.add", "inventory.consumptions.change_own", "inventory.consumptions.manage_own",
		"inventory.reservations.view",
		"inventory.units.view",
		"inventory.recipes.view",
	},
	"viewer": {
		"inventory.items.view",
		"inventory.variants.view",
		"inventory.categories.view",
		"inventory.warehouses.view",
		"inventory.stock.view",
		"inventory.recipes.view",
		"inventory.consumptions.view",
		"inventory.reservations.view",
		"inventory.units.view",
	},
}

func seedRoles(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	for _, d := range roleDefs {
		id := roleUUID(tenantID, d.Code)
		exists, err := client.InventoryRole.Query().
			Where(entinvrole.TenantID(tenantID), entinvrole.RoleCode(d.Code)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("check role %s: %w", d.Code, err)
		}
		if exists {
			continue
		}
		if _, err := client.InventoryRole.Create().
			SetID(id).
			SetTenantID(tenantID).
			SetRoleCode(d.Code).
			SetName(d.Name).
			SetDescription(d.Description).
			SetIsSystemRole(d.IsSystem).
			Save(ctx); err != nil {
			return fmt.Errorf("create role %s: %w", d.Code, err)
		}
		log.Printf("role created: %s", d.Code)
	}
	return nil
}

func seedRolePermissions(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	allPerms := buildPermDefs()
	adminCodes := make([]string, 0, len(allPerms))
	for _, p := range allPerms {
		adminCodes = append(adminCodes, p.Code)
	}
	rolePermMap["inventory_admin"] = adminCodes

	for _, rd := range roleDefs {
		roleID := roleUUID(tenantID, rd.Code)
		permCodes, ok := rolePermMap[rd.Code]
		if !ok {
			continue
		}
		for _, code := range permCodes {
			permID := permUUID(code)
			exists, err := client.RolePermission.Query().
				Where(entrp.RoleID(roleID), entrp.PermissionID(permID)).
				Exist(ctx)
			if err != nil {
				return fmt.Errorf("check role-perm %s/%s: %w", rd.Code, code, err)
			}
			if exists {
				continue
			}
			if _, err := client.RolePermission.Create().
				SetRoleID(roleID).
				SetPermissionID(permID).
				Save(ctx); err != nil {
				return fmt.Errorf("assign perm %s to role %s: %w", code, rd.Code, err)
			}
		}
		log.Printf("role-permissions assigned: %s (%d perms)", rd.Code, len(permCodes))
	}
	return nil
}
