package modifiers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	events "github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/modifiergroup"
	"github.com/bengobox/inventory-service/internal/ent/modifieroption"
)

// ModifierGroupDTO represents a modifier group with its options.
type ModifierGroupDTO struct {
	ID            uuid.UUID           `json:"id"`
	TenantID      uuid.UUID           `json:"tenant_id"`
	ItemID        uuid.UUID           `json:"item_id"`
	Name          string              `json:"name"`
	IsRequired    bool                `json:"is_required"`
	MinSelections int                 `json:"min_selections"`
	MaxSelections int                 `json:"max_selections"`
	DisplayOrder  int                 `json:"display_order"`
	Options       []ModifierOptionDTO `json:"options,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

// ModifierOptionDTO represents a single modifier option.
type ModifierOptionDTO struct {
	ID              uuid.UUID `json:"id"`
	GroupID         uuid.UUID `json:"group_id"`
	Name            string    `json:"name"`
	SKU             string    `json:"sku,omitempty"`
	PriceAdjustment float64  `json:"price_adjustment"`
	IsDefault       bool     `json:"is_default"`
	IsActive        bool     `json:"is_active"`
	DisplayOrder    int      `json:"display_order"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// CreateModifierGroupRequest is the request body for creating a modifier group.
type CreateModifierGroupRequest struct {
	ItemID        uuid.UUID `json:"item_id"`
	Name          string    `json:"name"`
	IsRequired    bool      `json:"is_required"`
	MinSelections int       `json:"min_selections"`
	MaxSelections int       `json:"max_selections"`
	DisplayOrder  int       `json:"display_order"`
}

// UpdateModifierGroupRequest is the request body for updating a modifier group.
type UpdateModifierGroupRequest struct {
	Name          *string `json:"name,omitempty"`
	IsRequired    *bool   `json:"is_required,omitempty"`
	MinSelections *int    `json:"min_selections,omitempty"`
	MaxSelections *int    `json:"max_selections,omitempty"`
	DisplayOrder  *int    `json:"display_order,omitempty"`
}

// CreateModifierOptionRequest is the request body for creating a modifier option.
type CreateModifierOptionRequest struct {
	Name            string  `json:"name"`
	SKU             string  `json:"sku,omitempty"`
	PriceAdjustment float64 `json:"price_adjustment"`
	IsDefault       bool    `json:"is_default"`
	IsActive        bool    `json:"is_active"`
	DisplayOrder    int     `json:"display_order"`
}

// UpdateModifierOptionRequest is the request body for updating a modifier option.
type UpdateModifierOptionRequest struct {
	Name            *string  `json:"name,omitempty"`
	SKU             *string  `json:"sku,omitempty"`
	PriceAdjustment *float64 `json:"price_adjustment,omitempty"`
	IsDefault       *bool    `json:"is_default,omitempty"`
	IsActive        *bool    `json:"is_active,omitempty"`
	DisplayOrder    *int     `json:"display_order,omitempty"`
}

// Service handles modifier-group business logic.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService creates a new modifiers service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("modifiers.service"),
	}
}

// ListModifierGroups returns modifier groups with options for an item.
func (s *Service) ListModifierGroups(ctx context.Context, tenantID, itemID uuid.UUID) ([]ModifierGroupDTO, error) {
	groups, err := s.client.ModifierGroup.Query().
		Where(
			modifiergroup.TenantID(tenantID),
			modifiergroup.ItemID(itemID),
		).
		WithOptions(func(q *ent.ModifierOptionQuery) {
			q.Order(ent.Asc(modifieroption.FieldDisplayOrder))
		}).
		Order(ent.Asc(modifiergroup.FieldDisplayOrder)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: list groups: %w", err)
	}

	dtos := make([]ModifierGroupDTO, len(groups))
	for i, g := range groups {
		dtos[i] = s.mapGroupToDTO(g)
	}
	return dtos, nil
}

