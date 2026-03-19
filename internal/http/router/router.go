package router

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.uber.org/zap"

	httpware "github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	handlers "github.com/bengobox/inventory-service/internal/http/handlers"
	"github.com/bengobox/inventory-service/internal/modules/tenant"
)

func New(
	log *zap.Logger,
	health *handlers.HealthHandler,
	userHandler *handlers.UserHandler,
	inventoryHandler *handlers.InventoryHandler,
	authMiddleware *authclient.AuthMiddleware,
	tenantSyncer *tenant.Syncer,
	allowedOrigins []string,
) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(httpware.RequestID)
	r.Use(httpware.Logging(log))
	r.Use(httpware.Recover(log))
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Tenant-ID", "X-Request-ID", "X-API-Key", "Idempotency-Key"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/healthz", health.Liveness)
	r.Get("/readyz", health.Readiness)
	r.Get("/metrics", health.Metrics)
	r.Get("/v1/docs/*", handlers.SwaggerUI)

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

		api.Get("/openapi.json", handlers.OpenAPIJSON)

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

			// Private User Routes (Always require auth)
			tenant.Group(func(private chi.Router) {
				if authMiddleware != nil {
					private.Use(authMiddleware.RequireAuth)
				}
				userHandler.RegisterRoutes(private)
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
								authMiddleware.RequireAuth(next).ServeHTTP(w, r)
							} else {
								next.ServeHTTP(w, r)
							}
						})
					})
					inventoryHandler.RegisterRoutes(g)
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
								authMiddleware.RequireAuth(next).ServeHTTP(w, r)
							} else {
								next.ServeHTTP(w, r)
							}
						})
					})
					inventoryHandler.RegisterRoutes(g)
				})
			}
		})
	})

	return r
}
