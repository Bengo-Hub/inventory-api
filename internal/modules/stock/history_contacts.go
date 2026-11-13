package stock

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Bengo-Hub/shared-service-client/crm"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// linkedBuyerFallback is the counterparty of a sale whose buyer is a CRM contact the CRM could
// not name right now (CRM down, contact erased or of another tenant). The sale did have a buyer,
// so it is not shown as "Walk-in".
const linkedBuyerFallback = "Customer"

// contactLookupTimeout bounds the one CRM call a ledger page makes.
const contactLookupTimeout = 3 * time.Second

// ContactBatchGetter reads CRM contacts by id. *crm.Client implements it; tests use a fake.
type ContactBatchGetter interface {
	BatchGet(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]crm.Contact, error)
}

// WithContactResolver wires the CRM client the stock-history ledger uses to name a linked
// buyer. Optional: when unset, a linked buyer's rows show linkedBuyerFallback.
func (s *Service) WithContactResolver(r ContactBatchGetter) *Service {
	s.contacts = r
	return s
}

// counterpartyContactIDs returns the distinct CRM contact ids of rows still waiting for a name.
func counterpartyContactIDs(rows []MovementRow) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{}
	var ids []uuid.UUID
	for _, r := range rows {
		if r.crmContactID == nil || *r.crmContactID == uuid.Nil {
			continue
		}
		if _, ok := seen[*r.crmContactID]; ok {
			continue
		}
		seen[*r.crmContactID] = struct{}{}
		ids = append(ids, *r.crmContactID)
	}
	return ids
}

// applyContactNames sets the counterparty of every row linked to a CRM contact: the contact's
// name when names has one, else linkedBuyerFallback.
func applyContactNames(rows []MovementRow, names map[uuid.UUID]string) {
	for i := range rows {
		if rows[i].crmContactID == nil {
			continue
		}
		if n := names[*rows[i].crmContactID]; n != "" {
			rows[i].Counterparty = n
		} else {
			rows[i].Counterparty = linkedBuyerFallback
		}
	}
}

// contactDisplayNames maps contacts to "first last", leaving out contacts with no name.
func contactDisplayNames(contacts map[uuid.UUID]crm.Contact) map[uuid.UUID]string {
	names := make(map[uuid.UUID]string, len(contacts))
	for id, c := range contacts {
		if n := joinName(c.FirstName, c.LastName); n != "" {
			names[id] = n
		}
	}
	return names
}

func joinName(first, last string) string {
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	switch {
	case first == "":
		return last
	case last == "":
		return first
	default:
		return first + " " + last
	}
}

// resolveContactCounterparties names the linked buyers on one ledger page with a single CRM
// batch call. Best-effort: any failure leaves linkedBuyerFallback. Only ids are logged.
func (s *Service) resolveContactCounterparties(ctx context.Context, tenantID uuid.UUID, rows []MovementRow) {
	ids := counterpartyContactIDs(rows)
	if len(ids) == 0 {
		return
	}
	var names map[uuid.UUID]string
	if s.contacts != nil {
		cctx, cancel := context.WithTimeout(ctx, contactLookupTimeout)
		contacts, err := s.contacts.BatchGet(cctx, tenantID, ids)
		cancel()
		if err != nil {
			if !errors.Is(err, crm.ErrDisabled) {
				s.log.Warn("stock history: CRM contact lookup failed, showing fallback counterparty",
					zap.String("tenant_id", tenantID.String()), zap.Int("contacts", len(ids)), zap.Error(err))
			}
		} else {
			names = contactDisplayNames(contacts)
			if missing := len(ids) - len(names); missing > 0 {
				s.log.Debug("stock history: CRM contacts without a name",
					zap.String("tenant_id", tenantID.String()), zap.Int("missing", missing))
			}
		}
	}
	applyContactNames(rows, names)
}
