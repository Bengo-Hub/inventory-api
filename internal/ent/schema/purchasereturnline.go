package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PurchaseReturnLine is a line on a PurchaseReturn (ERP PurchaseReturnedItem).
type PurchaseReturnLine struct{ ent.Schema }

func (PurchaseReturnLine) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.UUID("purchase_return_id", uuid.UUID{}),
		field.UUID("item_id", uuid.UUID{}).Comment("FK to Item (returned stock item)"),
		field.UUID("lot_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("FK to InventoryLot for lot-tracked items — lets an RTV target a specific expiring batch (pharmacy DAWA use-case)"),
		field.Float("quantity").Default(1).Comment("Quantity returned in the item's stock unit; fractional for weighed/measured items, same as GoodsReceiptLine quantities"),
		field.Float("sub_total").Default(0).Comment("Supplier returns: the credit value. Service returns: cost value snapshot at send (informational, never a credit)"),
		field.Float("received_back_qty").Default(0).Comment("Service returns: units already received back from repair and restocked"),
		field.Float("written_off_qty").Default(0).Comment("Service returns: units the provider could not repair and that will not come back"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (PurchaseReturnLine) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("purchase_return", PurchaseReturn.Type).Ref("lines").Field("purchase_return_id").Unique().Required(),
	}
}

func (PurchaseReturnLine) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "purchase_return_id"),
		index.Fields("item_id"),
	}
}
