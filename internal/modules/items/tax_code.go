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

// CanonicalTaxCode checks that code is one of the tenant's active treasury tax codes and returns
// it in treasury's spelling, or ErrUnknownTaxCode (also when treasury cannot be asked).
func (s *Service) CanonicalTaxCode(ctx context.Context, tenantID uuid.UUID, code string) (string, error) {
	code = strings.TrimSpace(code)
	if s.taxResolver == nil {
		return "", fmt.Errorf("%w: tax codes are unavailable", ErrUnknownTaxCode)
	}
	// ResolveVATRate falls back to the default code for an unknown one, so only an exact match
	// counts as valid.
	_, canonical, ok := s.taxResolver.ResolveVATRate(ctx, tenantID, code)
	if !ok || !strings.EqualFold(canonical, code) {
		return "", fmt.Errorf("%w: %q is not an active tax code for this business", ErrUnknownTaxCode, code)
	}
	return canonical, nil
}

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
	code, err := s.CanonicalTaxCode(ctx, tenantID, code)
	if err != nil {
		return nil, false, err
	}

	itm, err := s.client.Item.Query().Where(item.TenantID(tenantID), item.Sku(sku)).Only(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("items: item %q: %w", sku, err)
	}
	if itm.TaxCodeID == code && TaxCodeSource(itm.Metadata) == TaxCodeSourceManual {
		return s.mapToDTO(itm), false, nil
	}
	previous := itm.TaxCodeID

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("items: set tax code begin tx: %w", err)
	}
	// A code set here is a person's choice: the VAT on/off job and the daily reconcile leave it.
	updated, err := tx.Item.UpdateOneID(itm.ID).
		SetTaxCodeID(code).
		SetMetadata(withTaxCodeSource(itm.Metadata, TaxCodeSourceManual)).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, false, fmt.Errorf("items: set tax code for %q: %w", sku, err)
	}
	changed := !strings.EqualFold(previous, code)
	if changed {
		if err := s.emitItemUpdatedEvent(ctx, tx, tenantID, updated); err != nil {
			_ = tx.Rollback()
			return nil, false, fmt.Errorf("items: set tax code event for %q: %w", sku, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("items: set tax code commit for %q: %w", sku, err)
	}
	if !changed { // the same code, now pinned as a person's choice
		return s.mapToDTO(updated), false, nil
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
