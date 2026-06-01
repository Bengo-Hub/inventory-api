package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/modules/modifiers"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
	"github.com/bengobox/inventory-service/internal/modules/stock"
	"github.com/google/uuid"
)

// parseXLSXItems processes the "Items" sheet rows (row 0 = header).
func (h *InventoryHandler) parseXLSXItems(
	r *http.Request,
	tenantID uuid.UUID,
	rows [][]string,
	catMap map[string]uuid.UUID,
	unitMap map[string]uuid.UUID,
	skuToID map[string]uuid.UUID,
) importResult {
	colMap := xlsxColMap(rows)
	var res importResult

	for _, row := range rows[1:] { // skip header
		if len(row) == 0 {
			continue
		}
		col := xlsxRowToColFn(row, colMap)

		sku  := col(nil, "sku")
		name := col(nil, "name")
		if sku == "" || name == "" {
			res.Failed++
			continue
		}

		// Auto-create category / unit if referenced by name but missing.
		catName  := col(nil, "category_name")
		unitName := col(nil, "unit_name")
		if catName != "" {
			h.ensureCategory(r, tenantID, catName, catMap)
		}
		if unitName != "" {
			h.ensureUnit(r, tenantID, unitName, unitMap)
		}

		dto := buildItemDTOFromRow(col, nil, catMap, unitMap)

		if existingID, ok := skuToID[sku]; ok {
			if _, err := h.itemsSvc.UpdateItem(r.Context(), tenantID, existingID, dto); err != nil {
				res.Failed++
				res.Errors = append(res.Errors, fmt.Sprintf("sku=%s: %s", sku, err.Error()))
			} else {
				res.Updated++
			}
		} else {
			if created, err := h.itemsSvc.CreateItem(r.Context(), tenantID, dto); err != nil {
				res.Failed++
				res.Errors = append(res.Errors, fmt.Sprintf("sku=%s: %s", sku, err.Error()))
			} else {
				skuToID[sku] = created.ID
				res.Created++
			}
		}
	}
	return res
}

