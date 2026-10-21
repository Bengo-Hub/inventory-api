package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	entpr "github.com/bengobox/inventory-service/internal/ent/purchasereturn"
	entstockadj "github.com/bengobox/inventory-service/internal/ent/stockadjustment"
	"github.com/bengobox/inventory-service/internal/modules/stock"
)

// Pure helpers for the "service / repair" destination of a purchase return. Nothing here touches
// the database, so every rule is unit-tested without a Postgres.

const (
	maxServiceProviderLen = 200
	maxServiceContactLen  = 120
	maxServiceFaultLen    = 1000
)

// returnError is a validation failure carrying the HTTP status and API error code.
type returnError struct {
	status int
	code   string
	msg    string
}

func (e *returnError) Error() string { return e.msg }

// serviceInput is the validated repair-specific part of a create request.
type serviceInput struct {
	Provider string
	Contact  string
	Fault    string
	Expected *time.Time
}

// metadata is what is stored in purchase_returns.metadata for a service return.
func (s serviceInput) metadata() map[string]any {
	m := map[string]any{"service_provider": s.Provider}
	if s.Contact != "" {
		m["service_contact"] = s.Contact
	}
	if s.Fault != "" {
		m["fault"] = s.Fault
	}
	return m
}

// parseServiceInput validates the provider, contact, fault and expected date of a service return.
// The provider is required: a repair order without a destination cannot be followed up.
func parseServiceInput(req purchaseReturnPayload) (*serviceInput, *returnError) {
	in := &serviceInput{
		Provider: strings.TrimSpace(req.ServiceProvider),
		Contact:  strings.TrimSpace(req.ServiceContact),
		Fault:    strings.TrimSpace(req.Fault),
	}
	if in.Provider == "" {
		return nil, &returnError{http.StatusBadRequest, "SERVICE_PROVIDER_REQUIRED", "Enter who the items are being sent to for repair"}
	}
	if len([]rune(in.Provider)) > maxServiceProviderLen || len([]rune(in.Contact)) > maxServiceContactLen || len([]rune(in.Fault)) > maxServiceFaultLen {
		return nil, &returnError{http.StatusBadRequest, "SERVICE_TEXT_TOO_LONG", "The provider, contact or fault description is too long"}
	}
	if req.ExpectedBackDate != nil && strings.TrimSpace(*req.ExpectedBackDate) != "" {
		t, ok := parseFlexibleDate(strings.TrimSpace(*req.ExpectedBackDate))
		if !ok {
			return nil, &returnError{http.StatusBadRequest, "INVALID_EXPECTED_DATE", "expected_back_date must be YYYY-MM-DD"}
		}
		in.Expected = &t
	}
	return in, nil
}

// serviceProviderOf reads the provider name back out of a return's metadata.
func serviceProviderOf(meta map[string]any) string {
	p, _ := meta["service_provider"].(string)
	return p
}

// serviceLineState is one return line's quantities during a repair.
type serviceLineState struct {
	ID         uuid.UUID
	Quantity   float64
	Received   float64
	WrittenOff float64
}

// outstanding is how many units are still at the provider.
func (s serviceLineState) outstanding() float64 {
	return roundDecimal(s.Quantity - s.Received - s.WrittenOff)
}

// serviceReceiveLine is one line of a receive-back request.
type serviceReceiveLine struct {
	LineID        uuid.UUID `json:"line_id"`
	ReturnedQty   float64   `json:"returned_qty"`
	WrittenOffQty float64   `json:"written_off_qty"`
}

// receiveEntry is a validated receive for one line.
type receiveEntry struct {
	State      serviceLineState
	Returned   float64
	WrittenOff float64
}

// after is the line's state once this entry has been applied.
func (e receiveEntry) after() serviceLineState {
	s := e.State
	s.Received = roundDecimal(s.Received + e.Returned)
	s.WrittenOff = roundDecimal(s.WrittenOff + e.WrittenOff)
	return s
}

