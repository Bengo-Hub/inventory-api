package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Item holds the schema definition for inventory items.
type Item struct {
	ent.Schema
}

// Fields of the Item.
func (Item) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.String("sku").
			NotEmpty().
			Comment("Stock keeping unit, unique per tenant"),
		field.String("name").
			NotEmpty(),
		field.Text("description").
			Optional(),
		field.UUID("category_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Reference to ItemCategory"),
		field.UUID("unit_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Reference to Unit"),
		field.Enum("type").
			Values("GOODS", "SERVICE", "RECIPE", "INGREDIENT", "VOUCHER", "EQUIPMENT").
			Default("GOODS").
			Comment("Item type for master data classification: GOODS (Retail/Inventory), SERVICE (Non-stockable), RECIPE (Hospitality assembled), INGREDIENT (Raw material), VOUCHER (Digital), EQUIPMENT (Assets)"),
		field.Bool("is_active").
			Default(true),
		field.String("image_url").
			Optional(),
		field.JSON("tags", []string{}).
			Default([]string{}).
			Comment("Dietary, allergen, and custom tags (e.g. vegan, gluten_free, halal, contains_nuts)"),
		field.JSON("metadata", map[string]any{}).
			Default(map[string]any{}),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Item.
func (Item) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("tenant", Tenant.Type).
			Ref("items").
			Unique().
			Required().
			Field("tenant_id"),
		edge.To("balances", InventoryBalance.Type),
		edge.To("recipe_ingredients", RecipeIngredient.Type),
		edge.To("units", Unit.Type).
			Unique().
			Field("unit_id").
			Comment("Primary unit of measure"),
		edge.To("variants", ItemVariant.Type),
		edge.To("assets", ItemAsset.Type),
		edge.To("translations", ItemTranslation.Type),
		edge.To("modifier_groups", ModifierGroup.Type),
		edge.From("item_category", ItemCategory.Type).
			Ref("items").
			Unique().
			Field("category_id"),
	}
}

// Indexes of the Item.
func (Item) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "sku").Unique(),
		index.Fields("tenant_id", "category_id"),
		index.Fields("tenant_id", "is_active"),
	}
}
