package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ItemVariant holds the schema definition for item variations.
type ItemVariant struct {
	ent.Schema
}

// Fields of the ItemVariant.
func (ItemVariant) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("item_id", uuid.UUID{}).
			Comment("Parent item"),
		field.String("sku").
			NotEmpty().
			Comment("Variant specific SKU"),
		field.String("name").
			NotEmpty().
			Comment("e.g., Small, Blue, 500ml"),
		field.Float("price").
			Default(0).
			Comment("Variant specific price"),
		// Variant matrix attributes (Phase 1.2) — fashion: {size: M, color: Blue}
		field.JSON("attributes", map[string]string{}).
			Optional().
			Comment("Structured variant attributes: {size: M, color: Blue, material: Cotton}"),
		field.String("barcode").
			Optional().
			Comment("Variant-specific barcode (EAN-13/UPC)"),
		field.String("image_url").
			Optional().
			Comment("Variant-specific image"),
		field.Float("cost_price").
			Optional().
			Nillable().
			Comment("Variant cost for margin analysis"),
		field.Float("weight_kg").
			Optional().
			Nillable().
			Comment("Variant weight in kg for shipping"),
		field.Bool("is_active").
			Default(true),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the ItemVariant.
func (ItemVariant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("variants").
			Unique().
			Required().
			Field("item_id"),
	}
}

// Indexes of the ItemVariant.
func (ItemVariant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("item_id", "sku").Unique(),
		index.Fields("barcode"),
	}
}
