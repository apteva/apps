package api

import "testing"

func TestScrollDeltaUsesPixels(t *testing.T) {
	dx, dy, err := ScrollDelta("down", 240)
	if err != nil {
		t.Fatal(err)
	}
	if dx != 0 || dy != 240 {
		t.Fatalf("down 240 = (%v,%v), want (0,240)", dx, dy)
	}

	dx, dy, err = ScrollDelta("up", 80)
	if err != nil {
		t.Fatal(err)
	}
	if dx != 0 || dy != -80 {
		t.Fatalf("up 80 = (%v,%v), want (0,-80)", dx, dy)
	}
}

func TestScrollDeltaDefault(t *testing.T) {
	_, dy, err := ScrollDelta("down", 0)
	if err != nil {
		t.Fatal(err)
	}
	if dy != 300 {
		t.Fatalf("default scroll = %v, want 300", dy)
	}
}

func TestScrollDeltaRejectsUnknownDirection(t *testing.T) {
	if _, _, err := ScrollDelta("diagonal", 100); err == nil {
		t.Fatal("expected error for unknown direction")
	}
}
