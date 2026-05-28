package main

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entrecipe "github.com/bengobox/inventory-service/internal/ent/recipe"
	entring "github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	entconfig "github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
)

type ingredientDef struct {
	RawSKU       string
	Qty          float64
	UOM          string  // unit_of_measure string + used to derive unit_id via unitUUID(UOM)
	WastePercent float64 // shrinkage/cooking loss factor in %
}

type recipeDef struct {
	SKU                 string
	OutputQty           float64
	UOM                 string
	TargetMarginPercent *float64
	Ingredients         []ingredientDef
}

func recipeUUID(tenantID uuid.UUID, sku string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:recipe:%s:%s", tenantID, sku)))
}

func recipeIngredientUUID(recipeID uuid.UUID, rawSKU string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("bengobox:inventory:recipe-ing:%s:%s", recipeID, rawSKU)))
}

// recipeDefs maps every RECIPE-type menu item to its BOM with waste percentages and target margins.
var recipeDefs = []recipeDef{
	// ── Hot Beverages — target margin 72% (coffee is high-margin)
	{"BEV-ESP-001", 1, "CUP", ptr(72.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
	}},
	{"BEV-ESP-002", 1, "CUP", ptr(72.0), []ingredientDef{
		{"RAW-ESP-001", 0.036, "KG", 5.0},
	}},
	{"BEV-LAT-001", 1, "CUP", ptr(72.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
		{"RAW-MLK-001", 0.25, "LITRE", 3.0},
	}},
	{"BEV-CAP-001", 1, "CUP", ptr(72.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
		{"RAW-MLK-001", 0.20, "LITRE", 3.0},
		{"RAW-COC-001", 0.005, "KG", 2.0},
	}},
	{"BEV-AME-001", 1, "CUP", ptr(74.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
	}},
	{"BEV-MOC-001", 1, "CUP", ptr(70.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
		{"RAW-MLK-001", 0.20, "LITRE", 3.0},
		{"RAW-CHO-001", 0.030, "LITRE", 2.0},
		{"RAW-CRM-001", 0.030, "LITRE", 2.0},
	}},
	{"BEV-MAC-001", 1, "CUP", ptr(74.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
		{"RAW-MLK-001", 0.050, "LITRE", 3.0},
	}},
	{"BEV-TEA-001", 1, "CUP", ptr(75.0), []ingredientDef{
		{"RAW-TEA-001", 0.005, "KG", 2.0},
		{"RAW-SGR-001", 0.010, "KG", 1.0},
	}},
	{"BEV-TEA-002", 1, "CUP", ptr(72.0), []ingredientDef{
		{"RAW-TEA-001", 0.005, "KG", 2.0},
		{"RAW-MLK-001", 0.150, "LITRE", 3.0},
		{"RAW-GNG-001", 0.005, "KG", 5.0},
		{"RAW-CDM-001", 0.002, "KG", 2.0},
		{"RAW-SGR-001", 0.010, "KG", 1.0},
	}},
	{"BEV-HOT-001", 1, "CUP", ptr(70.0), []ingredientDef{
		{"RAW-CHO-001", 0.040, "LITRE", 2.0},
		{"RAW-MLK-001", 0.250, "LITRE", 3.0},
		{"RAW-CRM-001", 0.030, "LITRE", 2.0},
	}},

	// ── Cold Beverages — target margin 70%
	{"BEV-ICE-001", 1, "CUP", ptr(70.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
		{"RAW-MLK-001", 0.200, "LITRE", 3.0},
		{"RAW-ICE-001", 0.150, "KG", 10.0},
	}},
	{"BEV-ICE-002", 1, "CUP", ptr(72.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
		{"RAW-ICE-001", 0.150, "KG", 10.0},
	}},
	{"BEV-FRP-001", 1, "CUP", ptr(68.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
		{"RAW-MLK-001", 0.200, "LITRE", 3.0},
		{"RAW-CAR-001", 0.030, "LITRE", 2.0},
		{"RAW-ICE-001", 0.150, "KG", 10.0},
	}},
	{"BEV-FRP-002", 1, "CUP", ptr(68.0), []ingredientDef{
		{"RAW-ESP-001", 0.018, "KG", 5.0},
		{"RAW-MLK-001", 0.200, "LITRE", 3.0},
		{"RAW-VAN-001", 0.030, "LITRE", 2.0},
		{"RAW-ICE-001", 0.150, "KG", 10.0},
	}},
	{"BEV-SMO-001", 1, "CUP", ptr(65.0), []ingredientDef{
		{"RAW-MNG-001", 0.200, "KG", 12.0},
		{"RAW-YGT-001", 0.150, "LITRE", 3.0},
		{"RAW-ICE-001", 0.100, "KG", 10.0},
	}},
	{"BEV-SMO-002", 1, "CUP", ptr(65.0), []ingredientDef{
		{"RAW-BRY-001", 0.150, "KG", 5.0},
		{"RAW-BNA-001", 0.100, "KG", 8.0},
		{"RAW-YGT-001", 0.150, "LITRE", 3.0},
		{"RAW-ICE-001", 0.100, "KG", 10.0},
	}},
	{"BEV-JCE-001", 1, "CUP", ptr(65.0), []ingredientDef{
		{"RAW-ORG-001", 0.400, "KG", 15.0},
	}},

	// ── Sandwiches & Wraps — target margin 62%
	{"SND-CLB-001", 1, "PIECE", ptr(62.0), []ingredientDef{
		{"RAW-BRD-001", 3, "SLICE", 5.0},
		{"RAW-CKN-001", 0.150, "KG", 8.0},
		{"RAW-BCN-001", 0.050, "KG", 5.0},
		{"RAW-LET-001", 0.030, "KG", 10.0},
		{"RAW-TMT-001", 0.050, "KG", 8.0},
	}},
	{"SND-GRL-001", 1, "PIECE", ptr(62.0), []ingredientDef{
		{"RAW-BRD-001", 1, "PIECE", 5.0},
		{"RAW-CKN-001", 0.150, "KG", 8.0},
		{"RAW-PST-001", 0.020, "LITRE", 2.0},
		{"RAW-CHZ-001", 0.050, "KG", 3.0},
	}},
	{"SND-VEG-001", 1, "PIECE", ptr(65.0), []ingredientDef{
		{"RAW-TRT-001", 1, "PIECE", 2.0},
		{"RAW-HUM-001", 0.050, "KG", 3.0},
		{"RAW-AVO-001", 0.5, "PIECE", 5.0},
		{"RAW-VEG-001", 0.100, "KG", 10.0},
	}},
	{"SND-BLT-001", 1, "PIECE", ptr(62.0), []ingredientDef{
		{"RAW-BRD-001", 2, "SLICE", 5.0},
		{"RAW-BCN-001", 0.060, "KG", 5.0},
		{"RAW-LET-001", 0.030, "KG", 10.0},
		{"RAW-TMT-001", 0.050, "KG", 8.0},
	}},
	{"SND-TUN-001", 1, "PIECE", ptr(62.0), []ingredientDef{
		{"RAW-RYE-001", 2, "SLICE", 5.0},
		{"RAW-TNA-001", 0.100, "KG", 3.0},
		{"RAW-CDD-001", 0.040, "KG", 3.0},
	}},

	// ── Salads — target margin 65%
	{"SAL-CES-001", 1, "BOWL", ptr(65.0), []ingredientDef{
		{"RAW-LET-001", 0.150, "KG", 10.0},
		{"RAW-CRT-001", 0.030, "KG", 3.0},
		{"RAW-PRM-001", 0.030, "KG", 2.0},
		{"RAW-CSR-001", 0.040, "LITRE", 2.0},
	}},
	{"SAL-GRK-001", 1, "BOWL", ptr(65.0), []ingredientDef{
		{"RAW-CUC-001", 0.100, "KG", 8.0},
		{"RAW-TMT-001", 0.100, "KG", 8.0},
		{"RAW-OLV-001", 0.030, "KG", 3.0},
		{"RAW-FTA-001", 0.050, "KG", 2.0},
		{"RAW-OVO-001", 0.020, "LITRE", 2.0},
	}},

	// ── Light Bites — target margin 65%
	{"BTE-SAM-001", 1, "SERVING", ptr(65.0), []ingredientDef{
		{"RAW-SAM-001", 3, "PIECE", 3.0},
		{"RAW-VEG-001", 0.100, "KG", 10.0},
		{"RAW-TMR-001", 0.030, "LITRE", 2.0},
	}},
	{"BTE-SPR-001", 1, "SERVING", ptr(65.0), []ingredientDef{
		{"RAW-SPW-001", 4, "PIECE", 3.0},
		{"RAW-VEG-001", 0.100, "KG", 10.0},
		{"RAW-SCS-001", 0.030, "LITRE", 2.0},
	}},

	// ── Main Courses — target margin 60%
	{"MIN-GRL-001", 1, "PLATE", ptr(60.0), []ingredientDef{
		{"RAW-BEF-001", 0.250, "KG", 8.0},
		{"RAW-POT-001", 0.200, "KG", 10.0},
		{"RAW-VEG-001", 0.150, "KG", 10.0},
		{"RAW-BTR-001", 0.020, "KG", 2.0},
	}},
	{"MIN-GRL-002", 1, "PLATE", ptr(60.0), []ingredientDef{
		{"RAW-CKN-001", 0.250, "KG", 8.0},
		{"RAW-RIC-001", 0.150, "KG", 3.0},
		{"RAW-VEG-001", 0.150, "KG", 10.0},
	}},
	{"MIN-CUR-001", 1, "PLATE", ptr(60.0), []ingredientDef{
		{"RAW-CKN-001", 0.250, "KG", 8.0},
		{"RAW-CRY-001", 0.020, "KG", 2.0},
		{"RAW-RIC-001", 0.150, "KG", 3.0},
		{"RAW-NAN-001", 1, "PIECE", 2.0},
	}},
	{"MIN-CUR-002", 1, "PLATE", ptr(60.0), []ingredientDef{
		{"RAW-BEF-001", 0.250, "KG", 8.0},
		{"RAW-POT-001", 0.150, "KG", 10.0},
		{"RAW-VEG-001", 0.150, "KG", 10.0},
		{"RAW-UGL-001", 0.150, "KG", 3.0},
	}},
	{"MIN-SEA-001", 1, "PLATE", ptr(60.0), []ingredientDef{
		{"RAW-FSH-001", 0.200, "KG", 8.0},
		{"RAW-FLR-001", 0.050, "KG", 5.0},
		{"RAW-POT-001", 0.200, "KG", 10.0},
		{"RAW-TAR-001", 0.030, "LITRE", 2.0},
	}},
	{"MIN-PAS-001", 1, "PLATE", ptr(60.0), []ingredientDef{
		{"RAW-SPG-001", 0.150, "KG", 5.0},
		{"RAW-MNC-001", 0.150, "KG", 8.0},
		{"RAW-TMC-001", 0.100, "LITRE", 3.0},
		{"RAW-PRM-001", 0.020, "KG", 2.0},
		{"RAW-BRD-001", 1, "SLICE", 5.0},
	}},
	{"MIN-RIC-001", 1, "BOWL", ptr(62.0), []ingredientDef{
		{"RAW-RIC-001", 0.200, "KG", 3.0},
		{"RAW-PIL-001", 0.020, "KG", 2.0},
		{"RAW-CKN-001", 0.150, "KG", 8.0},
	}},

	// ── Breakfast — target margin 62%
	{"BRK-FUL-001", 1, "PLATE", ptr(62.0), []ingredientDef{
		{"RAW-EGG-001", 2, "PIECE", 2.0},
		{"RAW-BCN-001", 0.060, "KG", 5.0},
		{"RAW-SSG-001", 0.100, "KG", 5.0},
		{"RAW-BNS-001", 0.100, "KG", 3.0},
		{"RAW-BRD-001", 2, "SLICE", 5.0},
		{"RAW-TMT-001", 0.050, "KG", 8.0},
	}},
	{"BRK-PAN-001", 1, "PLATE", ptr(62.0), []ingredientDef{
		{"RAW-PNC-001", 0.150, "KG", 3.0},
		{"RAW-MPS-001", 0.030, "LITRE", 2.0},
		{"RAW-BRY-001", 0.050, "KG", 5.0},
		{"RAW-EGG-001", 1, "PIECE", 2.0},
	}},
	{"BRK-AVT-001", 1, "PLATE", ptr(65.0), []ingredientDef{
		{"RAW-BRD-001", 2, "SLICE", 5.0},
		{"RAW-AVO-001", 1, "PIECE", 5.0},
		{"RAW-EGG-001", 1, "PIECE", 2.0},
	}},
	{"BRK-OAT-001", 1, "BOWL", ptr(68.0), []ingredientDef{
		{"RAW-OAT-001", 0.080, "KG", 2.0},
		{"RAW-ALM-001", 0.200, "LITRE", 3.0},
		{"RAW-HNY-001", 0.020, "KG", 2.0},
		{"RAW-BRY-001", 0.030, "KG", 5.0},
	}},

	// ── Pizza — target margin 65%
	{"PIZ-MAR-001", 1, "PIECE", ptr(65.0), []ingredientDef{
		{"RAW-PZD-001", 1, "PIECE", 3.0},
		{"RAW-TMC-001", 0.100, "LITRE", 3.0},
		{"RAW-CHZ-001", 0.150, "KG", 3.0},
		{"RAW-BSL-001", 0.010, "KG", 5.0},
	}},
	{"PIZ-PEP-001", 1, "PIECE", ptr(65.0), []ingredientDef{
		{"RAW-PZD-001", 1, "PIECE", 3.0},
		{"RAW-TMC-001", 0.100, "LITRE", 3.0},
		{"RAW-CHZ-001", 0.120, "KG", 3.0},
		{"RAW-PEP-001", 0.080, "KG", 3.0},
	}},
}

