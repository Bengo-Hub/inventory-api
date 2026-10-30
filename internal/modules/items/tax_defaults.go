package items

import (
	"context"
	"errors"
	"fmt"
	"strings"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/audit"
	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/ent/predicate"
	"github.com/bengobox/inventory-service/internal/ent/tenantinventoryconfig"
	"github.com/bengobox/inventory-service/internal/platform/treasury"
)

// Catalog tax codes.
//
// Every item carries a treasury tax code. A business that is not charging VAT sells under KRA
// band D (Non-VAT); once treasury reports it VAT-active (registered and charging), its items
// default to band B (VAT 16%). The spelling of a band's code differs per tenant ("B" for one,
// "VAT-16" for the platform-global set), so codes are always resolved through treasury's
// kra_code, never by literal name.
//
// metadata.tax_code_source records who chose an item's code: "manual" when a person set it (the
// item form, the bulk tax-code action, an import column), "auto" or absent when the system did.
// The VAT on/off job and the daily reconcile only move automatic codes. A deliberate change of the
// tenant default in Settings is the one action that overrides every item, hand-set ones included.
const (
	MetaTaxCodeSource   = "tax_code_source"
	TaxCodeSourceManual = "manual"
	TaxCodeSourceAuto   = "auto"

	KRABandVAT    = "B" // VAT 16%
	KRABandNonVAT = "D" // Non-VAT
)

// Tax-code job modes (see ApplyTenantTaxCodes).
const (
	// TaxJobReconcile fills items with no code using the tenant's automatic code and, while the
	// tenant is VAT-active, moves automatic band D codes to band B. Safe to run any time.
	TaxJobReconcile = "reconcile"
	// TaxJobVATOff moves automatic band B codes back to band D after VAT is switched off.
	TaxJobVATOff = "vat_off"
	// TaxJobOverride sets every item to the tenant's automatic code and clears hand-set markers.
	TaxJobOverride = "override"
)

// ErrTaxCodesUnavailable is returned when treasury cannot say which codes or VAT status apply;
// the job then changes nothing rather than guess.
var ErrTaxCodesUnavailable = errors.New("tax codes unavailable")

// taxCatalog is what the tax-code job needs from treasury. treasury.Client implements it; the
// resolver injected through SetTaxResolver is checked for it at run time.
type taxCatalog interface {
	GetTaxCodes(ctx context.Context, tenantID uuid.UUID) ([]treasury.TaxCode, error)
	VATStatus(ctx context.Context, tenantID uuid.UUID) (bool, error)
}

// taxCodeJobBatch is how many items one transaction updates (plus their outbox events).
const taxCodeJobBatch = 250

// tenantTaxCodes is a tenant's treasury code list indexed by KRA band.
type tenantTaxCodes struct {
	byBand map[string][]string // band -> codes in treasury spelling, best match first
	band   map[string]string   // upper-cased code -> band
}

func indexTaxCodes(tenantID uuid.UUID, codes []treasury.TaxCode) tenantTaxCodes {
	idx := tenantTaxCodes{byBand: map[string][]string{}, band: map[string]string{}}
	// A tenant's own code beats the platform-global one for the same band; is_default breaks ties.
	rank := func(c treasury.TaxCode) int {
		r := 0
		if c.TenantID == tenantID.String() {
			r += 2
		}
		if c.IsDefault {
			r++
		}
		return r
	}
	best := map[string]int{}
	for _, c := range codes {
		band := strings.ToUpper(strings.TrimSpace(c.KraCode))
		code := strings.TrimSpace(c.Code)
		if code == "" || band == "" {
			continue
		}
		idx.band[strings.ToUpper(code)] = band
		list := idx.byBand[band]
		if r := rank(c); len(list) == 0 || r > best[band] {
			idx.byBand[band] = append([]string{code}, list...)
			best[band] = r
		} else {
			idx.byBand[band] = append(list, code)
		}
	}
	return idx
}

// codeFor returns the preferred code for a band ("" when the tenant has none).
func (t tenantTaxCodes) codeFor(band string) string {
	if list := t.byBand[band]; len(list) > 0 {
		return list[0]
	}
	return ""
}

