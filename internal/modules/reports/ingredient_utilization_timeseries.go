package reports

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent"
	entinventorybalance "github.com/bengobox/inventory-service/internal/ent/inventorybalance"
	entid "github.com/bengobox/inventory-service/internal/ent/itemconsumptiondaily"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/stocklevelevent"
)

// Granularity values accepted by GetTimeseries. "biweek" is a 14-day bucket anchored to the
// query's `from` date (there is no calendar-standard biweek, unlike week/month).
const (
	GranularityDay    = "day"
	GranularityWeek   = "week"
	GranularityBiweek = "biweek"
	GranularityMonth  = "month"
)

// TimeseriesRecipeSlice is one recipe's (or, for a direct-sale item, the item's own) share
// of a time bucket.
type TimeseriesRecipeSlice struct {
	RecipeID   *uuid.UUID `json:"recipe_id,omitempty"`
	RecipeSKU  string     `json:"recipe_sku,omitempty"`
	RecipeName string     `json:"recipe_name"`
	Quantity   float64    `json:"quantity"`
	Cost       float64    `json:"cost"`
}

// TimeseriesPoint is one bucket (day/week/biweek/month) of the consumption trend.
type TimeseriesPoint struct {
	BucketStart time.Time               `json:"bucket_start"`
	BucketEnd   time.Time               `json:"bucket_end"`
	Quantity    float64                 `json:"quantity"`
	Cost        float64                 `json:"cost"`
	ByRecipe    []TimeseriesRecipeSlice `json:"by_recipe"`
}

// StockLevelEventDTO is a low/out/restocked transition inside the query range, used by the
// UI to phase-band the trend chart (above reorder level / below / stocked out).
type StockLevelEventDTO struct {
	EventType    string    `json:"event_type"`
	OccurredAt   time.Time `json:"occurred_at"`
	OnHand       float64   `json:"on_hand_at_event"`
	ReorderLevel float64   `json:"reorder_level_at_event"`
}

// TimeseriesResponse is the full trend-chart payload.
type TimeseriesResponse struct {
	Granularity      string               `json:"granularity"`
	Points           []TimeseriesPoint    `json:"points"`
	ReorderLevel     int                  `json:"reorder_level"`
	StockLevelEvents []StockLevelEventDTO `json:"stock_level_events"`
}