func seedRecipes(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	for _, rd := range recipeDefs {
		recipeID := recipeUUID(tenantID, rd.SKU)

		var recipeName string
		for _, item := range catalogItemDefs {
			if item.SKU == rd.SKU {
				recipeName = item.Name
				break
			}
		}
		if recipeName == "" {
			recipeName = rd.SKU
		}

		// item_id links recipe → its RECIPE-type catalog item.
		itemID := itemUUID(tenantID, rd.SKU)

		_, err := client.Recipe.Get(ctx, recipeID)
		switch {
		case ent.IsNotFound(err):
			if _, createErr := client.Recipe.Create().
				SetID(recipeID).
				SetTenantID(tenantID).
				SetSku(rd.SKU).
				SetName(recipeName).
				SetOutputQty(rd.OutputQty).
				SetUnitOfMeasure(rd.UOM).
				SetIsActive(true).
				SetNillableTargetMarginPercent(rd.TargetMarginPercent).
				SetNillableItemID(&itemID).
				Save(ctx); createErr != nil {
				return fmt.Errorf("create recipe %s: %w", rd.SKU, createErr)
			}
			log.Printf("recipe created: %s — %s", rd.SKU, recipeName)
		case err != nil:
			return fmt.Errorf("check recipe %s: %w", rd.SKU, err)
		default:
			if _, updateErr := client.Recipe.UpdateOneID(recipeID).
				SetName(recipeName).
				SetOutputQty(rd.OutputQty).
				SetUnitOfMeasure(rd.UOM).
				SetIsActive(true).
				SetNillableTargetMarginPercent(rd.TargetMarginPercent).
				SetNillableItemID(&itemID).
				Save(ctx); updateErr != nil {
				return fmt.Errorf("update recipe %s: %w", rd.SKU, updateErr)
			}
		}

		// Upsert ingredients with unit_id and waste_percent.
		for i, ing := range rd.Ingredients {
			rawItemID := itemUUID(tenantID, ing.RawSKU)
			ingID := recipeIngredientUUID(recipeID, ing.RawSKU)
			unitID := unitUUID(ing.UOM)

			existing, getErr := client.RecipeIngredient.Query().
				Where(entring.RecipeID(recipeID), entring.ItemID(rawItemID)).
				First(ctx)
			switch {
			case ent.IsNotFound(getErr):
				if _, createErr := client.RecipeIngredient.Create().
					SetID(ingID).
					SetRecipeID(recipeID).
					SetItemID(rawItemID).
					SetItemSku(ing.RawSKU).
					SetQuantity(ing.Qty).
					SetUnitOfMeasure(ing.UOM).
					SetNillableUnitID(&unitID).
					SetWastePercent(ing.WastePercent).
					SetDisplayOrder(i).
					Save(ctx); createErr != nil {
					return fmt.Errorf("create ingredient %s→%s: %w", rd.SKU, ing.RawSKU, createErr)
				}
			case getErr != nil:
				return fmt.Errorf("check ingredient %s→%s: %w", rd.SKU, ing.RawSKU, getErr)
			default:
				if _, updateErr := client.RecipeIngredient.UpdateOneID(existing.ID).
					SetQuantity(ing.Qty).
					SetUnitOfMeasure(ing.UOM).
					SetNillableUnitID(&unitID).
					SetWastePercent(ing.WastePercent).
					SetDisplayOrder(i).
					Save(ctx); updateErr != nil {
					return fmt.Errorf("update ingredient %s→%s: %w", rd.SKU, ing.RawSKU, updateErr)
				}
			}
		}
	}

	log.Printf("seeded %d recipes with BOM ingredients", len(recipeDefs))
	return nil
}

