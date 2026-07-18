package units

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/shared-events"
	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/unit"
)

type UnitDTO struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Abbreviation string    `json:"abbreviation,omitempty"`
	Type         string    `json:"type,omitempty"`
	// KRA eTIMS quantity-unit code this unit maps to (e.g. KG, U, LTR) — the default
	// qtyUnitCd for items measured in this unit during eTIMS registration.
	KraQtyUnitCd string `json:"kra_qty_unit_cd,omitempty"`
	// Outlet use_cases this unit is relevant to (hospitality, pharmacy, retail…);
	// empty = universal. Units are global — tags keep culinary units (tot, pot,
	// portion) out of pharmacy/retail pickers.
	UseCases  []string `json:"use_cases,omitempty"`
	IsActive  bool     `json:"is_active"`
	ItemCount int      `json:"item_count"`
}

// RelevantToUseCase reports whether the unit applies to an outlet use_case:
// untagged units are universal; tagged units match only their use_cases.
func (d UnitDTO) RelevantToUseCase(useCase string) bool {
	if useCase == "" || len(d.UseCases) == 0 {
		return true
	}
	for _, uc := range d.UseCases {
		if strings.EqualFold(uc, useCase) {
			return true
		}
	}
	return false
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

// DuplicateUnitError is returned by CreateUnit/UpdateUnit when a case-insensitive
// name or abbreviation collision is found. Units are a GLOBAL table (see the
// service methods' unused tenantID params), so this is a system-wide check, not
// per-tenant. Handlers use errors.As to map this to a 409 with an actionable
// message instead of a raw 500 / DB constraint error.
type DuplicateUnitError struct {
	Field string // "name" or "abbreviation"
	Value string
}

func (e *DuplicateUnitError) Error() string {
	return fmt.Sprintf("a unit with %s %q already exists", e.Field, e.Value)
}

// checkDuplicateUnit looks for an existing ACTIVE unit whose name or (if given)
// abbreviation case-insensitively matches, excluding excludeID (used by updates so
// a unit doesn't collide with itself). Soft-deleted (is_active=false) units never
// block reuse of their name/abbreviation, mirroring ListUnits' active-only scope.
func checkDuplicateUnit(ctx context.Context, uc *ent.UnitClient, name, abbreviation string, excludeID *uuid.UUID) error {
	nameQ := uc.Query().Where(unit.IsActive(true), unit.NameEqualFold(name))
	if excludeID != nil {
		nameQ = nameQ.Where(unit.IDNEQ(*excludeID))
	}
	if existing, err := nameQ.Only(ctx); err == nil {
		return &DuplicateUnitError{Field: "name", Value: existing.Name}
	} else if !ent.IsNotFound(err) {
		return fmt.Errorf("units: check duplicate name: %w", err)
	}

	if abbreviation == "" {
		return nil
	}
	abbrQ := uc.Query().Where(unit.IsActive(true), unit.AbbreviationEqualFold(abbreviation))
	if excludeID != nil {
		abbrQ = abbrQ.Where(unit.IDNEQ(*excludeID))
	}
	if existing, err := abbrQ.Only(ctx); err == nil {
		return &DuplicateUnitError{Field: "abbreviation", Value: existing.Abbreviation}
	} else if !ent.IsNotFound(err) {
		return fmt.Errorf("units: check duplicate abbreviation: %w", err)
	}
	return nil
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
			KraQtyUnitCd: u.KraQtyUnitCd,
			UseCases:     u.UseCases,
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

// normalizeAbbreviation lowercases and trims a unit abbreviation so it always
// matches the canonical casing units.NormalizeUnit expects (kg, ml, pc, ...),
// regardless of whether the caller is a UI form, a direct API client, or a
// bulk import — the abbreviation is a short code, not a display string, so
// there is no legitimate reason for it to carry mixed case.
func normalizeAbbreviation(abbr string) string {
	return strings.ToLower(strings.TrimSpace(abbr))
}

func (s *Service) UpdateUnit(ctx context.Context, _ uuid.UUID, id uuid.UUID, dto UnitDTO) (*UnitDTO, error) {
	dto.Name = strings.TrimSpace(dto.Name)
	dto.Abbreviation = normalizeAbbreviation(dto.Abbreviation)
	if err := checkDuplicateUnit(ctx, s.client.Unit, dto.Name, dto.Abbreviation, &id); err != nil {
		return nil, err
	}

	upd := s.client.Unit.UpdateOneID(id).SetName(dto.Name)
	// Empty abbreviation means "clear it" (stored as NULL, not "") so it never
	// collides with another unit's blank abbreviation under the unique index.
	if dto.Abbreviation != "" {
		upd = upd.SetAbbreviation(dto.Abbreviation)
	} else {
		upd = upd.ClearAbbreviation()
	}
	if dto.Type != "" {
		upd = upd.SetType(dto.Type)
	} else {
		upd = upd.ClearType()
	}
	if kra := strings.ToUpper(strings.TrimSpace(dto.KraQtyUnitCd)); kra != "" {
		upd = upd.SetKraQtyUnitCd(kra)
	} else {
		upd = upd.ClearKraQtyUnitCd()
	}
	u, err := upd.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("units: unit not found")
		}
		if ent.IsConstraintError(err) {
			return nil, &DuplicateUnitError{Field: "name", Value: dto.Name}
		}
		return nil, fmt.Errorf("units: update unit: %w", err)
	}
	return &UnitDTO{
		ID:           u.ID,
		Name:         u.Name,
		Abbreviation: u.Abbreviation,
		Type:         u.Type,
		KraQtyUnitCd: u.KraQtyUnitCd,
		UseCases:     u.UseCases,
		IsActive:     u.IsActive,
	}, nil
}

func (s *Service) CreateUnit(ctx context.Context, _ uuid.UUID, dto UnitDTO) (*UnitDTO, error) {
	dto.Name = strings.TrimSpace(dto.Name)
	dto.Abbreviation = normalizeAbbreviation(dto.Abbreviation)
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("units: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = checkDuplicateUnit(ctx, tx.Unit, dto.Name, dto.Abbreviation, nil); err != nil {
		return nil, err
	}

	cre := tx.Unit.Create().
		SetName(dto.Name).
		SetIsActive(true)
	// Blank abbreviation is left unset (NULL) rather than "" so it never collides
	// with another unit's blank abbreviation under the unique index.
	if dto.Abbreviation != "" {
		cre = cre.SetAbbreviation(dto.Abbreviation)
	}
	if dto.Type != "" {
		cre = cre.SetType(dto.Type)
	}
	if kra := strings.ToUpper(strings.TrimSpace(dto.KraQtyUnitCd)); kra != "" {
		cre = cre.SetKraQtyUnitCd(kra)
	}
	// Use-case tags: a unit quick-created from a hospitality outlet is stamped
	// hospitality so it never pollutes pharmacy/retail pickers. Untagged = universal.
	if len(dto.UseCases) > 0 {
		cre = cre.SetUseCases(dto.UseCases)
	}
	u, err := cre.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			err = &DuplicateUnitError{Field: "name", Value: dto.Name}
		} else {
			err = fmt.Errorf("units: create unit: %w", err)
		}
		return nil, err
	}
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
		KraQtyUnitCd: u.KraQtyUnitCd,
		UseCases:     u.UseCases,
		IsActive:     u.IsActive,
	}, nil
}
