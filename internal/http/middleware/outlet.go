package middleware

import (
	"context"
	"net/http"
)

type ctxKey string

const outletIDKey ctxKey = "outlet_id"

// OutletContext extracts X-Outlet-ID from the request header and stores it in context.
// Downstream handlers use GetOutletID to scope warehouse lookups.
func OutletContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outletID := r.Header.Get("X-Outlet-ID")
		if outletID != "" {
			ctx := context.WithValue(r.Context(), outletIDKey, outletID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GetOutletID retrieves the outlet ID stored in context by OutletContext middleware.
func GetOutletID(ctx context.Context) string {
	v, _ := ctx.Value(outletIDKey).(string)
	return v
}
