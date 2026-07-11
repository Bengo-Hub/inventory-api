package render

// Platform-owner (Codevertex) advertisement rendered at the bottom of every generated inventory
// document (purchase order, GRN, stock document, …). Values mirror the codevertex platform-owner
// tenant's public auth-api record; kept as constants here because the renderer is a pure function
// with no tenant-cache access. Keep identical to the sibling docs engines (treasury-api docs).
const (
	providerFooterLead    = "Developed & maintained by Codevertex Africa Limited"
	providerFooterContact = "www.codevertexitsolutions.com  ·  info@codevertexitsolutions.com  ·  +254 742 201 368"
)