// recalculateAllRecipeCosts recalculates total_cost, cost_per_portion, and suggested_price for all seeded recipes.
func recalculateAllRecipeCosts(ctx context.Context, client *ent.Client, tenantID uuid.UUID) {
	for _, rd := range recipeDefs {
		recipeID := recipeUUID(tenantID, rd.SKU)
		if err := recalcRecipeCost(ctx, client, tenantID, recipeID, rd.SKU); err != nil {
			log.Printf("[WARN] cost recalc for %s: %v", rd.SKU, err)
		}
	}
}

func recalcRecipeCost(ctx context.Context, client *ent.Client, tenantID, recipeID uuid.UUID, sku string) error {
	r, err := client.Recipe.Query().
		Where(entrecipe.ID(recipeID), entrecipe.TenantID(tenantID)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) {
			q.WithItem()
		}).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("load recipe: %w", err)
	}

	var totalCost float64
	hasCost := false
	for _, ing := range r.Edges.Ingredients {
		if ing.Edges.Item == nil || ing.Edges.Item.CostPrice == nil {
			continue
		}
		effectiveQty := ing.Quantity * (1 + ing.WastePercent/100)
		totalCost += effectiveQty * *ing.Edges.Item.CostPrice
		hasCost = true
	}
	if !hasCost {
		return nil
	}

	outputQty := r.OutputQty
	if outputQty <= 0 {
		outputQty = 1
	}
	costPerPortion := totalCost / outputQty

	marginPct := 30.0
	if r.TargetMarginPercent != nil {
		marginPct = *r.TargetMarginPercent
	} else {
		cfg, cfgErr := client.TenantInventoryConfig.Query().
			Where(entconfig.TenantID(tenantID)).
			Only(ctx)
		if cfgErr == nil && cfg.DefaultTargetMarginPercent != nil {
			marginPct = *cfg.DefaultTargetMarginPercent
		}
	}

	suggestedPrice := costPerPortion / (1 - marginPct/100)

	if _, err := client.Recipe.UpdateOneID(recipeID).
		SetTotalCost(totalCost).
		SetCostPerPortion(costPerPortion).
		SetSuggestedPrice(suggestedPrice).
		Save(ctx); err != nil {
		return fmt.Errorf("update recipe costs: %w", err)
	}

	log.Printf("costs recalculated: %s — total=%.2f cost/portion=%.2f suggested=%.2f", sku, totalCost, costPerPortion, suggestedPrice)
	return nil
}
