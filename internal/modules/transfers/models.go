package transfers

import (
	"time"

	"github.com/google/uuid"
)

// CreateTransferRequest is the input for creating a new stock transfer.
type CreateTransferRequest struct {
	SourceWarehouseID      uuid.UUID             `json:"source_warehouse_id"`
	DestinationWarehouseID uuid.UUID             `json:"destination_warehouse_id"`
	Items                  []TransferLineRequest `json:"items"`
	Notes                  string                `json:"notes,omitempty"`
	ReferenceNo            string                `json:"reference_no,omitempty"`
	ShippingCharges        float64               `json:"shipping_charges,omitempty"`
	Carrier                string                `json:"carrier,omitempty"`
	FreightNotes           string                `json:"freight_notes,omitempty"`
	// TransferDate optionally backdates or postdates this transfer's reporting date ("YYYY-MM-DD").
	// Empty = report under created_at (today), same as before this field existed. See
	// parseAndValidateTransferDate for the accepted range.
	TransferDate string `json:"transfer_date,omitempty"`
}

// UpdateTransferRequest is the input for amending a DRAFT transfer's line items and header
// fields before it ships. Source/destination warehouse are immutable (not part of this
// request) — the wrong-warehouse case is served by Cancel + a fresh Create instead.
type UpdateTransferRequest struct {
	Items           []TransferLineRequest `json:"items"`
	Notes           string                `json:"notes,omitempty"`
	ReferenceNo     string                `json:"reference_no,omitempty"`
	ShippingCharges float64               `json:"shipping_charges,omitempty"`
	Carrier         string                `json:"carrier,omitempty"`
	FreightNotes    string                `json:"freight_notes,omitempty"`
	// TransferDate mirrors CreateTransferRequest's field — full-replace like every other header
	// field on this request: empty clears back to reporting under created_at.
	TransferDate string `json:"transfer_date,omitempty"`
}

// ReceiveTransferRequest is the input for receiving an in-transit transfer. Items is optional —
// omit entirely (or omit a given line) for the common full-receipt case, where every line
// credits its full drafted Quantity exactly as before. Include a line only when what actually
// arrived at the destination differs from what was shipped.
type ReceiveTransferRequest struct {
	Items []ReceiveLineInput `json:"items,omitempty"`
}

// ReceiveLineInput overrides one line's received quantity, keyed by the STOCK TRANSFER LINE id
// (not item_id — a transfer can carry more than one line for the same item, e.g. different
// lots, so the line id is the only unambiguous key).
type ReceiveLineInput struct {
	LineID         uuid.UUID `json:"line_id"`
	ReceivedQty    float64   `json:"received_qty"`
	VarianceReason string    `json:"variance_reason,omitempty"`
}

// TransferLineRequest represents a single line item on a transfer.
type TransferLineRequest struct {
	ItemID    uuid.UUID  `json:"item_id"`
	VariantID *uuid.UUID `json:"variant_id,omitempty"`
	LotID     *uuid.UUID `json:"lot_id,omitempty"`
	Quantity  float64    `json:"quantity"`
}

// TransferResponse is the full representation of a stock transfer with lines.
type TransferResponse struct {
	ID             uuid.UUID `json:"id"`
	TenantID       uuid.UUID `json:"tenant_id"`
	TransferNumber string    `json:"transfer_number"`
	Status         string    `json:"status"`
	// Origin distinguishes a normal user-initiated transfer ("manual", the New Transfer dialog)
	// from one auto-recorded after the fact by another feature (e.g. "bulk_adjust") — see
	// RecordCompletedTransfer.
	Origin               string        `json:"origin"`
	SourceWarehouse      WarehouseInfo `json:"source_warehouse"`
	DestinationWarehouse WarehouseInfo `json:"destination_warehouse"`
	InitiatedBy          *uuid.UUID    `json:"initiated_by,omitempty"`
	Notes                string        `json:"notes,omitempty"`
	ReferenceNo          string        `json:"reference_no,omitempty"`
	ShippingCharges      float64       `json:"shipping_charges"`
	Carrier              string        `json:"carrier,omitempty"`
	FreightNotes         string        `json:"freight_notes,omitempty"`
	ShippedAt            *time.Time    `json:"shipped_at,omitempty"`
	ReceivedAt           *time.Time    `json:"received_at,omitempty"`
	// TransferDate is the admin/staff-set override of which calendar day this transfer counts
	// toward in reports (see StockTransfer.transfer_date schema comment). Nil = report under
	// CreatedAt, same as every transfer created before this field existed.
	TransferDate *time.Time             `json:"transfer_date,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
	Lines        []TransferLineResponse `json:"lines"`
}

// TransferLineResponse represents a single line in a transfer response.
type TransferLineResponse struct {
	ID       uuid.UUID `json:"id"`
	ItemID   uuid.UUID `json:"item_id"`
	ItemName string    `json:"item_name,omitempty"`
	ItemSKU  string    `json:"item_sku,omitempty"`
	// ItemUnit is the item's unit-of-measure abbreviation (e.g. "PCS", "KG") — blank when the
	// item has no unit assigned.
	ItemUnit  string     `json:"item_unit,omitempty"`
	VariantID *uuid.UUID `json:"variant_id,omitempty"`
	LotID     *uuid.UUID `json:"lot_id,omitempty"`
	Quantity  float64    `json:"quantity"`
	// ReceivedQty is set once the transfer has been received — defaults to Quantity (full
	// receipt) unless the receiver recorded a shortfall via ReceiveTransferRequest.
	ReceivedQty *float64 `json:"received_qty,omitempty"`
	// VarianceReason classifies a shortfall (ReceivedQty < Quantity); empty on a full receipt.
	VarianceReason string `json:"variance_reason,omitempty"`
}

// WarehouseInfo is a lightweight warehouse representation for transfer responses.
type WarehouseInfo struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Code      string    `json:"code"`
	Address   string    `json:"address,omitempty"`
	Latitude  *float64  `json:"latitude,omitempty"`
	Longitude *float64  `json:"longitude,omitempty"`
}

// TransferSummary is a lightweight list representation.
type TransferSummary struct {
	ID                  uuid.UUID  `json:"id"`
	TransferNumber      string     `json:"transfer_number"`
	Status              string     `json:"status"`
	Origin              string     `json:"origin"`
	SourceWarehouseName string     `json:"source_warehouse_name"`
	DestWarehouseName   string     `json:"destination_warehouse_name"`
	LineCount           int        `json:"line_count"`
	ShippedAt           *time.Time `json:"shipped_at,omitempty"`
	ReceivedAt          *time.Time `json:"received_at,omitempty"`
	TransferDate        *time.Time `json:"transfer_date,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

// TransferListFilter contains filters for listing transfers.
type TransferListFilter struct {
	Status string
	Search string
	// From/To (zero value = no bound) optionally narrow the list to a created_at range.
	From   time.Time
	To     time.Time
	Limit  int
	Offset int
}
