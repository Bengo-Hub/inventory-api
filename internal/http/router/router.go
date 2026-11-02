package router

import (
	"context"
	ratelimit "github.com/Bengo-Hub/shared-ratelimit"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	httpware "github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/bengobox/inventory-service/internal/ent"
	handlers "github.com/bengobox/inventory-service/internal/http/handlers"
	ratelimitmw "github.com/bengobox/inventory-service/internal/http/middleware"
	"github.com/bengobox/inventory-service/internal/modules/rbac"
	"github.com/bengobox/inventory-service/internal/modules/tenant"
	"github.com/google/uuid"
)

func New(
	log *zap.Logger,
	health *handlers.HealthHandler,
	userHandler *handlers.UserHandler,
	inventoryHandler *handlers.InventoryHandler,
	warehouseHandler *handlers.WarehouseHandler,
	warehouseLocationHandler *handlers.WarehouseLocationHandler,
	pricingTierHandler *handlers.PricingTierHandler,
	brandHandler *handlers.BrandHandler,
	transferHandler *handlers.TransferHandler,
	inventoryExtrasHandler *handlers.InventoryExtrasHandler,
	analyticsHandler *handlers.AnalyticsHandler,
	rbacHandler *handlers.RBACHandler,
	authHandler *handlers.AuthHandler,
	authMiddleware *authclient.AuthMiddleware,
	tenantSyncer *tenant.Syncer,
	rbacService *rbac.Service,
	allowedOrigins []string,
	mediaHandler *handlers.MediaHandler,
	mediaFiles http.Handler,
	serviceConfigHandler *handlers.ServiceConfigHandler,
	inventorySettingsHandler *handlers.InventorySettingsHandler,
	redisClient *redis.Client,
	ormClient *ent.Client,
	stockCountHandler *handlers.StockCountHandler,
	pinAuthHandler *handlers.PINAuthHandler,
	internalServiceKey string,
	notificationsStreamHandler *handlers.NotificationsStreamHandler,
	stockClearanceHandler *handlers.StockClearanceHandler,
	tenantExport http.Handler,
) http.Handler {
	r := chi.NewRouter()

	// Terminal/PIN JWT secret — lets the private + inventory groups accept PIN sessions
	// (via RequireAnyAuth) in addition to SSO, and is nil-safe when PIN login is unwired.
	var pinSecret []byte
	if pinAuthHandler != nil {
		pinSecret = pinAuthHandler.Secret()
		if inventoryHandler != nil {
			inventoryHandler.SetTerminalSecret(pinSecret)
		}
	}

	// Feature-gated GET routes (e.g. GET /inventory/adjustments) are exempt from the
	// group-level GET auth, so give the handler the auth middleware to re-require auth
	// on those routes and populate claims before the feature check.
	if inventoryHandler != nil && authMiddleware != nil {
		inventoryHandler.SetAuthMiddleware(authMiddleware)
	}
	// Same for the extras handler's feature-gated report GETs (report_* codes).
	if inventoryExtrasHandler != nil && authMiddleware != nil {
		inventoryExtrasHandler.SetAuthForFeatureGets(handlers.RequireAnyAuth(pinSecret, authMiddleware))
	}

	r.Use(middleware.RequestID)
	// Never chi RealIP: it trusts client-sent True-Client-IP/X-Forwarded-For.
	r.Use(ratelimit.TrustedRealIP)
	r.Use(httpware.RequestID)
	// bypassForWebsocket: see its doc comment — httpware.Logging's wrapper structurally cannot
	// support Hijack (a pre-existing fleet-wide bug), which breaks every WS upgrade in this API.
	r.Use(httpware.BypassForStreaming(httpware.Logging(log)))
	r.Use(httpware.Recover(log))
	// gzip JSON responses (item/stock lists) — no compression existed at any layer for this API.
	r.Use(httpware.BypassForStreaming(middleware.Compress(5)))
	// bypassForWebsocket here too: chi's Timeout fires its own abort/WriteHeader on ITS timer
	// regardless of whether the connection was since hijacked for a long-lived WS stream, forcibly
	// disconnecting every open WS connection roughly every 30s ("http: response.WriteHeader on
	// hijacked connection" — confirmed live via kubectl logs during E2E verification). A 30s
	// request timeout is meaningless for a stream that's SUPPOSED to stay open indefinitely.
	r.Use(httpware.BypassForStreaming(middleware.Timeout(30 * time.Second)))
	r.Use(middleware.RequestSize(10 << 20)) // 10 MB max body size
	r.Use(httpware.CORS(httpware.StandardCORS(allowedOrigins)))
	// After CORS so 429 responses still carry Access-Control-Allow-* headers.
	r.Use(ratelimitmw.IPRateLimit(redisClient, log, ratelimitmw.DefaultRateLimitConfig()))

	r.Get("/healthz", health.Liveness)
	r.Get("/readyz", health.Readiness)
	// Tenant export for auth-api's SSO tenant backups; the handler checks INTERNAL_SERVICE_KEY
	// itself (httpware/tenantexport, Options.InternalKey).
	if tenantExport != nil {
		r.Get("/api/v1/s2s/tenant-export", tenantExport.ServeHTTP)
	}
	r.Get("/metrics", health.Metrics)
	r.Get("/v1/docs/*", handlers.SwaggerUI)

	// Media upload endpoint — requires authentication to prevent anonymous file uploads
	if mediaHandler != nil && authMiddleware != nil {
		r.With(authMiddleware.RequireAuth).Post("/api/v1/media/upload", mediaHandler.Upload)
		r.With(authMiddleware.RequireAuth).Post("/api/v1/media/upload/discard", mediaHandler.Discard)
	}

	// /media: shipped icons and images not yet moved to object storage, served from the media
	// directory; object keys redirect to the public media domain (objectstore.MediaHandler).
	if mediaFiles != nil {
		r.Handle("/media/*", http.StripPrefix("/media", mediaFiles))
	}

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v1/docs/", http.StatusMovedPermanently)
	})

	// outletScope refuses an outlet a branch user is not assigned to, then sets the request's
	// outlet (OutletContext). Both read the caller's claims, so each group adds it after its
	// authentication middleware.
	outletScope := func(g chi.Router) {
		if ormClient != nil {
			g.Use(ratelimitmw.EnforceOutletAssignment(ormClient, log))
		}
		g.Use(ratelimitmw.OutletContext)
	}

	r.Route("/api/v1", func(api chi.Router) {
		if authMiddleware != nil {
			// Require auth for mutation and sensitive data, but allow public GET for master data
			// api.Use(authMiddleware.RequireAuth) // Moved into sub-routes for granular control
		}

		if tenantSyncer != nil {
			api.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					claims, ok := authclient.ClaimsFromContext(r.Context())
					if ok && claims.Subject != "" && claims.TenantID != "" {
						slug := claims.GetTenantSlug()
						if slug != "" {
							// Trigger JIT tenant provisioning
							_, syncErr := tenantSyncer.SyncTenant(r.Context(), slug)
							if syncErr != nil {
								log.Warn("tenant sync failed during JIT user provisioning", zap.Error(syncErr))
							}
						}
					}
					next.ServeHTTP(w, r)
				})
			})
		}

		// JIT user provisioning: create local user and assign default role from JWT claims
		if rbacService != nil {
			api.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					claims, ok := authclient.ClaimsFromContext(r.Context())
					if ok && claims.Subject != "" && claims.TenantID != "" {
						tenantID, err := uuid.Parse(claims.TenantID)
						if err == nil {
							userID, err := uuid.Parse(claims.Subject)
							if err == nil {
								_, _ = rbacService.EnsureUserFromToken(
									r.Context(),
									tenantID,
									userID,
									claims.Email,
									claims.GetTenantSlug(),
									claims.Roles...,
								)
							}
						}
					}
					next.ServeHTTP(w, r)
				})
			})
		}

		api.Get("/openapi.json", handlers.OpenAPIJSON)

		// Platform admin routes (platform owner only)
		api.Route("/admin", func(admin chi.Router) {
			if authMiddleware != nil {
				admin.Use(authMiddleware.RequireAuth)
			}
			admin.Use(authclient.RequirePlatformOwner())
			if serviceConfigHandler != nil {
				serviceConfigHandler.RegisterPlatformRoutes(admin)
			}
			if warehouseHandler != nil {
				warehouseHandler.RegisterAdminRoutes(admin)
			}
			if inventoryHandler != nil {
				inventoryHandler.RegisterAdminRoutes(admin)
			}
		})

		api.Route("/{tenant}", func(tenant chi.Router) {
			tenant.Use(httpware.TenantV2(httpware.TenantConfig{
				ClaimsExtractor: func(ctx context.Context) (tenantID, tenantSlug string, isPlatformOwner bool, ok bool) {
					claims, found := authclient.ClaimsFromContext(ctx)
					if !found {
						// For public GET requests, we can't extract from claims.
						// The TenantV2 middleware will try to resolve from URL if possible.
						return "", "", false, false
					}
					return claims.TenantID, claims.GetTenantSlug(), claims.IsPlatformOwner, true
				},
				URLParamFunc: chi.URLParam,
				Required:     true,
			}))

			// The outlet context is applied inside each group after authentication (outletScope):
			// at this level no claims exist yet, so branch users would never be held to their outlet.

			// Public PIN/terminal login — this IS the login, so no auth. Registered in its own
			// group with NO auth middleware; TenantV2 resolves the tenant from the URL.
			if pinAuthHandler != nil {
				tenant.Group(func(pub chi.Router) {
					pub.Get("/inventory/auth/pin/outlets", pinAuthHandler.PINOutlets)
					// Dedicated, much stricter per-IP limit on the actual PIN-guess surface (bcrypt
					// compare against a candidate set), stacked on top of the general limiter above —
					// see PINRateLimit's doc comment for why (PIN uniqueness is per-tenant only, so a
					// common/guessed PIN can collide across unrelated tenants' admin accounts).
					pub.Group(func(pinGroup chi.Router) {
						pinGroup.Use(ratelimitmw.PINRateLimit(redisClient, log, ratelimitmw.DefaultPINRateLimitConfig()))
						pinGroup.Post("/inventory/auth/pin/identify", pinAuthHandler.IdentifyByPIN)
						pinGroup.Post("/inventory/auth/pin", pinAuthHandler.Login)
					})
				})
			}

			// Private User Routes (Always require auth — SSO or terminal PIN)
			tenant.Group(func(private chi.Router) {
				if authMiddleware != nil {
					private.Use(handlers.RequireAnyAuth(pinSecret, authMiddleware))
					// The token must belong to the tenant in the URL (and any X-Tenant-ID header).
					private.Use(handlers.RequireOwnTenant)
					// Layer 2: Subscription enforcement — reject expired/cancelled tenants
					private.Use(authclient.RequireActiveSubscription())
				}
				outletScope(private)
				// Auth sync endpoint — inventory-ui calls this after SSO callback to get local RBAC.
				// Kept OUTSIDE the module gate below (unlike everything else in this group) so a
				// tenant whose plan doesn't include inventory at all can still bootstrap identity
				// and see a graceful "not included in your plan" message instead of a raw 403 on
				// its very first authenticated call.
				if authHandler != nil {
					authHandler.RegisterAuthRoutes(private)
				}

				// Module gate: block the WHOLE inventory module (reads and writes alike) for a
				// tenant whose plan never included inventory at all. Distinct from the mutations-
				// only RequireActiveSubscription above (subscription STATUS, not module
				// membership) — without this, any SSO user of any tenant could still reach these
				// basic, ungated routes regardless of their plan.
				//
				// 2026-09-11: caused a fleet-wide outage on first deploy (every already-issued
				// JWT lacked the brand-new active_service_tags claim). Restored after all active
				// sessions were force-revoked — see auth-api revocation runbook.
				private.Group(func(inv chi.Router) {
					inv.Use(authclient.RequireServiceAccess("inventory"))
					// Mutations-only annual support-fee gate (e.g. a perpetual-license tenant whose
					// support fee is overdue past its 7-day grace window) — independent axis from
					// the subscription gates above, which never catch a one-time license (it never
					// expires). No-ops for every tenant without a support-fee obligation at all.
					inv.Use(authclient.RequireSupportFeeCurrentForMutations(7))

					userHandler.RegisterRoutes(inv)
					if rbacHandler != nil {
						rbacHandler.RegisterRBACRoutes(inv)
					}
					// User-outlet assignment, my-outlets, and audit-log read API
					// (always require auth → registered in the private group).
					if warehouseHandler != nil {
						warehouseHandler.RegisterPrivateRoutes(inv)
					}
					if serviceConfigHandler != nil {
						serviceConfigHandler.RegisterTenantRoutes(inv)
					}
					if inventorySettingsHandler != nil {
						inventorySettingsHandler.RegisterRoutes(inv)
					}
					// Self-service "Set PIN" (Team page) — unlike PINOutlets/IdentifyByPIN/Login
					// above (the public, pre-auth PIN *login* surface), this is a manager action
					// that requires an authenticated SSO session, so it lives in the private
					// group and is gated by the same permission as the other Team-page
					// user-management routes (see user_outlet.go's RegisterPrivateRoutes).
					if pinAuthHandler != nil && rbacService != nil {
						inv.With(ratelimitmw.RequirePermission(rbacService, log, rbac.PermUsersManage)).
							Post("/inventory/auth/pin/set", pinAuthHandler.SetPIN)
					}
					// Real-time push (stock changes) — inventory-ui connects here so a stock change
					// shows up live instead of on a manual refresh. Same group as everything else
					// above, so RequireAnyAuth's ?access_token= promotion (added alongside this) covers
					// the WebSocket handshake exactly like pos-api's equivalent notifications stream.
					if notificationsStreamHandler != nil {
						inv.Get("/inventory/notifications/stream", notificationsStreamHandler.StreamNotifications)
					}
				})
			})

			// Inventory Routes (Granular auth)
			if inventoryHandler != nil {
				tenant.Group(func(g chi.Router) {
					// Anonymous GETs are DELIBERATE here (public storefront catalog);
					// mutations require PIN/SSO auth.
					g.Use(publicCatalogReads(pinSecret, authMiddleware))
					// A signed-in caller may only act in its own tenant (anonymous catalogue reads
					// carry no token and are scoped by the URL).
					g.Use(handlers.RequireOwnTenant)
					outletScope(g)
					// Subscription gate for mutations (grace period + X-Sub-Grace-Days-Left header)
					g.Use(authclient.RequireActiveSubscriptionForMutationsWithGrace(7))
					// Module gate: block the WHOLE inventory module for an AUTHENTICATED tenant
					// whose plan never included it (anonymous public-storefront GETs above have no
					// claims and pass through unaffected, same as every other gate in this group).
					// 2026-09-11: restored after all active sessions were force-revoked — see
					// auth-api revocation runbook.
					g.Use(authclient.RequireServiceAccess("inventory"))
					// Mutations-only annual support-fee gate — see the matching comment on the
					// private-user-routes group above for the full rationale.
					g.Use(authclient.RequireSupportFeeCurrentForMutations(7))
					inventoryHandler.RegisterRoutes(g)
					if warehouseHandler != nil {
						warehouseHandler.RegisterRoutes(g)
					}
					if warehouseLocationHandler != nil {
						warehouseLocationHandler.RegisterRoutes(g)
					}
					if pricingTierHandler != nil {
						pricingTierHandler.RegisterRoutes(g)
					}
					if stockClearanceHandler != nil {
						stockClearanceHandler.RegisterRoutes(g)
					}
					if brandHandler != nil {
						brandHandler.RegisterRoutes(g)
					}
					if transferHandler != nil {
						transferHandler.RegisterRoutes(g)
					}
					if inventoryExtrasHandler != nil {
						inventoryExtrasHandler.RegisterRoutes(g)
					}
					if analyticsHandler != nil {
						analyticsHandler.RegisterRoutes(g)
					}
					if stockCountHandler != nil {
						stockCountHandler.RegisterRoutes(g)
					}
				})
			}
		})
	})

	// Also support /v1/ prefix (ordering-backend inventory client uses /v1/{tenant}/inventory/...)
	r.Route("/v1", func(v1 chi.Router) {
		// if authMiddleware != nil {
		// 	v1.Use(authMiddleware.RequireAuth)
		// }

		v1.Route("/{tenant}", func(tenant chi.Router) {
			tenant.Use(httpware.TenantV2(httpware.TenantConfig{
				ClaimsExtractor: func(ctx context.Context) (tenantID, tenantSlug string, isPlatformOwner bool, ok bool) {
					claims, found := authclient.ClaimsFromContext(ctx)
					if !found {
						return "", "", false, false
					}
					return claims.TenantID, claims.GetTenantSlug(), claims.IsPlatformOwner, true
				},
				URLParamFunc: chi.URLParam,
				Required:     true,
			}))

			// The outlet context is applied inside each group after authentication (outletScope):
			// at this level no claims exist yet, so branch users would never be held to their outlet.

			if inventoryHandler != nil {
				tenant.Group(func(g chi.Router) {
					// The /v1 tree is S2S-only (pos-api, ordering-backend) and its callers
					// ALWAYS send X-API-Key — so it is fully locked for ALL methods (GETs
					// included; they previously bypassed auth entirely).
					g.Use(requireInternalKeyOrAuth(internalServiceKey, pinSecret, authMiddleware))
					// Service-key callers carry no tenant and pass; a user token must be its own tenant's.
					g.Use(handlers.RequireOwnTenant)
					outletScope(g)
					// Subscription gate for mutations
					g.Use(authclient.RequireActiveSubscriptionForMutationsWithGrace(7))
					// Module gate — no-op for genuine S2S/API-key callers (HasServiceAccess fails
					// open for IsService), real enforcement for a user JWT reaching this path via
					// requireInternalKeyOrAuth's fallback. 2026-09-11: restored after all active
					// sessions were force-revoked — see auth-api revocation runbook.
					g.Use(authclient.RequireServiceAccess("inventory"))
					// Mutations-only annual support-fee gate — see the matching comment on the
					// private-user-routes group above for the full rationale.
					g.Use(authclient.RequireSupportFeeCurrentForMutations(7))
					inventoryHandler.RegisterRoutes(g)
					if warehouseHandler != nil {
						warehouseHandler.RegisterRoutes(g)
					}
					if warehouseLocationHandler != nil {
						warehouseLocationHandler.RegisterRoutes(g)
					}
					if pricingTierHandler != nil {
						pricingTierHandler.RegisterRoutes(g)
					}
					if stockClearanceHandler != nil {
						stockClearanceHandler.RegisterRoutes(g)
					}
					if brandHandler != nil {
						brandHandler.RegisterRoutes(g)
					}
					if transferHandler != nil {
						transferHandler.RegisterRoutes(g)
					}
					if inventoryExtrasHandler != nil {
						inventoryExtrasHandler.RegisterRoutes(g)
					}
					if analyticsHandler != nil {
						analyticsHandler.RegisterRoutes(g)
					}
					if stockCountHandler != nil {
						stockCountHandler.RegisterRoutes(g)
					}
				})
			}
		})
	})

	return r
}

