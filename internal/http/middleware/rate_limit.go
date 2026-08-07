package middleware

import (
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	sharedratelimit "github.com/Bengo-Hub/shared-ratelimit"
)

// RateLimitConfig holds defaults for the sliding-window rate limiter.
type RateLimitConfig struct {
	Requests int
	Window   time.Duration
}

// DefaultRateLimitConfig returns sensible production defaults (100 req / 60s per IP).
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{Requests: 100, Window: 60 * time.Second}
}

// IPRateLimit is a Redis sliding-window rate limiter keyed by client IP, built on
// github.com/Bengo-Hub/shared-ratelimit's Limiter (the same primitive treasury-api uses — this
// replaces a hand-rolled fixed-window INCR/EXPIRE counter, which has a real boundary-cliff
// weakness a sliding window doesn't). Returns 429 with standard Retry-After and X-RateLimit-*
// headers.
func IPRateLimit(rc *redis.Client, log *zap.Logger, cfg RateLimitConfig) func(http.Handler) http.Handler {
	if rc == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	if cfg.Requests <= 0 {
		cfg = DefaultRateLimitConfig()
	}
	limiter := sharedratelimit.NewLimiter(rc, log, "inventory")
	return limiter.Middleware(sharedratelimit.IPKey, cfg.Requests, cfg.Window)
}

// DefaultPINRateLimitConfig throttles PIN identify/login attempts much harder than ordinary
// traffic (see PINRateLimit) — 8 requests/minute per IP. Generous enough for a warehouse/desk
// user fumbling their own PIN a few times, far too tight to scan a common/guessed PIN across
// many tenant slugs.
func DefaultPINRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{Requests: 8, Window: 60 * time.Second}
}

// PINRateLimit is a Redis sliding-window rate limiter keyed by client IP, dedicated to PIN
// authentication routes (identify/login), built on shared-ratelimit's Limiter. It uses its own
// Redis key namespace ("rl:inventory-pin:" vs IPRateLimit's "rl:inventory:") so it counts
// independently of — and stacks with — the general per-IP limiter rather than sharing/racing its
// counter.
//
// Why this exists: PIN uniqueness is enforced per-tenant, not globally, so the SAME weak/common
// PIN (e.g. "1111") can independently be valid for two entirely unrelated tenants' admin
// accounts — confirmed live in production (pos-api, which mirrors this same PIN-login design).
// Without a much stricter limit specifically on these routes, the generous general-purpose
// budget (100 req/60s) lets an attacker who knows or guesses one tenant's PIN cheaply try it
// against many other tenant slugs per minute, looking for a collision. This does not fix any
// single request's tenant isolation (each lookup is already correctly scoped to its own
// tenant) — it makes that scanning pattern impractical.
func PINRateLimit(rc *redis.Client, log *zap.Logger, cfg RateLimitConfig) func(http.Handler) http.Handler {
	if rc == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	if cfg.Requests <= 0 {
		cfg = DefaultPINRateLimitConfig()
	}
	limiter := sharedratelimit.NewLimiter(rc, log, "inventory-pin")
	return limiter.Middleware(sharedratelimit.IPKey, cfg.Requests, cfg.Window)
}