// parseXLSXRecipeIngredients processes the "RecipeIngredients" sheet.
// Rows: recipe_sku | ingredient_sku | quantity | unit_name | waste_percent | notes | display_order
func (h *InventoryHandler) parseXLSXRecipeIngredients(
	r *http.Request,
	tenantID uuid.UUID,
	rows [][]string,
	skuToID map[string]uuid.UUID,
) importResult {
	colMap := xlsxColMap(rows)
	var res importResult

	// Group ingredient rows by recipe_sku (preserve order).
	type ingLine struct {
		ingredientSKU string
		quantity      float64
		unitName      string
		wastePercent  float64
		notes         string
		displayOrder  int
	}
	grouped := make(map[string][]ingLine)
	order   := make([]string, 0) // preserve insertion order of recipe_skus

	for _, row := range rows[1:] {
		if len(row) == 0 {
			continue
		}
		col := xlsxRowToColFn(row, colMap)
		recipeSKU := col(nil, "recipe_sku")
		ingrSKU   := col(nil, "ingredient_sku")
		if recipeSKU == "" || ingrSKU == "" {
			continue
		}
		qty, err := strconv.ParseFloat(col(nil, "quantity"), 64)
		if err != nil || qty <= 0 {
			continue
		}
		waste, _ := strconv.ParseFloat(col(nil, "waste_percent"), 64)
		disp, _   := strconv.Atoi(col(nil, "display_order"))

		if _, seen := grouped[recipeSKU]; !seen {
			order = append(order, recipeSKU)
		}
		grouped[recipeSKU] = append(grouped[recipeSKU], ingLine{
			ingredientSKU: ingrSKU,
			quantity:      qty,
			unitName:      col(nil, "unit_name"),
			wastePercent:  waste,
			notes:         col(nil, "notes"),
			displayOrder:  disp,
		})
	}

	for _, recipeSKU := range order {
		lines := grouped[recipeSKU]

		// Resolve recipe item ID.
		recipeItemID, ok := skuToID[recipeSKU]
		if !ok {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("recipe_sku=%s: item not found in Items sheet", recipeSKU))
			continue
		}

		// Build ingredient DTOs.
		ingrDTOs := make([]recipes.RecipeIngredientDTO, 0, len(lines))
		for i, ln := range lines {
			ingrItemID, ingrOK := skuToID[ln.ingredientSKU]
			if !ingrOK {
				res.Errors = append(res.Errors, fmt.Sprintf("recipe_sku=%s: ingredient_sku=%s not found", recipeSKU, ln.ingredientSKU))
				continue
			}
			ord := ln.displayOrder
			if ord == 0 {
				ord = i + 1
			}
			ingrDTOs = append(ingrDTOs, recipes.RecipeIngredientDTO{
				ItemID:        ingrItemID,
				ItemSKU:       ln.ingredientSKU,
				Quantity:      ln.quantity,
				UnitOfMeasure: ln.unitName,
				WastePercent:  ln.wastePercent,
				Notes:         ln.notes,
				DisplayOrder:  ord,
			})
		}
		if len(ingrDTOs) == 0 {
			res.Failed++
			continue
		}

		// Get or create recipe.
		existing, err := h.recipeSvc.GetRecipeBySKU(r.Context(), tenantID, recipeSKU)
		if err == nil && existing != nil {
			// Update (replace ingredients).
			if _, uErr := h.recipeSvc.UpdateRecipe(r.Context(), tenantID, existing.ID, recipes.RecipeDTO{
				ID:            existing.ID,
				SKU:           recipeSKU,
				Name:          existing.Name,
				ItemID:        &recipeItemID,
				OutputQty:     1,
				UnitOfMeasure: "PORTION",
				IsActive:      true,
				Ingredients:   ingrDTOs,
			}); uErr != nil {
				res.Failed++
				res.Errors = append(res.Errors, fmt.Sprintf("recipe_sku=%s: update: %s", recipeSKU, uErr.Error()))
			} else {
				res.Updated++
			}
		} else if ent.IsNotFound(err) || existing == nil {
			// Create new recipe.
			if _, cErr := h.recipeSvc.CreateRecipe(r.Context(), tenantID, recipes.RecipeDTO{
				SKU:           recipeSKU,
				Name:          recipeSKU, // UI can rename later
				ItemID:        &recipeItemID,
				OutputQty:     1,
				UnitOfMeasure: "PORTION",
				IsActive:      true,
				Ingredients:   ingrDTOs,
			}); cErr != nil {
				res.Failed++
				res.Errors = append(res.Errors, fmt.Sprintf("recipe_sku=%s: create: %s", recipeSKU, cErr.Error()))
			} else {
				res.Created++
			}
		} else {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("recipe_sku=%s: lookup: %s", recipeSKU, err.Error()))
		}
	}
	return res
}

