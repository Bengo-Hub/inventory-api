package items

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/ent/item"
)

// MaxDescriptionLength bounds a product description (characters, not bytes).
const MaxDescriptionLength = 4000

// ErrDescriptionTooLong is returned when a description exceeds MaxDescriptionLength.
var ErrDescriptionTooLong = errors.New("description is too long")

// SetDescriptionBySKU changes only an item's description, leaving every other field as it is
// (UpdateItem writes the whole item, so a bulk copy fix through it would clobber names, tags and
// metadata). Publishes item.updated in the same transaction, like SetTaxCodeBySKU. An unchanged
// description is a no-op (changed=false).
func (s *Service) SetDescriptionBySKU(ctx context.Context, tenantID uuid.UUID, sku, description string) (*ItemDTO, bool, error) {
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) > MaxDescriptionLength {
		return nil, false, fmt.Errorf("%w: at most %d characters", ErrDescriptionTooLong, MaxDescriptionLength)
	}
	itm, err := s.client.Item.Query().Where(item.TenantID(tenantID), item.Sku(sku)).Only(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("items: item %q: %w", sku, err)
	}
	if itm.Description == description {
		return s.mapToDTO(itm), false, nil
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("items: set description begin tx: %w", err)
	}
	updated, err := tx.Item.UpdateOneID(itm.ID).SetDescription(description).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, false, fmt.Errorf("items: set description for %q: %w", sku, err)
	}
	if err := s.emitItemUpdatedEvent(ctx, tx, tenantID, updated); err != nil {
		_ = tx.Rollback()
		return nil, false, fmt.Errorf("items: set description event for %q: %w", sku, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("items: set description commit for %q: %w", sku, err)
	}
	return s.mapToDTO(updated), true, nil
}
