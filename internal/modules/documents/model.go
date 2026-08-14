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
