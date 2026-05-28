package bundles

import (
	"context"
	"fmt"

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
}

// BundleDTO represents a product bundle.
type BundleDTO struct {
	ID         uuid.UUID            `json:"id"`
	TenantID   uuid.UUID            `json:"tenant_id"`
	ItemID     uuid.UUID            `json:"item_id"`
	ItemName   string               `json:"item_name,omitempty"`
	Name       string               `json:"name"`
	IsActive   bool                 `json:"is_active"`
	Components []BundleComponentDTO `json:"components"`
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

	b, err := tx.Bundle.Create().
		SetTenantID(tenantID).
		SetItemID(dto.ItemID).
		SetName(dto.Name).
		SetIsActive(dto.IsActive).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("bundles: create: %w", err)
	}

	for i, comp := range dto.Components {
		if _, err := tx.BundleComponent.Create().
			SetBundleID(b.ID).
			SetComponentItemID(comp.ComponentItemID).
			SetQuantity(comp.Quantity).
			SetSortOrder(i).
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("bundles: create component %d: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.log.Info("bundle created", zap.String("bundle_id", b.ID.String()))
	return s.GetBundle(ctx, tenantID, b.ID)
}

// UpdateBundle replaces a bundle's metadata and components.
func (s *Service) UpdateBundle(ctx context.Context, tenantID, id uuid.UUID, dto BundleDTO) (*BundleDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Bundle.Update().
		Where(bundle.TenantID(tenantID), bundle.ID(id)).
		SetName(dto.Name).
		SetIsActive(dto.IsActive).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("bundles: update: %w", err)
	}

	if _, err := tx.BundleComponent.Delete().
		Where(bundlecomponent.BundleID(id)).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("bundles: clear components: %w", err)
	}

	for i, comp := range dto.Components {
		if _, err := tx.BundleComponent.Create().
			SetBundleID(id).
			SetComponentItemID(comp.ComponentItemID).
			SetQuantity(comp.Quantity).
			SetSortOrder(i).
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("bundles: update component %d: %w", i, err)
		}
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
		ID:         b.ID,
		TenantID:   b.TenantID,
		ItemID:     b.ItemID,
		Name:       b.Name,
		IsActive:   b.IsActive,
		Components: make([]BundleComponentDTO, len(b.Edges.Components)),
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
		}
		if c.Edges.ComponentItem != nil {
			comp.ItemName = c.Edges.ComponentItem.Name
			comp.ItemSKU = c.Edges.ComponentItem.Sku
		}
		dto.Components[i] = comp
	}
	return dto
}
