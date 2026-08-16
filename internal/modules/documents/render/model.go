// Package render is the tenant-branding-aware, premium fpdf renderer for
// inventory-api documents. The visual language (gradient banner, brand-derived
// palette, styled item table, totals block with amount-in-words, signatures and
// footer) mirrors treasury-api's docs renderer and is fully driven by the
// document model + tenant branding.
package render

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
	// ProviderFooterEnabled gates the platform-owner ("Developed & maintained by
	// CodeVertex...") advertisement footer — platform default ON with an optional
	// per-tenant override (see modules/providerfooter.Resolve). Resolved by
	// documents.Service.GetBranding, not this package (pure renderer).
	ProviderFooterEnabled bool
	// ProviderFooterLead/ProviderFooterContact are the LIVE platform-owner strings resolved
	// from the "codevertex" tenant's auth-api record (documents.Service.ResolveProviderFooterText),
	// so a rename/new phone number on that tenant flows into every document without a redeploy.
	// Both are optional: an empty value falls back to this package's compiled-in
	// providerFooterLead/providerFooterContact constants, so a Branding built without the
	// resolve (tests, S2S callers, older code paths) still renders a complete footer.
	ProviderFooterLead    string
	ProviderFooterContact string
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
// All optional fields degrade gracefully — an empty value is simply not rendered.
type PurchaseOrderDoc struct {
	Branding Branding

	PONumber  string
	Date      string
	Currency  string
	Status    string
	Reference string // optional secondary reference shown in the meta box

	SupplierName string
	SupplierAddr []string

	WarehouseName string
	ExpectedDate  string

	Items     []DocLine
	Subtotal  string
	TaxLabel  string
	TaxAmount string
	Grand     string

	// AmountLabel overrides the banner caption (default "Total Order Value").
	AmountLabel string
	// Badge overrides the status badge text in the banner (default derived from Status).
	Badge string
	// AmountInWords, when set, is rendered in the amount-in-words band beneath the
	// totals. Empty means the band is omitted.
	AmountInWords string

	Notes      []string
	PreparedBy string
	ApprovedBy string
}
