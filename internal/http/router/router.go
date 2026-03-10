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
		api.Get("/openapi.json", handlers.OpenAPIJSON)

		if authMiddleware != nil {
			api.Use(authMiddleware.RequireAuth)
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

		api.Route("/{tenant}", func(tenant chi.Router) {
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

			userHandler.RegisterRoutes(tenant)

			if inventoryHandler != nil {
				inventoryHandler.RegisterRoutes(tenant)
			}
		})
	})

	// Also support /v1/ prefix (ordering-backend inventory client uses /v1/{tenant}/inventory/...)
	r.Route("/v1", func(v1 chi.Router) {
		if authMiddleware != nil {
			v1.Use(authMiddleware.RequireAuth)
		}

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
				inventoryHandler.RegisterRoutes(tenant)
			}
		})
	})

	return r
}
