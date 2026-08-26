package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ConsumptionLine is a normalized, per-ingredient-per-recipe row written alongside each
// Consumption record. Consumption.items stays the flattened JSON audit/idempotency record
// it already was; ConsumptionLine restores the recipe/finished-item attribution that BOM
// explosion computes in memory but the JSON blob discards — the piece the ingredient
// utilization report and the food-cost variance report both need to answer "how much of
// ingredient X went into recipe Y over time Z."
type ConsumptionLine struct {
	ent.Schema
}

// Fields of the ConsumptionLine.
func (ConsumptionLine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("consumption_id", uuid.UUID{}).
			Comment("The parent Consumption row this line belongs to"),
		field.UUID("order_id", uuid.UUID{}).
			Comment("The order that triggered consumption (denormalized for direct range queries)"),
		field.String("order_number").
			Optional().
			Comment("Human-readable POS order/receipt number (e.g. \"10707\"), denormalized from pos.sale.finalized so the stock-history ledger can show it without a cross-service call"),
		field.String("customer_name").
			Optional().
			Comment("Buyer name snapshot from the triggering sale (\"Walk-in\" when absent) — the stock-history ledger's Customer/Supplier column for a sale row"),
		field.String("customer_phone").
			Optional(),
		field.UUID("served_by_user_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("The cashier/staff who served the sale (POSOrder.served_by_user_id), for the stock-history ledger's User column"),
		field.String("served_by_name").
			Optional().
			Comment("Denormalized display name for served_by_user_id — avoids an S2S lookup per ledger row"),
		field.UUID("warehouse_id", uuid.UUID{}).
			Optional().
			Nillable(),
		field.UUID("outlet_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Resolved from warehouse at write time, so reports don't need a join"),
		field.UUID("recipe_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Null for a directly-sold item with no BOM — the ingredient IS the finished item"),
		field.String("recipe_sku").
			Optional().
			Comment("Denormalized recipe SKU for display without a join"),
		field.String("finished_item_sku").
			Comment("The menu/sale-line item actually sold; equals ingredient_sku when recipe_id is null"),
		field.UUID("ingredient_item_id", uuid.UUID{}).
			Comment("The raw stock item that was decremented"),
		field.String("ingredient_sku"),
		field.Float("quantity").
			Comment("Amount consumed, in the ingredient's stock (base) unit"),
		field.String("unit").
			Optional(),
		field.Float("unit_cost").
			Default(0).
			Comment("Item.cost_price snapshot at consumption time — freezes the value so later cost_price drift doesn't rewrite history"),
		field.Float("total_cost").
			Default(0).
			Comment("quantity * unit_cost, precomputed for fast aggregation"),
		field.Bool("theoretical").
			Default(false).
			Comment("Mirrors ConsumptionItemJSON.Theoretical: usage recorded, no balance decremented"),
		field.String("reason").
			Default("sale"),
		field.Time("consumed_at").
			Comment("Bucketing key for the trend chart; mirrors Consumption.processed_at"),
		// Lot/batch traceability (pharmacy DAWA use-case, Phase 2): which InventoryLot this
		// line actually drew from. A single sale line spanning two lots at a FEFO boundary
		// produces two ConsumptionLine rows, one per lot. Nil for non-lot-tracked items
		// (wavg costing method, or lots exhausted before this draw). This is what makes
		// "which sold orders contain batch X" answerable — previously consumeLots decremented
		// InventoryLot.quantity with no record of which consumption drew from it.
		field.UUID("lot_id", uuid.UUID{}).
			Optional().
			Nillable(),
		field.String("lot_number").
			Optional(),
		field.Time("expiry_date").
			Optional().
			Nillable(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Indexes of the ConsumptionLine.
func (ConsumptionLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "ingredient_item_id", "consumed_at"),
		index.Fields("tenant_id", "recipe_id", "consumed_at"),
		index.Fields("tenant_id", "order_id"),
		index.Fields("consumption_id"),
		index.Fields("tenant_id", "lot_id"),
	}
}
