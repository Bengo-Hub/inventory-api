package items

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent/item"
)

// ErrUnknownTaxCode is returned when a tax code is not one of the tenant's active treasury tax
// codes, or when treasury cannot be asked (fail closed: an item never gets a code nobody checked).
var ErrUnknownTaxCode = errors.New("unknown tax code")

// SetTaxCodeBySKU changes one item's tax code (tax_code_id) and nothing else. The code must be
// one of the tenant's active treasury tax codes; it is stored in treasury's spelling. Unchanged
// codes are a no-op (changed=false). A change writes the item and its item.updated event in one
// transaction, so POS and ordering re-price it, and is audited.
//
// Nothing here talks to KRA: an item's eTIMS registration (and its tax band there) is changed
// only by an explicit sync from treasury.
func (s *Service) SetTaxCodeBySKU(ctx context.Context, tenantID uuid.UUID, sku, code string) (*ItemDTO, bool, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, false, fmt.Errorf("%w: tax_code_id is required", ErrUnknownTaxCode)
	}
	if s.taxResolver == nil {
		return nil, false, fmt.Errorf("%w: tax codes are unavailable", ErrUnknownTaxCode)
	}
	// ResolveVATRate falls back to the default code for an unknown one, so only an exact match
	// counts as valid.
	_, canonical, ok := s.taxResolver.ResolveVATRate(ctx, tenantID, code)
	if !ok || !strings.EqualFold(canonical, code) {
		return nil, false, fmt.Errorf("%w: %q is not an active tax code for this business", ErrUnknownTaxCode, code)
	}
	code = canonical

	itm, err := s.client.Item.Query().Where(item.TenantID(tenantID), item.Sku(sku)).Only(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("items: item %q: %w", sku, err)
	}
	if itm.TaxCodeID == code {
		return s.mapToDTO(itm), false, nil
	}
	previous := itm.TaxCodeID

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("items: set tax code begin tx: %w", err)
	}
	updated, err := tx.Item.UpdateOneID(itm.ID).SetTaxCodeID(code).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, false, fmt.Errorf("items: set tax code for %q: %w", sku, err)
	}
	if err := s.emitItemUpdatedEvent(ctx, tx, tenantID, updated); err != nil {
		_ = tx.Rollback()
		return nil, false, fmt.Errorf("items: set tax code event for %q: %w", sku, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("items: set tax code commit for %q: %w", sku, err)
	}

	if s.auditSvc != nil {
		s.auditSvc.Record(ctx, audit.Entry{
			TenantID:    tenantID,
			ActorUserID: actorFromContext(ctx),
			Action:      "item.tax_code_changed",
			EntityType:  "item",
			EntityID:    itm.ID.String(),
			Before:      map[string]any{"sku": itm.Sku, "tax_code_id": previous},
			After:       map[string]any{"sku": itm.Sku, "tax_code_id": code},
		})
	}
	return s.mapToDTO(updated), true, nil
}
