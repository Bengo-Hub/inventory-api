package render

// simpleDoc is the shared render PIPELINE every document type added after the purchase order and
// the stock transfer composes through. Those two keep bespoke Render funcs (render.go /
// transfer.go) because each has document-specific chrome — the PO's gradient amount banner and
// fixed Subtotal/Tax/Grand block, the transfer's conditional RECEIVED/VARIANCE columns. Everything
// else (goods receipts, requisitions, RFQs, purchase returns, stock adjustments, stock counts,
// bundle specs) is the SAME vertical rhythm with different content, so each of those exposes its
// own typed model + a thin mapping into this one struct rather than nine near-identical copies of
// the page-geometry/pagination/footer sequence below.

import (
	"bytes"
	"fmt"
)

type simpleDoc struct {
	Branding Branding

	Title    string
	Subtitle string
	// MetaRows populate the right-hand key/value box (document number, date, status, …).
	MetaRows [][2]string

	// Parties, when non-nil, renders the two-up party cards (supplier/warehouse, requester/outlet…).
	Parties *[2]partyCard
	// Lead renders a bold purpose/acknowledgement line above the item table. Empty omits it.
	Lead string

	Numbered bool
	Columns  []docColumn
	Rows     []docRow

	// Appendix renders a SECOND table under its own small heading (an RFQ's supplier quotations,
	// say). Skipped entirely when AppendixRows is empty — an appendix is supplementary and must
	// never block or distort the base document when its data isn't there.
	AppendixTitle   string
	AppendixColumns []docColumn
	AppendixRows    []docRow

	// Totals renders the right-aligned money stack (rows with a blank Value are skipped, so an
	// entirely valueless stack draws nothing — correct for the non-financial documents).
	Totals []totalRow

	NotesTitle string
	Notes      []string

	LeftSigLabel  string
	LeftSigName   string
	RightSigLabel string
	RightSigName  string

	// Disclaimer is the footnote line above the tenant contact/provider footer.
	Disclaimer string
	// ErrLabel names the document in the error returned when fpdf output fails.
	ErrLabel string
}

// renderSimpleDoc runs the shared page pipeline: header → identity/meta → parties → lead →
// item table → totals → notes → signatures → footer.
//
// The ensurePage calls between blocks are what keep a long, multi-page item table from pushing
// the trailing sections past the physical page bottom, and auto page break is disabled before the
// bottom-pinned signature/footer stack so their fixed positions can't spill onto a near-empty
// extra page — the same sequencing render.go and transfer.go use (see primitives.go's
// pageBottomSafe doc comment).
func renderSimpleDoc(d simpleDoc, logo []byte, logoType string) ([]byte, error) {
	pdf := newDoc()
	pdf.AddPage()

	p := newPainter(pdf, newPalette(d.Branding.PrimaryColor))

	y := p.drawDocHeader(d.Branding, logo, logoType, d.Title, d.Subtitle)
	y = p.drawDocIdentity(d.Branding, y, d.MetaRows)
	if d.Parties != nil {
		y = p.drawPartyCards(y+5.0, d.Parties[0], d.Parties[1])
	}
	y = p.drawLeadLine(d.Lead, y+4.0)
	y = p.drawDocTable(y+4.0, d.Numbered, d.Columns, d.Rows)
	if len(d.AppendixRows) > 0 {
		// 30mm keeps the heading with at least its column bar and first row, never orphaned at
		// the very bottom of a page.
		y = p.ensurePage(y, 30.0)
		y = p.drawSectionHeading(y+6.0, d.AppendixTitle)
		y = p.drawDocTable(y+2.0, false, d.AppendixColumns, d.AppendixRows)
	}
	if len(d.Totals) > 0 {
		y = p.ensurePage(y, 26.0)
		y = p.drawTotalsStack(y+4.0, d.Totals)
	}
	y = p.ensurePage(y, 18.0)
	y = p.drawNotesBlock(y+5.0, d.NotesTitle, d.Notes)
	pdf.SetAutoPageBreak(false, 0)
	y = p.ensurePage(y, 24.0)
	y = p.drawSigPair(y+10.0, d.LeftSigLabel, d.LeftSigName, d.RightSigLabel, d.RightSigName)
	p.drawDocFooter(d.Branding, y+6.0, d.Disclaimer)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render %s pdf: %w", ifEmpty(d.ErrLabel, "document"), err)
	}
	return buf.Bytes(), nil
}

// issuedByDisclaimer is the default footnote for documents that don't warrant their own wording.
func issuedByDisclaimer(b Branding, what string) string {
	return "This " + what + " is issued by " + ifEmpty(b.CompanyName, "the issuer") + " for internal record purposes."
}
