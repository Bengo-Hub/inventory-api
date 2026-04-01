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
}

// TransferLineRequest represents a single line item on a transfer.
type TransferLineRequest struct {
	ItemID    uuid.UUID  `json:"item_id"`
	VariantID *uuid.UUID `json:"variant_id,omitempty"`
	LotID     *uuid.UUID `json:"lot_id,omitempty"`
	Quantity  int        `json:"quantity"`
}

// TransferResponse is the full representation of a stock transfer with lines.
type TransferResponse struct {
	ID                     uuid.UUID              `json:"id"`
	TenantID               uuid.UUID              `json:"tenant_id"`
	TransferNumber         string                 `json:"transfer_number"`
	Status                 string                 `json:"status"`
	SourceWarehouse        WarehouseInfo          `json:"source_warehouse"`
	DestinationWarehouse   WarehouseInfo          `json:"destination_warehouse"`
	InitiatedBy            *uuid.UUID             `json:"initiated_by,omitempty"`
	Notes                  string                 `json:"notes,omitempty"`
	ShippedAt              *time.Time             `json:"shipped_at,omitempty"`
	ReceivedAt             *time.Time             `json:"received_at,omitempty"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
	Lines                  []TransferLineResponse `json:"lines"`
}

// TransferLineResponse represents a single line in a transfer response.
type TransferLineResponse struct {
	ID        uuid.UUID  `json:"id"`
	ItemID    uuid.UUID  `json:"item_id"`
	VariantID *uuid.UUID `json:"variant_id,omitempty"`
	LotID     *uuid.UUID `json:"lot_id,omitempty"`
	Quantity  int        `json:"quantity"`
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
	ID                     uuid.UUID  `json:"id"`
	TransferNumber         string     `json:"transfer_number"`
	Status                 string     `json:"status"`
	SourceWarehouseName    string     `json:"source_warehouse_name"`
	DestWarehouseName      string     `json:"destination_warehouse_name"`
	LineCount              int        `json:"line_count"`
	ShippedAt              *time.Time `json:"shipped_at,omitempty"`
	ReceivedAt             *time.Time `json:"received_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
}

// TransferListFilter contains filters for listing transfers.
type TransferListFilter struct {
	Status string
	Search string
	Limit  int
	Offset int
}
