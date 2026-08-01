package barcode

import "testing"

func TestLabelTemplateByName_BackCompatSingleLane(t *testing.T) {
	tmpl := LabelTemplateByName("4x2")
	if tmpl.LabelWIn != 4 || tmpl.LabelHIn != 2 || tmpl.DPI != 203 || tmpl.Lanes != 1 {
		t.Fatalf("4x2 preset changed shape: %+v", tmpl)
	}
	if LabelTemplateByName("").LabelWIn != tmpl.LabelWIn {
		t.Fatalf("empty name should fall back to the 4x2 default")
	}
	if LabelTemplateByName("no-such-template").LabelWIn != tmpl.LabelWIn {
		t.Fatalf("unknown name should fall back to the 4x2 default")
	}
}

func TestLabelTemplateByName_MultiRow(t *testing.T) {
	tmpl := LabelTemplateByName("3row_25x40")
	if tmpl.Lanes != 3 {
		t.Fatalf("expected 3 lanes, got %d", tmpl.Lanes)
	}
	gotRollW := tmpl.RollWidthIn()
	wantRollW := 3*tmpl.LabelWIn + 2*tmpl.GapXIn
	if gotRollW != wantRollW {
		t.Fatalf("RollWidthIn() = %v, want %v", gotRollW, wantRollW)
	}
	// Lane offsets must be strictly increasing and spaced by one lane-width + gutter.
	step := tmpl.WidthDots() + tmpl.GapXDots()
	for lane := 0; lane < 3; lane++ {
		if got, want := tmpl.LaneXOffsetDots(lane), lane*step; got != want {
			t.Fatalf("LaneXOffsetDots(%d) = %d, want %d", lane, got, want)
		}
	}
}

func TestCustomLabelTemplate_ClampsLanes(t *testing.T) {
	if got := CustomLabelTemplate(2, 1, 0, 0, 0, false).Lanes; got != 1 {
		t.Fatalf("lanes=0 should clamp to 1, got %d", got)
	}
	if got := CustomLabelTemplate(2, 1, 9, 0, 0, false).Lanes; got != 4 {
		t.Fatalf("lanes=9 should clamp to 4, got %d", got)
	}
}
