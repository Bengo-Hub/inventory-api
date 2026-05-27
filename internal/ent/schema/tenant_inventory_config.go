package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// TenantInventoryConfig holds typed inventory settings per tenant.
// One row per tenant (upserted on save). Replaces scattered ServiceConfig key-value pairs
// for inventory-specific settings that the UI needs to read/write atomically.
type TenantInventoryConfig struct {
	ent.Schema
}

func (TenantInventoryConfig) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("One config row per tenant"),
		// Stock threshold alerts
		field.Float("low_stock_threshold_pct").
			Default(20.0).
			Comment("Percentage of reorder level at which low-stock alert fires"),
		field.Float("critical_stock_threshold_pct").
			Default(5.0).
			Comment("Percentage of reorder level at which critical-stock alert fires"),
		field.Int("default_reorder_level").
			Default(10).
			Comment("Default reorder quantity applied to new items when no unit-specific default is configured"),
		field.JSON("unit_reorder_defaults", map[string]int{}).
			Optional().
			Comment("Per-unit default reorder levels: maps unit abbreviation (e.g. 'kg','g','pc') to minimum quantity"),
		field.Int("expiry_warning_days").
			Default(30).
			Comment("Days before expiry to trigger expiry-approaching alert"),
		// Notification toggles
		field.Bool("enable_low_stock_notifications").
			Default(true),
		field.Bool("enable_expiry_notifications").
			Default(true),
		field.String("notification_email").
			Optional().
			Nillable().
			Comment("Email address for stock/expiry alert notifications"),
		field.String("default_warehouse_id").
			Optional().
			Nillable().
			Comment("Default warehouse UUID for new operations"),
		// Tracking toggles
		field.Bool("enable_lot_tracking").
			Default(false).
			Comment("Enable batch/lot number tracking on stock movements"),
		field.Bool("enable_expiry_tracking").
			Default(false).
			Comment("Track expiry dates on items (required for pharmacy/FMCG)"),
		field.Bool("purchase_order_approval_required").
			Default(false).
			Comment("Require manager approval before a Purchase Order can be issued"),
		field.Bool("auto_adjust_on_transfer").
			Default(true).
			Comment("Automatically adjust stock balances when a transfer is completed"),
		// Module toggles — tenant admin controls active inventory modules
		field.Bool("lots_module_enabled").
			Default(false).
			Comment("Lot/batch inventory module"),
		field.Bool("recipes_module_enabled").
			Default(false).
			Comment("Bill-of-materials / recipe module for production use cases"),
		field.Bool("purchase_orders_enabled").
			Default(true).
			Comment("Purchase order and supplier management module"),
		field.Bool("supplier_management_enabled").
			Default(true).
			Comment("Supplier directory and contract management"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

func (TenantInventoryConfig) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id").Unique(),
	}
}
