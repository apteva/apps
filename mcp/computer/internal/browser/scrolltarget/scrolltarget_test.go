package scrolltarget

import (
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestMovementReportsActualAndNoMovement(t *testing.T) {
	before := []computer.ScrollRegion{
		{ID: "scroll_child", ScrollTop: 100, MaxScrollY: 100, CanScrollY: true},
		{ID: "scroll_parent", ScrollTop: 0, MaxScrollY: 500, CanScrollY: true},
	}
	after := []computer.ScrollRegion{
		{ID: "scroll_child", ScrollTop: 100, MaxScrollY: 100, CanScrollY: true},
		{ID: "scroll_parent", ScrollTop: 240, MaxScrollY: 500, CanScrollY: true},
	}
	got := movement(before, after, "scroll_child")
	if !got.Moved || got.ActualTargetID != "scroll_parent" || got.DeltaY != 240 {
		t.Fatalf("movement=%+v", got)
	}
	got.WrongTarget = got.ActualTargetID != "scroll_child"
	if !got.WrongTarget {
		t.Fatalf("wrong target not detected: %+v", got)
	}

	still := movement(after, after, "scroll_child")
	if still.Moved || !still.AtEnd {
		t.Fatalf("boundary no-movement=%+v", still)
	}
}
