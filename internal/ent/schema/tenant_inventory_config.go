package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// LabelPrintDefaults is the tenant's preferred label template/format for barcode printing, so
// PrintLabelsDialog (bulk print) and GetItemLabelPDF (single-item print) default to it without
// the caller specifying one every time. Rotate is a pointer so "unset" (use the named template's
// own default) is distinguishable from an explicit false.
type LabelPrintDefaults struct {
	Format   string `json:"format,omitempty"`   // avery_a4 | thermal_zpl | thermal_tspl | dymo
	Template string `json:"template,omitempty"` // preset name (see barcode.LabelTemplateByName) or "custom"
	Rotate   *bool  `json:"rotate,omitempty"`
}

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
		// Notification toggles — OPT-IN (default false): alert emails to the tenant contact
		// are noisy for tenants that don't maintain ingredient balances, and burst volume
		// from a busy outlet has tripped the shared SMTP provider's abuse detection.
		field.Bool("enable_low_stock_notifications").
			Default(false),
		field.Bool("enable_expiry_notifications").
			Default(false),
		field.String("notification_email").
			Optional().
			Nillable().
			Comment("Email address for stock/expiry alert notifications"),
		field.String("default_warehouse_id").
			Optional().
			Nillable().
			Comment("Default warehouse UUID for new operations"),
		// Costing / consumption ordering method. Drives which lot is consumed first AND how stock
		// is valued: fifo=oldest received, lifo=newest received, fefo=earliest expiry, wavg=weighted
		// average (no lot ordering — item-level cost).
		field.Enum("costing_method").
			Values("wavg", "fifo", "lifo", "fefo").
			Default("wavg").
			Comment("Inventory costing/consumption method: wavg|fifo|lifo|fefo"),
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
		// Non-depletion (manual stock tracking) policy — AccuPOS-style opt-out for small
		// tenants who count stock manually. Applies to RECIPE-type items whose
		// stock_tracking_mode is "default"; GOODS/INGREDIENT items always deplete unless
		// individually flagged non_depleting.
		field.Bool("recipe_items_non_depleting_default").
			Default(false).
			Comment("When true, RECIPE-type items sell without depleting ingredient stock unless individually set to tracked"),
		field.Bool("record_theoretical_usage").
			Default(true).
			Comment("When true, non-depleting sales still write theoretical Consumption rows so AvT/food-cost reports stay meaningful"),
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
		// Add-on: per-branch/outlet pricing (2026-09-06 pricing/tiering plan, Phase 1). Gated on
		// the platform-admin-granted "multi_branch_pricing" feature (see subscriptions-api's
		// TenantFeatureGrant) the same way lots_batches/stock_alerts below are -- ENABLING requires
		// the feature, disabling is always allowed. When on, inventory-ui's Item Pricing tab shows
		// an outlet picker so ItemPricing.outlet_id rows can be created via PUT
		// /inventory/items/{id}/pricing (already outlet-capable end to end; this only exposes it).
		field.Bool("per_outlet_pricing_enabled").
			Default(false).
			Comment("Add-on: lets this tenant set a different base price per outlet for the same item"),
		// Add-on: stock-age/batch markdown pricing (Phase 2 of the same plan). Gated on the
		// platform-admin-granted "batch_period_pricing" feature the same way as above.
		field.Bool("batch_period_pricing_enabled").
			Default(false).
			Comment("Add-on: lets this tenant mark down old stock by receiving-batch age (clearance pricing)"),
		field.Int("stock_aging_threshold_days").
			Default(90).
			Comment("An item's oldest active lot older than this many days surfaces on the Aging Stock report as a clearance candidate"),
		// Hospitality module toggles
		field.Bool("enable_room_pricing").
			Default(false).
			Comment("Enable hotel room-type SERVICE items and nightly rate plans"),
		field.Bool("enable_facility_booking").
			Default(false).
			Comment("Enable facility/conference-hall SERVICE items and session rates"),
		field.Bool("enable_conference_packages").
			Default(false).
			Comment("Enable conference/event Bundle packages (DDR/RDR, meals included)"),
		field.Float("default_target_margin_percent").
			Optional().
			Nillable().
			Default(30.0).
			Comment("Default profit margin % for recipe costing when no per-recipe margin is set"),
		// Tax & compliance — treasury-api owns tax rates/codes (source of truth). Inventory stores
		// only the tenant's pricing convention + the default code reference; the rate is resolved
		// from treasury-api at read time and cached.
		field.Bool("prices_inclusive_of_tax").
			Default(false).
			Comment("When true, item selling prices are treated as VAT-inclusive; tax is back-computed from the price using the rate resolved from treasury-api"),
		field.String("default_tax_code").
			Optional().
			Comment("Default KRA/eTIMS tax code (e.g. VAT-16) applied to items missing one; resolved against treasury-api tax codes"),
		field.JSON("label_print_defaults", LabelPrintDefaults{}).
			Optional().
			Comment("Tenant's preferred label template/format for barcode printing (format, template, rotate) — see barcode.LabelTemplateByName"),
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