// CreateModifierGroup creates a new modifier group and publishes an event.
func (s *Service) CreateModifierGroup(ctx context.Context, tenantID uuid.UUID, req CreateModifierGroupRequest) (*ModifierGroupDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	g, err := tx.ModifierGroup.Create().
		SetTenantID(tenantID).
		SetItemID(req.ItemID).
		SetName(req.Name).
		SetIsRequired(req.IsRequired).
		SetMinSelections(req.MinSelections).
		SetMaxSelections(req.MaxSelections).
		SetDisplayOrder(req.DisplayOrder).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: create group: %w", err)
	}

	// Publish inventory.item.updated event for modifier change
	if err = s.publishItemUpdatedEvent(ctx, tx, tenantID, req.ItemID, "modifier_group_created"); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("modifiers: commit transaction: %w", err)
	}

	dto := s.mapGroupToDTO(g)
	return &dto, nil
}

// UpdateModifierGroup updates an existing modifier group.
func (s *Service) UpdateModifierGroup(ctx context.Context, tenantID, groupID uuid.UUID, req UpdateModifierGroupRequest) (*ModifierGroupDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	builder := tx.ModifierGroup.UpdateOneID(groupID).
		Where(modifiergroup.TenantID(tenantID))

	if req.Name != nil {
		builder.SetName(*req.Name)
	}
	if req.IsRequired != nil {
		builder.SetIsRequired(*req.IsRequired)
	}
	if req.MinSelections != nil {
		builder.SetMinSelections(*req.MinSelections)
	}
	if req.MaxSelections != nil {
		builder.SetMaxSelections(*req.MaxSelections)
	}
	if req.DisplayOrder != nil {
		builder.SetDisplayOrder(*req.DisplayOrder)
	}

	g, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: update group: %w", err)
	}

	if err = s.publishItemUpdatedEvent(ctx, tx, tenantID, g.ItemID, "modifier_group_updated"); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("modifiers: commit transaction: %w", err)
	}

	dto := s.mapGroupToDTO(g)
	return &dto, nil
}

// DeleteModifierGroup deletes a modifier group and its options.
func (s *Service) DeleteModifierGroup(ctx context.Context, tenantID, groupID uuid.UUID) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("modifiers: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Look up group to get itemID for event
	g, err := tx.ModifierGroup.Query().
		Where(modifiergroup.ID(groupID), modifiergroup.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("modifiers: find group: %w", err)
	}

	// Delete options first
	_, err = tx.ModifierOption.Delete().
		Where(modifieroption.GroupID(groupID)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("modifiers: delete group options: %w", err)
	}

	err = tx.ModifierGroup.DeleteOneID(groupID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("modifiers: delete group: %w", err)
	}

	if err = s.publishItemUpdatedEvent(ctx, tx, tenantID, g.ItemID, "modifier_group_deleted"); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("modifiers: commit transaction: %w", err)
	}

	return nil
}

// CreateModifierOption adds an option to a modifier group.
func (s *Service) CreateModifierOption(ctx context.Context, tenantID, groupID uuid.UUID, req CreateModifierOptionRequest) (*ModifierOptionDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Validate group belongs to tenant
	g, err := tx.ModifierGroup.Query().
		Where(modifiergroup.ID(groupID), modifiergroup.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: find group: %w", err)
	}

	builder := tx.ModifierOption.Create().
		SetGroupID(groupID).
		SetName(req.Name).
		SetPriceAdjustment(req.PriceAdjustment).
		SetIsDefault(req.IsDefault).
		SetIsActive(req.IsActive).
		SetDisplayOrder(req.DisplayOrder)

	if req.SKU != "" {
		builder.SetSku(req.SKU)
	}

	opt, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: create option: %w", err)
	}

	if err = s.publishItemUpdatedEvent(ctx, tx, tenantID, g.ItemID, "modifier_option_created"); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("modifiers: commit transaction: %w", err)
	}

	dto := s.mapOptionToDTO(opt)
	return &dto, nil
}

