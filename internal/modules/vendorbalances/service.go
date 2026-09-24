// Package vendorbalances serves treasury's AP balances ("what we owe this supplier") to
// inventory's supplier surfaces from the local VendorBalanceCache mirror, and keeps that mirror
// complete. Treasury owns AP balances; inventory owns the Supplier master
// (treasury-vendor-master-via-inventory).
package vendorbalances

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/vendorbalancecache"
	"github.com/bengobox/inventory-service/internal/platform/treasury"
)

// resyncInterval bounds how often one pod re-pulls a tenant's full AP list from treasury. The
// event consumer keeps the cache current in between; this only heals missed events (a vendor
// untouched since the consumer shipped, a Nak'd/expired delivery).
const resyncInterval = 30 * time.Minute

// Balance is one supplier's cached AP position. BalanceOwed is signed: positive = we owe the
// supplier, negative = the supplier holds a credit in our favour.
type Balance struct {
	BalanceOwed        string
	OutstandingPayable string
	Currency           string
}

// Entry is one balance to write into the cache.
type Entry struct {
	VendorID           *uuid.UUID
	VendorIdentifier   string
	VendorName         string
	BalanceOwed        string
	OutstandingPayable string
	Currency           string
}

type Service struct {
	db       *ent.Client
	treasury *treasury.Client
	log      *zap.Logger

	mu       sync.Mutex
	lastSync map[uuid.UUID]time.Time
}

func NewService(db *ent.Client, treasuryClient *treasury.Client, log *zap.Logger) *Service {
	return &Service{db: db, treasury: treasuryClient, log: log.Named("vendorbalances"), lastSync: map[uuid.UUID]time.Time{}}
}

// Upsert writes one balance into the cache, keyed by vendor_id when known, else by
// vendor_identifier. Shared by the treasury.vendor.balance_updated consumer and Resync.
func Upsert(ctx context.Context, db *ent.Client, tenantID uuid.UUID, e Entry) error {
	if e.VendorID == nil && e.VendorIdentifier == "" {
		return nil
	}
	cur := e.Currency
	if cur == "" {
		cur = "KES"
	}
	owed := zeroIfEmpty(e.BalanceOwed)
	outstanding := zeroIfEmpty(e.OutstandingPayable)

	q := db.VendorBalanceCache.Query().Where(vendorbalancecache.TenantID(tenantID))
	if e.VendorID != nil {
		q = q.Where(vendorbalancecache.VendorID(*e.VendorID))
	} else {
		q = q.Where(vendorbalancecache.VendorIdentifier(e.VendorIdentifier))
	}
	existing, err := q.First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	if existing != nil {
		return existing.Update().
			SetNillableVendorID(e.VendorID).
			SetVendorIdentifier(e.VendorIdentifier).
			SetVendorName(e.VendorName).
			SetBalanceOwed(owed).
			SetOutstandingPayable(outstanding).
			SetCurrency(cur).
			Exec(ctx)
	}
	return db.VendorBalanceCache.Create().
		SetTenantID(tenantID).
		SetNillableVendorID(e.VendorID).
		SetVendorIdentifier(e.VendorIdentifier).
		SetVendorName(e.VendorName).
		SetBalanceOwed(owed).
		SetOutstandingPayable(outstanding).
		SetCurrency(cur).
		Exec(ctx)
}

// Lookup returns the cached balance for each supplier, keyed by supplier ID. Matches on
// vendor_id first; a supplier with no vendor_id-keyed row falls back to an identifier-only row
// with the same name (treasury hadn't resolved the inventory supplier UUID for it yet).
// Suppliers with no cached balance are simply absent from the map.
func (s *Service) Lookup(ctx context.Context, tenantID uuid.UUID, suppliers []*ent.Supplier) map[uuid.UUID]Balance {
	out := make(map[uuid.UUID]Balance, len(suppliers))
	if s == nil || len(suppliers) == 0 {
		return out
	}
	ids := make([]uuid.UUID, len(suppliers))
	for i, sp := range suppliers {
		ids[i] = sp.ID
	}
	rows, err := s.db.VendorBalanceCache.Query().
		Where(vendorbalancecache.TenantID(tenantID), vendorbalancecache.VendorIDIn(ids...)).
		All(ctx)
	if err != nil {
		s.log.Warn("vendor balance lookup failed", zap.Error(err))
		return out
	}
	for _, r := range rows {
		if r.VendorID != nil {
			out[*r.VendorID] = toBalance(r)
		}
	}
	if len(out) == len(suppliers) {
		return out
	}
	byName, err := s.db.VendorBalanceCache.Query().
		Where(vendorbalancecache.TenantID(tenantID), vendorbalancecache.VendorIDIsNil()).
		All(ctx)
	if err != nil || len(byName) == 0 {
		return out
	}
	for _, sp := range suppliers {
		if _, ok := out[sp.ID]; ok {
			continue
		}
		for _, r := range byName {
			if strings.EqualFold(strings.TrimSpace(r.VendorName), strings.TrimSpace(sp.Name)) {
				out[sp.ID] = toBalance(r)
				break
			}
		}
	}
	return out
}

// MaybeResync refreshes the tenant's whole cache from treasury in the background, at most once
// per resyncInterval per pod. Never blocks the caller.
func (s *Service) MaybeResync(tenantID uuid.UUID) {
	if s == nil || s.treasury == nil || !s.treasury.Enabled() {
		return
	}
	s.mu.Lock()
	if last, ok := s.lastSync[tenantID]; ok && time.Since(last) < resyncInterval {
		s.mu.Unlock()
		return
	}
	s.lastSync[tenantID] = time.Now()
	s.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := s.Resync(ctx, tenantID); err != nil {
			s.log.Warn("vendor balance resync failed", zap.String("tenant_id", tenantID.String()), zap.Error(err))
			// Let the next request retry soon instead of waiting the full interval.
			s.mu.Lock()
			s.lastSync[tenantID] = time.Now().Add(-resyncInterval + 2*time.Minute)
			s.mu.Unlock()
		}
	}()
}

// Resync pulls every AP vendor balance for the tenant from treasury and upserts the cache.
func (s *Service) Resync(ctx context.Context, tenantID uuid.UUID) error {
	list, err := s.treasury.ListVendorBalances(ctx, tenantID)
	if err != nil {
		return err
	}
	for _, v := range list {
		var vid *uuid.UUID
		if id, perr := uuid.Parse(string(v.VendorID)); perr == nil {
			vid = &id
		}
		err := Upsert(ctx, s.db, tenantID, Entry{
			VendorID:           vid,
			VendorIdentifier:   v.VendorIdentifier,
			VendorName:         v.VendorName,
			BalanceOwed:        string(v.BalanceOwed),
			OutstandingPayable: string(v.OutstandingPayable),
			Currency:           v.Currency,
		})
		if err != nil && !ent.IsConstraintError(err) {
			return err
		}
	}
	s.log.Debug("vendor balance cache resynced", zap.String("tenant_id", tenantID.String()), zap.Int("vendors", len(list)))
	return nil
}

func toBalance(r *ent.VendorBalanceCache) Balance {
	return Balance{BalanceOwed: r.BalanceOwed, OutstandingPayable: r.OutstandingPayable, Currency: r.Currency}
}

func zeroIfEmpty(v string) string {
	if strings.TrimSpace(v) == "" {
		return "0"
	}
	return v
}
