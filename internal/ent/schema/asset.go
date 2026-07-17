package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Asset is a fixed asset (migrated from ERP assets.Asset). Inventory-api owns the
// register/tracking; depreciation schedules + GL posting are owned by treasury-api
// (inventory emits inventory.asset.depreciation_due events). The financial fields
// here are display snapshots kept in sync from treasury posting events.
type Asset struct{ ent.Schema }

func (Asset) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.UUID("tenant_id", uuid.UUID{}),
		field.String("asset_tag").NotEmpty().Comment("Unique asset tag per tenant"),
		field.String("name").NotEmpty(),
		field.Text("description").Optional(),
		field.UUID("category_id", uuid.UUID{}).Optional().Nillable(),
		// Identification
		field.String("serial_number").Optional(),
		field.String("model").Optional(),
		field.String("manufacturer").Optional(),
		field.String("barcode").Optional(),
		// Financial snapshot (authoritative depreciation lives in treasury)
		field.Time("purchase_date").Optional().Nillable(),
		field.Float("purchase_cost").Default(0),
		field.Float("current_value").Default(0),
		field.Float("salvage_value").Default(0),
		field.Float("depreciation_rate").Default(0),
		field.Enum("depreciation_method").Values("straight_line", "declining_balance").Default("straight_line"),
		// KRA capital-allowance class (wear-and-tear pool) this asset belongs to, e.g.
		// CA_CLASS_1 (37.5%), CA_CLASS_2 (30%), CA_CLASS_3 (25%), CA_CLASS_4 (computers),
		// CA_IBA (2.5%), CA_COMMERCIAL_BLDG. Inventory owns the asset and its KRA class; the
		// value is carried on inventory.asset.created/updated so treasury auto-classifies the
		// synced CapitalAllowanceAsset instead of parking it UNCLASSIFIED (zero allowance).
		field.String("kra_ca_class").Optional().Comment("KRA capital-allowance class code (treasury computes the allowance from it)"),
		field.Float("accumulated_depreciation").Default(0),
		field.Float("book_value").Default(0),
		field.String("last_depreciation_period").Optional().Comment("YYYY-MM of the last applied depreciation (idempotency guard)"),
		// Location & assignment
		field.String("location").Optional(),
		field.UUID("outlet_id", uuid.UUID{}).Optional().Nillable().Comment("Branch/outlet"),
		field.UUID("assigned_to", uuid.UUID{}).Optional().Nillable(),
		field.UUID("custodian_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("item_id", uuid.UUID{}).Optional().Nillable().Comment("Optional link to the inventory Item this asset was capitalised from"),
		// Status & condition
		field.Enum("status").Values("active", "inactive", "maintenance", "disposed", "lost", "damaged", "retired").Default("active"),
		field.String("condition").Optional().Comment("excellent|good|fair|poor|critical"),
		// Maintenance & warranty
		field.Time("warranty_expiry").Optional().Nillable(),
		field.Time("last_maintenance").Optional().Nillable(),
		field.Time("next_maintenance").Optional().Nillable(),
		field.String("maintenance_schedule").Optional().Comment("monthly|quarterly|yearly"),
		field.Text("notes").Optional(),
		field.Bool("is_active").Default(true),
		field.UUID("created_by", uuid.UUID{}).Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Asset) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "asset_tag").Unique(),
		index.Fields("tenant_id", "status"),
		index.Fields("tenant_id", "category_id"),
		index.Fields("serial_number"),
		index.Fields("barcode"),
	}
}
