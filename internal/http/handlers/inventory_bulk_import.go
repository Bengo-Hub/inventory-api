package handlers

import (
	"encoding/csv"
	"fmt"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/modifiers"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/bengobox/inventory-service/internal/modules/units"
	"github.com/google/uuid"
	excelize "github.com/xuri/excelize/v2"
	"go.uber.org/zap"
)

// BulkImportResult summarises per-entity import counts returned by /bulk-import.
type BulkImportResult struct {
	Items     importResult `json:"items"`
	Recipes   importResult `json:"recipes"`
	Modifiers importResult `json:"modifiers"`
	Stock     importResult `json:"stock"`
}

// BulkImport handles POST /inventory/bulk-import.
// Accepts .csv (items only, all columns) or .xlsx/.xlsm (5-sheet: Items,
// RecipeIngredients, ModifierGroups, ModifierOptions, InitialStock).
//
//	@Summary      Bulk import inventory items, recipes, modifiers and opening stock
//	@Tags         inventory
//	@Accept       multipart/form-data
//	@Produce      json
//	@Param        file  formData  file  true  "CSV or XLSX file"
//	@Success      200   {object}  BulkImportResult
//	@Router       /{tenant}/inventory/bulk-import [post]
func (h *InventoryHandler) BulkImport(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	if err := r.ParseMultipartForm(20 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FORM", "Expected multipart/form-data with a 'file' field")
		return
	}
	file, fh, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "MISSING_FILE", "File field 'file' is required")
		return
	}
	defer file.Close()

	// Optional warehouse_name form field — used as fallback for InitialStock rows
	// that have no warehouse_name cell, and to set outlet context for stock creation.
	defaultWarehouse := strings.TrimSpace(r.FormValue("warehouse_name"))

	ext := strings.ToLower(filepath.Ext(fh.Filename))
	switch ext {
	case ".csv":
		result := h.bulkImportCSV(r, tenantID, file)
		writeJSON(w, http.StatusOK, BulkImportResult{Items: result})
	case ".xlsx", ".xlsm":
		result := h.bulkImportXLSX(r, tenantID, file, defaultWarehouse)
		writeJSON(w, http.StatusOK, result)
	default:
		writeError(w, http.StatusBadRequest, "UNSUPPORTED_FORMAT",
			"Unsupported file format. Use .csv or .xlsx. Convert .xls files to .xlsx first.")
	}
}

// bulkImportCSV extends the simple CSV import with all item fields.
func (h *InventoryHandler) bulkImportCSV(r *http.Request, tenantID uuid.UUID, file multipart.File) importResult {
	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return importResult{Failed: 1, Errors: []string{"cannot read CSV header: " + err.Error()}}
	}

	colIdx := make(map[string]int, len(header))
	for i, c := range header {
		colIdx[strings.ToLower(strings.TrimSpace(c))] = i
	}
	col := func(row []string, name string) string {
		i, ok := colIdx[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	// Pre-load categories + units for name-based resolution.
	catMap := h.loadCategoryMap(r, tenantID)
	unitMap := h.loadUnitMap(r, tenantID)

	existingItems, _, _ := h.itemsSvc.ListItems(r.Context(), tenantID, "", "all", 10000, 0, nil, nil, "", nil, "")
	skuToID := make(map[string]uuid.UUID, len(existingItems))
	for _, it := range existingItems {
		skuToID[it.SKU] = it.ID
	}

	rows, _ := reader.ReadAll()
	var res importResult
	for _, row := range rows {
		sku := col(row, "sku")
		name := col(row, "name")
		if sku == "" || name == "" {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("row sku=%s: name and sku are required", sku))
			continue
		}
		dto := buildItemDTOFromRow(col, row, catMap, unitMap)
		if existingID, ok := skuToID[sku]; ok {
			if _, uErr := h.itemsSvc.UpdateItem(r.Context(), tenantID, existingID, dto); uErr != nil {
				res.Failed++
				res.Errors = append(res.Errors, "sku="+sku+": "+uErr.Error())
			} else {
				res.Updated++
				if dto.SuggestedPrice != nil {
					_ = h.itemsSvc.EnsureDefaultPrice(r.Context(), tenantID, existingID, *dto.SuggestedPrice)
				}
			}
		} else {
			if created, cErr := h.itemsSvc.CreateItem(r.Context(), tenantID, dto); cErr != nil {
				res.Failed++
				res.Errors = append(res.Errors, "sku="+sku+": "+cErr.Error())
			} else {
				skuToID[sku] = created.ID
				res.Created++
				if dto.SuggestedPrice != nil {
					_ = h.itemsSvc.EnsureDefaultPrice(r.Context(), tenantID, created.ID, *dto.SuggestedPrice)
				}
			}
		}
	}
	return res
}

