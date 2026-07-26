package docs

// Platform-owner (Codevertex) advertisement rendered at the bottom of every generated report.
// Values mirror the codevertex platform-owner tenant's public auth-api record; kept as constants
// here because the report renderer is a pure function with no tenant-cache access. Keep identical
// to the sibling docs engines (pos-api, treasury-api).
const (
	providerFooterLead    = "Developed & maintained by Codevertex Africa Limited"
	providerFooterContact = "www.codevertexafrica.com  ·  info@codevertexafrica.com  ·  +254 742 201 368"
)
