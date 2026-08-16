package render

// drawParties renders the purchase order's supplier (left) and deliver-to (right) cards. The
// cards flex to fit their wrapped content (long supplier addresses wrap inside the card instead
// of overflowing) and share a common height so the two columns stay aligned, matching the invoice
// layout — all of which is now the generic drawPartyCards in common.go; this is the PO-typed
// wrapper over it.
func (p *painter) drawParties(d *PurchaseOrderDoc, py float64) float64 {
	right := partyCard{Title: "DELIVER TO", Name: d.WarehouseName}
	if d.ExpectedDate != "" {
		right.Lines = []string{"Expected: " + d.ExpectedDate}
	}
	return p.drawPartyCards(py,
		partyCard{Title: "SUPPLIER", Name: d.SupplierName, Lines: d.SupplierAddr},
		right,
	)
}
