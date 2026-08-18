package render

// Bundle / hospitality-package SPEC SHEET rendering. Unlike everything else in this package a
// bundle is MASTER DATA, not a transaction: it has no date, no counterparty and no document
// number, so there is no sequence numbering here and the meta box carries the package's
// attributes instead of a document identity.

import "strings"

// BundleSpecDoc is the canonical input for rendering a bundle spec sheet.
type BundleSpecDoc struct {
	Branding Branding

	BundleName string
	// ItemName/ItemSKU identify the sellable item this bundle is sold as.
	ItemName string
	ItemSKU  string

	PackageType string // RETAIL_KIT / ROOM_RATE_PLAN / DDR / …
	PriceBasis  string // flat / per_delegate_per_day / …
	// Attributes are extra pre-formatted label/value pairs (min delegates, sessions, validity,
	// accommodation included, …) — the caller decides which of the optional fields apply.
	Attributes [][2]string
	IsActive   bool

	Components []BundleSpecDocLine

	Notes []string
}

// BundleSpecDocLine is a single component of the bundle.
type BundleSpecDocLine struct {
	Desc    string
	SubDesc string // SKU
	Kind    string // ITEM / MEAL_PERIOD / AV_EQUIPMENT / …
	Qty     string
	Unit    string
	// Metered marks a component consumed on usage rather than issued up front.
	Metered bool
	// MealPeriod, for hospitality packages (breakfast / lunch / …).
	MealPeriod string
}

// RenderBundleSpec builds a premium, tenant-branded A4 bundle spec sheet and returns PDF bytes.
func RenderBundleSpec(doc *BundleSpecDoc, logo []byte, logoType string) ([]byte, error) {
	return renderSimpleDoc(bundleSpecSimpleDoc(doc), logo, logoType)
}

// RenderBundleSpecXLSX renders the same bundle spec sheet as a styled, print-ready Excel
// workbook — see simpledoc_xlsx.go's doc comment.
func RenderBundleSpecXLSX(doc *BundleSpecDoc) ([]byte, error) {
	return renderSimpleDocXLSX(bundleSpecSimpleDoc(doc))
}

// RenderBundleSpecCSV renders the bundle spec sheet's data as plain CSV.
func RenderBundleSpecCSV(doc *BundleSpecDoc) ([]byte, error) {
	return renderSimpleDocCSV(bundleSpecSimpleDoc(doc))
}

// bundleSpecSimpleDoc maps a BundleSpecDoc into the shared simpleDoc pipeline — the one
// definition of this document's shape shared by every export format (PDF/XLSX/CSV above).
func bundleSpecSimpleDoc(doc *BundleSpecDoc) simpleDoc {
	rows := make([]docRow, 0, len(doc.Components))
	for _, c := range doc.Components {
		kind := strings.ToUpper(strings.ReplaceAll(c.Kind, "_", " "))
		sub := c.SubDesc
		if c.MealPeriod != "" {
			sub = strings.TrimSpace(sub + "  ·  " + strings.ToUpper(strings.ReplaceAll(c.MealPeriod, "_", " ")))
			sub = strings.TrimPrefix(sub, "·  ")
		}
		if c.Metered {
			sub = strings.TrimSpace(sub + "  ·  METERED")
			sub = strings.TrimPrefix(sub, "·  ")
		}
		rows = append(rows, docRow{
			SubDesc: sub,
			Cells:   []string{c.Desc, kind, c.Qty, c.Unit},
		})
	}

	meta := [][2]string{
		{"Package", orDash(humanizeConst(doc.PackageType))},
		{"Price Basis", orDash(humanizeConst(doc.PriceBasis))},
		{"Components", plural(len(doc.Components), "item")},
	}
	meta = append(meta, doc.Attributes...)
	status := "ACTIVE"
	if !doc.IsActive {
		status = "INACTIVE"
	}
	meta = append(meta, [2]string{"Status", status})

	return simpleDoc{
		Branding: doc.Branding,
		Title:    "Bundle Spec",
		Subtitle: doc.BundleName,
		MetaRows: meta,
		Parties: &[2]partyCard{
			{Title: "BUNDLE", Name: doc.BundleName},
			{Title: "SOLD AS", Name: ifEmpty(doc.ItemName, doc.BundleName), Lines: nonEmpty([]string{doc.ItemSKU})},
		},
		Lead:     "Each unit of this bundle is made up of the following components:",
		Numbered: true,
		Columns: []docColumn{
			{Title: "COMPONENT"},
			{Title: "KIND", Width: 40},
			{Title: "QTY", Width: 20, Right: true},
			{Title: "UNIT", Width: 24},
		},
		Rows:       rows,
		NotesTitle: "NOTES",
		Notes:      doc.Notes,
		// A spec sheet is a reference document, not a transaction — it needs one preparer line
		// for whoever compiled it, not a two-party sign-off.
		LeftSigLabel: "Prepared By",
		Disclaimer: "This bundle specification is a reference sheet issued by " +
			ifEmpty(doc.Branding.CompanyName, "the issuer") + " and is not a priced offer.",
		ErrLabel: "bundle spec",
	}
}

// humanizeConst turns a stored SCREAMING_SNAKE or snake_case enum value into "Screaming Snake"
// title case for display.
func humanizeConst(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "_", " "))
	if s == "" {
		return ""
	}
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
