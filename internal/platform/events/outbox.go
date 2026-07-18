package events

import (
	"context"
	"encoding/json"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
)

// WriteOutboxTx writes one domain event to the outbox inside the caller's Ent transaction,
// storing the FULL shared-events envelope (Event.ToJSON()) in the payload column — the
// shared poller rebuilds the event solely from that column, so a bare business payload
// would publish to subject "." and be lost. eventType must NOT repeat the aggregate prefix
// (subject = {aggregateType}.{eventType}). Best-effort: failures are logged, never fatal
// to the domain write. Single home for the previously-triplicated module copies
// (stock/transfers/recipes writeOutboxEvent).
func WriteOutboxTx(ctx context.Context, tx *ent.Tx, log *zap.Logger, tenantID, aggregateID uuid.UUID, aggregateType, eventType string, payload map[string]any) {
	evt := eventslib.NewEvent(eventType, aggregateType, aggregateID, tenantID, payload)
	data, err := evt.ToJSON()
	if err != nil {
		log.Warn("outbox: marshal event", zap.Error(err), zap.String("event_type", eventType))
		return
	}
	_, err = tx.OutboxEvent.Create().
		SetID(evt.ID).
		SetTenantID(tenantID).
		SetAggregateType(aggregateType).
		SetAggregateID(aggregateID.String()).
		SetEventType(eventType).
		SetPayload(json.RawMessage(data)).
		Save(ctx)
	if err != nil {
		log.Warn("outbox: write event", zap.Error(err), zap.String("event_type", eventType))
	}
}
