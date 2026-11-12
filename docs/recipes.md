# Recipes / Bill of Materials (BOM)

The recipe module links RECIPE-type catalog items to their raw ingredient items.
It drives food cost visibility, suggested pricing, and ingredient depletion. Ingredient
stock-outs only hide menu items for tenants that opted in (see "Availability policy" below);
by default availability is manual-only.

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

## Availability policy: manual-only by default (2026-10-03)

`tenant_inventory_configs.auto_hide_on_stock_out` (default `false`, editable in Settings > Stock and via `PUT /{tenant}/inventory/settings`) decides whether stock levels can change what customers and cashiers can sell.

- **Off (default, every tenant).** A sellable item (recipe or goods) is only ever made unavailable by a staff toggle on POS or the ordering app. When an item's own stock, or a recipe ingredient, reaches zero:
  - inventory still records the low/out band and publishes the `stock.out` alert, with `affects_availability: false`; consumers must not touch availability for it;
  - the ingredient-depletion cascade (`stock/cascade.go`) does not run, so no recipe is hidden;
  - sales keep depleting stock into negative, and the next goods receipt, adjustment or stock take settles the debt;
  - reservations hold the full requested quantity even beyond what's available, and report `oversell_allowed: true`, so an online order is never rejected for stock.

  Why: a mismatch between system and physical stock (a missed goods receipt, a wrong unit) used to block the sale of food that was on the shelf.
- **On.** This is the legacy behaviour. `stock.out` and the recipe cascade's `stock.out`/`stock.in` carry `affects_availability: true`, and POS/ordering mark the item unavailable and restore it. A restock re-enables only recipes whose ingredients are all positive again.

Consumers treat a missing `affects_availability` field as `true`, so events published before this change, still in flight during a rolling deploy, keep their old meaning.

The tenant config is served from a per-pod 30s cache (`internal/modules/tenantconfig`). The writing pod invalidates it immediately on save; other pods pick up the change within 30 seconds.

---

## Menu recipe items never hold stock of their own (2026-10-04)

A menu recipe's stock is tracked only through its ingredients:

- **No BOM.** A sale of a RECIPE item with no active recipe or no ingredient lines is recorded as theoretical usage and never decrements the item's own balance. It's flagged `missing_bom` on Recipe Health.
- **Reservations** never hold a recipe item's own balance.
- **`AdjustStock`** refuses menu recipe items with 422 `RECIPE_HOLDS_NO_STOCK`. Stock-count posting closes such lines without blocking approval.
- **Item list** (`GET /inventory/items`): a menu recipe's `available`/`on_hand` is the number of portions its ingredients can produce, in the same outlet scope (`recipePortionsForItems`, two batched queries per page). It's null when the recipe has no BOM.
- **Exception: manufactured finished goods** (a RECIPE item whose active recipe is a production BOM, `kind != menu`) keep a real balance, fed by production batches through `RestockItems`. See "Selling a production item" below for what a sale deducts.

## Selling a production item (2026-11-13)

What a sale deducts depends on how the item is made:

- **Made in advance (production BOM, recipe `kind` not `menu`).** The production run consumed the ingredients and added stock of the finished item. A sale deducts the finished item's own balance, like goods, and never the ingredients again. No `consumption` stock event is written for the ingredients; the sold item is reported by its sale, as goods are. Reservations hold and consume the item's own balance as a direct line.
- **Made to order (menu recipe).** The item holds no stock (`tracks_stock` and `holds_stock` false). A sale explodes the BOM and deducts the ingredients, each reported as a `consumption` stock movement. This has not changed.

The rule lives in `stock.isProductionItem` (`bom.go`), the same test `AdjustStock` and `items.holdsOwnStock` use. `explodeBOM` returns no BOM for a production recipe at the top level, so `RecordConsumption`, `CreateReservation` and modifier consumption take the direct path. A prep sub-recipe inside a menu recipe still backflushes when it has no stock. The tenant default `recipe_items_non_depleting_default` applies to menu recipes only; a production item left on the default mode depletes, while an explicit `non_depleting` mode still wins.

