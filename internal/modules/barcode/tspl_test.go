package barcode

import (
	"strconv"
	"strings"
	"testing"
)

func TestRenderTSPL_RotationParam(t *testing.T) {
	tmpl := LabelTemplateByName("4x2")

	tmpl.Rotate = false
	out := renderTSPL(sampleLabels(1), tmpl)
	if strings.Contains(out, `,90,`) {
		t.Fatalf("Rotate=false should not emit a 90 rotation param:\n%s", out)
	}

	tmpl.Rotate = true
	out = renderTSPL(sampleLabels(1), tmpl)
	if !strings.Contains(out, `,90,`) {
		t.Fatalf("Rotate=true should emit a 90 rotation param on TEXT/BARCODE commands:\n%s", out)
	}
}

func TestRenderTSPL_SizeMatchesRollWidth(t *testing.T) {
	tmpl := LabelTemplateByName("2row_38x30")
	out := renderTSPL(sampleLabels(2), tmpl)

	wantW := strconv.FormatFloat(tmpl.RollWidthIn()*25.4, 'f', 2, 64)
	if !strings.Contains(out, "SIZE "+wantW+" mm,") {
		t.Fatalf("SIZE should use the full roll width (%s mm) for a 2-lane template:\n%s", wantW, out)
	}
	if got := strings.Count(out, "CLS"); got != 1 {
		t.Fatalf("2 labels at 2 lanes should be exactly one CLS/PRINT block (one feed-row), got %d", got)
	}
	if got := strings.Count(out, "BARCODE"); got != 2 {
		t.Fatalf("expected 2 barcode commands (one per lane), got %d", got)
	}
}

func TestRenderTSPL_GapMatchesOneLabelBoundary(t *testing.T) {
	// The GAP value must reflect ONE label's real gap, not something derived from label count
	// or roll width — this is what stops a single barcode's content from bleeding across
	// several physical labels (SIZE height + GAP must match the die-cut boundary exactly).
	tmpl := LabelTemplateByName("4x2")
	out := renderTSPL(sampleLabels(1), tmpl)
	wantGap := strconv.FormatFloat(tmpl.GapYIn*25.4, 'f', 2, 64)
	if !strings.Contains(out, "GAP "+wantGap+" mm,0 mm") {
		t.Fatalf("GAP should be %s mm (one label's real gap):\n%s", wantGap, out)
	}
}
