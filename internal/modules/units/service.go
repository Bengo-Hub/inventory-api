package units

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/unit"
	"github.com/Bengo-Hub/shared-events"
)

type UnitDTO struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Abbreviation string    `json:"abbreviation,omitempty"`
	IsActive     bool      `json:"is_active"`
}

type Service struct {
	client *ent.Client
	log    *zap.Logger
}

func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("units.service"),
	}
}

func (s *Service) ListUnits(ctx context.Context) ([]UnitDTO, error) {
	units, err := s.client.Unit.Query().
		Where(unit.IsActive(true)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("units: query units: %w", err)
	}

	result := make([]UnitDTO, len(units))
	for i, u := range units {
		result[i] = UnitDTO{
			ID:           u.ID,
			Name:         u.Name,
			Abbreviation: u.Abbreviation,
			IsActive:     u.IsActive,
		}
	}
	return result, nil
}

func (s *Service) CreateUnit(ctx context.Context, dto UnitDTO) (*UnitDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("units: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	u, err := tx.Unit.Create().
		SetName(dto.Name).
		SetNillableAbbreviation(&dto.Abbreviation).
		SetIsActive(true).
		Save(ctx)
	// Publish event to outbox
	event := &events.Event{
		ID:            uuid.New(),
		AggregateType: "unit",
		AggregateID:   u.ID,
		EventType:     "inventory.unit.created",
		Payload: map[string]any{
			"id":           u.ID,
			"name":         u.Name,
			"abbreviation": u.Abbreviation,
		},
		Timestamp: time.Now().UTC(),
	}

	payload, err := event.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("units: marshal event: %w", err)
	}

	_, err = tx.OutboxEvent.Create().
		SetID(event.ID).
		SetTenantID(uuid.Nil).
		SetAggregateType(event.AggregateType).
		SetAggregateID(event.AggregateID.String()).
		SetEventType(event.EventType).
		SetPayload(payload).
		SetStatus("PENDING").
		SetCreatedAt(event.Timestamp).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("units: create outbox record: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("units: commit transaction: %w", err)
	}

	return &UnitDTO{
		ID:           u.ID,
		Name:         u.Name,
		Abbreviation: u.Abbreviation,
		IsActive:     u.IsActive,
	}, nil
}