// automaticTaxCode is the code a new or unassigned item gets: the tenant's Settings default when
// one is chosen, else band B while VAT-active and band D otherwise.
func automaticTaxCode(cfg *ent.TenantInventoryConfig, idx tenantTaxCodes, vatActive bool) string {
	if cfg != nil && strings.TrimSpace(cfg.DefaultTaxCode) != "" {
		return strings.TrimSpace(cfg.DefaultTaxCode)
	}
	if vatActive {
		return idx.codeFor(KRABandVAT)
	}
	return idx.codeFor(KRABandNonVAT)
}

func (s *Service) taxCatalog() (taxCatalog, bool) {
	tc, ok := s.taxResolver.(taxCatalog)
	return tc, ok && tc != nil
}

// tenantTaxState loads the tenant's codes and VAT status from treasury, or ErrTaxCodesUnavailable.
func (s *Service) tenantTaxState(ctx context.Context, tenantID uuid.UUID) (tenantTaxCodes, bool, error) {
	tc, ok := s.taxCatalog()
	if !ok {
		return tenantTaxCodes{}, false, ErrTaxCodesUnavailable
	}
	codes, err := tc.GetTaxCodes(ctx, tenantID)
	if err != nil || len(codes) == 0 {
		return tenantTaxCodes{}, false, fmt.Errorf("%w: %v", ErrTaxCodesUnavailable, err)
	}
	vat, err := tc.VATStatus(ctx, tenantID)
	if err != nil {
		return tenantTaxCodes{}, false, fmt.Errorf("%w: %v", ErrTaxCodesUnavailable, err)
	}
	return indexTaxCodes(tenantID, codes), vat, nil
}

func (s *Service) tenantInventoryConfig(ctx context.Context, tenantID uuid.UUID) *ent.TenantInventoryConfig {
	cfg, err := s.client.TenantInventoryConfig.Query().Where(tenantinventoryconfig.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil
	}
	return cfg
}

// AutomaticTaxCode returns the code a new item gets when none is chosen (see automaticTaxCode).
// Falls back to the Settings default alone when treasury is unreachable; "" leaves the item for
// the daily reconcile to fill.
func (s *Service) AutomaticTaxCode(ctx context.Context, tenantID uuid.UUID) string {
	cfg := s.tenantInventoryConfig(ctx, tenantID)
	idx, vat, err := s.tenantTaxState(ctx, tenantID)
	if err != nil {
		if cfg != nil {
			return strings.TrimSpace(cfg.DefaultTaxCode)
		}
		return ""
	}
	return automaticTaxCode(cfg, idx, vat)
}

// BandTaxCode is the code the band rule gives the tenant now, ignoring any Settings default:
// band B while VAT-active, band D otherwise. Settings shows it as the "Automatic" choice.
func (s *Service) BandTaxCode(ctx context.Context, tenantID uuid.UUID) (code string, vatActive bool, err error) {
	idx, vat, err := s.tenantTaxState(ctx, tenantID)
	if err != nil {
		return "", false, err
	}
	return automaticTaxCode(nil, idx, vat), vat, nil
}

// TaxCodeSource reads metadata.tax_code_source ("" when unset).
func TaxCodeSource(meta map[string]any) string {
	v, _ := meta[MetaTaxCodeSource].(string)
	return v
}

// withTaxCodeSource returns a copy of meta with tax_code_source set (or removed for "").
func withTaxCodeSource(meta map[string]any, source string) map[string]any {
	out := copyMetadata(meta)
	if source == "" {
		delete(out, MetaTaxCodeSource)
	} else {
		out[MetaTaxCodeSource] = source
	}
	return out
}

// stampTaxCodeSource decides an incoming create/update's tax_code_source. prev is the stored
// item (nil on create). A code the caller sends that differs from the stored one was chosen by a
// person, so it is marked manual. Otherwise the stored marker is kept, because UpdateItem
// replaces metadata wholesale and a client resending stale metadata must not drop or forge it.
// On create, a marker the caller set explicitly (a variant inheriting its parent's) is kept.
func stampTaxCodeSource(prev *ent.Item, dto *ItemDTO) {
	code := strings.TrimSpace(dto.TaxCodeID)
	var source string
	switch {
	case prev == nil:
		source = TaxCodeSource(dto.Metadata)
		if source == "" && code != "" {
			source = TaxCodeSourceManual
		}
	case code != "" && !strings.EqualFold(code, prev.TaxCodeID):
		source = TaxCodeSourceManual
	default:
		source = TaxCodeSource(prev.Metadata)
	}
	if source == "" && dto.Metadata == nil {
		return
	}
	dto.Metadata = withTaxCodeSource(dto.Metadata, source)
}

