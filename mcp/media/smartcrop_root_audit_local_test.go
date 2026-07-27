package main

import (
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestSmartCropRootAuditFrames is the private-media gate built from the random
// /hgv and /monika production audit. The files remain outside the repository;
// set SMARTCROP_ROOT_AUDIT_DIR to the audit directory containing landscapes/.
func TestSmartCropRootAuditFrames(t *testing.T) {
	root := os.Getenv("SMARTCROP_ROOT_AUDIT_DIR")
	if root == "" {
		t.Skip("set SMARTCROP_ROOT_AUDIT_DIR to run the private root-folder audit")
	}
	fixtures := []struct {
		name       string
		file       string
		minX, maxX int
	}{
		{name: "Ashley bowed seated subject", file: "69181.png", minX: 850, maxX: 1100},
		{name: "Monika hand-covered reclining face", file: "20700.png", minX: 300, maxX: 450},
		{name: "Monika crouched right-edge subject", file: "26391.png", minX: 900, maxX: 1050},
		{name: "Jen floor recline", file: "47114.png", minX: 950, maxX: 1150},
		{name: "Monika right-edge recline", file: "13141.png", minX: 1200, maxX: 1314},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			file, err := os.Open(filepath.Join(root, "landscapes", fixture.file))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			img, _, err := image.Decode(file)
			if err != nil {
				t.Fatal(err)
			}
			window, _, err := analyzeSmartCropV2FrameDetailed(1920, 1080, 9, 16, img)
			if err != nil {
				t.Fatal(err)
			}
			if window.X < fixture.minX || window.X > fixture.maxX {
				t.Fatalf("crop_x=%d want [%d,%d]", window.X, fixture.minX, fixture.maxX)
			}
		})
	}
}
