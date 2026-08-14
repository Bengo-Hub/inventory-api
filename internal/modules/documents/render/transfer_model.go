package render

// TransferDoc is the canonical input for rendering a stock-transfer document: a Dispatch/Transit
// Note (once shipped) or a Goods-Received Note (once received). Both render off the SAME
// transfer record and its already-issued TransferNumber — no separate document sequence — so
// this one model covers both moments, selected by which fields the caller populates (see
// DocTitle/AcknowledgementText/sign-off labels). Deliberately has NO financial fields (Subtotal/
// Tax/Grand) — a stock transfer moves goods between the tenant's own warehouses, not a sale.
type TransferDoc struct {
	Branding Branding

	DocTitle    string // "DELIVERY NOTE" / "TRANSIT DOCUMENT" or "GOODS RECEIVED NOTE"
	DocSubtitle string
	Status      string // banner-free, but still shown in the meta box for context

	TransferNumber string
	Date           string // dispatch date on the delivery note, receipt date on the GRN
	Reference      string // waybill / dispatch no.
	Carrier        string

	FromWarehouseName string
	FromWarehouseAddr []string
	ToWarehouseName   string
	ToWarehouseAddr   []string

	Items []TransferDocLine

	// AcknowledgementText, when set, renders a bold lead line above the item table (mirrors
	// treasury's delivery_challan profile) — e.g. "Received the following goods into <warehouse>
	// in good order and condition:". Empty omits the line entirely.
	AcknowledgementText string

	Notes []string

	// Sign-off block. Labels differ by document (LeftSigLabel/RightSigLabel), so both are
	// caller-supplied rather than hardcoded — the dispatch note reads "Dispatched By"/"Received
	// By" (right blank until actually received); the GRN reads "Delivered By"/"Received By".
	LeftSigLabel  string
	LeftSigName   string
	RightSigLabel string
	RightSigName  string
}

// TransferDocLine is a single item row. ReceivedQty/VarianceReason are only populated on a
// Goods-Received Note render — an empty ReceivedQty means "not applicable" (the dispatch note
// never shows a received column at all).
type TransferDocLine struct {
	Desc           string
	SubDesc        string // SKU
	Unit           string
	Qty            string // shipped/drafted quantity
	ReceivedQty    string
	VarianceReason string
}