// notManualTaxCode matches items whose code was not set by hand (marker absent or "auto").
func notManualTaxCode() predicate.Item {
	return predicate.Item(func(sel *entsql.Selector) {
		sel.Where(entsql.ExprP(fmt.Sprintf("COALESCE(%s->>'%s', '') <> '%s'",
			sel.C(item.FieldMetadata), MetaTaxCodeSource, TaxCodeSourceManual)))
	})
}

func noTaxCode() predicate.Item {
	return item.Or(item.TaxCodeIDIsNil(), item.TaxCodeIDEQ(""))
}

// codesInBand lists every spelling of a band's codes for an IN filter.
func (t tenantTaxCodes) codesInBand(band string) []string {
	var out []string
	for code, b := range t.band {
		if b == band {
			out = append(out, code, strings.ToLower(code))
		}
	}
	for _, c := range t.byBand[band] {
		out = append(out, c)
	}
	return out
}

// TaxCodeJobPlan is what ApplyTenantTaxCodes would do: the code it writes and how many items it
// touches (two targets in reconcile mode: unassigned items and band D items moving to B).
type TaxCodeJobPlan struct {
	Mode      string `json:"mode"`
	VATActive bool   `json:"vat_active"`
	FillCode  string `json:"fill_code,omitempty"` // code for items with none (reconcile/override)
	MoveCode  string `json:"move_code,omitempty"` // code automatic items move to (reconcile/vat_off)
	Total     int    `json:"total"`

	fill predicate.Item
	move predicate.Item
}

