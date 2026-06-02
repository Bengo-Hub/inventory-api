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
		// Use-case classification (Phase: hospitality) — refines how a sellable item is presented/priced.
		// Orthogonal to `type`; hotel room-types/facilities/amenities are SERVICE items with a hospitality use_case.
		field.Enum("use_case").
			Values("RETAIL", "FOOD_BEVERAGE", "HOSPITALITY_ROOM", "HOSPITALITY_FACILITY", "CONFERENCE", "SALON_SERVICE", "AMENITY").
			Default("RETAIL").
			Comment("Sellable use-case: drives hospitality pricing/booking semantics. pos-api references these masters via inventory_item_id"),
		// Room rate-plan attributes (HOSPITALITY_ROOM use_case)
		field.Enum("meal_plan").
			Values("RO", "BB", "HB", "FB", "AI").
			Optional().
			Nillable().
			Comment("Rate-plan inclusion: RO=room only, BB=bed&breakfast, HB=half board, FB=full board, AI=all inclusive"),
		field.Enum("occupancy_basis").
			Values("per_person_sharing", "per_room").
			Optional().
			Nillable().
			Comment("Pricing basis for room-type items"),
		field.Int("max_adults").
			Optional().
			Nillable().
			Comment("Max adult occupancy for a room-type item"),
		field.Int("max_children").
			Optional().
			Nillable().
			Comment("Max child occupancy for a room-type item"),
		field.Bool("extra_bed_allowed").
			Default(false).
			Comment("Whether an extra bed can be added to this room-type"),
		field.Float("single_supplement").
			Optional().
			Nillable().
			Comment("Surcharge (KES) for single occupancy on a per_person_sharing rate"),
		field.Bool("is_active").
			Default(true),
		field.String("image_url").
			Optional(),
		// Barcode management (Phase 1.1)
		field.String("barcode").
			Optional().
			Comment("EAN-13/UPC barcode for scanning"),
		field.String("barcode_type").
			Optional().
			Comment("EAN13, UPC, CODE128, QR"),
		// Compliance flags (Phase 1.4) — supports liquor stores, pharmacies, electronics
		field.Bool("requires_age_verification").
			Default(false).
			Comment("Liquor, tobacco, 18+ items"),
		field.Bool("is_controlled_substance").
			Default(false).
			Comment("Pharmacy: scheduled drugs requiring special handling"),
		field.Bool("is_perishable").
			Default(false).
			Comment("Requires expiry/lot tracking — bakeries, pharmacies, food"),
		field.Bool("track_serial_numbers").
			Default(false).
			Comment("Electronics, equipment — require serial at sale"),
		field.Bool("track_lots").
			Default(false).
			Comment("Pharma batches, food lots — require lot/expiry tracking"),
		// Physical attributes (Phase 1.4) — shipping/logistics
		field.Float("weight_kg").
			Optional().
			Nillable().
			Comment("Weight in kg for shipping/logistics pricing"),
		field.JSON("dimensions_cm", map[string]float64{}).
			Optional().
			Comment("Physical dimensions {length, width, height} in cm"),
		// Service attributes (Phase 1.4) — salons, barber shops
		field.Int("duration_minutes").
			Optional().
			Nillable().
			Comment("Service duration for appointment booking (salon, barber)"),
		field.JSON("tags", []string{}).
			Default([]string{}).
			Comment("Dietary, allergen, and custom tags (e.g. vegan, gluten_free, halal, contains_nuts)"),
		// KRA eTIMS / tax fields (Phase 3) — enable correct tax calculation in treasury
		field.String("tax_code_id").
			Optional().
			Comment("KRA eTIMS tax category code (e.g. VAT16, VAT8, EXM, ZER) for tax calculation"),
		field.Bool("tax_inclusive").
			Default(false).
			Comment("True if selling price already includes VAT; treasury back-calculates tax portion"),
		field.Float("cost_price").
			Optional().
			Nillable().
			Comment("Edible-portion cost per base unit (KES). Auto-computed when purchase fields are set; otherwise manually entered"),
		// Purchase / supplier fields — enable auto EP-cost calculation
		field.Float("purchase_price").
			Optional().
			Nillable().
			Comment("Price paid per purchase_unit (KES) — e.g. 750 per kg"),
		field.Float("purchase_pack_size").
			Optional().
			Nillable().
			Comment("Base units per purchase_unit — e.g. 1 kg = 1000 g. cost_price = purchase_price / pack_size / yield_pct"),
		field.String("purchase_unit").
			Optional().
			MaxLen(50).
			Comment("How the ingredient is bought — e.g. 'kg', 'litre', 'crate'"),
		field.Float("yield_pct").
			Optional().
			Nillable().
			Default(1.0).
			Comment("Usable fraction after trim/cooking loss — 0 < y <= 1. EP cost = purchase_price / pack_size / yield_pct"),
		// Event capacity fields (Phase 2) — SERVICE-type items only
		field.Int("total_capacity").
			Optional().
			Nillable().
			Comment("Total seats/tickets for SERVICE-type event items"),
		field.Int("booked_capacity").
			Optional().
			Nillable().
			Default(0).
			Comment("Confirmed bookings against total_capacity"),
		field.Time("event_start_at").
			Optional().
			Nillable().
			Comment("Event start datetime for SERVICE-type event items"),
		field.Time("event_end_at").
			Optional().
			Nillable().
			Comment("Event end datetime for SERVICE-type event items"),
		field.String("event_venue").
			Optional().
			Nillable().
			MaxLen(500).
			Comment("Venue name/address for event items"),
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
		edge.To("lots", InventoryLot.Type),
		edge.To("custom_field_values", CustomFieldValue.Type),
		edge.To("bundle", Bundle.Type).Unique(),
		edge.To("bundle_components", BundleComponent.Type).
			Comment("Items where this item is a component in a bundle"),
		edge.To("warranties", Warranty.Type),
		edge.To("produced_by_recipe", Recipe.Type).
			Unique().
			Comment("The Recipe (BOM) that produces this RECIPE-type item"),
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
		index.Fields("tenant_id", "barcode"),
		index.Fields("tenant_id", "created_at"),
		index.Fields("tenant_id", "unit_id"),
	}
}
