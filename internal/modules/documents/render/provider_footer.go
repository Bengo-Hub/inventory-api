package render

// Platform-owner (Codevertex) advertisement rendered at the bottom of every generated inventory
// document (purchase order, GRN, stock document, …). Values mirror the codevertex platform-owner
// tenant's public auth-api record; kept as constants here because the renderer is a pure function
// with no tenant-cache access. Keep identical to the sibling docs engines (treasury-api docs).
const (
	providerFooterLead    = "Developed & maintained by Codevertex Africa Limited"
	providerFooterContact = "www.codevertexafrica.com  ·  info@codevertexafrica.com  ·  +254 742 201 368"
)

// providerFooterLines resolves the two advertisement lines actually printed by drawDocFooter:
// the LIVE strings that documents.Service.ResolveProviderFooterText put on Branding when it
// could reach the platform-owner tenant, else these compiled-in constants. Split out as a pure
// function so the fallback contract is directly testable (and so both lines resolve identically
// — a live lead with an unresolvable contact still gets a usable contact line).
func providerFooterLines(b Branding) (lead, contact string) {
	return ifEmpty(b.ProviderFooterLead, providerFooterLead),
		ifEmpty(b.ProviderFooterContact, providerFooterContact)
}
