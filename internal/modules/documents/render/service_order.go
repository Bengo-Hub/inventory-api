package render

// Service / repair order rendering: the same PurchaseReturnDoc input as the supplier debit note,
// presented as the paper that travels with goods handed to a repair provider. It claims nothing
// from anyone; it records what left, why, and when it is expected back.

import "strings"

func serviceOrderSimpleDoc(doc *PurchaseReturnDoc) simpleDoc {
	cur := currencyCode(doc.Currency)
	valueTitle := "COST VALUE"
	if cur != "" {
		valueTitle = "COST VALUE (" + cur + ")"
	}

	rows := make([]docRow, 0, len(doc.Items))
	for _, it := range doc.Items {
		rows = append(rows, docRow{
			SubDesc: it.SubDesc,
			Cells:   []string{it.Desc, it.Qty, it.UnitPrice, it.Amount},
		})
	}

	meta := [][2]string{
		{"Order No.", orDash(doc.ReturnNumber)},
		{"Date", orDash(doc.Date)},
	}
	if strings.TrimSpace(doc.ExpectedBack) != "" {
		meta = append(meta, [2]string{"Expected Back", doc.ExpectedBack})
	}
	if strings.TrimSpace(doc.Location) != "" {
		meta = append(meta, [2]string{"Location", doc.Location})
	}
	if strings.TrimSpace(doc.ServiceStatus) != "" {
		meta = append(meta, [2]string{"Status", doc.ServiceStatus})
	}

	providerLines := []string{}
	if c := strings.TrimSpace(doc.ServiceContact); c != "" {
		providerLines = append(providerLines, c)
	}

	var notes []string
	if f := strings.TrimSpace(doc.Fault); f != "" {
		notes = append(notes, "Reported fault: "+f)
	}
	if r := strings.TrimSpace(doc.Reason); r != "" {
		notes = append(notes, "Notes: "+r)
	}
	notes = append(notes, doc.Notes...)

	return simpleDoc{
		Branding: doc.Branding,
		Title:    "Service / Repair Order",
		Subtitle: "Items sent for repair or service",
		MetaRows: meta,
		Parties: &[2]partyCard{
			{Title: "SERVICE PROVIDER", Name: doc.ServiceProvider, Lines: providerLines},
			{Title: "SENT BY", Name: doc.Branding.CompanyName, Lines: companyAddressLines(doc.Branding)},
		},
		Lead:     "The items listed below have been handed to you for repair or service. They remain our property and are expected back once repaired.",
		Numbered: true,
		Columns: []docColumn{
			{Title: "DESCRIPTION"},
			{Title: "QTY", Width: 20, Right: true},
			{Title: "UNIT COST", Width: 30, Right: true},
			{Title: valueTitle, Width: 34, Right: true},
		},
		Rows: rows,
		Totals: []totalRow{
			{Label: "Total Cost Value", Value: moneyOrEmpty(doc.Currency, doc.ReturnAmount), Grand: true},
		},
		NotesTitle:    "NOTES",
		Notes:         notes,
		LeftSigLabel:  "Sent By",
		LeftSigName:   doc.PreparedBy,
		RightSigLabel: "Received By (Provider)",
		RightSigName:  "",
		Disclaimer: "These items remain the property of " + ifEmpty(doc.Branding.CompanyName, "the issuer") +
			" and must be returned, repaired or reported as unrepairable.",
		ErrLabel: "service order",
	}
}
