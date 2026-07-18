package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Unit holds the schema definition for units of measure.
type Unit struct {
	ent.Schema
}

// Fields of the Unit.
func (Unit) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.String("name").
			NotEmpty().
			Unique().
			Comment("Unit name, e.g., KG, Gram, Piece"),
		field.String("abbreviation").
			Optional().
			Comment("Short name, e.g., kg, g, pc"),
		field.String("type").
			Optional().
			Comment("Unit type: weight, volume, count, length, area, other"),
		field.String("kra_qty_unit_cd").
			Optional().
			Comment("KRA eTIMS quantity-unit code (qtyUnitCd) this unit maps to, e.g. KG→KG, Piece→U, Litre→LTR; items may override per-item via etims_qty_unit_cd"),
		// Use-case relevance tags. Units are a GLOBAL reference table, but a culinary
		// unit (tot, pot, portion) is noise in a pharmacy or retail outlet — tagged
		// units only surface for outlets of those use_cases; EMPTY = universal (kg, g,
		// L, ml, pc…). Stamped from the creating outlet's use_case on quick-create.
		field.JSON("use_cases", []string{}).
			Optional().
			Comment("Outlet use_cases this unit is relevant to (hospitality, pharmacy, retail…); empty = all"),
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

// Edges of the Unit.
func (Unit) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("items", Item.Type).
			Ref("units"),
		edge.To("recipe_ingredients", RecipeIngredient.Type),
	}
}

// Indexes of the Unit.
func (Unit) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
		// Units are a GLOBAL reference table (shared across all tenants — see
		// units.Service, whose tenantID params are intentionally unused), so this is a
		// true system-wide uniqueness guarantee, not per-tenant. Case-insensitive
		// duplicate rejection is additionally enforced in units.Service.CreateUnit /
		// UpdateUnit (EqualFold pre-check) since Postgres unique indexes are
		// case-sensitive. NULL abbreviations (no abbreviation given) never collide.
		index.Fields("abbreviation").Unique(),
	}
}
