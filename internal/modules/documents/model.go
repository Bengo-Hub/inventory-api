package documents

import "github.com/bengobox/inventory-service/internal/modules/documents/render"

// The canonical document model lives in the render subpackage so the renderer can
// use it without an import cycle. These aliases keep the public API stable for the
// rest of the service (handlers, service.go) which reference documents.* types.

// Branding is the tenant-branding-aware header/footer data sourced from the auth
// cache (logo, company identity, contacts). Colors fall back to the house palette.
type Branding = render.Branding

// DocLine is a single line item rendered in a document table.
type DocLine = render.DocLine

// PurchaseOrderDoc is the canonical input for rendering a Purchase Order PDF.
type PurchaseOrderDoc = render.PurchaseOrderDoc

// TransferDoc is the canonical input for rendering a stock-transfer Dispatch/Transit Note or
// Goods-Received Note PDF.
type TransferDoc = render.TransferDoc

// TransferDocLine is a single item row on a TransferDoc.
type TransferDocLine = render.TransferDocLine

// GoodsReceiptDoc is the canonical input for rendering a PO goods-receipt (GRN) PDF.
type GoodsReceiptDoc = render.GoodsReceiptDoc

// GoodsReceiptDocLine is a single received line on a GoodsReceiptDoc.
type GoodsReceiptDocLine = render.GoodsReceiptDocLine

// RequisitionDoc is the canonical input for rendering a purchase-requisition PDF.
type RequisitionDoc = render.RequisitionDoc

// RequisitionDocLine is a single requested line on a RequisitionDoc.
type RequisitionDocLine = render.RequisitionDocLine

// RFQDoc is the canonical input for rendering a request-for-quotation PDF.
type RFQDoc = render.RFQDoc

// RFQDocLine is a single quoted-on item line on an RFQDoc.
type RFQDocLine = render.RFQDocLine

// RFQDocQuote is one supplier's response in an RFQDoc's quotation appendix.
type RFQDocQuote = render.RFQDocQuote

// PurchaseReturnDoc is the canonical input for rendering a supplier purchase-return debit note.
type PurchaseReturnDoc = render.PurchaseReturnDoc

// PurchaseReturnDocLine is a single returned line on a PurchaseReturnDoc.
type PurchaseReturnDocLine = render.PurchaseReturnDocLine

// StockAdjustmentDoc is the canonical input for rendering a stock-adjustment note.
type StockAdjustmentDoc = render.StockAdjustmentDoc

// StockAdjustmentDocLine is a single adjusted item on a StockAdjustmentDoc.
type StockAdjustmentDocLine = render.StockAdjustmentDocLine

// StockCountDoc is the canonical input for rendering a stock-take sheet or variance report.
type StockCountDoc = render.StockCountDoc

// StockCountDocLine is a single counted item on a StockCountDoc.
type StockCountDocLine = render.StockCountDocLine

// Stock-count render modes (see StockCountDoc.Mode).
const (
	StockCountModeBlank    = render.StockCountModeBlank
	StockCountModeVariance = render.StockCountModeVariance
)

// BundleSpecDoc is the canonical input for rendering a bundle/package spec sheet.
type BundleSpecDoc = render.BundleSpecDoc

// BundleSpecDocLine is a single component on a BundleSpecDoc.
type BundleSpecDocLine = render.BundleSpecDocLine
