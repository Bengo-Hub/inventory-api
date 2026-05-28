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
