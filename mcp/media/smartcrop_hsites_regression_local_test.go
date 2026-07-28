package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHSitesRecliningAndStaticReelLocalRegression covers three production
// findings that predate this release: two reclining portrait frames and one
// static reel whose old crop followed bright curtains instead of the person.
// The source excerpts remain external because they contain private media.
func TestHSitesRecliningAndStaticReelLocalRegression(t *testing.T) {
	root := os.Getenv("HSITES_SMARTCROP_FIXTURE_DIR")
	if root == "" {
		t.Skip("set HSITES_SMARTCROP_FIXTURE_DIR")
	}

	stills := []struct {
		name       string
		file       string
		focus      int64
		minX, maxX int
	}{
		{name: "14140", file: "portrait-14140-context.mp4", focus: 21_000, minX: 180, maxX: 300},
		{name: "20521", file: "portrait-20521-context.mp4", focus: 21_000, minX: 350, maxX: 500},
	}
	for _, fixture := range stills {
		fixture := fixture
		t.Run("portrait-"+fixture.name, func(t *testing.T) {
			x := localSmartCropStillX(t, filepath.Join(root, fixture.file),
				42_000, fixture.focus, 1920, 1080)
			if x < fixture.minX || x > fixture.maxX {
				t.Fatalf("crop x=%d want [%d,%d]", x, fixture.minX, fixture.maxX)
			}
		})
	}

	t.Run("reel-7605", func(t *testing.T) {
		const duration = int64(26_640)
		path := localSmartCropReelPath(t, filepath.Join(root, "reel-7605-range.mp4"),
			duration, 0, duration, 1280, 720)
		if len(path) == 0 {
			t.Fatal("empty crop path")
		}
		for _, point := range path {
			if point.X < 350 || point.X > 550 {
				t.Fatalf("person leaves crop at %dms: x=%d path=%v", point.AtMs, point.X, path)
			}
		}
	})
}
