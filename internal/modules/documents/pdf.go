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

// RenderTransferPDF renders a branded A4 stock-transfer document (Dispatch/Transit Note or
// Goods-Received Note, depending on what the caller populated on d) and returns PDF bytes.
func RenderTransferPDF(d TransferDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderTransfer(&d, logo, logoType)
}

// RenderTransferXLSX renders the same stock-transfer document as a styled, print-ready Excel
// workbook (colored/bordered item table, totals footer, A4 page setup) — see
// render.RenderTransferXLSX's doc comment.
func RenderTransferXLSX(d TransferDoc) ([]byte, error) {
	return render.RenderTransferXLSX(&d)
}

// RenderTransferCSV renders the stock-transfer document's data as plain CSV.
func RenderTransferCSV(d TransferDoc) ([]byte, error) {
	return render.RenderTransferCSV(&d)
}

// RenderGoodsReceiptPDF renders a branded A4 goods-receipt note (GRN) for a PO receiving and
// returns PDF bytes.
func RenderGoodsReceiptPDF(d GoodsReceiptDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderGoodsReceipt(&d, logo, logoType)
}

// RenderRequisitionPDF renders a branded A4 purchase requisition and returns PDF bytes.
func RenderRequisitionPDF(d RequisitionDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderRequisition(&d, logo, logoType)
}

// RenderRFQPDF renders a branded A4 request-for-quotation and returns PDF bytes.
func RenderRFQPDF(d RFQDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderRFQ(&d, logo, logoType)
}

// RenderPurchaseReturnPDF renders a branded A4 supplier purchase-return debit note and returns
// PDF bytes.
func RenderPurchaseReturnPDF(d PurchaseReturnDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderPurchaseReturn(&d, logo, logoType)
}

// RenderStockAdjustmentPDF renders a branded A4 stock-adjustment note (the batch of adjustments
// sharing one reference) and returns PDF bytes.
func RenderStockAdjustmentPDF(d StockAdjustmentDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderStockAdjustment(&d, logo, logoType)
}

// RenderStockCountPDF renders a branded A4 stock-take document — a blank count sheet or a
// post-count variance report, per d.Mode — and returns PDF bytes.
func RenderStockCountPDF(d StockCountDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderStockCount(&d, logo, logoType)
}

// RenderBundleSpecPDF renders a branded A4 bundle/package spec sheet and returns PDF bytes.
func RenderBundleSpecPDF(d BundleSpecDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderBundleSpec(&d, logo, logoType)
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
