package units

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/unit"
	"github.com/Bengo-Hub/shared-events"
)

type UnitDTO struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Abbreviation string    `json:"abbreviation,omitempty"`
	Type         string    `json:"type,omitempty"`
	IsActive     bool      `json:"is_active"`
	ItemCount    int       `json:"item_count"`
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

func (s *Service) ListUnits(ctx context.Context, _ uuid.UUID) ([]UnitDTO, error) {
	us, err := s.client.Unit.Query().
		Where(unit.IsActive(true)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("units: query units: %w", err)
	}

	// Collect unit IDs to batch-count linked items
	ids := make([]uuid.UUID, len(us))
	for i, u := range us {
		ids[i] = u.ID
	}

	var counts []struct {
		UnitID uuid.UUID `json:"unit_id"`
		Count  int       `json:"count"`
	}
	if scanErr := s.client.Item.Query().
		Where(entitem.UnitIDIn(ids...)).
		GroupBy(entitem.FieldUnitID).
		Aggregate(ent.Count()).
		Scan(ctx, &counts); scanErr != nil {
		// non-fatal: proceed with zero counts
	}

	countMap := make(map[uuid.UUID]int, len(counts))
	for _, c := range counts {
		countMap[c.UnitID] = c.Count
	}

	result := make([]UnitDTO, len(us))
	for i, u := range us {
		result[i] = UnitDTO{
			ID:           u.ID,
			Name:         u.Name,
			Abbreviation: u.Abbreviation,
			Type:         u.Type,
			IsActive:     u.IsActive,
			ItemCount:    countMap[u.ID],
		}
	}
	return result, nil
}

func (s *Service) DeleteUnit(ctx context.Context, _ uuid.UUID, id uuid.UUID) error {
	if _, err := s.client.Unit.UpdateOneID(id).SetIsActive(false).Save(ctx); err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("units: unit not found")
		}
		return fmt.Errorf("units: delete unit: %w", err)
	}
	return nil
}

func (s *Service) UpdateUnit(ctx context.Context, _ uuid.UUID, id uuid.UUID, dto UnitDTO) (*UnitDTO, error) {
	upd := s.client.Unit.UpdateOneID(id).
		SetName(dto.Name).
		SetNillableAbbreviation(&dto.Abbreviation)
	if dto.Type != "" {
		upd = upd.SetType(dto.Type)
	} else {
		upd = upd.ClearType()
	}
	u, err := upd.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("units: unit not found")
		}
		return nil, fmt.Errorf("units: update unit: %w", err)
	}
	return &UnitDTO{
		ID:           u.ID,
		Name:         u.Name,
		Abbreviation: u.Abbreviation,
		Type:         u.Type,
		IsActive:     u.IsActive,
	}, nil
}

func (s *Service) CreateUnit(ctx context.Context, _ uuid.UUID, dto UnitDTO) (*UnitDTO, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("units: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	cre := tx.Unit.Create().
		SetName(dto.Name).
		SetNillableAbbreviation(&dto.Abbreviation).
		SetIsActive(true)
	if dto.Type != "" {
		cre = cre.SetType(dto.Type)
	}
	u, err := cre.Save(ctx)
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
		SetPayload(json.RawMessage(payload)).
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
		Type:         u.Type,
		IsActive:     u.IsActive,
	}, nil
}