// PlanTenantTaxCodes works out a job without changing anything (used for the job's total).
func (s *Service) PlanTenantTaxCodes(ctx context.Context, tenantID uuid.UUID, mode string) (*TaxCodeJobPlan, error) {
	idx, vat, err := s.tenantTaxState(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	cfg := s.tenantInventoryConfig(ctx, tenantID)
	plan := &TaxCodeJobPlan{Mode: mode, VATActive: vat}
	auto := automaticTaxCode(cfg, idx, vat)

	switch mode {
	case TaxJobOverride:
		if auto == "" {
			return nil, fmt.Errorf("%w: no tax code to apply", ErrTaxCodesUnavailable)
		}
		plan.FillCode = auto
		// Every item not already on the code, plus hand-set items whose marker must be cleared.
		plan.fill = item.Or(item.TaxCodeIDIsNil(), item.TaxCodeIDNEQ(auto), item.Not(notManualTaxCode()))
	case TaxJobReconcile:
		if auto != "" {
			plan.FillCode = auto
			plan.fill = noTaxCode()
		}
		if vat {
			if b := idx.codeFor(KRABandVAT); b != "" {
				if from := idx.codesInBand(KRABandNonVAT); len(from) > 0 {
					plan.MoveCode = b
					plan.move = item.And(notManualTaxCode(), item.TaxCodeIDIn(from...))
				}
			}
		}
	case TaxJobVATOff:
		if d := idx.codeFor(KRABandNonVAT); d != "" {
			if from := idx.codesInBand(KRABandVAT); len(from) > 0 {
				plan.MoveCode = d
				plan.move = item.And(notManualTaxCode(), item.TaxCodeIDIn(from...))
			}
		}
	default:
		return nil, fmt.Errorf("items: unknown tax-code job mode %q", mode)
	}

	for _, p := range []predicate.Item{plan.fill, plan.move} {
		if p == nil {
			continue
		}
		n, cerr := s.client.Item.Query().Where(item.TenantID(tenantID), p).Count(ctx)
		if cerr != nil {
			return nil, fmt.Errorf("items: count tax-code job: %w", cerr)
		}
		plan.Total += n
	}
	return plan, nil
}

// ApplyTenantTaxCodes runs a tax-code job for one tenant (see the TaxJob* modes) and returns how
// many items it changed. Items are walked in id order, taxCodeJobBatch per transaction; each
// changed item gets its item.updated event in the same transaction so POS, ordering and
// treasury's eTIMS item mirror follow. Nothing here talks to KRA.
func (s *Service) ApplyTenantTaxCodes(ctx context.Context, tenantID uuid.UUID, plan *TaxCodeJobPlan) (int, error) {
	if plan == nil {
		return 0, nil
	}
	updated := 0
	for _, step := range []struct {
		code  string
		where predicate.Item
	}{{plan.FillCode, plan.fill}, {plan.MoveCode, plan.move}} {
		if step.where == nil || step.code == "" {
			continue
		}
		n, err := s.applyTaxCodeStep(ctx, tenantID, step.code, step.where, plan.Mode == TaxJobOverride)
		updated += n
		if err != nil {
			return updated, err
		}
	}
	if s.auditSvc != nil && updated > 0 {
		s.auditSvc.Record(ctx, audit.Entry{
			TenantID:    tenantID,
			ActorUserID: actorFromContext(ctx),
			Action:      "items.tax_codes_applied",
			EntityType:  "tenant",
			EntityID:    tenantID.String(),
			After: map[string]any{
				"mode": plan.Mode, "vat_active": plan.VATActive,
				"fill_code": plan.FillCode, "move_code": plan.MoveCode, "items": updated,
			},
		})
	}
	return updated, nil
}

func (s *Service) applyTaxCodeStep(ctx context.Context, tenantID uuid.UUID, code string, where predicate.Item, clearManual bool) (int, error) {
	updated := 0
	lookups := newItemEventLookups()
	last := uuid.Nil
	for {
		rows, err := s.client.Item.Query().
			Where(item.TenantID(tenantID), where, item.IDGT(last)).
			Order(ent.Asc(item.FieldID)).
			Limit(taxCodeJobBatch).
			All(ctx)
		if err != nil {
			return updated, fmt.Errorf("items: load tax-code batch: %w", err)
		}
		if len(rows) == 0 {
			return updated, nil
		}
		last = rows[len(rows)-1].ID

		tx, err := s.client.Tx(ctx)
		if err != nil {
			return updated, fmt.Errorf("items: begin tax-code batch: %w", err)
		}
		for _, it := range rows {
			upd := tx.Item.UpdateOneID(it.ID).SetTaxCodeID(code)
			if clearManual {
				upd = upd.SetMetadata(withTaxCodeSource(it.Metadata, TaxCodeSourceAuto))
			}
			saved, uerr := upd.Save(ctx)
			if uerr == nil {
				uerr = s.emitItemUpdatedEventWith(ctx, tx, tenantID, saved, lookups)
			}
			if uerr != nil {
				_ = tx.Rollback()
				return updated, fmt.Errorf("items: apply tax code to %s: %w", it.Sku, uerr)
			}
		}
		if err := tx.Commit(); err != nil {
			return updated, fmt.Errorf("items: commit tax-code batch: %w", err)
		}
		updated += len(rows)
		if len(rows) < taxCodeJobBatch {
			return updated, nil
		}
	}
}

// ResetBandDefaultOnVATChange clears a Settings default that names the band VAT just left (band D
// when VAT turns on, band B when it turns off), so new items follow the automatic rule instead of
// staying on the old band. A default in any other band (zero-rated, exempt, 8%) is a deliberate
// choice and is kept. Returns true when it cleared the default.
func (s *Service) ResetBandDefaultOnVATChange(ctx context.Context, tenantID uuid.UUID, vatActive bool) bool {
	cfg := s.tenantInventoryConfig(ctx, tenantID)
	if cfg == nil || strings.TrimSpace(cfg.DefaultTaxCode) == "" {
		return false
	}
	idx, _, err := s.tenantTaxState(ctx, tenantID)
	if err != nil {
		return false
	}
	left := KRABandVAT
	if vatActive {
		left = KRABandNonVAT
	}
	if idx.band[strings.ToUpper(strings.TrimSpace(cfg.DefaultTaxCode))] != left {
		return false
	}
	if _, err := cfg.Update().SetDefaultTaxCode("").Save(ctx); err != nil {
		s.log.Warn("items: reset band default tax code", zap.String("tenant", tenantID.String()), zap.Error(err))
		return false
	}
	return true
}

// TenantsWithItems lists the tenants that have at least one item (the daily reconcile's scope).
func (s *Service) TenantsWithItems(ctx context.Context) ([]uuid.UUID, error) {
	var rows []struct {
		TenantID uuid.UUID `json:"tenant_id"`
	}
	if err := s.client.Item.Query().Unique(true).Select(item.FieldTenantID).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.TenantID)
	}
	return out, nil
}
