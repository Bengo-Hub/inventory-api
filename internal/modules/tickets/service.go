// Package tickets implements event-ticket selling against event Items (type=SERVICE) with hard
// capacity/oversell enforcement, redemption (check-in) and cancellation.
package tickets

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	events "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	entticket "github.com/bengobox/inventory-service/internal/ent/ticket"
)

// Service provides ticket issuance, redemption, cancellation and availability.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService constructs a tickets Service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{client: client, log: log.Named("tickets.service")}
}

// IssueInput is the payload to issue one ticket (covering `Quantity` seats).
type IssueInput struct {
	EventItemID uuid.UUID      `json:"event_item_id"`
	OrderID     *uuid.UUID     `json:"order_id,omitempty"`
	TierID      string         `json:"tier_id,omitempty"`
	TierName    string         `json:"tier_name,omitempty"`
	BuyerID     *uuid.UUID     `json:"buyer_id,omitempty"`
	BuyerName   string         `json:"buyer_name,omitempty"`
	BuyerEmail  string         `json:"buyer_email,omitempty"`
	Quantity    int            `json:"quantity,omitempty"`
	UnitPrice   float64        `json:"unit_price,omitempty"`
	Currency    string         `json:"currency,omitempty"`
	ValidFrom   *time.Time     `json:"valid_from,omitempty"`
	ValidUntil  *time.Time     `json:"valid_until,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// RedeemInput is the check-in payload.
type RedeemInput struct {
	RedeemedBy      *uuid.UUID `json:"redeemed_by,omitempty"`
	CheckInLocation string     `json:"check_in_location,omitempty"`
}

// TierAvailability is the per-tier remaining breakdown.
type TierAvailability struct {
	TierID    string  `json:"tier_id"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Capacity  int     `json:"capacity"`
	Issued    int     `json:"issued"`
	Remaining int     `json:"remaining"`
}

// Availability is the event-level capacity snapshot.
type Availability struct {
	EventItemID    uuid.UUID          `json:"event_item_id"`
	TotalCapacity  int                `json:"total_capacity"`
	BookedCapacity int                `json:"booked_capacity"`
	Remaining      int                `json:"remaining"`
	Tiers          []TierAvailability `json:"tiers"`
}

// generateTicketCode returns a short, collision-resistant uppercase code (e.g. "TKT-4A7B2C9D").
func generateTicketCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	enc := strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
	if len(enc) > 8 {
		enc = enc[:8]
	}
	return "TKT-" + enc, nil
}

// IssueTicket validates capacity and atomically issues a ticket, incrementing booked_capacity.
func (s *Service) IssueTicket(ctx context.Context, tenantID uuid.UUID, in IssueInput) (*ent.Ticket, error) {
	qty := in.Quantity
	if qty <= 0 {
		qty = 1
	}
	currency := in.Currency
	if currency == "" {
		currency = "KES"
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("tickets: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	event, err := tx.Item.Query().
		Where(entitem.ID(in.EventItemID), entitem.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("tickets: event not found: %w", err)
	}
	if event.Type != entitem.TypeSERVICE {
		err = fmt.Errorf("tickets: item %s is not a sellable event", in.EventItemID)
		return nil, err
	}
	if event.TotalCapacity == nil || *event.TotalCapacity <= 0 {
		err = fmt.Errorf("tickets: event has no capacity configured")
		return nil, err
	}
	booked := 0
	if event.BookedCapacity != nil {
		booked = *event.BookedCapacity
	}
	if booked+qty > *event.TotalCapacity {
		err = fmt.Errorf("tickets: oversell — requested %d, remaining %d", qty, *event.TotalCapacity-booked)
		return nil, err
	}

	code, cerr := generateTicketCode()
	if cerr != nil {
		err = fmt.Errorf("tickets: generate code: %w", cerr)
		return nil, err
	}

	create := tx.Ticket.Create().
		SetTenantID(tenantID).
		SetEventItemID(in.EventItemID).
		SetCode(code).
		SetQuantity(qty).
		SetUnitPrice(in.UnitPrice).
		SetTotalPrice(in.UnitPrice*float64(qty)).
		SetCurrency(currency).
		SetStatus(entticket.StatusIssued)
	if in.OrderID != nil {
		create = create.SetOrderID(*in.OrderID)
	}
	if in.TierID != "" {
		create = create.SetTierID(in.TierID)
	}
	if in.TierName != "" {
		create = create.SetTierName(in.TierName)
	}
	if in.BuyerID != nil {
		create = create.SetBuyerID(*in.BuyerID)
	}
	if in.BuyerName != "" {
		create = create.SetBuyerName(in.BuyerName)
	}
	if in.BuyerEmail != "" {
		create = create.SetBuyerEmail(in.BuyerEmail)
	}
	if in.ValidFrom != nil {
		create = create.SetValidFrom(*in.ValidFrom)
	}
	if in.ValidUntil != nil {
		create = create.SetValidUntil(*in.ValidUntil)
	}
	if in.Metadata != nil {
		create = create.SetMetadata(in.Metadata)
	}

	ticket, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tickets: create ticket: %w", err)
	}

	if _, err = tx.Item.UpdateOneID(in.EventItemID).SetBookedCapacity(booked + qty).Save(ctx); err != nil {
		return nil, fmt.Errorf("tickets: increment booked_capacity: %w", err)
	}

	if err = s.emit(ctx, tx, tenantID, ticket, "ticket.issued"); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("tickets: commit: %w", err)
	}
	return ticket, nil
}