Before this change a production item's sale exploded its BOM, so the ingredients were deducted twice (once by the run, once by the sale) and the finished item's balance never went down.

**Returns.** A returned production item with no consumption recorded on its order goes back into its own balance on the direct path (`directReturnRestock`, `return_restock.go`), into the same warehouse and with the same `customer_return` stock event as a returned goods item. A returned menu recipe is still skipped there; its ingredients come back only through the recorded-line reversal. A production item sold before 2026-11-13 has ingredient lines recorded, so its return reverses those ingredients, which is what that sale took.

**Reservations made before the deploy.** A production item reserved before 2026-11-13 was exploded: its ingredient lines carry `component: true` and its summary line `composite: true`. Consuming one still deducts the ingredients. I left this without a code guard: component lines do not name their parent, an ingredient can be shared with a menu recipe on the same order, a partly held reservation has no summary line, and the recipe may have changed since. Instead the ones still open are counted with the query below and released or consumed by hand. A reservation that could not hold every ingredient (only on tenants with `auto_hide_on_stock_out`) has no summary line, so the query cannot see it.

```sql
-- Open reservations still holding an exploded production item (read only).
SELECT r.tenant_id, r.id AS reservation_id, r.order_id, r.status, r.created_at,
       line->>'sku' AS production_sku, (line->>'reserved_qty')::numeric AS qty
FROM reservations r
CROSS JOIN LATERAL jsonb_array_elements(r.items::jsonb) AS line
JOIN items i ON i.tenant_id = r.tenant_id AND i.sku = line->>'sku' AND i.type = 'RECIPE'
WHERE r.status IN ('pending', 'confirmed')
  -- composite tag since 2026-10-03; an older summary line is untagged and would now also
  -- deduct the item's own balance on top of the ingredients
  AND (COALESCE((line->>'composite')::boolean, false) OR r.created_at < '2026-10-04')
  AND EXISTS (SELECT 1 FROM recipes rc
              WHERE rc.tenant_id = r.tenant_id AND (rc.item_id = i.id OR rc.sku = i.sku)
                AND rc.is_active AND rc.kind <> 'menu')
ORDER BY r.created_at;
```

## Stock units: changing them, and entering stock in another unit (2026-10-04)

- **Changing an item's stock unit** (`UpdateItem`, `items/unit_rescale.go`) rescales everything in the same transaction: balances, reorder levels, lots (quantity and cost), open reservations, `cost_price`, selling guardrails and tier prices.
  - The purchase basis keeps its money: 450 per "1000 g" becomes 450 per "1 kg".
  - Values the client changed in the same request are taken as already in the new unit.
  - A cross-dimension change (g to pc) on an item holding stock returns 422 `STOCK_UNIT_CHANGE_NEEDS_FACTOR` unless `rescale_old_per_new` says how many old units make one new unit.
  - Audited as `item.stock_unit_changed`; recipes using the item are recosted.
- **Stock-in in another unit.** A stock adjustment with a `unit_id` different from the stock unit is converted (same dimension, or the content-per-unit bridge) and noted ("entered 5 kg = 5000 g"); an unconvertible unit returns 422 `UNIT_NOT_CONVERTIBLE`. Goods receipts convert quantity and unit cost from the PO line's unit.
- **Count-stocked produce used by weight** (one tilapia, one lime) gets a content-per-unit bridge on the item, exactly like tots drawn from a bottle, so recipe lines can stay in grams or ml.

## Recipe Health (2026-10-04)

- `GET /inventory/recipes/health/summary` returns counts per issue for the inventory-ui banner:
  - `missing_bom`
  - `unconvertible_line`
  - `cost_basis`: cost per stock unit more than 5x off the purchase basis
  - `frequent_stock_outs`: 3+ outs in 30 days
- `GET /inventory/recipes/health?issue=` lists the rows with deep-link ids.
- `POST /inventory/recipes/recompute-costs` recosts every active recipe (prep first) and publishes each cost through `SetCostPriceAndPublish`, so POS cost snapshots and treasury COGS follow after fixing ingredient costs.