// publicCatalogReads leaves only the public catalogue open to anonymous GETs on the /api/v1
// tenant tree: what a storefront shows a visitor (items, item details and images, categories,
// brands, units, recipes for the menu, availability). Every other request, and any request that
// carries a token, is authenticated (terminal PIN or SSO), so warehouses, transfers, stock and
// analytics are never readable without signing in, and the claims are there for
// RequireOwnTenant to compare with the tenant in the URL.
func publicCatalogReads(pinSecret []byte, authMiddleware *authclient.AuthMiddleware) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			anonymous := r.Header.Get("Authorization") == "" && r.URL.Query().Get("access_token") == ""
			if r.Method == http.MethodGet && anonymous && isPublicCatalogPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if authMiddleware != nil {
				handlers.RequireAnyAuth(pinSecret, authMiddleware)(next).ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// publicCatalogPrefixes are the inventory reads a storefront makes without signing in, relative
// to /api/v1/{tenant}/.
var publicCatalogPrefixes = []string{
	"inventory/items", "inventory/categories", "inventory/brands", "inventory/units",
	"inventory/recipes", "inventory/availability",
}

// isPublicCatalogPath reports whether path (/api/v1/{tenant}/...) is a public catalogue read.
// A prefix matches whole path segments only, so inventory/items-export never counts as items.
func isPublicCatalogPath(path string) bool {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 4) // api, v1, tenant, rest
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "v1" {
		return false
	}
	rest := strings.TrimSuffix(parts[3], "/")
	for _, p := range publicCatalogPrefixes {
		if rest == p || strings.HasPrefix(rest, p+"/") {
			return true
		}
	}
	return false
}

// requireInternalKeyOrAuth guards the /v1 S2S tree for ALL methods. First chance: a
// constant-time X-API-Key compare against the shared INTERNAL_SERVICE_KEY (the static-compare
// pattern every other service's S2S receiver uses) — on match a service principal (IsService)
// is injected so outlet/use-case gates treat the caller exactly like the registered-key path.
// Otherwise falls back to RequireAnyAuth (terminal PIN JWT → SSO JWT → auth-api registered
// bng_* API keys). GETs previously bypassed auth entirely on this tree — the only callers are
// pos-api and ordering-backend, which always send the key, so nothing anonymous depends on it.
func requireInternalKeyOrAuth(internalKey string, pinSecret []byte, authMiddleware *authclient.AuthMiddleware) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if httpware.ValidServiceKey(r, internalKey) {
				claims := &authclient.Claims{
					IsService:          true,
					ServiceName:        "platform-internal",
					SubscriptionExempt: true,
				}
				next.ServeHTTP(w, r.WithContext(authclient.ContextWithClaims(r.Context(), claims)))
				return
			}
			if authMiddleware != nil {
				handlers.RequireAnyAuth(pinSecret, authMiddleware)(next).ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
