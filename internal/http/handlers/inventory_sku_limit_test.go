package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/bengobox/inventory-service/internal/modules/items"
	"github.com/bengobox/inventory-service/internal/platform/subscriptions"
)

const skuCap = 5

// skuLimitMock reports `active` items in the catalog and applies a reactivation only when the
// handler's guard allows it, as the real service does.
func skuLimitMock(active int, applied *int) *mockItemsSvc {
	return &mockItemsSvc{
		listItemsFn: func(_ context.Context, _ uuid.UUID, _, status string, _, _ int, _ *uuid.UUID, _ *uuid.UUID, _ string, _ *uuid.UUID, _ *uuid.UUID, _ string, _ ...string) ([]items.ItemDTO, int, error) {
			if status != "active" {
				return nil, 0, fmt.Errorf("unexpected status %q", status)
			}
			return nil, active, nil
		},
		restoreEOLFn: func(ctx context.Context, _ uuid.UUID, sku string) (*items.ItemDTO, error) {
			if err := items.GuardActivation(ctx, 1); err != nil { // the item was End of Life
				return nil, err
			}
			*applied++
			return &items.ItemDTO{SKU: sku, IsActive: true}, nil
		},
		bulkActionFn: func(ctx context.Context, _ uuid.UUID, ids []uuid.UUID, _ string) (*items.BulkActionResult, error) {
			if err := items.GuardActivation(ctx, len(ids)); err != nil { // every id was inactive
				return nil, err
			}
			*applied += len(ids)
			return &items.BulkActionResult{Processed: len(ids), Skipped: []items.BulkSkipped{}}, nil
		},
		getStockFn: func(_ context.Context, _ uuid.UUID, sku string) (*items.StockAvailability, error) {
			return &items.StockAvailability{ItemID: testItemID, SKU: sku}, nil
		},
		updateItemFn: func(ctx context.Context, _ uuid.UUID, _ uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error) {
			// The stored item is already active, so the service never asks the guard.
			*applied++
			return &dto, nil
		},
	}
}

func skuLimitRequest(t *testing.T, svc *mockItemsSvc, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, fmt.Sprintf("/v1/%s/inventory%s", testTenantID, path), &buf)
	claims := &authclient.Claims{TenantID: testTenantID.String(), SubscriptionLimits: map[string]int{subscriptions.LimitSKU: skuCap}}
	req = req.WithContext(authclient.ContextWithClaims(req.Context(), claims))
	w := httptest.NewRecorder()
	newChiRouter(newTestHandler(t, svc, &mockStockSvc{})).ServeHTTP(w, req)
	return w
}

// Reactivating an archived or End of Life item takes a SKU slot like creating one: at the
// plan's inventory_max_sku it is refused with the 402 limit response, below it it works, and
// editing an item that is already active is never refused.
func TestReactivationRespectsTheSKULimit(t *testing.T) {
	applied := 0
	w := skuLimitRequest(t, skuLimitMock(skuCap, &applied), http.MethodPost, "/items/OLD-1/eol/restore", nil)
	require.Equal(t, http.StatusPaymentRequired, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "usage_limit_exceeded")
	require.Contains(t, w.Body.String(), `"metric":"skus"`)
	require.Equal(t, 0, applied)

	w = skuLimitRequest(t, skuLimitMock(skuCap-1, &applied), http.MethodPost, "/items/OLD-1/eol/restore", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, applied)

	// Bulk activate needs a slot per item: two items with one slot left are refused, two slots fit.
	applied = 0
	body := map[string]any{"ids": []string{uuid.NewString(), uuid.NewString()}, "action": "activate"}
	w = skuLimitRequest(t, skuLimitMock(skuCap-1, &applied), http.MethodPost, "/items/bulk-status", body)
	require.Equal(t, http.StatusPaymentRequired, w.Code, w.Body.String())
	require.Equal(t, 0, applied)
	w = skuLimitRequest(t, skuLimitMock(skuCap-2, &applied), http.MethodPost, "/items/bulk-status", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 2, applied)

	// Editing an already active item at the cap still works.
	applied = 0
	w = skuLimitRequest(t, skuLimitMock(skuCap, &applied), http.MethodPut, "/items/LIVE-1",
		map[string]any{"name": "Live renamed", "type": "GOODS", "is_active": true})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, applied)
}

