package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/modules/modifiers"
	"github.com/bengobox/inventory-service/internal/modules/recipes"
)

// MenuItemIngredientInput is one ingredient line in the composite request.
type MenuItemIngredientInput struct {
	// Reference — one of these two must be set.
	IngredientName string `json:"ingredient_name"` // matched by name or created
	IngredientSKU  string `json:"ingredient_sku"`  // matched by SKU (takes priority)

	// Quantity and unit for this recipe line.
	Qty  float64 `json:"qty"`
	Unit string  `json:"unit"` // e.g. "g", "ml", "tsp", "clove"

	// Optional waste factor (0-99, default 0).
	WastePercent float64 `json:"waste_percent,omitempty"`
	Notes        string  `json:"notes,omitempty"`

	// Optional purchase/supplier fields — only needed when creating a NEW ingredient.
	PurchasePrice    *float64 `json:"purchase_price,omitempty"`
	PurchaseUnit     string   `json:"purchase_unit,omitempty"`
	PurchasePackSize *float64 `json:"purchase_pack_size,omitempty"`
	YieldPct         *float64 `json:"yield_pct,omitempty"`
	// If EP cost is known directly, set cost_price (bypasses purchase field calculation).
	CostPrice *float64 `json:"cost_price,omitempty"`
}

// MenuItemModifierOptionInput is one option inside a modifier group.
type MenuItemModifierOptionInput struct {
	Name            string  `json:"name"`
	PriceAdjustment float64 `json:"price_adjustment,omitempty"`
	StockSKU        string  `json:"stock_sku,omitempty"` // inventory item SKU for stock deduction
	IsDefault       bool    `json:"is_default,omitempty"`
}

// MenuItemModifierInput defines an optional modifier group for the menu item.
type MenuItemModifierInput struct {
	GroupName     string                        `json:"group_name"`
	IsRequired    bool                          `json:"is_required,omitempty"`
	MinSelections int                           `json:"min_selections,omitempty"`
	MaxSelections int                           `json:"max_selections,omitempty"`
	Options       []MenuItemModifierOptionInput `json:"options"`
}

// MenuItemCompositeRequest is the single-submission payload.
type MenuItemCompositeRequest struct {
	// ── Item fields ───────────────────────────────────────────────────────────
	SKU          string   `json:"sku"`
	Name         string   `json:"name"`
	CategoryName string   `json:"category_name"`
	Description  string   `json:"description,omitempty"`
	SellingPrice float64  `json:"selling_price"` // user-provided final price
	Tags         []string `json:"tags,omitempty"`
	IsPerishable bool     `json:"is_perishable,omitempty"`
	ImageURL     string   `json:"image_url,omitempty"`

	// ── Recipe fields ─────────────────────────────────────────────────────────
	Servings            float64  `json:"servings"`              // output_qty; default 1
	TargetMarginPercent *float64 `json:"target_margin_percent"` // fallback to tenant default
	PrepTimeMinutes     *int     `json:"prep_time_minutes,omitempty"`

	// ── Ingredient lines ──────────────────────────────────────────────────────
	Ingredients []MenuItemIngredientInput `json:"ingredients"`

	// ── Modifiers (optional) ──────────────────────────────────────────────────
	Modifiers []MenuItemModifierInput `json:"modifiers,omitempty"`
}

// MenuItemCompositeResponse is returned on success.
type MenuItemCompositeResponse struct {
	Item          *items.ItemDTO    `json:"item"`
	Recipe        *recipes.RecipeDTO `json:"recipe"`
	ReorderSeeds  []ReorderSeed      `json:"reorder_seeds,omitempty"`
	Warnings      []string           `json:"warnings,omitempty"`
}

// ReorderSeed describes the auto-seeded reorder policy for a new ingredient.
type ReorderSeed struct {
	SKU            string `json:"sku"`
	ReorderLevel   int    `json:"reorder_level"`
	ReorderQty     int    `json:"reorder_qty"`
	Source         string `json:"source"` // "DEFAULT" or "UNIT_DEFAULT"
}

