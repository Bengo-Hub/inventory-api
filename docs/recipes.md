# Recipes / Bill of Materials (BOM)

The recipe module links RECIPE-type catalog items to their raw ingredient items.
It drives food cost visibility, suggested pricing, and (via Phase 3) ingredient
stock-out → menu availability cascades.

---

## Endpoints

| Method | Path | Permission |
|--------|------|-----------|
| GET | `/api/v1/{tenant}/inventory/recipes` | `inventory.recipes.view` |
| POST | `/api/v1/{tenant}/inventory/recipes` | `inventory.recipes.add` |
| GET | `/api/v1/{tenant}/inventory/recipes/{id}` | `inventory.recipes.view` |
| PUT | `/api/v1/{tenant}/inventory/recipes/{id}` | `inventory.recipes.change` |
| DELETE | `/api/v1/{tenant}/inventory/recipes/{id}` | `inventory.recipes.delete` |

### Query parameters (GET list)
| Param | Type | Description |
|-------|------|-------------|
| `sku` | string | Filter by exact SKU (returns single-element array) |
| `search` | string | Search by name |
| `page` | int | Page number (default 1) |
| `limit` | int | Page size (default 20, max 100) |

---

## RecipeDTO

```json
{
  "id": "uuid",
  "tenant_id": "uuid",
  "sku": "BEV-CAP-001",
  "name": "Cappuccino",
  "item_name": "Cappuccino",
  "item_id": "uuid | null",
  "output_qty": 1,
  "servings": 1,
  "unit_of_measure": "CUP",
  "is_active": true,
  "total_cost": 39.87,
  "cost_per_portion": 39.87,
  "target_margin_percent": 72.0,
  "suggested_price": 142.4,
  "prep_time_minutes": null,
  "allergens": [],
  "ingredients": [...]
}
```

### Fields

| Field | Description |
|-------|-------------|
| `item_id` | FK → the RECIPE-type `Item` this BOM produces. Set via `item_id` in POST/PUT. |
| `item_name` | Denormalised name of the produced item (populated from `item.name`). |
| `total_cost` | Sum of effective ingredient costs: `Σ(qty × (1 + waste%/100) × cost_price)`. |
| `cost_per_portion` | `total_cost / output_qty`. |
| `target_margin_percent` | Per-recipe margin override. Falls back to `TenantInventoryConfig.default_target_margin_percent` (default 30%). |
| `suggested_price` | `cost_per_portion / (1 - margin/100)`. |
| `servings` | Alias for `output_qty` (UI convenience). |

---

## RecipeIngredientDTO

```json
{
  "id": "uuid",
  "item_id": "uuid",
  "item_sku": "RAW-ESP-001",
  "item_name": "Espresso Beans",
  "quantity": 0.018,
  "unit_of_measure": "KG",
  "unit_id": "uuid",
  "waste_percent": 5.0,
  "notes": "",
  "display_order": 0
}
```

| Field | Description |
|-------|-------------|
| `unit_id` | FK → `Unit` row (preferred over `unit_of_measure` string for UI dropdowns). |
| `waste_percent` | Shrinkage/cooking loss as %. `effective_qty = quantity × (1 + waste_percent/100)`. |
| `item_name` | Populated from `ing.edges.item.name` — no extra query needed. |

---

## Cost Calculation Formula

```
effective_qty  = quantity × (1 + waste_percent / 100)
ingredient_cost = effective_qty × item.cost_price
total_cost     = Σ(ingredient_cost) for all ingredients
cost_per_portion = total_cost / output_qty
margin         = recipe.target_margin_percent ?? config.default_target_margin_percent ?? 30.0
suggested_price = cost_per_portion / (1 - margin / 100)
```

Costs are recalculated by calling `RecalculateRecipeCosts(ctx, tenantID, recipeID)` in `internal/modules/recipes/costing.go`:
- On every ingredient create/update/delete
- After the seed runs (`recalculateAllRecipeCosts` in `cmd/seed/seed_recipes.go`)

---

## TenantInventoryConfig defaults

`default_target_margin_percent` (default `30.0`) is the fallback margin used when a recipe has no `target_margin_percent` set. It is stored in the `tenant_inventory_configs` table and can be updated via the settings API.

---

## A RECIPE item with no configured ingredients silently sells like a plain GOODS item (2026-09-23)

`RecordConsumption` (`internal/modules/stock/service.go`) resolves each sale SKU via `explodeBOM` (`bom.go`). That function returns `isBOM=false` — meaning "consume this SKU directly" — whenever there is **no active `Recipe` row for the item, or the active recipe has zero `RecipeIngredient` lines**. This is correct for a genuinely non-recipe item, but it is a silent trap for a `RECIPE`-type item whose ingredients were simply never added (or whose recipe was deactivated): every sale decrements **the recipe item's own `InventoryBalance`** instead of any ingredient, and — because the ingredient-depletion cascade (`cascade.go`'s `cascadeIngredientStockOut`/`cascadeIngredientRestocked`) is keyed entirely off `RecipeIngredient` rows — the item is invisible to that mechanism. It never auto-86s when "ingredients" (which don't exist) run out, and never auto-restores, since nothing restocks a recipe item's own SKU operationally. The balance just drifts negative indefinitely.

`RecordConsumption` now logs a `zap.Warn` ("consumption: RECIPE item has no active recipe/ingredients …") every time this fallback fires for a `RECIPE`-type item, so the gap surfaces in logs instead of only being found by a manual DB audit. It deliberately does not block the sale or auto-fabricate ingredients.

**Audit query** to find every affected item, fleet-wide or per tenant:
```sql
SELECT t.slug, i.sku, i.name,
       CASE WHEN r.id IS NULL THEN 'no_active_recipe' ELSE 'zero_ingredients' END AS problem
FROM items i JOIN tenants t ON t.id = i.tenant_id
LEFT JOIN recipes r ON r.item_id = i.id AND r.is_active = true
LEFT JOIN (SELECT recipe_id, COUNT(*) cnt FROM recipe_ingredients GROUP BY recipe_id) ri ON ri.recipe_id = r.id
WHERE i.type = 'RECIPE' AND i.is_active = true
  AND (r.id IS NULL OR (r.id IS NOT NULL AND ri.cnt IS NULL));
```
The fix for an affected item is always the same: add its `RecipeIngredient` lines (or reactivate/create its `Recipe` row) via the normal recipe-editing UI/API. If the item has already accrued a negative balance from being wrongly direct-consumed, that balance is a byproduct of the misconfiguration, not real backorder debt — reset it to 0 via an audited stock adjustment (`reason=correction`) once the ingredients are in place, matching [[oversell-negative-stock-settlement]]'s established remediation pattern. See project memory `recipe-item-no-bom-direct-consumption-bug-2026-09-23` for the 2026-09-23 fleet audit and repair.