// importLimitMock is a catalog of `active` items plus one archived item OLD. A create or a
// reactivation through it is counted only when it reaches the service.
func importLimitMock(active int, created, reactivated *int) *mockItemsSvc {
	oldID := uuid.New()
	return &mockItemsSvc{
		listItemsFn: func(_ context.Context, _ uuid.UUID, _, status string, _, _ int, _ *uuid.UUID, _ *uuid.UUID, _ string, _ *uuid.UUID, _ *uuid.UUID, _ string, _ ...string) ([]items.ItemDTO, int, error) {
			if status == "active" {
				return nil, active, nil
			}
			return []items.ItemDTO{{ID: oldID, SKU: "OLD", Name: "Old", Type: "GOODS", IsActive: false}}, active + 1, nil
		},
		createItemFn: func(_ context.Context, _ uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error) {
			*created++
			dto.ID = uuid.New()
			return &dto, nil
		},
		updateItemFn: func(ctx context.Context, _ uuid.UUID, id uuid.UUID, dto items.ItemDTO) (*items.ItemDTO, error) {
			// OLD is stored inactive, so turning it on asks the guard, as the real service does.
			if id == oldID && dto.IsActive {
				if err := items.GuardActivation(ctx, 1); err != nil {
					return nil, err
				}
				*reactivated++
			}
			return &dto, nil
		},
	}
}

func importCSV(t *testing.T, svc *mockItemsSvc, csv string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "items.csv")
	require.NoError(t, err)
	_, err = fw.Write([]byte(csv))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/%s/inventory/items/import", testTenantID), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	claims := &authclient.Claims{
		TenantID:             testTenantID.String(),
		SubscriptionLimits:   map[string]int{subscriptions.LimitSKU: skuCap},
		SubscriptionFeatures: []string{"bulk_import"},
	}
	req = req.WithContext(authclient.ContextWithClaims(req.Context(), claims))
	w := httptest.NewRecorder()
	newChiRouter(newTestHandler(t, svc, &mockStockSvc{})).ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	return out
}

// A bulk import counts every new item and every reactivated item against inventory_max_sku as it
// goes. A row past the cap fails with its own error and is never written; the rest carry on.
func TestImportRespectsTheSKULimit(t *testing.T) {
	const rows = "name,sku\nNew one,NEW-1\nOld,OLD\nNew two,NEW-2\n"

	// One slot left: NEW-1 takes it, then OLD and NEW-2 are refused.
	created, reactivated := 0, 0
	out := importCSV(t, importLimitMock(skuCap-1, &created, &reactivated), rows)
	require.Equal(t, 1, created)
	require.Equal(t, 0, reactivated)
	require.EqualValues(t, 1, out["created"])
	require.EqualValues(t, 2, out["failed"])
	errs := fmt.Sprint(out["errors"])
	require.Contains(t, errs, "sku=OLD: items: plan SKU limit reached: reactivating this item")
	require.Contains(t, errs, "sku=NEW-2: items: plan SKU limit reached: adding this item")

	// At the cap nothing is added.
	created, reactivated = 0, 0
	out = importCSV(t, importLimitMock(skuCap, &created, &reactivated), rows)
	require.Equal(t, 0, created)
	require.Equal(t, 0, reactivated)
	require.EqualValues(t, 3, out["failed"])

	// Three slots left: every row goes through.
	created, reactivated = 0, 0
	out = importCSV(t, importLimitMock(skuCap-3, &created, &reactivated), rows)
	require.Equal(t, 2, created)
	require.Equal(t, 1, reactivated)
	require.EqualValues(t, 0, out["failed"])
}