// UpdateModifierOption updates a modifier option.
func (s *Service) UpdateModifierOption(ctx context.Context, tenantID, optionID uuid.UUID, req UpdateModifierOptionRequest) (*ModifierOptionDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Get option's group to validate tenant
	opt, err := tx.ModifierOption.Query().
		Where(modifieroption.ID(optionID)).
		WithGroup().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: find option: %w", err)
	}
	if opt.Edges.Group == nil || opt.Edges.Group.TenantID != tenantID {
		return nil, fmt.Errorf("modifiers: option not found for tenant")
	}

	builder := tx.ModifierOption.UpdateOneID(optionID)
	if req.Name != nil {
		builder.SetName(*req.Name)
	}
	if req.SKU != nil {
		builder.SetSku(*req.SKU)
	}
	if req.PriceAdjustment != nil {
		builder.SetPriceAdjustment(*req.PriceAdjustment)
	}
	if req.IsDefault != nil {
		builder.SetIsDefault(*req.IsDefault)
	}
	if req.IsActive != nil {
		builder.SetIsActive(*req.IsActive)
	}
	if req.DisplayOrder != nil {
		builder.SetDisplayOrder(*req.DisplayOrder)
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("modifiers: update option: %w", err)
	}

	if err = s.publishItemUpdatedEvent(ctx, tx, tenantID, opt.Edges.Group.ItemID, "modifier_option_updated"); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("modifiers: commit transaction: %w", err)
	}

	dto := s.mapOptionToDTO(updated)
	return &dto, nil
}

// DeleteModifierOption deletes a modifier option.
func (s *Service) DeleteModifierOption(ctx context.Context, tenantID, optionID uuid.UUID) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("modifiers: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Get option's group to validate tenant
	opt, err := tx.ModifierOption.Query().
		Where(modifieroption.ID(optionID)).
		WithGroup().
		Only(ctx)
	if err != nil {
		return fmt.Errorf("modifiers: find option: %w", err)
	}
	if opt.Edges.Group == nil || opt.Edges.Group.TenantID != tenantID {
		return fmt.Errorf("modifiers: option not found for tenant")
	}

	err = tx.ModifierOption.DeleteOneID(optionID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("modifiers: delete option: %w", err)
	}

	if err = s.publishItemUpdatedEvent(ctx, tx, tenantID, opt.Edges.Group.ItemID, "modifier_option_deleted"); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("modifiers: commit transaction: %w", err)
	}

	return nil
}

// publishItemUpdatedEvent writes an outbox event when modifiers change.
func (s *Service) publishItemUpdatedEvent(ctx context.Context, tx *ent.Tx, tenantID, itemID uuid.UUID, reason string) error {
	event := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "item",
		AggregateID:   itemID,
		EventType:     "inventory.item.updated",
		Payload: map[string]any{
			"item_id": itemID,
			"reason":  reason,
		},
		Timestamp: time.Now().UTC(),
	}

	payload, err := event.ToJSON()
	if err != nil {
		return fmt.Errorf("modifiers: marshal event: %w", err)
	}

	_, err = tx.OutboxEvent.Create().
		SetID(event.ID).
		SetTenantID(tenantID).
		SetAggregateType(event.AggregateType).
		SetAggregateID(event.AggregateID.String()).
		SetEventType(event.EventType).
		SetPayload(payload).
		SetStatus("PENDING").
		SetCreatedAt(event.Timestamp).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("modifiers: create outbox record: %w", err)
	}

	return nil
}

// mapGroupToDTO maps an ent ModifierGroup to a DTO.
func (s *Service) mapGroupToDTO(g *ent.ModifierGroup) ModifierGroupDTO {
	dto := ModifierGroupDTO{
		ID:            g.ID,
		TenantID:      g.TenantID,
		ItemID:        g.ItemID,
		Name:          g.Name,
		IsRequired:    g.IsRequired,
		MinSelections: g.MinSelections,
		MaxSelections: g.MaxSelections,
		DisplayOrder:  g.DisplayOrder,
		CreatedAt:     g.CreatedAt,
		UpdatedAt:     g.UpdatedAt,
	}

	if g.Edges.Options != nil {
		dto.Options = make([]ModifierOptionDTO, len(g.Edges.Options))
		for i, opt := range g.Edges.Options {
			dto.Options[i] = s.mapOptionToDTO(opt)
		}
	}

	return dto
}

// mapOptionToDTO maps an ent ModifierOption to a DTO.
func (s *Service) mapOptionToDTO(opt *ent.ModifierOption) ModifierOptionDTO {
	return ModifierOptionDTO{
		ID:              opt.ID,
		GroupID:         opt.GroupID,
		Name:            opt.Name,
		SKU:             opt.Sku,
		PriceAdjustment: opt.PriceAdjustment,
		IsDefault:       opt.IsDefault,
		IsActive:        opt.IsActive,
		DisplayOrder:    opt.DisplayOrder,
		CreatedAt:       opt.CreatedAt,
		UpdatedAt:       opt.UpdatedAt,
	}
}
