package documents

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bengobox/inventory-service/internal/modules/documents/render"
)

// RenderPurchaseOrderPDF renders a branded A4 purchase order and returns PDF bytes.
// It fetches the tenant logo (best-effort) and delegates the premium, tenant-
// branding-aware rendering to the render subpackage.
func RenderPurchaseOrderPDF(d PurchaseOrderDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.Render(&d, logo, logoType)
}

// FetchLogoBytes downloads a tenant logo from the (trusted auth-api) URL, returning the raw
// bytes and the fpdf image-type ("PNG"/"JPG"/"GIF"). Graceful: returns (nil, "") on any failure
// so callers (e.g. report PDFs) still render without a logo. Exported wrapper over fetchLogoBytes.
func FetchLogoBytes(url string) ([]byte, string) { return fetchLogoBytes(url) }

// fetchLogoBytes downloads a logo from the (trusted auth-api) URL. Graceful: returns
// nil on any failure so the document still renders without a logo.
func fetchLogoBytes(url string) ([]byte, string) {
	if strings.TrimSpace(url) == "" {
		return nil, ""
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil || len(b) == 0 {
		return nil, ""
	}
	return b, imgType(b)
}

// imgType returns the fpdf image-type string for the given raw image bytes.
func imgType(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "JPG"
	case len(b) >= 4 && b[0] == 0x47 && b[1] == 0x49 && b[2] == 0x46:
		return "GIF"
	default:
		return "PNG"
	}
}
