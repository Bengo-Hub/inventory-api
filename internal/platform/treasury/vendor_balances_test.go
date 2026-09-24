package treasury

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// TestListVendorBalances_PagesAndDecodesBothAmountShapes guards the S2S backfill source: it must
// follow hasMore across pages, send the service key, and accept treasury decimals whether they
// arrive quoted ("1500.00") or bare (1500), plus a null vendor_id.
func TestListVendorBalances_PagesAndDecodesBothAmountShapes(t *testing.T) {
	tenantID := uuid.New()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("X-API-Key"); got != "k" {
			t.Errorf("X-API-Key = %q, want k", got)
		}
		if want := fmt.Sprintf("/api/v1/s2s/%s/ap/vendors", tenantID); r.URL.Path != want {
			t.Errorf("path = %s, want %s", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			fmt.Fprint(w, `{"data":[{"vendor_id":"11111111-1111-1111-1111-111111111111","vendor_name":"ACME","balance_owed":"1500.00","outstanding_payable":"1500.00","currency":"KES"}],"hasMore":true}`)
		case "2":
			fmt.Fprint(w, `{"data":[{"vendor_id":null,"vendor_identifier":"legacy","vendor_name":"Old Co","balance_owed":-200,"outstanding_payable":0,"currency":"KES"}],"hasMore":false}`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", nil, zap.NewNop())
	got, err := c.ListVendorBalances(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("ListVendorBalances: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (must follow hasMore)", calls)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].BalanceOwed != "1500.00" || got[0].VendorID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("row 0 = %+v", got[0])
	}
	if got[1].BalanceOwed != "-200" || got[1].VendorID != "" || got[1].VendorIdentifier != "legacy" {
		t.Errorf("row 1 = %+v", got[1])
	}
}
