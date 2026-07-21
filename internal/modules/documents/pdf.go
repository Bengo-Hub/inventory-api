package documents

import (
	sharedcache "github.com/Bengo-Hub/cache"

	"github.com/bengobox/inventory-service/internal/modules/documents/render"
)

// RenderPurchaseOrderPDF renders a branded A4 purchase order and returns PDF bytes.
// It fetches the tenant logo (best-effort) and delegates the premium, tenant-
// branding-aware rendering to the render subpackage.
func RenderPurchaseOrderPDF(d PurchaseOrderDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.Render(&d, logo, logoType)
}

// FetchLogoBytes downloads a tenant logo and returns the raw bytes + fpdf image-type
// ("PNG"/"JPG"/"GIF"). Graceful: returns (nil, "") on any failure so callers (e.g. report PDFs)
// still render without a logo. Exported wrapper over fetchLogoBytes.
func FetchLogoBytes(url string) ([]byte, string) { return fetchLogoBytes(url) }

// fetchLogoBytes resolves the tenant logo through the shared cache.FetchLogo — it handles a hosted
// URL as well as an inline base64 data: URI (http.Get cannot fetch a data: URI), and sniffs the
// fpdf image type from the real bytes rather than a mislabeled Content-Type. Graceful: returns
// (nil, "") on any failure so the document still renders without a logo.
func fetchLogoBytes(url string) ([]byte, string) {
	return sharedcache.FetchLogo(url)
}
