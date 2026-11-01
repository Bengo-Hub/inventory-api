package router

import "testing"

func TestIsPublicCatalogPath(t *testing.T) {
	cases := map[string]bool{
		"/api/v1/urban-loft/inventory/items":             true,
		"/api/v1/urban-loft/inventory/items/BEV-AME-001": true,
		"/api/v1/urban-loft/inventory/items/SKU/images":  true,
		"/api/v1/urban-loft/inventory/categories":        true,
		"/api/v1/urban-loft/inventory/recipes/abc":       true,
		"/api/v1/urban-loft/inventory/availability":      true,
		"/api/v1/urban-loft/inventory/warehouses":        false,
		"/api/v1/urban-loft/inventory/transfers":         false,
		"/api/v1/urban-loft/inventory/analytics/summary": false,
		"/api/v1/urban-loft/inventory/adjustments":       false,
		"/api/v1/urban-loft/inventory/items-export":      false,
		"/api/v1/urban-loft/inventory/summary":           false,
		"/v1/urban-loft/inventory/items":                 false,
	}
	for path, want := range cases {
		if got := isPublicCatalogPath(path); got != want {
			t.Errorf("%s: got %v, want %v", path, got, want)
		}
	}
}
