package barcode

import (
	"strconv"
	"strings"
	"testing"
)

func sampleLabels(n int) []Label {
	labels := make([]Label, n)
	for i := range labels {
		labels[i] = Label{Title: "Item", Sku: "SKU1", Symbology: SymCode128, Content: "ABC123"}
	}
	return labels
}

func TestRenderZPL_RotateEmitsFWR(t *testing.T) {
	tmpl := LabelTemplateByName("4x2")

	tmpl.Rotate = false
	out := renderZPL(sampleLabels(1), tmpl)
	if strings.Contains(out, "^FWR") {
		t.Fatalf("Rotate=false must not emit ^FWR:\n%s", out)
	}

	tmpl.Rotate = true
	out = renderZPL(sampleLabels(1), tmpl)
	if !strings.Contains(out, "^FWR") {
		t.Fatalf("Rotate=true must emit ^FWR:\n%s", out)
	}
}

func TestRenderZPL_NeverSwapsPWLL(t *testing.T) {
	tmpl := LabelTemplateByName("4x2")
	wantPW := tmpl.WidthDots() // single lane, so roll width == one label's width
	wantLL := tmpl.HeightDots()

	for _, rotate := range []bool{false, true} {
		tmpl.Rotate = rotate
		out := renderZPL(sampleLabels(1), tmpl)
		if !strings.Contains(out, "^PW"+strconv.Itoa(wantPW)) {
			t.Fatalf("rotate=%v: ^PW should always be %d regardless of Rotate:\n%s", rotate, wantPW, out)
		}
		if !strings.Contains(out, "^LL"+strconv.Itoa(wantLL)) {
			t.Fatalf("rotate=%v: ^LL should always be %d regardless of Rotate:\n%s", rotate, wantLL, out)
		}
	}
}

func TestRenderZPL_MultiLaneTilesOneBlockPerRow(t *testing.T) {
	tmpl := LabelTemplateByName("3row_25x40")
	out := renderZPL(sampleLabels(3), tmpl) // exactly one full row (3 lanes)

	if got := strings.Count(out, "^XA"); got != 1 {
		t.Fatalf("3 labels at 3 lanes should be ONE ^XA block (one feed-row), got %d", got)
	}
	wantPW := tmpl.RollWidthDots()
	if !strings.Contains(out, "^PW"+strconv.Itoa(wantPW)) {
		t.Fatalf("^PW should be the FULL roll width (%d) for a 3-lane template:\n%s", wantPW, out)
	}
	if got := strings.Count(out, "^BC"); got != 3 {
		t.Fatalf("expected 3 barcode fields (one per lane), got %d", got)
	}

	// A 4th label should start a second feed-row (second ^XA block).
	out2 := renderZPL(sampleLabels(4), tmpl)
	if got := strings.Count(out2, "^XA"); got != 2 {
		t.Fatalf("4 labels at 3 lanes should span 2 feed-rows, got %d ^XA blocks", got)
	}
}
