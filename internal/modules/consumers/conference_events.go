package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/bundlecomponent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/recipe"
	"github.com/bengobox/inventory-service/internal/ent/recipeingredient"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

const (
	conferenceMealcardDurable = "inventory-conference-mealcards"
	conferenceAckWait         = 30 * time.Second
	conferenceMaxDeliver      = 5
)

// ConferenceEventsConsumer consumes pos.conference.mealcard.redeemed and backflushes the
// redeemed meal's bill-of-materials into stock. The pos meal voucher carries the inventory
// Bundle id + meal period; the matching MEAL_PERIOD BundleComponent points to the meal item
// (a RECIPE → exploded to ingredients, or a stockable item → consumed directly).
type ConferenceEventsConsumer struct {
	log      *zap.Logger
	stockSvc *stock.Service
	orm      *ent.Client
}

// NewConferenceEventsConsumer creates the conference meal-card consumer.
func NewConferenceEventsConsumer(log *zap.Logger, stockSvc *stock.Service, orm *ent.Client) *ConferenceEventsConsumer {
	return &ConferenceEventsConsumer{
		log:      log.Named("consumers.conference_events"),
		stockSvc: stockSvc,
		orm:      orm,
	}
}

// Start subscribes to pos.conference.mealcard.redeemed via a JetStream durable consumer.
func (c *ConferenceEventsConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	if _, err := js.StreamInfo("pos"); err != nil {
		if _, aerr := js.AddStream(&nats.StreamConfig{
			Name: "pos", Subjects: []string{"pos.>"}, Retention: nats.LimitsPolicy,
			MaxAge: 72 * time.Hour, Storage: nats.FileStorage,
		}); aerr != nil && aerr != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("conference events: ensure stream: %w", aerr)
		}
	}
	sub, err := js.Subscribe(
		"pos.conference.mealcard.redeemed",
		c.handleMessage,
		nats.Durable(conferenceMealcardDurable),
		nats.AckExplicit(),
		nats.AckWait(conferenceAckWait),
		nats.MaxDeliver(conferenceMaxDeliver),
		nats.DeliverAll(),
	)
	if err != nil {
		return fmt.Errorf("conference events: subscribe: %w", err)
	}
	c.log.Info("conference meal-card events consumer started", zap.String("durable", conferenceMealcardDurable))
	<-ctx.Done()
	return sub.Unsubscribe()
}

func (c *ConferenceEventsConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()
	var env struct {
		Payload struct {
			TenantID          string `json:"tenant_id"`
			InventoryBundleID string `json:"inventory_bundle_id"`
			MealPeriod        string `json:"meal_period"`
			MealEntitlementID string `json:"meal_entitlement_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		c.log.Warn("conference events: unmarshal failed", zap.Error(err))
		_ = msg.Ack() // malformed — don't retry
		return
	}
	tenantID, err := uuid.Parse(env.Payload.TenantID)
	if err != nil {
		_ = msg.Ack()
		return
	}
	bundleID, err := uuid.Parse(env.Payload.InventoryBundleID)
	if err != nil {
		// No package bundle linked (e.g. hall-hire only) — nothing to backflush.
		_ = msg.Ack()
		return
	}
	if err := c.backflushMeal(ctx, tenantID, bundleID, env.Payload.MealPeriod, env.Payload.MealEntitlementID); err != nil {
		c.log.Error("conference events: meal backflush failed", zap.Error(err),
			zap.String("bundle_id", bundleID.String()), zap.String("meal_period", env.Payload.MealPeriod))
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// backflushMeal resolves the bundle's MEAL_PERIOD component for the redeemed period and
// records its consumption (one cover), exploding the BOM when the meal is a RECIPE.
func (c *ConferenceEventsConsumer) backflushMeal(ctx context.Context, tenantID, bundleID uuid.UUID, mealPeriod, entitlementID string) error {
	if mealPeriod == "" {
		return nil
	}
	comp, err := c.orm.BundleComponent.Query().
		Where(
			bundlecomponent.BundleID(bundleID),
			bundlecomponent.MealPeriodEQ(bundlecomponent.MealPeriod(mealPeriod)),
		).
		WithComponentItem().
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			c.log.Debug("conference events: no meal component for period — skipping",
				zap.String("bundle_id", bundleID.String()), zap.String("meal_period", mealPeriod))
			return nil
		}
		return fmt.Errorf("query meal component: %w", err)
	}
	if comp.Edges.ComponentItem == nil {
		return nil
	}
	sku := comp.Edges.ComponentItem.Sku
	qty := float64(comp.Quantity)
	if qty <= 0 {
		qty = 1
	}

	var items []stock.ConsumptionItem
	if comp.Edges.ComponentItem.Type == item.TypeRECIPE {
		if ings, e := c.explodeRecipe(ctx, tenantID, sku, qty); e == nil {
			items = ings
		}
	}
	if len(items) == 0 {
		items = []stock.ConsumptionItem{{SKU: sku, Quantity: qty}}
	}

	key := entitlementID
	if key == "" {
		key = uuid.NewString()
	}
	_, err = c.stockSvc.RecordConsumption(ctx, tenantID, stock.ConsumptionRequest{
		TenantID:       tenantID,
		Items:          items,
		Reason:         "conference_meal",
		IdempotencyKey: fmt.Sprintf("mealcard-%s", key),
	})
	if err != nil {
		return fmt.Errorf("record meal consumption: %w", err)
	}
	c.log.Info("conference meal backflushed", zap.String("sku", sku),
		zap.String("meal_period", mealPeriod), zap.Int("items", len(items)))
	return nil
}

// explodeRecipe returns the ingredient consumption items for a RECIPE sku scaled to qty covers.
func (c *ConferenceEventsConsumer) explodeRecipe(ctx context.Context, tenantID uuid.UUID, sku string, qty float64) ([]stock.ConsumptionItem, error) {
	r, err := c.orm.Recipe.Query().
		Where(recipe.TenantID(tenantID), recipe.Sku(sku), recipe.IsActive(true)).
		WithIngredients(func(q *ent.RecipeIngredientQuery) { q.Order(ent.Asc(recipeingredient.FieldDisplayOrder)) }).
		Only(ctx)
	if err != nil || len(r.Edges.Ingredients) == 0 {
		return nil, fmt.Errorf("recipe not found / empty for sku=%s", sku)
	}
	mult := qty / r.OutputQty
	out := make([]stock.ConsumptionItem, 0, len(r.Edges.Ingredients))
	for _, ing := range r.Edges.Ingredients {
		out = append(out, stock.ConsumptionItem{SKU: ing.ItemSku, Quantity: math.Round(ing.Quantity*mult*10000) / 10000})
	}
	return out, nil
}
