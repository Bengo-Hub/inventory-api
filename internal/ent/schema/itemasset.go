package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ItemAsset holds the schema definition for item media and assets.
type ItemAsset struct {
	ent.Schema
}

// Fields of the ItemAsset.
func (ItemAsset) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("item_id", uuid.UUID{}).
			Comment("Reference to the parent item"),
		field.String("asset_type").
			Comment("IMAGE | VIDEO | DOCUMENT | 3D_MODEL"),
		field.String("url").
			NotEmpty().
			Comment("Publicly accessible URL or storage path"),
		field.String("file_name").
			Optional().
			Comment("Original filename"),
		field.String("file_size").
			Optional().
			Comment("Size in bytes/KB/MB"),
		field.String("mime_type").
			Optional().
			Comment("e.g. image/jpeg, video/mp4"),
		field.JSON("metadata", map[string]any{}).
			Optional().
			Comment("Additional metadata like dimensions, duration, alt-text"),
		field.Int("display_order").
			Default(0).
			Comment("Order in which to display the asset"),
		field.Bool("is_primary").
			Default(false).
			Comment("Whether this is the main image/asset for the item"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the ItemAsset.
func (ItemAsset) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("assets").
			Field("item_id").
			Unique().
			Required(),
	}
}

// Indexes of the ItemAsset.
func (ItemAsset) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("item_id"),
		index.Fields("asset_type"),
		index.Fields("is_primary"),
	}
}