// bulkImportXLSX orchestrates the 5-sheet XLSX import.
// defaultWarehouse is used as a fallback warehouse_name for InitialStock rows that have no warehouse cell.
func (h *InventoryHandler) bulkImportXLSX(r *http.Request, tenantID uuid.UUID, file multipart.File, defaultWarehouse string) BulkImportResult {
	xl, err := excelize.OpenReader(file)
	if err != nil {
		return BulkImportResult{Items: importResult{Failed: 1, Errors: []string{"cannot open XLSX: " + err.Error()}}}
	}
	defer xl.Close()

	catMap := h.loadCategoryMap(r, tenantID)
	unitMap := h.loadUnitMap(r, tenantID)

	existingItems, _, _ := h.itemsSvc.ListItems(r.Context(), tenantID, "", "all", 10000, 0, nil, nil, "", nil, "")
	skuToID := make(map[string]uuid.UUID, len(existingItems))
	for _, it := range existingItems {
		skuToID[it.SKU] = it.ID
	}

	var result BulkImportResult

	// ── Sheet 1: Items ────────────────────────────────────────────────────────
	if rows, sheetErr := xl.GetRows("Items"); sheetErr == nil && len(rows) > 1 {
		result.Items = h.parseXLSXItems(r, tenantID, rows, catMap, unitMap, skuToID)
	}

	// ── Sheet 2: RecipeIngredients ────────────────────────────────────────────
	if rows, sheetErr := xl.GetRows("RecipeIngredients"); sheetErr == nil && len(rows) > 1 {
		result.Recipes = h.parseXLSXRecipeIngredients(r, tenantID, rows, skuToID)
	}

	// ── Sheets 3+4: ModifierGroups / ModifierOptions ──────────────────────────
	if h.modifiersSvc == nil {
		h.log.Warn("bulk import: modifiersSvc is nil — ModifierGroups and ModifierOptions sheets skipped")
	} else {
		groupRows, _ := xl.GetRows("ModifierGroups")
		optRows, _ := xl.GetRows("ModifierOptions")
		result.Modifiers = h.parseXLSXModifiers(r, tenantID, groupRows, optRows, skuToID)
	}

	// ── Sheet 5: InitialStock ─────────────────────────────────────────────────
	if rows, sheetErr := xl.GetRows("InitialStock"); sheetErr == nil && len(rows) > 1 {
		result.Stock = h.parseXLSXInitialStock(r, tenantID, rows, defaultWarehouse)
	}

	h.log.Info("bulk import complete",
		zap.String("tenant_id", tenantID.String()),
		zap.Int("items_created", result.Items.Created),
		zap.Int("items_updated", result.Items.Updated),
		zap.Int("items_failed", result.Items.Failed),
		zap.Int("recipes_created", result.Recipes.Created),
		zap.Int("recipes_failed", result.Recipes.Failed),
		zap.Int("modifiers_created", result.Modifiers.Created),
		zap.Int("modifiers_failed", result.Modifiers.Failed),
		zap.Int("stock_created", result.Stock.Created),
		zap.Strings("item_errors", result.Items.Errors),
		zap.Strings("recipe_errors", result.Recipes.Errors),
		zap.Strings("modifier_errors", result.Modifiers.Errors),
	)

	return result
}

