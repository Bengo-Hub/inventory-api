package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entinvrole "github.com/bengobox/inventory-service/internal/ent/inventoryrole"
	entrp "github.com/bengobox/inventory-service/internal/ent/rolepermission"
	entura "github.com/bengobox/inventory-service/internal/ent/userroleassignment"
)

type roleDef struct {
	Code        string
	Name        string
	Description string
	IsSystem    bool
}

// globalRoleUUID returns the deterministic ID for a shared, platform-wide (global) inventory role.
func globalRoleUUID(code string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:role:global:%s", code)))
}

var roleDefs = []roleDef{
	{"inventory_admin", "Inventory Admin", "Full access to all inventory operations including config and user management", true},
	{"warehouse_manager", "Warehouse Manager", "Manage warehouses, stock, reservations, recipes, and consumptions", true},
	{"accountant", "Accountant", "Finance/back-office: manage purchases (procurement), approvals, fixed assets, and stock valuation adjustments", true},
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
		"inventory.tickets.add", "inventory.tickets.view", "inventory.tickets.change", "inventory.tickets.delete", "inventory.tickets.manage",
		// Procurement / manufacturing / assets (matches the prior items.add+change grant;
		// asset delete stays admin-only, mirroring no items.delete).
		"inventory.procurement.view", "inventory.procurement.add", "inventory.procurement.change", "inventory.procurement.manage",
		"inventory.manufacturing.view", "inventory.manufacturing.add", "inventory.manufacturing.change", "inventory.manufacturing.manage",
		"inventory.assets.view", "inventory.assets.add", "inventory.assets.change", "inventory.assets.manage",
		"inventory.approvals.view", "inventory.approvals.add", "inventory.approvals.change", "inventory.approvals.manage",
		// Audit trail visibility + cycle-count lifecycle (managers approve count variance).
		"inventory.audit.view",
		"inventory.stock_count.view", "inventory.stock_count.add", "inventory.stock_count.change", "inventory.stock_count.approve",
	},
	// accountant — finance/back-office. Owns purchasing (procurement), approves purchase orders,
	// maintains the fixed-asset register, and makes stock valuation adjustments. Read access to the
	// rest of inventory for costing/reconciliation. Catalog/recipe deletes stay admin-only.
	"accountant": {
		"inventory.items.view", "inventory.items.add", "inventory.items.change",
		"inventory.variants.view",
		"inventory.categories.view",
		"inventory.warehouses.view",
		// Valuation write-offs / cost adjustments.
		"inventory.stock.view", "inventory.stock.add", "inventory.stock.change", "inventory.stock.manage",
		"inventory.recipes.view",
		"inventory.consumptions.view",
		"inventory.reservations.view",
		"inventory.units.view",
		"inventory.tickets.view",
		// Purchases — full procurement lifecycle (requisitions, POs, goods receipts, returns).
		"inventory.procurement.view", "inventory.procurement.add", "inventory.procurement.change", "inventory.procurement.manage",
		"inventory.manufacturing.view",
		// Fixed-asset register (asset accounting / depreciation).
		"inventory.assets.view", "inventory.assets.add", "inventory.assets.change", "inventory.assets.manage",
		// Approve purchase orders / run the approval matrix.
		"inventory.approvals.view", "inventory.approvals.add", "inventory.approvals.change", "inventory.approvals.manage",
		// Tax/compliance settings visibility.
		"inventory.settings.view",
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
		"inventory.tickets.view", "inventory.tickets.add", "inventory.tickets.change",
		"inventory.procurement.view", "inventory.manufacturing.view", "inventory.assets.view", "inventory.approvals.view",
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
		"inventory.tickets.view",
		"inventory.procurement.view", "inventory.manufacturing.view", "inventory.assets.view", "inventory.approvals.view",
	},
}