### Scope: only tenants that use recipes see it (2026-10-20)

A pure retail tenant (boi-enterprises: 0 recipes, no hospitality outlet) was shown Recipes / BOM, Recipe Health, Food Cost Variance and Menu Engineering, and a banner about recipe costs that made no sense for a shop. inventory-ui now decides once, in `useRecipeScope` (`lib/use-case-modules.ts`: `RECIPE_USE_CASES`, `usesRecipes`, `hasHealthPage`):

- **Uses recipes**: the outlet's use case is hospitality, quick service or manufacturing, or the tenant already has at least one recipe (a retail shop baking in house). It gets the full surface, unchanged.
- **Does not**: Recipes / BOM and the two recipe-cost reports leave the menu, and the health page becomes **Stock Health** for retail and warehouse outlets: only `frequent_stock_outs` is shown, worded for a shop ("check that deliveries are received in the system and that each item's unit matches how it is counted"), with no recipe cost recalculation. The banner follows the same rule and counts only the stock problem.
- **Other use cases** (pharmacy, services): no health entry, because `/recipes/health` is only served to hospitality, quick service, warehouse, manufacturing and retail outlets (`RequireOutletUseCase`). `HEALTH_USE_CASES` mirrors that list; keep them in step.
- The first recipe a retail tenant creates flips it to the full surface (the recipe check is one cached `GET /recipes?limit=1`, invalidated by recipe changes).

Recipe portion maths shared by the stock engine and the items read model lives in `internal/modules/stockcalc` (`ConvertToStockUnit`, `PerPortionStockQty`, `ProduciblePortions`, `AllIngredientsAvailable`), so the deduction path and every availability figure use the same unit conversion and waste factor.

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

## Availability, sub-recipes and KRA stock (2026-11-12)

KRA's eTIMS team saw recipe items flagged as out of stock at the 2026-11-11 review although recipes
hold no stock. The causes and the rules now:

- **Availability resolves sub-recipes.** `stockcalc.ProduciblePortions` and
  `AllIngredientsAvailable` expand a prepared ingredient (a RECIPE item with `usable_in_recipes`)
  through its own recipe, up to `MaxSubRecipeDepth` (5) levels with a cycle guard, the same way
  `bom.go` deducts. Deduction now also nests to 5 levels, so the two agree.
- **A recipe that nothing limits is unlimited**, not zero: `ProduciblePortions` returns `Unlimited`
  (-1) when no line constrains stock (all non-depleting, or every line has a unit mismatch).
- **`tracks_stock`** is on every availability answer. Services, vouchers and menu recipes report
  `tracks_stock: false` and are never out of stock; a client must read `available` only when
  `tracks_stock` is true. A RECIPE made by a production BOM holds its own balance and reads it like
  goods. The inventory summary counts low and out of stock only for items that hold stock.
- **Batched.** `items/recipe_availability.go` loads recipes, sub-recipe levels and balances for a
  whole page in a fixed number of queries, never one per item.
- **The cascade restores parents.** Restocking an ingredient re-evaluates every recipe that uses it,
  directly or through a sub-recipe, and brings it back.
- **Item lists carry `holds_stock`.** Treasury registers a stock-holding recipe with KRA as a
  finished product (item type 2) and a menu recipe as a service (3).

**What KRA learns about recipe stock.** A recipe's sale line holds no stock, so its ingredients
leaving were never reported. Now each ingredient (and each stock-holding modifier) deducted for a
sale publishes `inventory.stock.updated` with reason `consumption`, from `RecordConsumption` and from
`ConsumeReservation` (reservation lines carry `component: true`), and treasury sends it to KRA as an
adjustment out (16). A return or void brings ingredients back as `consumption_reversal` (06). A sold
item coming back as itself is `sale_reversal`, which treasury skips because the refund already
reports it. Stock moves made for a treasury invoice or its credit note carry `fiscal_document: true`
and are not sent again.
