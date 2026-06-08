package handlers

import (
	"fmt"
	"net/http"

	excelize "github.com/xuri/excelize/v2"
	"go.uber.org/zap"
)

// ImportTemplate handles GET /inventory/import-template.
// Returns a pre-populated XLSX workbook with headers, one example row per sheet,
// and a README sheet describing every field.
//
//	@Summary      Download inventory import template
//	@Tags         inventory
//	@Produce      application/vnd.openxmlformats-officedocument.spreadsheetml.sheet
//	@Success      200  {file}  binary
//	@Router       /{tenant}/inventory/import-template [get]
func (h *InventoryHandler) ImportTemplate(w http.ResponseWriter, r *http.Request) {
	xl := excelize.NewFile()
	defer xl.Close()

	// ── Sheet: Items ──────────────────────────────────────────────────────────
	itemHeaders := []string{
		"sku", "name", "type", "description", "category_name", "unit_name",
		"cost_price", "selling_price", "is_perishable", "track_lots",
		"reorder_level", "reorder_quantity", "barcode", "tags", "image_url",
		"requires_age_verification", "is_active", "initial_quantity", "warehouse_name",
		// Extended (full Item alignment)
		"use_case", "tax_code_id", "tax_inclusive",
		"purchase_price", "purchase_pack_size", "purchase_unit", "yield_pct",
		"weight_kg", "dimensions_cm",
		"meal_plan", "occupancy_basis", "max_adults", "max_children", "extra_bed_allowed", "single_supplement",
		"total_capacity", "event_start_at", "event_end_at", "event_venue",
	}
	itemExample := []any{
		"INGR-BEEF-STEAK", "Beef steak (sirloin)", "INGREDIENT",
		"Butchery retail", "Raw Ingredients", "g",
		0.9375, "", "TRUE", "FALSE",
		100, 500, "", "halal", "",
		"FALSE", "TRUE", 0, "",
		"FOOD_BEVERAGE", "", "FALSE",
		750, 1000, "kg", 0.8,
		"", "",
		"", "", "", "", "", "",
		"", "", "", "",
	}
	itemExampleGoods := []any{
		"SOD001", "Soda 300ml", "GOODS",
		"Resale beverage", "Soft Drink", "each",
		35, 150, "FALSE", "FALSE",
		10, 50, "", "", "",
		"FALSE", "TRUE", 24, "",
		"FOOD_BEVERAGE", "", "FALSE",
		35, 1, "each", 1,
		0.33, "",
		"", "", "", "", "", "",
		"", "", "", "",
	}
	itemExampleRecipe := []any{
		"BEE006", "Beef Grilled (200g)", "RECIPE",
		"Grilled sirloin steak", "Main Dishes", "portion",
		227.73, 900, "FALSE", "FALSE",
		0, 0, "", "", "",
		"FALSE", "TRUE", 0, "",
		"FOOD_BEVERAGE", "", "FALSE",
		"", "", "", "",
		"", "",
		"", "", "", "", "", "",
		"", "", "", "",
	}
	xl.NewSheet("Items")
	writeXLSXHeader(xl, "Items", itemHeaders)
	writeXLSXRow(xl, "Items", 2, itemExample)
	writeXLSXRow(xl, "Items", 3, itemExampleGoods)
	writeXLSXRow(xl, "Items", 4, itemExampleRecipe)

	// ── Sheet: RecipeIngredients ──────────────────────────────────────────────
	riHeaders := []string{
		"recipe_sku", "ingredient_sku", "quantity", "unit_name",
		"waste_percent", "notes", "display_order",
	}
	riExample := []any{"BEE006", "INGR-BEEF-STEAK", 200.0, "g", 0, "200g raw", 1}
	xl.NewSheet("RecipeIngredients")
	writeXLSXHeader(xl, "RecipeIngredients", riHeaders)
	writeXLSXRow(xl, "RecipeIngredients", 2, riExample)

	// ── Sheet: ModifierGroups ─────────────────────────────────────────────────
	mgHeaders := []string{
		"item_sku", "group_name", "is_required",
		"min_selections", "max_selections", "display_order",
	}
	mgExample := []any{"PIZ003", "Pizza Extras", "FALSE", 0, 5, 1}
	xl.NewSheet("ModifierGroups")
	writeXLSXHeader(xl, "ModifierGroups", mgHeaders)
	writeXLSXRow(xl, "ModifierGroups", 2, mgExample)

	// ── Sheet: ModifierOptions ────────────────────────────────────────────────
	moHeaders := []string{
		"item_sku", "group_name", "option_name",
		"price_adjustment", "option_sku", "is_default", "display_order",
	}
	moExample := []any{"PIZ003", "Pizza Extras", "Extra Cheese", 200, "INGR-CHEESE-SLICE", "FALSE", 1}
	xl.NewSheet("ModifierOptions")
	writeXLSXHeader(xl, "ModifierOptions", moHeaders)
	writeXLSXRow(xl, "ModifierOptions", 2, moExample)

	// ── Sheet: InitialStock ───────────────────────────────────────────────────
	isHeaders := []string{
		"item_sku", "warehouse_name", "quantity", "lot_number", "expiry_date",
	}
	isExample := []any{"INGR-BEEF-STEAK", "Main Warehouse", 5000, "", ""}
	xl.NewSheet("InitialStock")
	writeXLSXHeader(xl, "InitialStock", isHeaders)
	writeXLSXRow(xl, "InitialStock", 2, isExample)

	// ── Sheet: README ─────────────────────────────────────────────────────────
	xl.NewSheet("README")
	readme := [][]any{
		{"INVENTORY BULK IMPORT TEMPLATE"},
		{""},
		{"SHEET", "DESCRIPTION"},
		{"Items", "Master item list. type = GOODS | INGREDIENT | RECIPE | SERVICE | VOUCHER | EQUIPMENT"},
		{"RecipeIngredients", "BOM lines. recipe_sku must exist in Items. quantity = per-serving."},
		{"ModifierGroups", "Modifier groups attached to items (e.g. 'Pizza Extras', 'Burger Extras')."},
		{"ModifierOptions", "Individual options inside a group. price_adjustment is the delta in KES."},
		{"InitialStock", "Opening stock quantities (absolute, not delta). Leave blank if using Items.initial_quantity."},
		{""},
		{"ITEMS — FIELD REFERENCE"},
		{"Field", "Required", "Type", "Notes"},
		{"sku", "YES", "string", "Unique per tenant. Alphanumeric + hyphens. Max 40 chars."},
		{"name", "YES", "string", "Display name."},
		{"type", "NO", "string", "Default: GOODS. Options: GOODS, INGREDIENT, RECIPE, SERVICE, VOUCHER, EQUIPMENT"},
		{"description", "NO", "string", ""},
		{"category_name", "NO", "string", "Auto-created if not found."},
		{"unit_name", "NO", "string", "Auto-created if not found. E.g. g, ml, kg, each, portion"},
		{"cost_price", "NO", "number", "Purchase/EP cost in KES."},
		{"selling_price", "NO", "number", "Suggested sell price in KES (stored as suggested_price on the item)."},
		{"is_perishable", "NO", "TRUE/FALSE", "Default FALSE."},
		{"track_lots", "NO", "TRUE/FALSE", "Default FALSE."},
		{"reorder_level", "NO", "integer", "Trigger reorder when stock falls below this."},
		{"reorder_quantity", "NO", "integer", "Quantity to reorder."},
		{"barcode", "NO", "string", ""},
		{"tags", "NO", "string", "Comma-separated. E.g. halal,vegan"},
		{"requires_age_verification", "NO", "TRUE/FALSE", "Set TRUE for alcohol/tobacco."},
		{"is_active", "NO", "TRUE/FALSE", "Default TRUE."},
		{"initial_quantity", "NO", "integer", "Set opening stock when creating new items."},
		{"warehouse_name", "NO", "string", "Leave blank to use primary warehouse."},
		{"use_case", "NO", "string", "RETAIL | PHARMACY | FOOD_BEVERAGE | HOSPITALITY_ROOM | HOSPITALITY_FACILITY | CONFERENCE | SALON_SERVICE | AMENITY. Drives POS classification."},
		{"tax_code_id", "NO", "string", "KRA eTIMS tax category code (e.g. VAT16). Resolved against treasury tax codes."},
		{"tax_inclusive", "NO", "TRUE/FALSE", "TRUE if selling_price already includes tax. Default FALSE."},
		{"purchase_price", "NO", "number", "Supplier price per purchase unit (KES). Used with pack size + yield to auto-calc EP cost."},
		{"purchase_pack_size", "NO", "number", "Base units per purchase unit (e.g. 1kg = 1000g)."},
		{"purchase_unit", "NO", "string", "Purchase UoM (kg, litre, crate, ...)."},
		{"yield_pct", "NO", "number", "Usable fraction after trim/loss (0 < y <= 1). E.g. 0.8 = 80%."},
		{"weight_kg", "NO", "number", "Shipping/logistics weight."},
		{"dimensions_cm", "NO", "string", "LxWxH in cm, e.g. 30x20x10."},
		{"meal_plan", "NO", "string", "HOSPITALITY_ROOM only: RO | BB | HB | FB | AI."},
		{"occupancy_basis", "NO", "string", "Room basis: per_person_sharing | per_room."},
		{"max_adults", "NO", "integer", "Room max adult occupancy."},
		{"max_children", "NO", "integer", "Room max child occupancy."},
		{"extra_bed_allowed", "NO", "TRUE/FALSE", "Room: extra bed allowed."},
		{"single_supplement", "NO", "number", "Surcharge for single occupancy on per_person_sharing rooms."},
		{"total_capacity", "NO", "integer", "Event/SERVICE total seats or tickets."},
		{"event_start_at", "NO", "datetime", "Event start (YYYY-MM-DD or RFC3339)."},
		{"event_end_at", "NO", "datetime", "Event end (YYYY-MM-DD or RFC3339)."},
		{"event_venue", "NO", "string", "Event venue name/address."},
	}
	for i, rowData := range readme {
		for j, val := range rowData {
			cell, _ := excelize.CoordinatesToCellName(j+1, i+1)
			xl.SetCellValue("README", cell, fmt.Sprint(val))
		}
	}

	// Remove the default Sheet1 created by excelize.
	xl.DeleteSheet("Sheet1")

	// Stream back as XLSX download.
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="inventory_import_template.xlsx"`)
	if err := xl.Write(w); err != nil {
		h.log.Error("failed to write import template", zap.Field(zap.Error(err)))
	}
}

// ─── excelize write helpers ───────────────────────────────────────────────────

func writeXLSXHeader(xl *excelize.File, sheet string, headers []string) {
	for j, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(j+1, 1)
		xl.SetCellValue(sheet, cell, h)
	}
}

func writeXLSXRow(xl *excelize.File, sheet string, row int, vals []any) {
	for j, v := range vals {
		cell, _ := excelize.CoordinatesToCellName(j+1, row)
		xl.SetCellValue(sheet, cell, v)
	}
}
