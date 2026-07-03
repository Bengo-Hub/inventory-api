package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entconsumption "github.com/bengobox/inventory-service/internal/ent/consumption"
	entfcv "github.com/bengobox/inventory-service/internal/ent/foodcostvariance"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
)

// s2sHTTPClient is used for service-to-service HTTP calls with a bounded timeout.
var s2sHTTPClient = &http.Client{Timeout: 15 * time.Second}

// VarianceService computes actual vs theoretical food cost variance.
type VarianceService struct {
	client      *ent.Client
	log         *zap.Logger
	orderingURL string
	posURL      string
	apiKey      string
}

// NewVarianceService creates a new VarianceService.
func NewVarianceService(client *ent.Client, log *zap.Logger, orderingURL, posURL, apiKey string) *VarianceService {
	return &VarianceService{
		client:      client,
		log:         log.Named("recipes.variance"),
		orderingURL: orderingURL,
		posURL:      posURL,
		apiKey:      apiKey,
	}
}

// VarianceReportDTO represents one recipe's variance result for a period.
type VarianceReportDTO struct {
	RecipeSKU       string             `json:"recipe_sku"`
	RecipeName      string             `json:"recipe_name"`
	TheoreticalCost float64            `json:"theoretical_cost"`
	ActualCost      float64            `json:"actual_cost"`
	VariancePct     float64            `json:"variance_pct"`
	Breakdown       map[string]float64 `json:"breakdown,omitempty"`
}

// orderSummary is the minimal fields we need from the ordering service.
type orderSummary struct {
	Items []orderItemSummary `json:"items"`
}

type orderItemSummary struct {
	SKU      string  `json:"sku"`
	Quantity float64 `json:"quantity"`
}

// orderingListResponse is the expected paginated response from ordering service.
type orderingListResponse struct {
	Data []orderSummary `json:"data"`
}

// CalculatePeriodVariance computes actual vs theoretical food cost for all recipes in a period.
// Results are persisted to food_cost_variances and returned.
func (s *VarianceService) CalculatePeriodVariance(ctx context.Context, tenantID uuid.UUID, tenantSlug string, start, end time.Time) ([]VarianceReportDTO, error) {
	// 1. Load all active recipes with their cost_per_portion.
	recipes, err := s.client.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.IsActive(true)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("variance: load recipes: %w", err)
	}
	if len(recipes) == 0 {
		return nil, nil
	}

	// Build a SKU → recipe map.
	recipeMap := make(map[string]*ent.Recipe, len(recipes))
	for _, r := range recipes {
		recipeMap[r.Sku] = r
	}

	// 2. Fetch units sold per SKU from ordering + POS sales (S2S).
	soldQty, err := s.fetchSoldQty(ctx, tenantID, tenantSlug, start, end)
	if err != nil {
		s.log.Warn("variance: sales S2S call failed, falling back to zero sales", zap.Error(err))
		soldQty = map[string]float64{}
	}

	// 3. Load consumption records for the period to compute actual cost.
	consumptions, err := s.client.Consumption.Query().
		Where(
			entconsumption.TenantID(tenantID),
			entconsumption.ProcessedAtGTE(start),
			entconsumption.ProcessedAtLTE(end),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("variance: load consumptions: %w", err)
	}

	// Build SKU → total consumed quantity from Consumption records.
	actualConsumed := map[string]float64{}
	for _, c := range consumptions {
		for _, it := range c.Items {
			actualConsumed[it.SKU] += it.Quantity
		}
	}

	// 4. For each recipe with sales, compute theoretical and actual costs.
	var results []VarianceReportDTO
	now := time.Now().UTC()

	for sku, r := range recipeMap {
		qty := soldQty[sku]
		if qty <= 0 {
			continue
		}

		var theoreticalCost float64
		if r.CostPerPortion != nil {
			theoreticalCost = qty * *r.CostPerPortion
		}

		// Actual cost: sum consumed ingredient costs.
		// For simplicity we track actual as consumed qty × ingredient cost_price via stock adjustments.
		// Here we use Consumption records tagged with recipe SKU when available.
		actualCost := actualConsumed[sku] * func() float64 {
			if r.CostPerPortion != nil {
				return *r.CostPerPortion
			}
			return 0
		}()

		var variancePct float64
		if theoreticalCost > 0 {
			variancePct = ((actualCost - theoreticalCost) / theoreticalCost) * 100
		}

		dto := VarianceReportDTO{
			RecipeSKU:       sku,
			RecipeName:      r.Name,
			TheoreticalCost: theoreticalCost,
			ActualCost:      actualCost,
			VariancePct:     variancePct,
		}
		results = append(results, dto)

		// Persist to food_cost_variances (upsert-style: always insert new record per calculation).
		if _, err := s.client.FoodCostVariance.Create().
			SetTenantID(tenantID).
			SetPeriodStart(start).
			SetPeriodEnd(end).
			SetRecipeSku(sku).
			SetRecipeName(r.Name).
			SetTheoreticalCost(theoreticalCost).
			SetActualCost(actualCost).
			SetVariancePct(variancePct).
			SetCalculatedAt(now).
			Save(ctx); err != nil {
			s.log.Warn("variance: persist failed", zap.String("sku", sku), zap.Error(err))
		}
	}

	return results, nil
}

