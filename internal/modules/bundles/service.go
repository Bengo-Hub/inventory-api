package bundles

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	events "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/bundle"
	"github.com/bengobox/inventory-service/internal/ent/bundlecomponent"
)

// Service handles bundle (product kit) management.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService creates a new bundle service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("bundles.service"),
	}
}

// BundleComponentDTO represents a single item within a bundle.
type BundleComponentDTO struct {
	ID              uuid.UUID `json:"id"`
	ComponentItemID uuid.UUID `json:"component_item_id"`
	ItemName        string    `json:"item_name,omitempty"`
	ItemSKU         string    `json:"item_sku,omitempty"`
	Quantity        int       `json:"quantity"`
	SortOrder       int       `json:"sort_order"`
	// Hospitality package semantics
	ComponentKind string  `json:"component_kind,omitempty"` // ITEM | MEAL_PERIOD | AV_EQUIPMENT | STATIONERY | CONSUMABLE | FACILITY | SERVICE_SESSION
	MealPeriod    *string `json:"meal_period,omitempty"`    // breakfast | am_break | lunch | pm_break | dinner
	IsMetered     bool    `json:"is_metered"`
	Unit          string  `json:"unit,omitempty"`
}

// BundleDTO represents a product bundle / hospitality package.
type BundleDTO struct {
	ID         uuid.UUID            `json:"id"`
	TenantID   uuid.UUID            `json:"tenant_id"`
	ItemID     uuid.UUID            `json:"item_id"`
	ItemName   string               `json:"item_name,omitempty"`
	Name       string               `json:"name"`
	IsActive   bool                 `json:"is_active"`
	Components []BundleComponentDTO `json:"components"`
	// Hospitality package attributes
	PackageType           string    `json:"package_type,omitempty"` // RETAIL_KIT | ROOM_RATE_PLAN | DDR | RDR | HALF_BOARD | FULL_BOARD | HALL_HIRE_ONLY | SERVICE_SESSIONS
	PriceBasis            string    `json:"price_basis,omitempty"`  // flat | per_delegate_per_day | per_person_sharing | per_session
	MinDelegates          *int      `json:"min_delegates,omitempty"`
	AccommodationIncluded bool      `json:"accommodation_included"`
	SessionsTotal         *int      `json:"sessions_total,omitempty"`
	ValidityDays          *int      `json:"validity_days,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

// ListBundles returns paginated bundles for a tenant.
func (s *Service) ListBundles(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]BundleDTO, int, error) {
	q := s.client.Bundle.Query().Where(bundle.TenantID(tenantID))
	total, _ := q.Clone().Count(ctx)
	bundles, err := q.
		WithComponents(func(cq *ent.BundleComponentQuery) {
			cq.WithComponentItem().Order(ent.Asc(bundlecomponent.FieldSortOrder))
		}).
		WithItem().
		Order(ent.Asc(bundle.FieldName)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("bundles: list: %w", err)
	}
	result := make([]BundleDTO, len(bundles))
	for i, b := range bundles {
		result[i] = toDTO(b)
	}
	return result, total, nil
}

// GetBundle returns a single bundle by ID.
func (s *Service) GetBundle(ctx context.Context, tenantID, id uuid.UUID) (*BundleDTO, error) {
	b, err := s.client.Bundle.Query().
		Where(bundle.TenantID(tenantID), bundle.ID(id)).
		WithComponents(func(cq *ent.BundleComponentQuery) {
			cq.WithComponentItem().Order(ent.Asc(bundlecomponent.FieldSortOrder))
		}).
		WithItem().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("bundles: get: %w", err)
	}
	dto := toDTO(b)
	return &dto, nil
}

// CreateBundle creates a new bundle with its components.
func (s *Service) CreateBundle(ctx context.Context, tenantID uuid.UUID, dto BundleDTO) (*BundleDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}

	bc := tx.Bundle.Create().
		SetTenantID(tenantID).
		SetItemID(dto.ItemID).
		SetName(dto.Name).
		SetIsActive(dto.IsActive).
		SetAccommodationIncluded(dto.AccommodationIncluded)
	if dto.PackageType != "" {
		bc = bc.SetPackageType(bundle.PackageType(dto.PackageType))
	}
	if dto.PriceBasis != "" {
		bc = bc.SetPriceBasis(bundle.PriceBasis(dto.PriceBasis))
	}
	if dto.MinDelegates != nil {
		bc = bc.SetMinDelegates(*dto.MinDelegates)
	}
	if dto.SessionsTotal != nil {
		bc = bc.SetSessionsTotal(*dto.SessionsTotal)
	}
	if dto.ValidityDays != nil {
		bc = bc.SetValidityDays(*dto.ValidityDays)
	}
	b, err := bc.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("bundles: create: %w", err)
	}

	if err := s.createComponents(ctx, tx, b.ID, dto.Components); err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	if err := s.publishBundleEvent(ctx, tx, tenantID, b, "bundle.created"); err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.log.Info("bundle created", zap.String("bundle_id", b.ID.String()))
	return s.GetBundle(ctx, tenantID, b.ID)
}

// createComponents inserts bundle component rows within the given transaction.
func (s *Service) createComponents(ctx context.Context, tx *ent.Tx, bundleID uuid.UUID, comps []BundleComponentDTO) error {
	for i, comp := range comps {
		cb := tx.BundleComponent.Create().
			SetBundleID(bundleID).
			SetComponentItemID(comp.ComponentItemID).
			SetQuantity(comp.Quantity).
			SetIsMetered(comp.IsMetered).
			SetSortOrder(i)
		if comp.ComponentKind != "" {
			cb = cb.SetComponentKind(bundlecomponent.ComponentKind(comp.ComponentKind))
		}
		if comp.MealPeriod != nil {
			cb = cb.SetMealPeriod(bundlecomponent.MealPeriod(*comp.MealPeriod))
		}
		if comp.Unit != "" {
			cb = cb.SetUnit(comp.Unit)
		}
		if _, err := cb.Save(ctx); err != nil {
			return fmt.Errorf("bundles: create component %d: %w", i, err)
		}
	}
	return nil
}

// publishBundleEvent writes an inventory.bundle.* outbox event so POS can project packages.
func (s *Service) publishBundleEvent(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, b *ent.Bundle, eventType string) error {
	// Resolve the parent item's SKU/name so POS can join its catalog projection by sku
	// (preferred join key) without waiting for the item event to arrive first.
	sku, itemName := "", ""
	if itm, ierr := tx.Item.Get(ctx, b.ItemID); ierr == nil {
		sku, itemName = itm.Sku, itm.Name
	}
	evt := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "inventory",
		AggregateID:   b.ID,
		EventType:     eventType,
		Payload: map[string]any{
			"id":                     b.ID,
			"item_id":                b.ItemID,
			"sku":                    sku,
			"item_name":              itemName,
			"name":                   b.Name,
			"is_active":              b.IsActive,
			"package_type":           b.PackageType,
			"price_basis":            b.PriceBasis,
			"min_delegates":          b.MinDelegates,
			"accommodation_included": b.AccommodationIncluded,
			"sessions_total":         b.SessionsTotal,
			"validity_days":          b.ValidityDays,
		},
		Timestamp: time.Now().UTC(),
	}
	payload, err := evt.ToJSON()
	if err != nil {
		return fmt.Errorf("bundles: marshal event: %w", err)
	}
	_, err = tx.OutboxEvent.Create().
		SetID(evt.ID).
		SetTenantID(tenantID).
		SetAggregateType(evt.AggregateType).
		SetAggregateID(evt.AggregateID.String()).
		SetEventType(evt.EventType).
		SetPayload(json.RawMessage(payload)).
		SetStatus("PENDING").
		SetCreatedAt(evt.Timestamp).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("bundles: create outbox record: %w", err)
	}
	return nil
}

// UpdateBundle replaces a bundle's metadata and components.
func (s *Service) UpdateBundle(ctx context.Context, tenantID, id uuid.UUID, dto BundleDTO) (*BundleDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}

	ub := tx.Bundle.Update().
		Where(bundle.TenantID(tenantID), bundle.ID(id)).
		SetName(dto.Name).
		SetIsActive(dto.IsActive).
		SetAccommodationIncluded(dto.AccommodationIncluded)
	if dto.PackageType != "" {
		ub = ub.SetPackageType(bundle.PackageType(dto.PackageType))
	}
	if dto.PriceBasis != "" {
		ub = ub.SetPriceBasis(bundle.PriceBasis(dto.PriceBasis))
	}
	if dto.MinDelegates != nil {
		ub = ub.SetMinDelegates(*dto.MinDelegates)
	}
	if dto.SessionsTotal != nil {
		ub = ub.SetSessionsTotal(*dto.SessionsTotal)
	}
	if dto.ValidityDays != nil {
		ub = ub.SetValidityDays(*dto.ValidityDays)
	}
	if _, err := ub.Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("bundles: update: %w", err)
	}

	if _, err := tx.BundleComponent.Delete().
		Where(bundlecomponent.BundleID(id)).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("bundles: clear components: %w", err)
	}

	if err := s.createComponents(ctx, tx, id, dto.Components); err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	b, err := tx.Bundle.Get(ctx, id)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("bundles: reload after update: %w", err)
	}
	if err := s.publishBundleEvent(ctx, tx, tenantID, b, "bundle.updated"); err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetBundle(ctx, tenantID, id)
}

// DeleteBundle removes a bundle.
func (s *Service) DeleteBundle(ctx context.Context, tenantID, id uuid.UUID) error {
	_, err := s.client.Bundle.Delete().
		Where(bundle.TenantID(tenantID), bundle.ID(id)).
		Exec(ctx)
	return err
}

func toDTO(b *ent.Bundle) BundleDTO {
	dto := BundleDTO{
		ID:                    b.ID,
		TenantID:              b.TenantID,
		ItemID:                b.ItemID,
		Name:                  b.Name,
		IsActive:              b.IsActive,
		PackageType:           string(b.PackageType),
		PriceBasis:            string(b.PriceBasis),
		MinDelegates:          b.MinDelegates,
		AccommodationIncluded: b.AccommodationIncluded,
		SessionsTotal:         b.SessionsTotal,
		ValidityDays:          b.ValidityDays,
		CreatedAt:             b.CreatedAt,
		Components:            make([]BundleComponentDTO, len(b.Edges.Components)),
	}
	if b.Edges.Item != nil {
		dto.ItemName = b.Edges.Item.Name
	}
	for i, c := range b.Edges.Components {
		comp := BundleComponentDTO{
			ID:              c.ID,
			ComponentItemID: c.ComponentItemID,
			Quantity:        c.Quantity,
			SortOrder:       c.SortOrder,
			ComponentKind:   string(c.ComponentKind),
			IsMetered:       c.IsMetered,
			Unit:            c.Unit,
		}
		if c.MealPeriod != nil {
			mp := string(*c.MealPeriod)
			comp.MealPeriod = &mp
		}
		if c.Edges.ComponentItem != nil {
			comp.ItemName = c.Edges.ComponentItem.Name
			comp.ItemSKU = c.Edges.ComponentItem.Sku
		}
		dto.Components[i] = comp
	}
	return dto
}