// GetTimeseries buckets ItemConsumptionDaily rows (already day-grain, written transactionally
// alongside every ConsumptionLine — see stock/consumption_lines.go) into the requested
// granularity, stacked by recipe. recipeIDs, when non-empty, restricts to those recipes
// (uuid.Nil in the slice means "the direct-sale, no-recipe bucket").
func (s *Service) GetTimeseries(ctx context.Context, tenantID, itemID, warehouseID uuid.UUID, from, to time.Time, granularity string, recipeIDs []uuid.UUID) (*TimeseriesResponse, error) {
	q := s.client.ItemConsumptionDaily.Query().
		Where(
			entid.TenantID(tenantID),
			entid.ItemID(itemID),
			entid.WarehouseID(warehouseID),
			entid.BucketDateGTE(truncateDay(from)),
			entid.BucketDateLTE(truncateDay(to)),
		)
	if len(recipeIDs) > 0 {
		q = q.Where(entid.RecipeIDIn(recipeIDs...))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("reports: load daily rollup: %w", err)
	}

	bucketOf := bucketFunc(granularity, from)
	type key struct {
		start time.Time
		recID uuid.UUID
	}
	agg := map[key]*TimeseriesRecipeSlice{}
	bucketRange := map[time.Time][2]time.Time{}
	recipeNames := map[uuid.UUID]string{}
	var recipeIDList []uuid.UUID
	seen := map[uuid.UUID]bool{}

	for _, row := range rows {
		bStart, bEnd := bucketOf(row.BucketDate)
		bucketRange[bStart] = [2]time.Time{bStart, bEnd}
		k := key{start: bStart, recID: row.RecipeID}
		if agg[k] == nil {
			agg[k] = &TimeseriesRecipeSlice{RecipeSKU: row.RecipeSku}
			if row.RecipeID != uuid.Nil {
				id := row.RecipeID
				agg[k].RecipeID = &id
			}
		}
		agg[k].Quantity = round4(agg[k].Quantity + row.Quantity)
		agg[k].Cost = round4(agg[k].Cost + row.TotalCost)
		if row.RecipeID != uuid.Nil && !seen[row.RecipeID] {
			seen[row.RecipeID] = true
			recipeIDList = append(recipeIDList, row.RecipeID)
		}
	}

	if len(recipeIDList) > 0 {
		recs, rerr := s.client.Recipe.Query().Where(recipe.IDIn(recipeIDList...)).All(ctx)
		if rerr == nil {
			for _, r := range recs {
				recipeNames[r.ID] = r.Name
			}
		}
	}
	// Resolve the item's own name once for the no-recipe ("direct sale") bucket.
	directName := ""
	if itm, ierr := s.client.Item.Get(ctx, itemID); ierr == nil {
		directName = itm.Name
	}

	starts := make([]time.Time, 0, len(bucketRange))
	for start := range bucketRange {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

	points := make([]TimeseriesPoint, 0, len(starts))
	for _, start := range starts {
		rng := bucketRange[start]
		point := TimeseriesPoint{BucketStart: rng[0], BucketEnd: rng[1]}
		for k, slice := range agg {
			if k.start != start {
				continue
			}
			if slice.RecipeID != nil {
				slice.RecipeName = recipeNames[*slice.RecipeID]
			} else {
				slice.RecipeName = directName
			}
			point.Quantity = round4(point.Quantity + slice.Quantity)
			point.Cost = round4(point.Cost + slice.Cost)
			point.ByRecipe = append(point.ByRecipe, *slice)
		}
		sort.Slice(point.ByRecipe, func(i, j int) bool { return point.ByRecipe[i].Quantity > point.ByRecipe[j].Quantity })
		points = append(points, point)
	}

	resp := &TimeseriesResponse{Granularity: granularity, Points: points}

	if bal, berr := s.client.InventoryBalance.Query().
		Where(
			entinventorybalance.TenantID(tenantID),
			entinventorybalance.ItemID(itemID),
			entinventorybalance.WarehouseID(warehouseID),
		).
		First(ctx); berr == nil {
		resp.ReorderLevel = bal.ReorderLevel
	}

	events, eerr := s.client.StockLevelEvent.Query().
		Where(
			stocklevelevent.TenantID(tenantID),
			stocklevelevent.ItemID(itemID),
			stocklevelevent.WarehouseID(warehouseID),
			stocklevelevent.OccurredAtGTE(from),
			stocklevelevent.OccurredAtLTE(to),
		).
		Order(ent.Asc(stocklevelevent.FieldOccurredAt)).
		All(ctx)
	if eerr == nil {
		for _, e := range events {
			resp.StockLevelEvents = append(resp.StockLevelEvents, StockLevelEventDTO{
				EventType:    string(e.EventType),
				OccurredAt:   e.OccurredAt,
				OnHand:       e.OnHandAtEvent,
				ReorderLevel: e.ReorderLevelAtEvent,
			})
		}
	}

	return resp, nil
}

// RecipeBreakdownRow is one row of the "which recipes consumed this ingredient" table.
type RecipeBreakdownRow struct {
	RecipeID   *uuid.UUID `json:"recipe_id,omitempty"`
	RecipeSKU  string     `json:"recipe_sku,omitempty"`
	RecipeName string     `json:"recipe_name"`
	Quantity   float64    `json:"quantity"`
	Cost       float64    `json:"cost"`
	PctOfTotal float64    `json:"pct_of_total"`
}

// GetByRecipe returns the recipe breakdown table for one ingredient over a period, sorted
// by quantity descending.
func (s *Service) GetByRecipe(ctx context.Context, tenantID, itemID, warehouseID uuid.UUID, from, to time.Time) ([]RecipeBreakdownRow, error) {
	var rows []struct {
		RecipeID  uuid.UUID `json:"recipe_id"`
		RecipeSku string    `json:"recipe_sku"`
		Qty       float64   `json:"sum_quantity"`
		Cost      float64   `json:"sum_total_cost"`
	}
	if err := s.client.ItemConsumptionDaily.Query().
		Where(
			entid.TenantID(tenantID),
			entid.ItemID(itemID),
			entid.WarehouseID(warehouseID),
			entid.BucketDateGTE(truncateDay(from)),
			entid.BucketDateLTE(truncateDay(to)),
		).
		GroupBy(entid.FieldRecipeID, entid.FieldRecipeSku).
		Aggregate(ent.Sum(entid.FieldQuantity), ent.Sum(entid.FieldTotalCost)).
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("reports: group by recipe: %w", err)
	}

	var total float64
	var ids []uuid.UUID
	for _, r := range rows {
		total += r.Qty
		if r.RecipeID != uuid.Nil {
			ids = append(ids, r.RecipeID)
		}
	}
	names := map[uuid.UUID]string{}
	if len(ids) > 0 {
		if recs, rerr := s.client.Recipe.Query().Where(recipe.IDIn(ids...)).All(ctx); rerr == nil {
			for _, r := range recs {
				names[r.ID] = r.Name
			}
		}
	}
	directName := ""
	if itm, ierr := s.client.Item.Get(ctx, itemID); ierr == nil {
		directName = itm.Name
	}

	out := make([]RecipeBreakdownRow, 0, len(rows))
	for _, r := range rows {
		row := RecipeBreakdownRow{RecipeSKU: r.RecipeSku, Quantity: round4(r.Qty), Cost: round4(r.Cost)}
		if r.RecipeID != uuid.Nil {
			id := r.RecipeID
			row.RecipeID = &id
			row.RecipeName = names[id]
		} else {
			row.RecipeName = directName
		}
		if total > 0 {
			row.PctOfTotal = round4(r.Qty / total * 100)
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Quantity > out[j].Quantity })
	return out, nil
}

func truncateDay(t time.Time) time.Time {
	return t.UTC().Truncate(24 * time.Hour)
}

// bucketFunc returns a function mapping a day-grain bucket_date to its [start, end) coarser
// bucket for the requested granularity. "day" is the identity mapping.
func bucketFunc(granularity string, from time.Time) func(time.Time) (time.Time, time.Time) {
	switch granularity {
	case GranularityWeek:
		return func(d time.Time) (time.Time, time.Time) {
			offset := (int(d.Weekday()) + 6) % 7 // Monday-anchored week
			start := d.AddDate(0, 0, -offset)
			return start, start.AddDate(0, 0, 7)
		}
	case GranularityBiweek:
		anchor := truncateDay(from)
		return func(d time.Time) (time.Time, time.Time) {
			days := int(d.Sub(anchor).Hours() / 24)
			periods := days / 14
			start := anchor.AddDate(0, 0, periods*14)
			return start, start.AddDate(0, 0, 14)
		}
	case GranularityMonth:
		return func(d time.Time) (time.Time, time.Time) {
			start := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
			return start, start.AddDate(0, 1, 0)
		}
	default: // day
		return func(d time.Time) (time.Time, time.Time) {
			start := truncateDay(d)
			return start, start.AddDate(0, 0, 1)
		}
	}
}