// ─── shared lookup helpers ────────────────────────────────────────────────────

// loadCategoryMap returns a lower-cased-name → ID map for the tenant's item categories.
// Unknown names will be auto-created when first encountered via ensureCategory.
func (h *InventoryHandler) loadCategoryMap(r *http.Request, tenantID uuid.UUID) map[string]uuid.UUID {
	cats, _ := h.itemsSvc.ListCategories(r.Context(), tenantID)
	m := make(map[string]uuid.UUID, len(cats))
	for _, c := range cats {
		m[strings.ToLower(strings.TrimSpace(c.Name))] = c.ID
	}
	return m
}

// loadUnitMap returns a lower-cased-name → ID map for the tenant's units.
func (h *InventoryHandler) loadUnitMap(r *http.Request, tenantID uuid.UUID) map[string]uuid.UUID {
	us, _ := h.unitSvc.ListUnits(r.Context(), tenantID)
	m := make(map[string]uuid.UUID, len(us))
	for _, u := range us {
		m[strings.ToLower(strings.TrimSpace(u.Name))] = u.ID
		if u.Abbreviation != "" {
			m[strings.ToLower(strings.TrimSpace(u.Abbreviation))] = u.ID
		}
	}
	return m
}

// ensureCategory returns an existing category ID or creates a new global one.
// Categories created via bulk import are marked is_global=true so they are
// visible to all tenants, avoiding per-tenant duplication.
func (h *InventoryHandler) ensureCategory(r *http.Request, tenantID uuid.UUID, name string, m map[string]uuid.UUID) *uuid.UUID {
	if name == "" {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if id, ok := m[key]; ok {
		return &id
	}
	c, err := h.itemsSvc.CreateCategory(r.Context(), tenantID, items.CategoryDTO{
		Name:     name,
		IsActive: true,
		IsGlobal: true,
	})
	if err != nil {
		h.log.Error("bulk import: failed to create global category",
			zap.String("name", name),
			zap.String("tenant_id", tenantID.String()),
			zap.Error(err))
		return nil
	}
	m[key] = c.ID
	return &c.ID
}

// inferUnitType guesses a category for an ad-hoc imported unit so a newly created unit is
// never left without a type (which previously surfaced as a "-" type in the UI pickers).
func inferUnitType(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "g", "gram", "grams", "kg", "kilogram", "mg":
		return "weight"
	case "ml", "l", "litre", "liter", "cl", "cup", "shot", "tot", "gls", "glass", "btl", "bottle":
		return "volume"
	case "day", "hour", "hr", "min", "minute", "week", "month":
		return "other"
	default:
		return "count"
	}
}

// ensureUnit returns an existing unit ID or creates a new global one.
// Units have no tenant_id (already globally shared); this creates if missing.
func (h *InventoryHandler) ensureUnit(r *http.Request, tenantID uuid.UUID, name string, m map[string]uuid.UUID) *uuid.UUID {
	name = strings.TrimSpace(name)
	// Never create a placeholder/blank unit (e.g. "-") — these are the source of "-" units.
	if name == "" || name == "-" || name == "—" || strings.EqualFold(name, "n/a") {
		return nil
	}
	key := strings.ToLower(name)
	if id, ok := m[key]; ok {
		return &id
	}
	u, err := h.unitSvc.CreateUnit(r.Context(), tenantID, units.UnitDTO{Name: strings.ToUpper(name), Abbreviation: name, Type: inferUnitType(name), IsActive: true})
	if err != nil {
		h.log.Error("bulk import: failed to create unit",
			zap.String("name", name),
			zap.Error(err))
		return nil
	}
	m[key] = u.ID
	return &u.ID
}

