package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/bengobox/inventory-service/internal/http/docs"
)

func pathsIn(t *testing.T, body []byte) int {
	t.Helper()
	var doc struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("spec is not JSON: %v", err)
	}
	return len(doc.Paths)
}

// The full generated spec goes only to a caller holding the internal service key (MarketFlow's
// endpoint discovery); everyone else keeps getting the short public spec.
func TestOpenAPIJSONServesTheFullSpecToTheServiceKeyOnly(t *testing.T) {
	h := OpenAPIJSONFor("svc-key")
	get := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
		if key != "" {
			r.Header.Set("X-API-Key", key)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	public := get("")
	if public.Code != http.StatusOK || public.Header().Get("X-Docs-View") != "external" {
		t.Fatalf("anonymous: %d %q", public.Code, public.Header().Get("X-Docs-View"))
	}
	wrong := get("not-the-key")
	if wrong.Header().Get("X-Docs-View") != "external" || pathsIn(t, wrong.Body.Bytes()) != pathsIn(t, public.Body.Bytes()) {
		t.Fatal("a wrong key must get the public spec")
	}
	full := get("svc-key")
	if full.Code != http.StatusOK || full.Header().Get("X-Docs-View") != "internal" {
		t.Fatalf("service key: %d %q", full.Code, full.Header().Get("X-Docs-View"))
	}
	if n, p := pathsIn(t, full.Body.Bytes()), pathsIn(t, public.Body.Bytes()); n <= p {
		t.Fatalf("full spec has %d paths, public %d", n, p)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	r.Header.Set("X-API-Key", "anything")
	OpenAPIJSONFor("")(w, r)
	if w.Header().Get("X-Docs-View") != "external" {
		t.Fatal("with no key configured nobody gets the full spec")
	}
}
