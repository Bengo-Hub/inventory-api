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
		field.UUID("brand_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Reference to ItemBrand"),
		field.String("manufacturer").
			Optional().
			Comment("Manufacturer — retail/pharmacy"),
		field.String("model").
			Optional().
			Comment("Product/item model — retail only"),
		// E-commerce / online-store attributes (additive; safe for online auto-migrate).
		field.String("gtin").
			Optional().
			Comment("Global Trade Item Number (GTIN-8/12/13/14) for marketplace feeds"),
		field.String("mpn").
			Optional().
			Comment("Manufacturer Part Number — identifies a product across suppliers"),
		field.Enum("condition").
			Values("NEW", "REFURBISHED", "USED", "OPEN_BOX").
			Default("NEW").
			Comment("Product condition for e-commerce listings"),
		field.String("slug").
			Optional().
			Comment("URL-friendly identifier for storefront product pages / SEO"),
		field.Text("short_description").
			Optional().
			Comment("Short product-card description (long-form lives in `description`)"),
		field.String("meta_title").
			Optional().
			Comment("SEO meta title"),
		field.Text("meta_description").
			Optional().
			Comment("SEO meta description"),
		field.String("country_of_origin").
			Optional().
			Comment("ISO country of origin — customs / marketplace compliance"),
		field.String("hs_code").
			Optional().
			Comment("Harmonised System tariff code for customs"),
		field.Bool("is_returnable").
			Default(true).
			Comment("Whether the item can be returned by a customer"),
		field.Int("return_window_days").
			Optional().
			Nillable().
			Comment("Days after purchase a return is accepted (nil = tenant default)"),
		field.Bool("allow_backorder").
			Default(false).
			Comment("Allow ordering when out of stock (e-commerce pre-order)"),
		field.Bool("is_discontinued").
			Default(false).
			Comment("Hidden from new listings but remaining stock still sellable"),
		// Non-billable items are never charged at the point of sale, even when a selling
		// price is set: free accompaniments (ugali, greens) and consumable supplies (tissue,
		// packaging) that must reach the POS terminal and deduct stock without billing the
		// customer. Downstream catalogs (pos-api) force their price to 0.
		field.Bool("non_billable").
			Default(false).
			Comment("Never charged at POS even if a selling price exists (free accompaniments, supplies); stock still deducts"),
		// Not-for-sale items are stocked/purchased/counted like any other item but are
		// EXCLUDED from every sales surface (POS terminal, back-office sales, ordering
		// storefront): raw ingredients bought pre-portioned, cleaning supplies, internal
		// consumables. Distinct from non_billable (which still reaches the POS at price 0)
		// and from is_active=false (which hides the item everywhere including stock).
		field.Bool("not_for_sale").
			Default(false).
			Comment("Excluded from ALL sales surfaces (POS, ordering); still stockable/purchasable — ingredients, internal supplies"),
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
			Values("RETAIL", "PHARMACY", "FOOD_BEVERAGE", "HOSPITALITY_ROOM", "HOSPITALITY_FACILITY", "CONFERENCE", "SALON_SERVICE", "AMENITY").
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
		field.Int("shelf_life_days").
			Optional().
			Nillable().
			Comment("Default shelf life in days for perishables — seeds lot expiry_date at goods receipt"),
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
		// KRA eTIMS catalog classification — curated on the inventory item (source of truth)
		// and carried on item.created/updated events so treasury-api registers the item with
		// real codes instead of hardcoded defaults.
		field.String("etims_item_cls_cd").
			Optional().
			Nillable().
			Comment("KRA eTIMS item classification code (itemClsCd) — 10-digit UNSPSC leaf from KRA selectItemClass, e.g. 5020230602"),
		field.String("etims_pkg_unit_cd").
			Optional().
			Nillable().
			Comment("KRA eTIMS packaging unit code (pkgUnitCd) from the PKG_UNIT code list, e.g. NT/CT/BX"),
		field.String("etims_qty_unit_cd").
			Optional().
			Nillable().
			Comment("KRA eTIMS quantity unit code (qtyUnitCd) from the QTY_UNIT code list, e.g. U/KG/LTR — per-item override of the Unit table's kra_qty_unit_cd mapping"),
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
		field.UUID("preferred_supplier_id", uuid.UUID{}).
			Optional().
			Nillable().
			Comment("Preferred Supplier for procurement — drives per-vendor PO split in procure-to-order; nil = unassigned"),
		field.Float("yield_pct").
			Optional().
			Nillable().
			Default(1.0).
			Comment("Usable fraction after trim/cooking loss — 0 < y <= 1. EP cost = purchase_price / pack_size / yield_pct"),
		// Content-per-unit — bridges count-stocked packaged goods (a 750ml whiskey bottle
		// stocked in pieces) to volume/mass recipe lines (a 30ml tot). A recipe line of
		// 30 ml deducts 30/750 = 0.04 pieces; 25 tots deplete exactly one bottle.
		field.Float("unit_content_qty").
			Optional().
			Nillable().
			Comment("How much of unit_content_uom ONE stock unit contains (e.g. 750 for a 750ml bottle stocked in pieces)"),
		field.String("unit_content_uom").
			Optional().
			MaxLen(20).
			Comment("Unit of the per-stock-unit content (e.g. 'ml', 'g') — enables cross-dimension recipe deduction"),
		// Reusable menu components: a RECIPE-type item flagged here may be picked as an
		// ingredient in OTHER recipes (e.g. Black Tea 30 ml inside an Iced Passion Tea).
		// The deduction path already handles it (sub-recipe auto-link + backflush); this
		// flag only controls which RECIPE items surface in the recipe-ingredient picker so
		// the whole menu doesn't flood it. GOODS/INGREDIENT are always pickable.
		field.Bool("usable_in_recipes").
			Default(false).
			Comment("RECIPE-type items only: may be used as an ingredient/sub-recipe input in other recipes (surfaces in the recipe-ingredient picker)"),
		// Stock tracking mode — AccuPOS/Square-style per-item depletion control.
		// "default": RECIPE-type items follow TenantInventoryConfig.recipe_items_non_depleting_default,
		// everything else is tracked. Explicit "tracked"/"non_depleting" always wins.
		field.Enum("stock_tracking_mode").
			Values("default", "tracked", "non_depleting").
			Default("default").
			Comment("Depletion behavior: default (tenant policy for recipes), tracked (always deplete), non_depleting (sell without stock effect; excluded from stock-out cascade)"),
		// Selling-price guardrails (Phase 4) — hard floor/ceiling enforced at price upsert AND POS sale.
		field.Float("min_selling_price").
			Optional().
			Nillable().
			Comment("Hard minimum selling price (KES). Prices below this are rejected at price upsert and require manager approval at POS"),
		field.Float("max_selling_price").
			Optional().
			Nillable().
			Comment("Hard maximum selling price (KES). Prices above this are rejected at price upsert and require manager approval at POS"),
		field.Float("target_margin_percent").
			Optional().
			Nillable().
			Comment("Desired profit margin % for GOODS auto-pricing — suggested_price = cost_price / (1 - margin/100)"),
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
		// End-of-Life (EOL) lifecycle — generic, tenant-scoped. Non-null = the item is marked
		// End-of-Life: it is set is_active=false at the same time (so it disappears from item
		// lists, the POS live catalog, and ordering), surfaces only under the dedicated EOL
		// listing (status=eol), and is hard-deleted by the purge scheduler once this timestamp
		// is older than the retention window (default 7 days). Cleared on restore.
		field.Time("end_of_life_at").
			Optional().
			Nillable().
			Comment("When the item was marked End-of-Life. Non-null = EOL (hidden everywhere); purged after the EOL retention window; cleared on restore"),
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
		edge.From("item_brand", ItemBrand.Type).
			Ref("items").
			Unique().
			Field("brand_id"),
		edge.From("preferred_supplier", Supplier.Type).
			Ref("preferred_items").
			Unique().
			Field("preferred_supplier_id").
			Comment("Preferred supplier for procurement (procure-to-order per-vendor PO split)"),
	}
}

// Indexes of the Item.
func (Item) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id", "sku").Unique(),
		index.Fields("tenant_id", "category_id"),
		index.Fields("tenant_id", "brand_id"),
		index.Fields("tenant_id", "is_active"),
		index.Fields("tenant_id", "end_of_life_at"),
		index.Fields("tenant_id", "barcode"),
		index.Fields("tenant_id", "created_at"),
		index.Fields("tenant_id", "unit_id"),
		index.Fields("tenant_id", "preferred_supplier_id"),
	}
}
