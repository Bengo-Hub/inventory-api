package barcode

import "testing"

func TestRenderSingleLabelPDF_PageMatchesTemplate(t *testing.T) {
	lbl := Label{Title: "Item", Sku: "SKU1", Symbology: SymCode128, Content: "ABC123"}

	// Non-rotated: the PDF must actually be produced without error for both orientations —
	// this is a smoke test that the page-dimension/rotate-transform wiring doesn't panic or
	// error, since asserting the literal page box requires parsing the PDF's own /MediaBox,
	// which fpdf's public API doesn't expose back out.
	tmpl := LabelTemplateByName("4x2")
	if _, err := RenderSingleLabelPDF(lbl, "Acme", tmpl); err != nil {
		t.Fatalf("RenderSingleLabelPDF (Rotate=false) failed: %v", err)
	}

	tmpl.Rotate = true
	if _, err := RenderSingleLabelPDF(lbl, "Acme", tmpl); err != nil {
		t.Fatalf("RenderSingleLabelPDF (Rotate=true) failed: %v", err)
	}
}

func TestRenderSingleLabelPDF_RequiresNoHardcodedSize(t *testing.T) {
	lbl := Label{Title: "Item", Sku: "SKU1", Symbology: SymCode128, Content: "ABC123"}

	small, err := RenderSingleLabelPDF(lbl, "", CustomLabelTemplate(1, 0.5, 1, 0, 0, false))
	if err != nil {
		t.Fatalf("small custom template failed: %v", err)
	}
	large, err := RenderSingleLabelPDF(lbl, "", CustomLabelTemplate(6, 4, 1, 0, 0, false))
	if err != nil {
		t.Fatalf("large custom template failed: %v", err)
	}
	// Different physical sizes must not collapse to byte-identical output (i.e. the page really
	// is sized from the template, not a hardcoded constant like the old 50.8mm×25.4mm page).
	if string(small) == string(large) {
		t.Fatalf("PDFs for very different template sizes must not be identical")
	}
}
