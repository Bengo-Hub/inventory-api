package pdfcolor

import "testing"

// TestTextSafeDarken_WhiteBrandColor locks in the fix for the reported bug: a tenant whose
// primary brand color is white (or near-white — a completely normal on-screen UI theming choice)
// produced an unreadable printed delivery note, because plain Darken(white, 0.45) only reaches
// (140,140,140) — a mid-gray with enough contrast on a backlit screen but not on paper.
func TestTextSafeDarken_WhiteBrandColor(t *testing.T) {
	r, g, b := Darken(255, 255, 255, 0.45)
	if r != 140 || g != 140 || b != 140 {
		t.Fatalf("Darken(white, 0.45) = (%d,%d,%d), want (140,140,140) — sanity check for the premise below", r, g, b)
	}

	r, g, b = TextSafeDarken(255, 255, 255, 0.45, 90)
	if r > 90 || g > 90 || b > 90 {
		t.Fatalf("TextSafeDarken(white, 0.45, floor=90) = (%d,%d,%d), want every channel <= 90", r, g, b)
	}
	if r != g || g != b {
		t.Fatalf("TextSafeDarken(white, ...) = (%d,%d,%d), want a neutral gray (equal channels) for a neutral input", r, g, b)
	}
}

// TestTextSafeDarken_PreservesHueBelowFloor checks that a color already dark enough passes
// through Darken unchanged — TextSafeDarken must not needlessly flatten a tenant's genuine dark
// brand color toward gray.
func TestTextSafeDarken_PreservesHueBelowFloor(t *testing.T) {
	wantR, wantG, wantB := Darken(10, 40, 70, 0.45)
	gotR, gotG, gotB := TextSafeDarken(10, 40, 70, 0.45, 90)
	if gotR != wantR || gotG != wantG || gotB != wantB {
		t.Fatalf("TextSafeDarken(10,40,70, 0.45, 90) = (%d,%d,%d), want unchanged Darken result (%d,%d,%d)", gotR, gotG, gotB, wantR, wantG, wantB)
	}
}

// TestTextSafeDarken_NoDarkenFactor covers the pal.blue use case (f=0: no proportional darken
// step, floor-clamp only) against a light pastel primary color.
func TestTextSafeDarken_NoDarkenFactor(t *testing.T) {
	r, g, b := TextSafeDarken(255, 200, 210, 0, 150)
	if r > 150 || g > 150 || b > 150 {
		t.Fatalf("TextSafeDarken(pastel, f=0, floor=150) = (%d,%d,%d), want every channel <= 150", r, g, b)
	}
}