// GetStoredVariance retrieves previously-calculated variance records for a period.
func (s *VarianceService) GetStoredVariance(ctx context.Context, tenantID uuid.UUID, start, end time.Time) ([]VarianceReportDTO, error) {
	records, err := s.client.FoodCostVariance.Query().
		Where(
			entfcv.TenantID(tenantID),
			entfcv.PeriodStartGTE(start),
			entfcv.PeriodEndLTE(end),
		).
		Order(ent.Desc(entfcv.FieldCalculatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("variance: get stored: %w", err)
	}

	result := make([]VarianceReportDTO, len(records))
	for i, r := range records {
		result[i] = VarianceReportDTO{
			RecipeSKU:       r.RecipeSku,
			RecipeName:      r.RecipeName,
			TheoreticalCost: r.TheoreticalCost,
			ActualCost:      r.ActualCost,
			VariancePct:     r.VariancePct,
		}
	}
	return result, nil
}

// fetchSoldQty returns SKU → units sold for a date range, combining BOTH sources of sales:
// the ordering service (online/e-commerce orders) AND pos-api (in-store POS sales). A POS-driven
// outlet records no ordering-service orders, so counting ordering alone made menu-engineering /
// variance report zero units sold for every recipe — this merges POS sales in.
func (s *VarianceService) fetchSoldQty(ctx context.Context, tenantID uuid.UUID, tenantSlug string, start, end time.Time) (map[string]float64, error) {
	sold := map[string]float64{}

	// Source 1: ordering service (best-effort — POS-only tenants have none).
	if s.orderingURL != "" {
		if ord, err := s.fetchOrderingSoldQty(ctx, tenantSlug, start, end); err != nil {
			s.log.Warn("fetchSoldQty: ordering source failed", zap.Error(err))
		} else {
			for sku, q := range ord {
				sold[sku] += q
			}
		}
	}

	// Source 2: pos-api completed sales (in-store).
	if s.posURL != "" {
		if pos, err := s.fetchPOSSoldQty(ctx, tenantID, start, end); err != nil {
			s.log.Warn("fetchSoldQty: pos source failed", zap.Error(err))
		} else {
			for sku, q := range pos {
				sold[sku] += q
			}
		}
	}

	return sold, nil
}

// fetchOrderingSoldQty calls the ordering service for SKU → units sold in a date range.
func (s *VarianceService) fetchOrderingSoldQty(ctx context.Context, tenantSlug string, start, end time.Time) (map[string]float64, error) {
	url := fmt.Sprintf("%s/api/v1/%s/orders/summary?from=%s&to=%s&status=completed",
		s.orderingURL,
		tenantSlug,
		start.Format(time.RFC3339),
		end.Format(time.RFC3339),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if s.apiKey != "" {
		req.Header.Set("X-API-Key", s.apiKey)
	}

	resp, err := s2sHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ordering service returned HTTP %d", resp.StatusCode)
	}

	var body orderingListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode ordering response: %w", err)
	}

	sold := map[string]float64{}
	for _, order := range body.Data {
		for _, item := range order.Items {
			sold[item.SKU] += item.Quantity
		}
	}
	return sold, nil
}

// posSalesResponse is the shape pos-api /s2s/{tenant}/pos/sales/by-sku returns.
type posSalesResponse struct {
	Data []struct {
		SKU          string  `json:"sku"`
		QuantitySold float64 `json:"quantity_sold"`
	} `json:"data"`
}

// fetchPOSSoldQty calls pos-api (S2S) for SKU → units sold from completed POS sales in a range.
func (s *VarianceService) fetchPOSSoldQty(ctx context.Context, tenantID uuid.UUID, start, end time.Time) (map[string]float64, error) {
	url := fmt.Sprintf("%s/api/v1/s2s/%s/pos/sales/by-sku?from=%s&to=%s",
		s.posURL,
		tenantID.String(),
		start.Format(time.RFC3339),
		end.Format(time.RFC3339),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if s.apiKey != "" {
		req.Header.Set("X-API-Key", s.apiKey)
	}
	resp, err := s2sHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pos service returned HTTP %d", resp.StatusCode)
	}
	var body posSalesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode pos response: %w", err)
	}
	sold := map[string]float64{}
	for _, row := range body.Data {
		if row.SKU != "" {
			sold[row.SKU] += row.QuantitySold
		}
	}
	return sold, nil
}
