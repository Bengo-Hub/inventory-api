package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Bundle holds the schema for pre-packaged item kits/bundles.
// Unlike Recipes (assembled from ingredients), Bundles are groups of existing items
// sold as a single unit — e.g., "Back to School Kit" (notebook + pen + ruler).
type Bundle struct {
	ent.Schema
}

// Fields of the Bundle.
func (Bundle) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.UUID("tenant_id", uuid.UUID{}).
			Comment("Owning tenant"),
		field.UUID("item_id", uuid.UUID{}).
			Comment("The bundle item SKU (type=GOODS with is_bundle=true)"),
		field.String("name").
			NotEmpty(),
		// Hospitality package attributes — conference/event/room rate plans.
		// A plain retail kit leaves these at their defaults.
		field.Enum("package_type").
			Values("RETAIL_KIT", "ROOM_RATE_PLAN", "DDR", "RDR", "HALF_BOARD", "FULL_BOARD", "HALL_HIRE_ONLY", "SERVICE_SESSIONS").
			Default("RETAIL_KIT").
			Comment("Package classification: DDR=day delegate rate, RDR=residential delegate rate, SERVICE_SESSIONS=prepaid session bundle"),
		field.Enum("price_basis").
			Values("flat", "per_delegate_per_day", "per_person_sharing", "per_session").
			Default("flat").
			Comment("How the bundle price is interpreted at point of sale"),
		field.Int("min_delegates").
			Optional().
			Nillable().
			Comment("Minimum chargeable delegates for DDR/RDR packages"),
		field.Bool("accommodation_included").
			Default(false).
			Comment("True for residential (RDR) packages that bundle a room stay"),
		field.Int("sessions_total").
			Optional().
			Nillable().
			Comment("Total prepaid sessions for SERVICE_SESSIONS packages (replaces pos-api ServicePackage.sessions_total)"),
		field.Int("validity_days").
			Optional().
			Nillable().
			Comment("Days from purchase before a SERVICE_SESSIONS package expires"),
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

// Edges of the Bundle.
func (Bundle) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("item", Item.Type).
			Ref("bundle").
			Field("item_id").
			Unique().
			Required(),
		edge.To("components", BundleComponent.Type),
	}
}

// Indexes of the Bundle.
func (Bundle) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "item_id").Unique(),
		index.Fields("tenant_id", "is_active"),
	}
}
