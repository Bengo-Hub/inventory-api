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

// RenderPurchaseOrderXLSX renders the same purchase order as a styled, print-ready Excel
// workbook — see render.RenderTransferXLSX's doc comment for the design.
func RenderPurchaseOrderXLSX(d PurchaseOrderDoc) ([]byte, error) {
	return render.RenderPurchaseOrderXLSX(&d)
}

// RenderPurchaseOrderCSV renders the purchase order's data as plain CSV.
func RenderPurchaseOrderCSV(d PurchaseOrderDoc) ([]byte, error) {
	return render.RenderPurchaseOrderCSV(&d)
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

// RenderGoodsReceiptXLSX renders the same goods-receipt note as a styled, print-ready Excel
// workbook.
func RenderGoodsReceiptXLSX(d GoodsReceiptDoc) ([]byte, error) {
	return render.RenderGoodsReceiptXLSX(&d)
}

// RenderGoodsReceiptCSV renders the goods-receipt note's data as plain CSV.
func RenderGoodsReceiptCSV(d GoodsReceiptDoc) ([]byte, error) {
	return render.RenderGoodsReceiptCSV(&d)
}

// RenderRequisitionPDF renders a branded A4 purchase requisition and returns PDF bytes.
func RenderRequisitionPDF(d RequisitionDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderRequisition(&d, logo, logoType)
}

// RenderRequisitionXLSX renders the same requisition as a styled, print-ready Excel workbook.
func RenderRequisitionXLSX(d RequisitionDoc) ([]byte, error) { return render.RenderRequisitionXLSX(&d) }

// RenderRequisitionCSV renders the requisition's data as plain CSV.
func RenderRequisitionCSV(d RequisitionDoc) ([]byte, error) { return render.RenderRequisitionCSV(&d) }

// RenderRFQPDF renders a branded A4 request-for-quotation and returns PDF bytes.
func RenderRFQPDF(d RFQDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderRFQ(&d, logo, logoType)
}

// RenderRFQXLSX renders the same request-for-quotation as a styled, print-ready Excel workbook.
func RenderRFQXLSX(d RFQDoc) ([]byte, error) { return render.RenderRFQXLSX(&d) }

// RenderRFQCSV renders the RFQ's data as plain CSV.
func RenderRFQCSV(d RFQDoc) ([]byte, error) { return render.RenderRFQCSV(&d) }

// RenderPurchaseReturnPDF renders a branded A4 supplier purchase-return debit note and returns
// PDF bytes.
func RenderPurchaseReturnPDF(d PurchaseReturnDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderPurchaseReturn(&d, logo, logoType)
}

// RenderPurchaseReturnXLSX renders the same purchase return as a styled, print-ready Excel
// workbook.
func RenderPurchaseReturnXLSX(d PurchaseReturnDoc) ([]byte, error) {
	return render.RenderPurchaseReturnXLSX(&d)
}

// RenderPurchaseReturnCSV renders the purchase return's data as plain CSV.
func RenderPurchaseReturnCSV(d PurchaseReturnDoc) ([]byte, error) {
	return render.RenderPurchaseReturnCSV(&d)
}

// RenderStockAdjustmentPDF renders a branded A4 stock-adjustment note (the batch of adjustments
// sharing one reference) and returns PDF bytes.
func RenderStockAdjustmentPDF(d StockAdjustmentDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderStockAdjustment(&d, logo, logoType)
}

// RenderStockAdjustmentXLSX renders the same stock-adjustment note as a styled, print-ready
// Excel workbook.
func RenderStockAdjustmentXLSX(d StockAdjustmentDoc) ([]byte, error) {
	return render.RenderStockAdjustmentXLSX(&d)
}

// RenderStockAdjustmentCSV renders the stock-adjustment note's data as plain CSV.
func RenderStockAdjustmentCSV(d StockAdjustmentDoc) ([]byte, error) {
	return render.RenderStockAdjustmentCSV(&d)
}

// RenderStockCountPDF renders a branded A4 stock-take document — a blank count sheet or a
// post-count variance report, per d.Mode — and returns PDF bytes.
func RenderStockCountPDF(d StockCountDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderStockCount(&d, logo, logoType)
}

// RenderStockCountXLSX renders the same stock-take document as a styled, print-ready Excel
// workbook.
func RenderStockCountXLSX(d StockCountDoc) ([]byte, error) { return render.RenderStockCountXLSX(&d) }

// RenderStockCountCSV renders the stock-take document's data as plain CSV.
func RenderStockCountCSV(d StockCountDoc) ([]byte, error) { return render.RenderStockCountCSV(&d) }

// RenderBundleSpecPDF renders a branded A4 bundle/package spec sheet and returns PDF bytes.
func RenderBundleSpecPDF(d BundleSpecDoc) ([]byte, error) {
	logo, logoType := fetchLogoBytes(d.Branding.LogoURL)
	return render.RenderBundleSpec(&d, logo, logoType)
}

// RenderBundleSpecXLSX renders the same bundle spec sheet as a styled, print-ready Excel
// workbook.
func RenderBundleSpecXLSX(d BundleSpecDoc) ([]byte, error) { return render.RenderBundleSpecXLSX(&d) }

// RenderBundleSpecCSV renders the bundle spec sheet's data as plain CSV.
func RenderBundleSpecCSV(d BundleSpecDoc) ([]byte, error) { return render.RenderBundleSpecCSV(&d) }

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