// GetByCode fetches a ticket by its code within a tenant.
func (s *Service) GetByCode(ctx context.Context, tenantID uuid.UUID, code string) (*ent.Ticket, error) {
	return s.client.Ticket.Query().
		Where(entticket.TenantID(tenantID), entticket.Code(code)).
		Only(ctx)
}

// GetEventItem fetches the event Item backing a ticket (for the ticket PDF: name/venue/date).
func (s *Service) GetEventItem(ctx context.Context, tenantID, eventItemID uuid.UUID) (*ent.Item, error) {
	return s.client.Item.Query().
		Where(entitem.ID(eventItemID), entitem.TenantID(tenantID)).
		Only(ctx)
}

// List returns tickets for a tenant, optionally filtered by event/status/buyer.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, eventItemID *uuid.UUID, status string, buyerID *uuid.UUID, limit, offset int) ([]*ent.Ticket, int, error) {
	q := s.client.Ticket.Query().Where(entticket.TenantID(tenantID))
	if eventItemID != nil {
		q = q.Where(entticket.EventItemID(*eventItemID))
	}
	if status != "" {
		q = q.Where(entticket.StatusEQ(entticket.Status(status)))
	}
	if buyerID != nil {
		q = q.Where(entticket.BuyerID(*buyerID))
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("tickets: count: %w", err)
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.Order(ent.Desc(entticket.FieldCreatedAt)).Limit(limit).Offset(offset).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("tickets: list: %w", err)
	}
	return rows, total, nil
}

// RedeemTicket marks an issued ticket as redeemed (check-in). Idempotent guards apply.
func (s *Service) RedeemTicket(ctx context.Context, tenantID uuid.UUID, code string, in RedeemInput) (*ent.Ticket, error) {
	t, err := s.GetByCode(ctx, tenantID, code)
	if err != nil {
		return nil, fmt.Errorf("tickets: not found: %w", err)
	}
	if t.Status != entticket.StatusIssued {
		return nil, fmt.Errorf("tickets: cannot redeem a %s ticket", t.Status)
	}
	now := time.Now()
	if t.ValidFrom != nil && now.Before(*t.ValidFrom) {
		return nil, fmt.Errorf("tickets: not yet valid")
	}
	if t.ValidUntil != nil && now.After(*t.ValidUntil) {
		return nil, fmt.Errorf("tickets: expired")
	}

	upd := t.Update().SetStatus(entticket.StatusRedeemed).SetRedeemedAt(now)
	if in.RedeemedBy != nil {
		upd = upd.SetRedeemedBy(*in.RedeemedBy)
	}
	if in.CheckInLocation != "" {
		upd = upd.SetCheckInLocation(in.CheckInLocation)
	}
	redeemed, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tickets: redeem: %w", err)
	}
	if err := s.emitNoTx(ctx, tenantID, redeemed, "ticket.redeemed"); err != nil {
		s.log.Warn("tickets: emit redeemed event failed", zap.Error(err))
	}
	return redeemed, nil
}

// CancelTicket cancels an issued ticket and releases its capacity.
func (s *Service) CancelTicket(ctx context.Context, tenantID, ticketID uuid.UUID) (*ent.Ticket, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("tickets: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	t, err := tx.Ticket.Query().Where(entticket.ID(ticketID), entticket.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("tickets: not found: %w", err)
	}
	if t.Status == entticket.StatusRedeemed {
		err = fmt.Errorf("tickets: cannot cancel a redeemed ticket")
		return nil, err
	}
	if t.Status == entticket.StatusCancelled || t.Status == entticket.StatusRefunded || t.Status == entticket.StatusVoid {
		// Already released — return as-is.
		err = tx.Rollback()
		return t, nil
	}

	cancelled, err := t.Update().SetStatus(entticket.StatusCancelled).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tickets: cancel: %w", err)
	}

	// Release capacity (never below zero).
	event, eerr := tx.Item.Query().Where(entitem.ID(t.EventItemID), entitem.TenantID(tenantID)).Only(ctx)
	if eerr == nil && event.BookedCapacity != nil {
		newBooked := *event.BookedCapacity - t.Quantity
		if newBooked < 0 {
			newBooked = 0
		}
		if _, err = tx.Item.UpdateOneID(t.EventItemID).SetBookedCapacity(newBooked).Save(ctx); err != nil {
			return nil, fmt.Errorf("tickets: release capacity: %w", err)
		}
	}

	if err = s.emit(ctx, tx, tenantID, cancelled, "ticket.cancelled"); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("tickets: commit: %w", err)
	}
	return cancelled, nil
}