// planServiceReceive validates a receive-back request against the lines' outstanding quantities.
// Blank rows (both quantities zero) are ignored; at least one real row is required. A line can
// never be received beyond what was sent, so a double submit cannot restock units twice.
func planServiceReceive(states []serviceLineState, in []serviceReceiveLine) ([]receiveEntry, *returnError) {
	byID := make(map[uuid.UUID]serviceLineState, len(states))
	for _, s := range states {
		byID[s.ID] = s
	}
	seen := make(map[uuid.UUID]struct{}, len(in))
	out := make([]receiveEntry, 0, len(in))
	for _, l := range in {
		st, ok := byID[l.LineID]
		if !ok {
			return nil, &returnError{http.StatusBadRequest, "UNKNOWN_LINE", "A line does not belong to this order"}
		}
		if _, dup := seen[l.LineID]; dup {
			return nil, &returnError{http.StatusBadRequest, "DUPLICATE_LINE", "A line was listed twice"}
		}
		seen[l.LineID] = struct{}{}
		for _, q := range []float64{l.ReturnedQty, l.WrittenOffQty} {
			if q < 0 || math.IsNaN(q) || math.IsInf(q, 0) {
				return nil, &returnError{http.StatusBadRequest, "INVALID_QUANTITY", "Quantities cannot be negative"}
			}
		}
		ret, wo := roundDecimal(l.ReturnedQty), roundDecimal(l.WrittenOffQty)
		if ret == 0 && wo == 0 {
			continue
		}
		if roundDecimal(ret+wo) > st.outstanding() {
			return nil, &returnError{http.StatusBadRequest, "OVER_RECEIVE", fmt.Sprintf("Only %s units are still out for repair on a line", formatQty(st.outstanding()))}
		}
		out = append(out, receiveEntry{State: st, Returned: ret, WrittenOff: wo})
	}
	if len(out) == 0 {
		return nil, &returnError{http.StatusBadRequest, "NOTHING_TO_RECEIVE", "Enter how many units came back or could not be repaired"}
	}
	return out, nil
}

// serviceStatusFor derives the order status from its lines: out (nothing back yet), partial
// (some units settled), returned (all settled, at least one repaired) or written_off (all
// settled and none repaired).
func serviceStatusFor(states []serviceLineState) entpr.ServiceStatus {
	var total, settled, writtenOff float64
	for _, s := range states {
		total += s.Quantity
		settled += s.Received + s.WrittenOff
		writtenOff += s.WrittenOff
	}
	total, settled, writtenOff = roundDecimal(total), roundDecimal(settled), roundDecimal(writtenOff)
	switch {
	case settled <= 0:
		return entpr.ServiceStatusOut
	case settled < total:
		return entpr.ServiceStatusPartial
	case writtenOff >= total:
		return entpr.ServiceStatusWrittenOff
	}
	return entpr.ServiceStatusReturned
}

// repairReasonRejection returns the API error for a manual stock adjustment that tries to use a
// repair reason. service_out and service_in only make sense with a repair order behind them (the
// provider, expected date and cost layer live there), so they are created by the return flow only.
func repairReasonRejection(reason string) *returnError {
	switch reason {
	case string(entstockadj.ReasonServiceOut), string(entstockadj.ReasonServiceIn):
		return &returnError{http.StatusBadRequest, "RESERVED_REASON", "Use Procurement > Returns to send items for repair or receive them back"}
	}
	return nil
}

// serviceStatusLabel is the plain-language label shown on printed repair orders.
func serviceStatusLabel(st *entpr.ServiceStatus) string {
	if st == nil {
		return "Awaiting approval"
	}
	switch *st {
	case entpr.ServiceStatusOut:
		return "Out for repair"
	case entpr.ServiceStatusPartial:
		return "Partly back"
	case entpr.ServiceStatusReturned:
		return "Returned"
	case entpr.ServiceStatusWrittenOff:
		return "Written off"
	}
	return string(*st)
}