// seedRoles creates the platform-wide system roles (shared across all tenants, tenant_id NULL) and
// consolidates redundant legacy per-tenant system-role duplicates that have no user assignments.
func seedRoles(ctx context.Context, client *ent.Client) error {
	systemCodes := make(map[string]bool, len(roleDefs))
	for _, d := range roleDefs {
		systemCodes[d.Code] = true
	}

	for _, d := range roleDefs {
		id := globalRoleUUID(d.Code)
		exists, err := client.InventoryRole.Query().Where(entinvrole.ID(id)).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check role %s: %w", d.Code, err)
		}
		if exists {
			continue
		}
		// Do NOT set tenant_id — leaving it NULL marks this as a global/system role.
		if _, err := client.InventoryRole.Create().
			SetID(id).
			SetRoleCode(d.Code).
			SetName(d.Name).
			SetDescription(d.Description).
			SetIsSystemRole(d.IsSystem).
			Save(ctx); err != nil {
			return fmt.Errorf("create global role %s: %w", d.Code, err)
		}
		log.Printf("global role created: %s", d.Code)
	}

	// Consolidate legacy per-tenant system roles now served by the shared global role: delete those
	// with a known system code and zero user assignments (delete their role-permission rows first).
	legacy, err := client.InventoryRole.Query().
		Where(entinvrole.IsSystemRole(true), entinvrole.TenantIDNotNil()).
		All(ctx)
	if err != nil {
		return fmt.Errorf("list legacy tenant system roles: %w", err)
	}
	consolidated := 0
	for _, lr := range legacy {
		if !systemCodes[lr.RoleCode] {
			continue // genuine custom role — keep it
		}
		cnt, err := client.UserRoleAssignment.Query().
			Where(entura.RoleID(lr.ID)).
			Count(ctx)
		if err != nil {
			return fmt.Errorf("count assignments for legacy role %s: %w", lr.ID, err)
		}
		if cnt > 0 {
			continue // still referenced — keep it (perms reconciled in seedRolePermissions)
		}
		if _, err := client.RolePermission.Delete().Where(entrp.RoleID(lr.ID)).Exec(ctx); err != nil {
			return fmt.Errorf("delete perms for legacy role %s: %w", lr.ID, err)
		}
		if err := client.InventoryRole.DeleteOneID(lr.ID).Exec(ctx); err != nil {
			return fmt.Errorf("delete legacy role %s: %w", lr.ID, err)
		}
		consolidated++
	}
	log.Printf("legacy per-tenant system roles consolidated: %d", consolidated)
	return nil
}

// seedRolePermissions reconciles permissions onto EVERY role bearing each system code — the shared
// global role AND any tenant-scoped copy/override — so no role (global or tenant) is ever left without
// its required permissions. Idempotent.
func seedRolePermissions(ctx context.Context, client *ent.Client) error {
	allPerms := buildPermDefs()
	adminCodes := make([]string, 0, len(allPerms))
	for _, p := range allPerms {
		adminCodes = append(adminCodes, p.Code)
	}
	rolePermMap["inventory_admin"] = adminCodes

	for _, rd := range roleDefs {
		permCodes, ok := rolePermMap[rd.Code]
		if !ok {
			continue
		}
		roles, err := client.InventoryRole.Query().
			Where(entinvrole.RoleCode(rd.Code)).
			All(ctx)
		if err != nil {
			return fmt.Errorf("list roles for code %s: %w", rd.Code, err)
		}
		for _, role := range roles {
			for _, code := range permCodes {
				permID := permUUID(code)
				exists, err := client.RolePermission.Query().
					Where(entrp.RoleID(role.ID), entrp.PermissionID(permID)).
					Exist(ctx)
				if err != nil {
					return fmt.Errorf("check role-perm %s/%s: %w", rd.Code, code, err)
				}
				if exists {
					continue
				}
				if _, err := client.RolePermission.Create().
					SetRoleID(role.ID).
					SetPermissionID(permID).
					Save(ctx); err != nil {
					return fmt.Errorf("assign perm %s to role %s: %w", code, rd.Code, err)
				}
			}
		}
		log.Printf("role-permissions reconciled: %s (%d perms across all matching roles)", rd.Code, len(permCodes))
	}
	return nil
}
