// Command prune-users removes the inventory users created before auth.user events were gated by
// shared UserRelevance (2026-10-08). Back then every member of every tenant got an inventory
// user. Every POS tenant runs on inventory, so it only touches tenants that have never used it
// (no warehouse and no item), and there only users with no PIN and no outlet assignment. Anyone
// removed by mistake comes back on first sign-in.
//
// It reports counts per tenant and changes nothing unless run with --apply.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/inventoryuser"
	"github.com/bengobox/inventory-service/internal/ent/item"
	enttenant "github.com/bengobox/inventory-service/internal/ent/tenant"
	"github.com/bengobox/inventory-service/internal/ent/useroutlet"
	"github.com/bengobox/inventory-service/internal/ent/userroleassignment"
	"github.com/bengobox/inventory-service/internal/ent/warehouse"
)

func main() {
	apply := flag.Bool("apply", false, "delete the rows (default reports only)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn := os.Getenv("POSTGRES_MIGRATE_URL")
	if dsn == "" {
		dsn = os.Getenv("POSTGRES_URL")
	}
	if dsn == "" {
		log.Fatal("POSTGRES_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer client.Close()

	candidates, err := client.InventoryUser.Query().
		Where(inventoryuser.Or(inventoryuser.PinHashIsNil(), inventoryuser.PinHashEQ(""))).
		Select(inventoryuser.FieldID, inventoryuser.FieldTenantID, inventoryuser.FieldAuthServiceUserID).
		All(ctx)
	if err != nil {
		log.Fatalf("load users: %v", err)
	}

	// unused caches whether a tenant has never used inventory.
	unused := map[uuid.UUID]bool{}
	isUnused := func(tid uuid.UUID) bool {
		if v, ok := unused[tid]; ok {
			return v
		}
		hasWarehouse, err := client.Warehouse.Query().Where(warehouse.TenantID(tid)).Exist(ctx)
		if err != nil {
			log.Fatalf("check warehouses: %v", err)
		}
		hasItem, err := client.Item.Query().Where(item.TenantID(tid)).Exist(ctx)
		if err != nil {
			log.Fatalf("check items: %v", err)
		}
		unused[tid] = !hasWarehouse && !hasItem
		return unused[tid]
	}

	byTenant := map[uuid.UUID]int{}
	var ids []uuid.UUID
	for _, u := range candidates {
		if !isUnused(u.TenantID) {
			continue
		}
		// user_outlets.user_id has held both ids over time; either one means keep.
		assigned, err := client.UserOutlet.Query().
			Where(useroutlet.UserIDIn(u.ID, u.AuthServiceUserID)).Exist(ctx)
		if err != nil {
			log.Fatalf("check outlet assignments: %v", err)
		}
		if assigned {
			continue
		}
		byTenant[u.TenantID]++
		ids = append(ids, u.ID)
	}
	for tid, n := range byTenant {
		slug := "?"
		if t, err := client.Tenant.Query().Where(enttenant.ID(tid)).Only(ctx); err == nil {
			slug = t.Slug
		}
		log.Printf("tenant %-28s %s  removable: %d", slug, tid, n)
	}
	total, _ := client.InventoryUser.Query().Count(ctx)
	log.Printf("inventory users: %d total, %d removable, %d kept", total, len(ids), total-len(ids))

	if !*apply || len(ids) == 0 {
		log.Printf("dry run: nothing changed (pass --apply to delete)")
		return
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		log.Fatalf("begin: %v", err)
	}
	roles, err := tx.UserRoleAssignment.Delete().Where(userroleassignment.UserIDIn(ids...)).Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		log.Fatalf("delete role assignments: %v (rolled back)", err)
	}
	n, err := tx.InventoryUser.Delete().Where(inventoryuser.IDIn(ids...)).Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		log.Fatalf("delete users: %v (rolled back)", err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	log.Printf("deleted %d inventory users and %d role assignments", n, roles)
}
