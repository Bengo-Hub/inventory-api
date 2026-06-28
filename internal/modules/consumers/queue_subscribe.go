package consumers

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// queueRebindSettle is the buffer before the first subscribe attempt, mirroring
// shared-events.RebindSettle so behaviour is identical to the (non-queue) helper.
// Tune via NATS_REBIND_SETTLE_SECONDS (set 0 to disable).
func queueRebindSettle() time.Duration {
	if v := os.Getenv("NATS_REBIND_SETTLE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 25 * time.Second
}

func isAlreadyBound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already bound")
}

// missingDeliverGroup reports the migration case where a durable JetStream consumer
// was previously created WITHOUT a deliver group (the old eventslib.SubscribeWithRebind
// path). nats.go refuses a queue subscription against such a consumer with this exact
// message; the fix is to delete the stale consumer so it is recreated WITH the deliver
// group on the next attempt.
func missingDeliverGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "without a deliver group")
}

// SubscribeQueueWithRebind is the multi-replica-safe replacement for
// eventslib.SubscribeWithRebind for durable PUSH consumers.
//
// Root cause it fixes: eventslib.SubscribeWithRebind calls js.Subscribe with only
// nats.Durable(...) and no queue group. A push durable consumer in JetStream can bind
// exactly ONE subscription at a time, so with >1 replica the first pod binds and every
// other pod gets "consumer is already bound to a subscription" and retries forever —
// meaning all but one replica process zero events for that subject. (Observed on every
// inventory-api durable consumer with 2 replicas.)
//
// Fix: subscribe as a durable QUEUE group (js.QueueSubscribe with the durable name as the
// deliver/queue group). NATS then load-balances the subject across every replica in the
// group instead of rejecting the extra binders, so all replicas share the consumer and
// each message is still processed exactly once.
//
// queue is the deliver-group name; pass the same value as the durable name so one logical
// consumer == one queue group. Callers MUST still pass nats.Durable(queue) in opts (it is
// what makes the consumer durable); QueueSubscribe uses the queue purely as the deliver
// group, not as the durable name, when an explicit Durable opt is given.
//
// Self-healing migration: if a stale non-deliver-group consumer already exists on the
// server (created by the old code path), the first queue subscribe fails non-retryably
// with "without a deliver group". We delete that consumer once and retry, so the durable
// is recreated WITH the deliver group automatically on deploy — no manual nats CLI step.
func SubscribeQueueWithRebind(log *zap.Logger, js nats.JetStreamContext, stream, subject, queue string, handler nats.MsgHandler, opts ...nats.SubOpt) {
	go func() {
		time.Sleep(queueRebindSettle())
		backoff := 3 * time.Second
		const maxBackoff = 30 * time.Second
		deletedStale := false
		for attempt := 1; attempt <= 40; attempt++ {
			if _, err := js.QueueSubscribe(subject, queue, handler, opts...); err == nil {
				if log != nil {
					log.Info("jetstream queue subscription active",
						zap.String("subject", subject), zap.String("queue", queue))
				}
				return
			} else if missingDeliverGroup(err) && !deletedStale {
				// One-time migration: drop the legacy non-queue durable so it is recreated
				// with a deliver group below. Both replicas may race to delete; a delete of
				// an already-deleted consumer is harmless.
				deletedStale = true
				if log != nil {
					log.Warn("jetstream durable lacks deliver group; deleting stale consumer to migrate to queue group",
						zap.String("subject", subject), zap.String("queue", queue))
				}
				if derr := js.DeleteConsumer(stream, queue); derr != nil && log != nil {
					log.Warn("jetstream delete stale consumer failed (will retry subscribe anyway)",
						zap.String("consumer", queue), zap.Error(derr))
				}
				continue // retry immediately
			} else if !isAlreadyBound(err) && !missingDeliverGroup(err) {
				if log != nil {
					log.Error("jetstream queue subscribe failed (non-retryable)",
						zap.String("subject", subject), zap.String("queue", queue), zap.Error(err))
				}
				return
			}
			if log != nil {
				log.Warn("jetstream queue subscribe bind conflict; retrying",
					zap.String("subject", subject), zap.String("queue", queue),
					zap.Int("attempt", attempt), zap.Duration("backoff", backoff))
			}
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
		}
		if log != nil {
			log.Error("jetstream queue subscribe gave up after retries",
				zap.String("subject", subject), zap.String("queue", queue))
		}
	}()
}
