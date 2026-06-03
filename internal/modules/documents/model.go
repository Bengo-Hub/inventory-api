package documents

// Branding is the tenant-branding-aware header/footer data sourced from the auth
// cache (logo, company identity, contacts). Colors fall back to the house palette.
type Branding struct {
	CompanyName string
	Tagline     string
	Address     []string
	KRAPIN      string
	LogoURL     string
	Email       string
	Phone       string
	Website     string
	// PrimaryColor is an optional hex (e.g. "#1F6FB2"); empty uses the default palette.
	PrimaryColor string
}

// DocLine is a single line item rendered in a document table.
type DocLine struct {
	Desc    string
	SubDesc string
	Unit    string
	Qty     string
	Rate    string
	Amount  string
}

// PurchaseOrderDoc is the canonical input for rendering a Purchase Order PDF.
type PurchaseOrderDoc struct {
	Branding Branding

	PONumber string
	Date     string
	Currency string
	Status   string

	SupplierName string
	SupplierAddr []string

	WarehouseName string
	ExpectedDate  string

	Items     []DocLine
	Subtotal  string
	TaxLabel  string
	TaxAmount string
	Grand     string

	Notes      []string
	PreparedBy string
	ApprovedBy string
}