// applyReceive returns every line's state with the receive entries applied.
func applyReceive(states []serviceLineState, entries []receiveEntry) []serviceLineState {
	byID := make(map[uuid.UUID]receiveEntry, len(entries))
	for _, e := range entries {
		byID[e.State.ID] = e
	}
	out := make([]serviceLineState, len(states))
	for i, s := range states {
		if e, ok := byID[s.ID]; ok {
			out[i] = e.after()
		} else {
			out[i] = s
		}
	}
	return out
}

// isOverdue reports whether an open service order's expected date has passed. today is the
// caller's calendar day (YYYY-MM-DD stored at 00:00 UTC), so an order due today is not overdue.
func isOverdue(status *entpr.ServiceStatus, expected *time.Time, today time.Time) bool {
	if status == nil || expected == nil {
		return false
	}
	if *status != entpr.ServiceStatusOut && *status != entpr.ServiceStatusPartial {
		return false
	}
	return expected.Before(today)
}

// resolveToday reads the caller's local calendar day from ?today=YYYY-MM-DD (the browser's day,
// since the tenant is not necessarily on UTC), defaulting to the UTC day.
func resolveToday(raw string, now time.Time) time.Time {
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(raw)); err == nil {
		return t
	}
	n := now.UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// stockMove is one stock adjustment of a return, with the unit cost needed to undo it.
type stockMove struct {
	Req      stock.AdjustStockRequest
	UnitCost float64
}

// adjustFunc is stock.Service.AdjustStock, injectable so the compensation logic is testable.
type adjustFunc func(ctx context.Context, tenantID uuid.UUID, req stock.AdjustStockRequest) (*stock.AdjustStockResponse, error)

// inverseMove builds the adjustment that cancels m. Reasons are chosen so the reversal is as
// neutral as the original: a purchase_return and a service pair are never GL-posted nor sent to
// eTIMS, so their reversals are not either; a reversed damaged write-off goes through "found",
// which treasury books as the opposite value movement instead of a second expense.
func inverseMove(m stockMove) stock.AdjustStockRequest {
	r := m.Req
	r.Adjustment = -r.Adjustment
	r.Notes = "Reversal: " + r.Notes
	r.ApprovalIntentID = nil
	r.CostLayerUnitCost = 0
	switch r.Reason {
	case string(entstockadj.ReasonServiceOut):
		r.Reason = string(entstockadj.ReasonServiceIn)
		r.CostLayerUnitCost = m.UnitCost
	case string(entstockadj.ReasonServiceIn):
		r.Reason = string(entstockadj.ReasonServiceOut)
	case string(entstockadj.ReasonPurchaseReturn):
		// Same reason, opposite sign: nets to zero in the ledger and stays out of GL and eTIMS.
	case string(entstockadj.ReasonDamaged):
		r.Reason = string(entstockadj.ReasonFound)
	default:
		r.Reason = string(entstockadj.ReasonCorrection)
	}
	return r
}

// runStockMoves applies the moves in order. If one fails, the ones already applied are reversed
// (most recent first) so a return never ends up with only some of its items taken out of stock.
// It returns the index of the failing move and its error, plus undoErr when a reversal itself
// failed (the caller must then surface the partial state).
func runStockMoves(ctx context.Context, tenantID uuid.UUID, moves []stockMove, adjust adjustFunc) (failed int, err error, undoErr error) {
	for i, m := range moves {
		if _, e := adjust(ctx, tenantID, m.Req); e != nil {
			for j := i - 1; j >= 0; j-- {
				if _, ue := adjust(ctx, tenantID, inverseMove(moves[j])); ue != nil && undoErr == nil {
					undoErr = fmt.Errorf("reverse %s: %w", moves[j].Req.SKU, ue)
				}
			}
			return i, e, undoErr
		}
	}
	return -1, nil, nil
}