// sanitizeImportedDescription drops internal costing/sourcing notes that costing
// spreadsheets place in the description column (e.g. "Resale", "Not in manual",
// "~1000/kg", "Bottle ÷ tots", "Space/time"). These are operational notes, not
// customer-facing descriptions, so storing them surfaces meaningless text in the
// storefront/POS. Returns the cleaned description, or "" when the cell is a note.
func sanitizeImportedDescription(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return ""
	}
	switch t {
	case "resale", "costed", "test", "n/a", "na", "none", "tbd", "-", "—",
		"own product", "space/time", "free with mains", "composite platter", "veg salad":
		return ""
	}
	for _, sub := range []string{
		"not in manual", "auto-added", "manual entry", "size variant", "variant of",
		"cooking loss", "÷", "/kg", "/l", " per kg", "own-brand", "proxy", "spirit +",
	} {
		if strings.Contains(t, sub) {
			return ""
		}
	}
	// Price/sourcing notes such as "~1000/kg", "~600/L", "~15 each", "Tinned ~400/kg".
	if strings.Contains(t, "~") {
		return ""
	}
	return strings.TrimSpace(s)
}

// buildItemDTOFromRow constructs an ItemDTO from a generic column accessor.
func buildItemDTOFromRow(
	col func(row []string, name string) string,
	row []string,
	catMap map[string]uuid.UUID,
	unitMap map[string]uuid.UUID,
) items.ItemDTO {
	sku := col(row, "sku")
	name := col(row, "name")
	tp := strings.ToUpper(col(row, "type"))
	if tp == "" {
		tp = "GOODS"
	}

	dto := items.ItemDTO{
		SKU:                     sku,
		Name:                    name,
		Type:                    tp,
		Description:             sanitizeImportedDescription(col(row, "description")),
		IsActive:                parseBool(col(row, "is_active"), true),
		Barcode:                 col(row, "barcode"),
		BarcodeType:             col(row, "barcode_type"),
		IsPerishable:            parseBool(col(row, "is_perishable"), false),
		TrackLots:               parseBool(col(row, "track_lots"), false),
		TrackSerialNumbers:      parseBool(col(row, "track_serial_numbers"), false),
		RequiresAgeVerification: parseBool(col(row, "requires_age_verification"), false),
		ReorderLevel:            parseInt(col(row, "reorder_level"), 0),
		ReorderQuantity:         parseInt(col(row, "reorder_quantity"), 0),
		InitialQuantity:         parseInt(col(row, "initial_quantity"), 0),
		AddToAllOutlets:         false, // bulk import targets only the user-selected warehouse
	}

	if tagsStr := col(row, "tags"); tagsStr != "" {
		for _, t := range strings.Split(tagsStr, ",") {
			if trimmed := strings.TrimSpace(t); trimmed != "" {
				dto.Tags = append(dto.Tags, trimmed)
			}
		}
	}

	if cp := parseFloat(col(row, "cost_price")); cp != nil {
		dto.CostPrice = cp
	}
	if sp := parseFloat(col(row, "selling_price")); sp != nil {
		dto.SuggestedPrice = sp
	}

	// Category / Unit resolution by name
	if catName := col(row, "category_name"); catName != "" {
		if id, ok := catMap[strings.ToLower(strings.TrimSpace(catName))]; ok {
			dto.CategoryID = &id
		}
	}
	if unitName := col(row, "unit_name"); unitName != "" {
		if id, ok := unitMap[strings.ToLower(strings.TrimSpace(unitName))]; ok {
			dto.UnitID = &id
		}
	}

	// ── Extended fields (full Item alignment) ──────────────────────────────────
	if v := col(row, "use_case"); v != "" {
		dto.UseCase = strings.ToUpper(strings.TrimSpace(v))
	}
	dto.TaxCodeID = strings.TrimSpace(col(row, "tax_code_id"))
	dto.TaxInclusive = parseBool(col(row, "tax_inclusive"), false)
	dto.PurchaseUnit = strings.TrimSpace(col(row, "purchase_unit"))
	dto.ExtraBedAllowed = parseBool(col(row, "extra_bed_allowed"), false)
	if v := parseFloat(col(row, "purchase_price")); v != nil {
		dto.PurchasePrice = v
	}
	if v := parseFloat(col(row, "purchase_pack_size")); v != nil {
		dto.PurchasePackSize = v
	}
	if v := parseFloat(col(row, "yield_pct")); v != nil {
		dto.YieldPct = v
	}
	if v := parseFloat(col(row, "weight_kg")); v != nil {
		dto.WeightKg = v
	}
	if v := parseFloat(col(row, "single_supplement")); v != nil {
		dto.SingleSupplement = v
	}
	if v := parseIntPtr(col(row, "max_adults")); v != nil {
		dto.MaxAdults = v
	}
	if v := parseIntPtr(col(row, "max_children")); v != nil {
		dto.MaxChildren = v
	}
	if v := parseIntPtr(col(row, "total_capacity")); v != nil {
		dto.TotalCapacity = v
	}
	if v := strPtrOrNil(col(row, "meal_plan")); v != nil {
		dto.MealPlan = v
	}
	if v := strPtrOrNil(col(row, "occupancy_basis")); v != nil {
		dto.OccupancyBasis = v
	}
	if v := strPtrOrNil(col(row, "event_venue")); v != nil {
		dto.EventVenue = v
	}
	if v := parseTimePtr(col(row, "event_start_at")); v != nil {
		dto.EventStartAt = v
	}
	if v := parseTimePtr(col(row, "event_end_at")); v != nil {
		dto.EventEndAt = v
	}
	if dims := parseDimensions(col(row, "dimensions_cm")); dims != nil {
		dto.DimensionsCm = dims
	}

	return dto
}

