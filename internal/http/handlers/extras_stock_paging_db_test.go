package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bengo-Hub/httpware"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	entitem "github.com/bengobox/inventory-service/internal/ent/item"
	"github.com/bengobox/inventory-service/internal/testutil"
)

type pagedStockBody struct {
	Data []struct {
		ItemName string `json:"item_name"`
	} `json:"data"`
	Total   int  `json:"total"`
	HasMore bool `json:"hasMore"`
	Counts  struct {
		Low      int `json:"low"`
		Out      int `json:"out"`
		Negative int `json:"negative"`
	} `json:"counts"`
}

// TestListStockPagingCountsAndSQLFilters covers the opt-in paging of GET /inventory/stock: the
// page is ordered by item name, the total and the banner counts cover the whole scope, the
// low/out filters run in SQL (low = reorder_level > 0 AND 0 < available <= reorder_level),
// search matches name/SKU/warehouse, non-stock items never appear, and without ?page= the
// bare list is unchanged.
func TestListStockPagingCountsAndSQLFilters(t *testing.T) {
	client := testutil.SchemaClient(t, "inv_stockpage_")
	ctx := context.Background()
	tenant := uuid.New()
	client.Tenant.Create().SetID(tenant).SetName("Shop").SetSlug("shop-" + tenant.String()[:8]).SaveX(ctx)
	wh := client.Warehouse.Create().SetTenantID(tenant).SetName("Main store").SetCode("WH-1").SaveX(ctx)

	add := func(name, sku string, typ entitem.Type, onHand, available float64, reorder int) {
		it := client.Item.Create().SetTenantID(tenant).SetSku(sku).SetName(name).SetType(typ).SaveX(ctx)
		client.InventoryBalance.Create().SetTenantID(tenant).SetItemID(it.ID).SetWarehouseID(wh.ID).
			SetOnHand(onHand).SetAvailable(available).SetReorderLevel(reorder).SetUnitOfMeasure("pc").SaveX(ctx)
	}
	add("Apple", "APL-1", entitem.TypeGOODS, 0, 0, 0)          // out of stock
	add("Banana", "BAN-1", entitem.TypeGOODS, 3, 3, 5)         // low
	add("Cherry", "CHR-1", entitem.TypeGOODS, -2, -2, 0)       // out and below zero
	add("Date", "DAT-1", entitem.TypeGOODS, 50, 50, 5)         // fine
	add("Delivery fee", "SRV-1", entitem.TypeSERVICE, 9, 9, 0) // not stockable: never listed

	h := &InventoryExtrasHandler{log: zap.NewNop(), orm: client}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), httpware.TenantIDKey, tenant.String())))
		})
	})
	r.Get("/stock", h.ListStock)
	get := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/stock"+query, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /stock%s = %d: %s", query, w.Code, w.Body.String())
		}
		return w
	}
	paged := func(query string) pagedStockBody {
		var b pagedStockBody
		if err := json.Unmarshal(get(query).Body.Bytes(), &b); err != nil {
			t.Fatalf("decode %s: %v", query, err)
		}
		return b
	}
	names := func(b pagedStockBody) []string {
		out := make([]string, 0, len(b.Data))
		for _, d := range b.Data {
			out = append(out, d.ItemName)
		}
		return out
	}
	eq := func(label string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s = %v, want %v", label, got, want)
			}
		}
	}

	p1 := paged("?page=1&limit=2")
	eq("page 1", names(p1), []string{"Apple", "Banana"})
	if p1.Total != 4 || !p1.HasMore {
		t.Fatalf("page 1 total=%d hasMore=%v, want 4 true", p1.Total, p1.HasMore)
	}
	if p1.Counts.Low != 1 || p1.Counts.Out != 2 || p1.Counts.Negative != 1 {
		t.Fatalf("counts = %+v, want low 1 out 2 negative 1", p1.Counts)
	}
	p2 := paged("?page=2&limit=2")
	eq("page 2", names(p2), []string{"Cherry", "Date"})
	if p2.HasMore {
		t.Fatal("page 2 must be the last page")
	}

	low := paged("?page=1&limit=10&low_stock=true")
	eq("low stock", names(low), []string{"Banana"})
	if low.Counts.Out != 2 {
		t.Fatalf("banner counts must ignore the status tab, got %+v", low.Counts)
	}
	eq("out of stock", names(paged("?page=1&limit=10&out_of_stock=true")), []string{"Apple", "Cherry"})
	eq("search", names(paged("?page=1&limit=10&search=%20cher%20")), []string{"Cherry"})
	eq("warehouse search", names(paged("?page=1&limit=10&search=main")), []string{"Apple", "Banana", "Cherry", "Date"})

	var bare []map[string]any
	if err := json.Unmarshal(get("").Body.Bytes(), &bare); err != nil {
		t.Fatalf("unpaged response must stay a bare list: %v", err)
	}
	if len(bare) != 4 {
		t.Fatalf("unpaged list has %d rows, want 4", len(bare))
	}
}