// parseXLSXModifiers processes the "ModifierGroups" and "ModifierOptions" sheets.
func (h *InventoryHandler) parseXLSXModifiers(
	r *http.Request,
	tenantID uuid.UUID,
	groupRows [][]string,
	optRows [][]string,
	skuToID map[string]uuid.UUID,
) importResult {
	var res importResult

	// ── Groups ─────────────────────────────────────────────────────────────────
	// groupIDMap[(itemSKU, groupName)] → modifier group UUID
	groupIDMap := make(map[modifierGroupKey]uuid.UUID)

	if len(groupRows) > 1 {
		colMap := xlsxColMap(groupRows)
		for _, row := range groupRows[1:] {
			if len(row) == 0 {
				continue
			}
			col := xlsxRowToColFn(row, colMap)

			itemSKU   := col(nil, "item_sku")
			groupName := col(nil, "group_name")
			if itemSKU == "" || groupName == "" {
				continue
			}
			itemID, ok := skuToID[itemSKU]
			if !ok {
				res.Errors = append(res.Errors, fmt.Sprintf("modifier_group item_sku=%s: item not found", itemSKU))
				continue
			}

			isReq    := parseBool(col(nil, "is_required"), false)
			minSel   := parseInt(col(nil, "min_selections"), 0)
			maxSel   := parseInt(col(nil, "max_selections"), 5)
			dispOrd  := parseInt(col(nil, "display_order"), 1)

			// Check if group already exists for this item+name.
			existing, _ := h.modifiersSvc.ListModifierGroups(r.Context(), tenantID, itemID)
			var foundID uuid.UUID
			for _, g := range existing {
				if strings.EqualFold(g.Name, groupName) {
					foundID = g.ID
					break
				}
			}
			if foundID != uuid.Nil {
				groupIDMap[modifierGroupKey{itemSKU, groupName}] = foundID
				res.Updated++
				continue
			}

			created, err := h.modifiersSvc.CreateModifierGroup(r.Context(), tenantID, modifiers.CreateModifierGroupRequest{
				ItemID:        itemID,
				Name:          groupName,
				IsRequired:    isReq,
				MinSelections: minSel,
				MaxSelections: maxSel,
				DisplayOrder:  dispOrd,
			})
			if err != nil {
				res.Failed++
				res.Errors = append(res.Errors, fmt.Sprintf("modifier_group item=%s group=%s: %s", itemSKU, groupName, err.Error()))
				continue
			}
			groupIDMap[modifierGroupKey{itemSKU, groupName}] = created.ID
			res.Created++
		}
	}

	// ── Options ────────────────────────────────────────────────────────────────
	if len(optRows) > 1 {
		colMap := xlsxColMap(optRows)
		for _, row := range optRows[1:] {
			if len(row) == 0 {
				continue
			}
			col := xlsxRowToColFn(row, colMap)

			itemSKU    := col(nil, "item_sku")
			groupName  := col(nil, "group_name")
			optionName := col(nil, "option_name")
			if itemSKU == "" || groupName == "" || optionName == "" {
				continue
			}

			groupID, ok := groupIDMap[modifierGroupKey{itemSKU, groupName}]
			if !ok {
				// Try loading from existing groups.
				itemID, itemOK := skuToID[itemSKU]
				if !itemOK {
					continue
				}
				existing, _ := h.modifiersSvc.ListModifierGroups(r.Context(), tenantID, itemID)
				for _, g := range existing {
					if strings.EqualFold(g.Name, groupName) {
						groupID = g.ID
						groupIDMap[modifierGroupKey{itemSKU, groupName}] = g.ID
						break
					}
				}
				if groupID == uuid.Nil {
					res.Errors = append(res.Errors, fmt.Sprintf("modifier_option: group not found item=%s group=%s", itemSKU, groupName))
					continue
				}
			}

			priceAdj := 0.0
			if v, err := strconv.ParseFloat(col(nil, "price_adjustment"), 64); err == nil {
				priceAdj = v
			}
			isDefault := parseBool(col(nil, "is_default"), false)
			dispOrd   := parseInt(col(nil, "display_order"), 1)
			optSKU    := col(nil, "option_sku")

			if _, err := h.modifiersSvc.CreateModifierOption(r.Context(), tenantID, groupID, modifiers.CreateModifierOptionRequest{
				Name:            optionName,
				SKU:             optSKU,
				PriceAdjustment: priceAdj,
				IsDefault:       isDefault,
				IsActive:        true,
				DisplayOrder:    dispOrd,
			}); err != nil {
				res.Failed++
				res.Errors = append(res.Errors, fmt.Sprintf("modifier_option item=%s opt=%s: %s", itemSKU, optionName, err.Error()))
			} else {
				res.Created++
			}
		}
	}
	return res
}

// parseXLSXInitialStock processes the "InitialStock" sheet.
// Rows: item_sku | warehouse_name | quantity | lot_number | expiry_date
func (h *InventoryHandler) parseXLSXInitialStock(
	r *http.Request,
	tenantID uuid.UUID,
	rows [][]string,
) importResult {
	colMap := xlsxColMap(rows)
	var res importResult

	for _, row := range rows[1:] {
		if len(row) == 0 {
			continue
		}
		col := xlsxRowToColFn(row, colMap)

		itemSKU := col(nil, "item_sku")
		qtyStr  := col(nil, "quantity")
		if itemSKU == "" || qtyStr == "" {
			continue
		}
		qty, err := strconv.ParseFloat(qtyStr, 64)
		if err != nil || qty <= 0 {
			continue
		}

		// Get current balance to compute the delta.
		avail, stockErr := h.itemsSvc.GetStockAvailability(r.Context(), tenantID, itemSKU)
		if stockErr != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("sku=%s: item not found for stock adjustment", itemSKU))
			continue
		}

		delta := int(qty) - avail.OnHand
		if delta == 0 {
			res.Updated++ // already at target
			continue
		}

		if _, adjErr := h.stockSvc.AdjustStock(r.Context(), tenantID, stock.AdjustStockRequest{
			SKU:        itemSKU,
			Adjustment: delta,
			Reason:     "opening_balance",
			Notes:      "Bulk import opening stock",
		}); adjErr != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("sku=%s: stock adjustment: %s", itemSKU, adjErr.Error()))
		} else {
			res.Created++
		}
	}
	return res
}
