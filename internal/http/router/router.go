package router

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
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
	mediaRoot string,
	serviceConfigHandler *handlers.ServiceConfigHandler,
	inventorySettingsHandler *handlers.InventorySettingsHandler,
	redisClient *redis.Client,
	ormClient *ent.Client,
	stockCountHandler *handlers.StockCountHandler,
	backupsHandler *handlers.Backups,
	backupDestHandler *handlers.BackupDestinationHandler,
	pinAuthHandler *handlers.PINAuthHandler,
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

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(httpware.RequestID)
	r.Use(httpware.Logging(log))
	r.Use(httpware.Recover(log))
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(middleware.RequestSize(10 << 20)) // 10 MB max body size
	r.Use(ratelimitmw.IPRateLimit(redisClient, ratelimitmw.DefaultRateLimitConfig()))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Origin", "X-Request-ID", "X-Tenant-ID", "X-Tenant-Slug", "X-API-Key", "Idempotency-Key", "X-Outlet-ID"},
		ExposedHeaders:   []string{"Link", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/healthz", health.Liveness)
	r.Get("/readyz", health.Readiness)
	r.Get("/metrics", health.Metrics)
	r.Get("/v1/docs/*", handlers.SwaggerUI)

	// Media upload endpoint — requires authentication to prevent anonymous file uploads
	if mediaHandler != nil && authMiddleware != nil {
		r.With(authMiddleware.RequireAuth).Post("/api/v1/media/upload", mediaHandler.Upload)
	}

	// Serve uploaded media files from the media root directory
	if mediaRoot != "" {
		r.Handle("/media/*", http.StripPrefix("/media", http.FileServer(http.Dir(mediaRoot))))
	}

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v1/docs/", http.StatusMovedPermanently)
	})

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
			// Platform-default backup destination (OneDrive/GDrive/S3/WebDAV/SFTP/SMB).
			if backupDestHandler != nil {
				backupDestHandler.RegisterPlatformRoutes(admin)
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

			// Optional outlet context for warehouse/inventory filtering
			tenant.Use(ratelimitmw.OutletContext)
			// Reject non-HQ users targeting an outlet they are not assigned to.
			if ormClient != nil {
				tenant.Use(ratelimitmw.EnforceOutletAssignment(ormClient, log))
			}

			// Public PIN/terminal login — this IS the login, so no auth. Registered in its own
			// group with NO auth middleware; TenantV2 resolves the tenant from the URL.
			if pinAuthHandler != nil {
				tenant.Group(func(pub chi.Router) {
					pinAuthHandler.RegisterRoutes(pub)
				})
			}

			// Private User Routes (Always require auth — SSO or terminal PIN)
			tenant.Group(func(private chi.Router) {
				if authMiddleware != nil {
					private.Use(handlers.RequireAnyAuth(pinSecret, authMiddleware))
					// Layer 2: Subscription enforcement — reject expired/cancelled tenants
					private.Use(authclient.RequireActiveSubscription())
				}
				// Auth sync endpoint — inventory-ui calls this after SSO callback to get local RBAC
				if authHandler != nil {
					authHandler.RegisterAuthRoutes(private)
				}
				userHandler.RegisterRoutes(private)
				if rbacHandler != nil {
					rbacHandler.RegisterRBACRoutes(private)
				}
				// User-outlet assignment, my-outlets, and audit-log read API
				// (always require auth → registered in the private group).
				if warehouseHandler != nil {
					warehouseHandler.RegisterPrivateRoutes(private)
				}
				if serviceConfigHandler != nil {
					serviceConfigHandler.RegisterTenantRoutes(private)
				}
				if inventorySettingsHandler != nil {
					inventorySettingsHandler.RegisterRoutes(private)
				}
				// Tenant-scoped backups (this tenant's data only) — settings-gated.
				if backupsHandler != nil {
					backupsHandler.RegisterRoutes(private)
				}
				// Per-tenant backup-destination override (mirrors backups off the PVC)
				// — same settings permission gate as the tenant backups routes.
				if backupDestHandler != nil {
					backupDestHandler.RegisterRoutes(private)
				}
			})

			// Inventory Routes (Granular auth)
			if inventoryHandler != nil {
				tenant.Group(func(g chi.Router) {
					// Apply authentication only to non-GET requests
					g.Use(func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if r.Method == http.MethodGet {
								next.ServeHTTP(w, r)
								return
							}
							if authMiddleware != nil {
								handlers.RequireAnyAuth(pinSecret, authMiddleware)(next).ServeHTTP(w, r)
							} else {
								next.ServeHTTP(w, r)
							}
						})
					})
					// Subscription gate for mutations (grace period + X-Sub-Grace-Days-Left header)
					g.Use(authclient.RequireActiveSubscriptionForMutations())
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

			// Optional outlet context for warehouse/inventory filtering
			tenant.Use(ratelimitmw.OutletContext)
			// Reject non-HQ users targeting an outlet they are not assigned to.
			if ormClient != nil {
				tenant.Use(ratelimitmw.EnforceOutletAssignment(ormClient, log))
			}

			if inventoryHandler != nil {
				tenant.Group(func(g chi.Router) {
					// Apply authentication only to non-GET requests
					g.Use(func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if r.Method == http.MethodGet {
								next.ServeHTTP(w, r)
								return
							}
							if authMiddleware != nil {
								handlers.RequireAnyAuth(pinSecret, authMiddleware)(next).ServeHTTP(w, r)
							} else {
								next.ServeHTTP(w, r)
							}
						})
					})
					// Subscription gate for mutations
					g.Use(authclient.RequireActiveSubscriptionForMutations())
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