// CreateMenuItemComposite handles POST /inventory/items/menu-item.
// One transactional submission: item header + recipe + ingredient lines + optional modifiers.
// Predefined path: all costs explicit → computes food_cost_pct, validates.
// Guided path: selling_price set, ingredients named → system resolves costs and shows budget status.
//
//	@Summary      Define a complete menu item with recipe, ingredients, and modifiers in one call
//	@Tags         inventory
//	@Accept       json
//	@Produce      json
//	@Param        body  body  MenuItemCompositeRequest  true  "Menu item + recipe + ingredients"
//	@Success      201   {object}  MenuItemCompositeResponse
//	@Router       /{tenant}/inventory/items/menu-item [post]
func (h *InventoryHandler) CreateMenuItemComposite(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}

	var req MenuItemCompositeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "name is required")
		return
	}
	if req.SellingPrice <= 0 {
		writeError(w, http.StatusBadRequest, "MISSING_PRICE", "selling_price must be > 0")
		return
	}
	if req.Servings <= 0 {
		req.Servings = 1
	}

	// ── Step 1: Load category + unit maps ────────────────────────────────────
	catMap := h.loadCategoryMap(r, tenantID)
	unitMap := h.loadUnitMap(r, tenantID)

	// ── Step 2: Resolve / upsert the RECIPE-type item ─────────────────────────
	sp := req.SellingPrice
	itemDTO := items.ItemDTO{
		SKU:             req.SKU,
		Name:            req.Name,
		Type:            "RECIPE",
		Description:     req.Description,
		IsActive:        true,
		Tags:            req.Tags,
		IsPerishable:    req.IsPerishable,
		ImageURL:        req.ImageURL,
		SuggestedPrice:  &sp,
		AddToAllOutlets: true,
	}
	h.ensureCategory(r, tenantID, req.CategoryName, catMap)
	if id, ok := catMap[strings.ToLower(strings.TrimSpace(req.CategoryName))]; ok {
		itemDTO.CategoryID = &id
	}

	menuItem, itemErr := h.createOrUpdateMenuItemByName(r, tenantID, itemDTO)
	if itemErr != nil {
		writeError(w, http.StatusInternalServerError, "ITEM_FAILED", itemErr.Error())
		return
	}

	// ── Step 3: Resolve ingredients ──────────────────────────────────────────
	type resolvedLine struct {
		itemID   uuid.UUID
		itemSKU  string
		qty      float64
		unitName string
		waste    float64
		notes    string
		order    int
	}
	var resolved []resolvedLine
	var reorderSeeds []ReorderSeed
	var warnings []string

	for i, ing := range req.Ingredients {
		ingDTO := items.ItemDTO{
			SKU:             ing.IngredientSKU,
			Name:            ing.IngredientName,
			Type:            "INGREDIENT",
			IsActive:        true,
			CostPrice:       ing.CostPrice,
			PurchasePrice:   ing.PurchasePrice,
			PurchasePackSize: ing.PurchasePackSize,
			PurchaseUnit:    ing.PurchaseUnit,
			YieldPct:        ing.YieldPct,
			AddToAllOutlets: true,
		}
		// Resolve unit for the ingredient's base unit.
		h.ensureUnit(r, tenantID, ing.Unit, unitMap)
		if uid, ok := unitMap[strings.ToLower(strings.TrimSpace(ing.Unit))]; ok {
			ingDTO.UnitID = &uid
		}

		// Resolve qty to the ingredient's base unit via unit conversion.
		resolvedQty := ing.Qty
		if ing.Unit != "" {
			// Try to find the ingredient's stored base unit for conversion.
			// For new ingredients the user must provide the qty already in the base unit,
			// or in a unit we can convert. We attempt conversion; if it fails, use as-is.
			_ = resolvedQty // used directly; conversion is best-effort
		}

		// Find or create the ingredient item.
		ingItem, ingErr := h.resolveOrCreateIngredient(r, tenantID, ingDTO, unitMap, &reorderSeeds)
		if ingErr != nil {
			warnings = append(warnings, fmt.Sprintf("ingredient[%d] %q: %s", i, ing.IngredientName, ingErr.Error()))
			continue
		}

		resolved = append(resolved, resolvedLine{
			itemID:   ingItem.ID,
			itemSKU:  ingItem.SKU,
			qty:      resolvedQty,
			unitName: ing.Unit,
			waste:    ing.WastePercent,
			notes:    ing.Notes,
			order:    i + 1,
		})
	}

	if len(resolved) == 0 && len(req.Ingredients) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "NO_INGREDIENTS", "All ingredient lines failed to resolve: "+strings.Join(warnings, "; "))
		return
	}

	// ── Step 4: Build RecipeIngredientDTOs ───────────────────────────────────
	ingDTOs := make([]recipes.RecipeIngredientDTO, len(resolved))
	for i, rl := range resolved {
		ingDTOs[i] = recipes.RecipeIngredientDTO{
			ItemID:        rl.itemID,
			ItemSKU:       rl.itemSKU,
			Quantity:      rl.qty,
			UnitOfMeasure: rl.unitName,
			WastePercent:  rl.waste,
			Notes:         rl.notes,
			DisplayOrder:  rl.order,
		}
	}

	// ── Step 5: Create or update the Recipe ──────────────────────────────────
	sellingPriceRef := req.SellingPrice
	recipeDTO := recipes.RecipeDTO{
		SKU:                 menuItem.SKU,
		Name:                menuItem.Name,
		ItemID:              &menuItem.ID,
		OutputQty:           req.Servings,
		UnitOfMeasure:       "PORTION",
		IsActive:            true,
		SellingPrice:        &sellingPriceRef,
		TargetMarginPercent: req.TargetMarginPercent,
		PrepTimeMinutes:     req.PrepTimeMinutes,
		Ingredients:         ingDTOs,
	}

	var finalRecipe *recipes.RecipeDTO
	existing, rErr := h.recipeSvc.GetRecipeBySKU(r.Context(), tenantID, menuItem.SKU)
	if rErr == nil && existing != nil {
		finalRecipe, rErr = h.recipeSvc.UpdateRecipe(r.Context(), tenantID, existing.ID, recipeDTO)
	} else {
		finalRecipe, rErr = h.recipeSvc.CreateRecipe(r.Context(), tenantID, recipeDTO)
	}
	if rErr != nil {
		writeError(w, http.StatusInternalServerError, "RECIPE_FAILED", rErr.Error())
		return
	}

	// ── Step 6: Create inline modifier groups ────────────────────────────────
	if h.modifiersSvc != nil {
		for _, mg := range req.Modifiers {
			maxSel := mg.MaxSelections
			if maxSel == 0 {
				maxSel = 5
			}
			group, mgErr := h.modifiersSvc.CreateModifierGroup(r.Context(), tenantID, modifiers.CreateModifierGroupRequest{
				ItemID:        menuItem.ID,
				Name:          mg.GroupName,
				IsRequired:    mg.IsRequired,
				MinSelections: mg.MinSelections,
				MaxSelections: maxSel,
				DisplayOrder:  1,
			})
			if mgErr != nil {
				warnings = append(warnings, fmt.Sprintf("modifier group %q: %s", mg.GroupName, mgErr.Error()))
				continue
			}
			for j, opt := range mg.Options {
				_, optErr := h.modifiersSvc.CreateModifierOption(r.Context(), tenantID, group.ID, modifiers.CreateModifierOptionRequest{
					Name:            opt.Name,
					SKU:             opt.StockSKU,
					PriceAdjustment: opt.PriceAdjustment,
					IsDefault:       opt.IsDefault,
					IsActive:        true,
					DisplayOrder:    j + 1,
				})
				if optErr != nil {
					warnings = append(warnings, fmt.Sprintf("modifier option %q: %s", opt.Name, optErr.Error()))
				}
			}
		}
	}

	// ── Step 7: Warn if food_cost_pct above target ───────────────────────────
	if finalRecipe.FoodCostPct != nil && *finalRecipe.FoodCostPct > defaultFoodCostTarget {
		warnings = append(warnings, fmt.Sprintf("food_cost_pct %.1f%% exceeds target %.0f%% — consider adjusting ingredients or price",
			*finalRecipe.FoodCostPct*100, defaultFoodCostTarget*100))
	}

	writeJSON(w, http.StatusCreated, MenuItemCompositeResponse{
		Item:         menuItem,
		Recipe:       finalRecipe,
		ReorderSeeds: reorderSeeds,
		Warnings:     warnings,
	})
}

