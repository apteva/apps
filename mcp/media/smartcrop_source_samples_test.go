package main

import "testing"

func TestSmartCropSupplementPositionsLongSourceStill(t *testing.T) {
	const fourHours = int64(4 * 60 * 60 * 1000)
	focus := int64(3 * 60 * 60 * 1000)
	got := smartCropSupplementPositions(smartCropTarget{FocusMs: focus}, fourHours)
	if len(got) != smartCropTemporalMaxSamples {
		t.Fatalf("positions=%d want=%d: %v", len(got), smartCropTemporalMaxSamples, got)
	}
	if got[0] != focus-20_000 || got[len(got)-1] != focus+20_000 {
		t.Fatalf("still context=%v", got)
	}
	if got[len(got)/2] != focus {
		t.Fatalf("exact requested frame missing: %v", got)
	}
}

func TestSmartCropSupplementPositionsLongSourceShortReel(t *testing.T) {
	const eightHours = int64(8 * 60 * 60 * 1000)
	start := int64(6 * 60 * 60 * 1000)
	end := start + 30_000
	got := smartCropSupplementPositions(smartCropTarget{StartMs: start, EndMs: end}, eightHours)
	if len(got) != 7 {
		t.Fatalf("positions=%d want=7: %v", len(got), got)
	}
	if got[0] != start || got[len(got)-1] != end {
		t.Fatalf("reel boundaries missing: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i]-got[i-1] > smartCropSupplementIntervalMs {
			t.Fatalf("supplemental gap=%d exceeds %d: %v", got[i]-got[i-1], smartCropSupplementIntervalMs, got)
		}
	}
}

func TestSmartCropSupplementPositionsCapsLongReelWork(t *testing.T) {
	start := int64(60 * 60 * 1000)
	end := start + 10*60*1000
	got := smartCropSupplementPositions(smartCropTarget{StartMs: start, EndMs: end}, 4*60*60*1000)
	if len(got) != smartCropV2MaxSamples {
		t.Fatalf("positions=%d want=%d", len(got), smartCropV2MaxSamples)
	}
	if got[0] != start || got[len(got)-1] != end {
		t.Fatalf("capped reel boundaries missing: %v", got)
	}
}

func TestSmartCropSupplementPositionsClampAtSourceEdges(t *testing.T) {
	got := smartCropSupplementPositions(smartCropTarget{FocusMs: 2_000}, 12_000)
	if got[0] != 0 || got[len(got)-1] != 11_900 {
		t.Fatalf("edge-clamped positions=%v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("positions are not unique and sorted: %v", got)
		}
	}
}