// EventAvailability returns total/booked/remaining plus per-tier availability.
func (s *Service) EventAvailability(ctx context.Context, tenantID, eventItemID uuid.UUID) (*Availability, error) {
	event, err := s.client.Item.Query().Where(entitem.ID(eventItemID), entitem.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("tickets: event not found: %w", err)
	}
	total := 0
	if event.TotalCapacity != nil {
		total = *event.TotalCapacity
	}
	booked := 0
	if event.BookedCapacity != nil {
		booked = *event.BookedCapacity
	}
	av := &Availability{
		EventItemID:    eventItemID,
		TotalCapacity:  total,
		BookedCapacity: booked,
		Remaining:      max0(total - booked),
		Tiers:          []TierAvailability{},
	}

	// Per-tier issued counts.
	issuedByTier := map[string]int{}
	rows, _ := s.client.Ticket.Query().
		Where(entticket.TenantID(tenantID), entticket.EventItemID(eventItemID), entticket.StatusEQ(entticket.StatusIssued)).
		All(ctx)
	for _, t := range rows {
		issuedByTier[t.TierID] += t.Quantity
	}

	// Tier definitions from Item.metadata.ticket_tiers.
	if tiers, ok := event.Metadata["ticket_tiers"].([]any); ok {
		for idx, raw := range tiers {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["id"].(string)
			if id == "" {
				id, _ = m["tier"].(string)
			}
			name, _ := m["name"].(string)
			if name == "" {
				name, _ = m["label"].(string)
			}
			// UI-defined tiers carry only {name, price, capacity}; derive a stable id from the name
			// (fallback to index) so each tier is uniquely addressable for selection, cart lines,
			// and per-tier availability/issuance — otherwise every tier collapses to a single id.
			if id == "" {
				id = slugifyTierID(name)
			}
			if id == "" {
				id = fmt.Sprintf("tier-%d", idx)
			}
			capF, _ := m["capacity"].(float64)
			priceF, _ := m["price"].(float64)
			cap := int(capF)
			issued := issuedByTier[id]
			av.Tiers = append(av.Tiers, TierAvailability{
				TierID:    id,
				Name:      name,
				Price:     priceF,
				Capacity:  cap,
				Issued:    issued,
				Remaining: max0(cap - issued),
			})
		}
	}
	return av, nil
}

// emit writes a domain event to the outbox within an existing tx (poller publishes to NATS).
func (s *Service) emit(ctx context.Context, tx *ent.Tx, tenantID uuid.UUID, t *ent.Ticket, eventType string) error {
	ev := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "inventory",
		AggregateID:   t.ID,
		EventType:     eventType,
		Payload:       ticketPayload(t),
		Timestamp:     time.Now().UTC(),
	}
	payload, err := ev.ToJSON()
	if err != nil {
		return fmt.Errorf("tickets: marshal event: %w", err)
	}
	if _, err := tx.OutboxEvent.Create().
		SetID(ev.ID).
		SetTenantID(tenantID).
		SetAggregateType(ev.AggregateType).
		SetAggregateID(ev.AggregateID.String()).
		SetEventType(ev.EventType).
		SetPayload(json.RawMessage(payload)).
		SetStatus("PENDING").
		SetCreatedAt(ev.Timestamp).
		Save(ctx); err != nil {
		return fmt.Errorf("tickets: create outbox record: %w", err)
	}
	return nil
}

// emitNoTx writes a domain event to the outbox outside a tx (best-effort).
func (s *Service) emitNoTx(ctx context.Context, tenantID uuid.UUID, t *ent.Ticket, eventType string) error {
	ev := &events.Event{
		ID:            uuid.New(),
		TenantID:      tenantID,
		AggregateType: "inventory",
		AggregateID:   t.ID,
		EventType:     eventType,
		Payload:       ticketPayload(t),
		Timestamp:     time.Now().UTC(),
	}
	payload, err := ev.ToJSON()
	if err != nil {
		return err
	}
	_, err = s.client.OutboxEvent.Create().
		SetID(ev.ID).
		SetTenantID(tenantID).
		SetAggregateType(ev.AggregateType).
		SetAggregateID(ev.AggregateID.String()).
		SetEventType(ev.EventType).
		SetPayload(json.RawMessage(payload)).
		SetStatus("PENDING").
		SetCreatedAt(ev.Timestamp).
		Save(ctx)
	return err
}

func ticketPayload(t *ent.Ticket) map[string]any {
	return map[string]any{
		"id":            t.ID,
		"code":          t.Code,
		"event_item_id": t.EventItemID,
		"tier_id":       t.TierID,
		"tier_name":     t.TierName,
		"quantity":      t.Quantity,
		"status":        t.Status,
		"buyer_name":    t.BuyerName,
		"buyer_email":   t.BuyerEmail,
		"total_price":   t.TotalPrice,
		"currency":      t.Currency,
	}
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// slugifyTierID derives a stable, URL-safe tier id from a tier name
// (e.g. "VIP Lounge" → "vip-lounge"). Returns "" for an empty/symbol-only name.
func slugifyTierID(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