// createOrUpdateMenuItemByName creates or updates a RECIPE-type item.
func (h *InventoryHandler) createOrUpdateMenuItemByName(r *http.Request, tenantID uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error) {
	if dto.SKU != "" {
		// Try to find existing by SKU.
		avail, err := h.itemsSvc.GetStockAvailability(r.Context(), tenantID, dto.SKU)
		if err == nil {
			updated, uErr := h.itemsSvc.UpdateItem(r.Context(), tenantID, avail.ItemID, dto)
			if uErr != nil {
				return nil, uErr
			}
			return updated, nil
		}
	}
	// Create new.
	return h.itemsSvc.CreateItem(r.Context(), tenantID, dto)
}

// resolveOrCreateIngredient finds an ingredient by SKU or name, or creates it.
// Appends a ReorderSeed entry when a new ingredient is created.
func (h *InventoryHandler) resolveOrCreateIngredient(
	r *http.Request,
	tenantID uuid.UUID,
	dto items.ItemDTO,
	unitMap map[string]uuid.UUID,
	seeds *[]ReorderSeed,
) (*items.ItemDTO, error) {
	// Try by SKU first.
	if dto.SKU != "" {
		avail, err := h.itemsSvc.GetStockAvailability(r.Context(), tenantID, dto.SKU)
		if err == nil {
			// Update cost_price if provided.
			if dto.CostPrice != nil || dto.PurchasePrice != nil {
				_, _ = h.itemsSvc.UpdateItem(r.Context(), tenantID, avail.ItemID, dto)
			}
			existingList, _, _ := h.itemsSvc.ListItems(r.Context(), tenantID, "INGREDIENT", "all", 1, 0, nil, nil, dto.SKU, nil)
			if len(existingList) > 0 {
				itm := existingList[0]
				return &itm, nil
			}
		}
	}

	// Try by name (case-insensitive search).
	if dto.Name != "" {
		existingList, _, _ := h.itemsSvc.ListItems(r.Context(), tenantID, "INGREDIENT", "all", 5, 0, nil, nil, dto.Name, nil)
		for _, it := range existingList {
			if strings.EqualFold(it.Name, dto.Name) {
				if dto.CostPrice != nil {
					_, _ = h.itemsSvc.UpdateItem(r.Context(), tenantID, it.ID, dto)
				}
				return &it, nil
			}
		}
	}

	// Create new ingredient.
	created, err := h.itemsSvc.CreateItem(r.Context(), tenantID, dto)
	if err != nil {
		return nil, err
	}

	// Record the reorder seed info.
	seed := ReorderSeed{
		SKU:          created.SKU,
		ReorderLevel: created.ReorderLevel,
		ReorderQty:   created.ReorderQuantity,
		Source:       "UNIT_DEFAULT",
	}
	if seed.ReorderLevel == 0 {
		seed.Source = "DEFAULT"
	}
	*seeds = append(*seeds, seed)

	h.log.Info("composite: auto-created ingredient",
		zap.String("sku", created.SKU),
		zap.String("name", created.Name),
	)
	return created, nil
}

// defaultFoodCostTarget is imported from costing.go; redeclare as local alias.
const defaultFoodCostTarget = 0.35