// ─── row-value parse helpers ──────────────────────────────────────────────────

func parseBool(s string, def bool) bool {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TRUE", "YES", "1":
		return true
	case "FALSE", "NO", "0":
		return false
	}
	return def
}

func parseInt(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func parseFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func parseIntPtr(s string) *int {
	if s = strings.TrimSpace(s); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return &v
		}
	}
	return nil
}

func strPtrOrNil(s string) *string {
	if s = strings.TrimSpace(s); s != "" {
		return &s
	}
	return nil
}

func parseTimePtr(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// parseDimensions parses a "LxWxH" cm string (e.g. "30x20x10") into {length,width,height}.
func parseDimensions(s string) map[string]float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == 'x' || r == 'X' || r == '*' })
	keys := []string{"length", "width", "height"}
	out := map[string]float64{}
	for i, p := range parts {
		if i >= len(keys) {
			break
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(p), 64); err == nil {
			out[keys[i]] = f
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// xlsxColMap builds a lower-cased header-name → column-index map from the
// first row of an XLSX sheet.
func xlsxColMap(rows [][]string) map[string]int {
	if len(rows) == 0 {
		return nil
	}
	m := make(map[string]int, len(rows[0]))
	for i, h := range rows[0] {
		m[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return m
}

// xlsxCol safely reads a cell value by column name.
func xlsxCol(row []string, m map[string]int, name string) string {
	i, ok := m[name]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// xlsxRowToColFn returns a col() function bound to a specific row and map.
func xlsxRowToColFn(row []string, m map[string]int) func([]string, string) string {
	return func(_ []string, name string) string {
		return xlsxCol(row, m, name)
	}
}

// modifierGroupKey identifies a modifier group across items.
type modifierGroupKey struct {
	itemSKU   string
	groupName string
}

// Compile-time checks that recipe/modifier/stock types are used.
var _ = recipes.RecipeDTO{}
var _ = modifiers.CreateModifierGroupRequest{}
var _ = stock.AdjustStockRequest{}
