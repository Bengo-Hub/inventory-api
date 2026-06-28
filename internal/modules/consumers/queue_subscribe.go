package consumers

import (
	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// SubscribeQueueWithRebind delegates to the canonical shared-events helper
// (shared-events v0.6.0+), so the deliver-group push-durable logic lives in
// exactly ONE place fleet-wide instead of being duplicated per service. Kept as a
// thin package-local alias so the many consumer call sites in this package don't
// need to change.
//
// See sharedevents.SubscribeQueueWithRebind for the full contract: a durable PUSH
// consumer WITH a deliver group, so all replicas share the consumer (load-balanced,
// no "already bound" conflict) while keeping JetStream persistence; plus the
// one-time self-heal that migrates a legacy non-deliver-group durable.
func SubscribeQueueWithRebind(log *zap.Logger, js nats.JetStreamContext, stream, subject, queue string, handler nats.MsgHandler, opts ...nats.SubOpt) {
	sharedevents.SubscribeQueueWithRebind(log, js, stream, subject, queue, handler, opts...)
}
