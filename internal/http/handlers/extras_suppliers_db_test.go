package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bengo-Hub/httpware"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	"github.com/bengobox/inventory-service/internal/testutil"
)

// supplierTestClient runs against Postgres when INVENTORY_TEST_DATABASE_URL is set: a throwaway
// schema with the current ent schema, dropped when the test ends.
func supplierTestClient(t *testing.T) *ent.Client {
	t.Helper()
	return testutil.SchemaClient(t, "inv_sup_")
}

// Archive (DELETE), restore (PUT is_active=true) and a full round-trip PUT that must keep the
// payment configuration the edit form does not show.
func TestSupplierArchiveRestoreAndRoundTrip(t *testing.T) {
	client := supplierTestClient(t)
	ctx := context.Background()
	tenant := uuid.New()
	s := client.Supplier.Create().SetTenantID(tenant).SetName("Pioneer Holdings").SetCode("SUP-1").
		SetMpesaPhone("254700000001").SetMpesaBusinessName("PIONEER").SetSwiftBic("KCBLKENX").SaveX(ctx)

	h := &InventoryExtrasHandler{log: zap.NewNop(), orm: client}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), httpware.TenantIDKey, tenant.String())))
		})
	})
	r.Put("/suppliers/{supplierID}", h.UpdateSupplier)
	r.Delete("/suppliers/{supplierID}", h.DeleteSupplier)
	do := func(method string, body any) int {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/suppliers/"+s.ID.String(), &buf))
		return w.Code
	}

	if code := do(http.MethodDelete, nil); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if got := client.Supplier.GetX(ctx, s.ID); got.IsActive {
		t.Fatal("DELETE must archive the supplier")
	}

	// The edit flow sends the stored supplier back with its edits: everything is kept.
	full := supplierToDTO(client.Supplier.GetX(ctx, s.ID), nil)
	raw, _ := json.Marshal(full)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	body["phone"] = "0711000000"
	body["is_active"] = true
	if code := do(http.MethodPut, body); code != http.StatusOK {
		t.Fatalf("restore = %d", code)
	}
	got := client.Supplier.GetX(ctx, s.ID)
	if !got.IsActive || got.ContactPhone != "0711000000" || got.MpesaPhone != "254700000001" ||
		got.MpesaBusinessName != "PIONEER" || got.SwiftBic != "KCBLKENX" || got.Name != "Pioneer Holdings" {
		t.Fatalf("after round-trip edit: %+v", got)
	}

	// An update that does not mention is_active leaves it alone.
	delete(body, "is_active")
	if code := do(http.MethodPut, body); code != http.StatusOK || !client.Supplier.GetX(ctx, s.ID).IsActive {
		t.Fatalf("update without is_active changed the status (code %d)", code)
	}

	// Another tenant cannot archive it.
	other := chi.NewRouter()
	other.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), httpware.TenantIDKey, uuid.NewString())))
		})
	})
	other.Delete("/suppliers/{supplierID}", h.DeleteSupplier)
	w := httptest.NewRecorder()
	other.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/suppliers/"+s.ID.String(), nil))
	if w.Code != http.StatusNotFound || !client.Supplier.Query().Where(entsupplier.ID(s.ID)).OnlyX(ctx).IsActive {
		t.Fatalf("foreign delete = %d", w.Code)
	}
}
