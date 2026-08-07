package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestAppleStoreAssetsValidateDimensionsAndRequiredDeviceFamilies(t *testing.T) {
	root := t.TempDir()
	d := &Deployment{ID: 1, EnvironmentID: 2, TargetKind: "ios", TargetConfigJSON: `{"device_families":["iphone","ipad"]}`}
	phone := writeTestStorePNG(t, root, d, "phone", 1290, 2796, true)
	tablet := writeTestStorePNG(t, root, d, "tablet", 2048, 2732, true)
	doc := StoreDocument{
		DefaultLocale: "en-US", Localizations: map[string]StoreLocalization{"en-US": {}},
		Assets: []StoreAsset{
			{ID: "phone", Locale: "en-US", Kind: "phone_screenshot", DisplayTarget: "APP_IPHONE_67", Path: phone},
			{ID: "tablet", Locale: "en-US", Kind: "tablet_screenshot", DisplayTarget: "APP_IPAD_PRO_13", Path: tablet},
		},
	}
	if findings := validateStoreAssets(root, d, nil, doc); len(findings) != 0 {
		t.Fatalf("valid Apple assets rejected: %#v", findings)
	}
	doc.Assets = doc.Assets[:1]
	if findings := validateStoreAssets(root, d, nil, doc); !storeFindingsContain(findings, "screenshots.ipad_required") {
		t.Fatalf("missing iPad requirement: %#v", findings)
	}
}

func TestGoogleStoreAssetsValidateRequiredShapes(t *testing.T) {
	root := t.TempDir()
	d := &Deployment{ID: 3, EnvironmentID: 4, TargetKind: "android"}
	doc := StoreDocument{
		DefaultLocale: "en-US", Localizations: map[string]StoreLocalization{"en-US": {}},
		Assets: []StoreAsset{
			{ID: "icon", Locale: "en-US", Kind: "icon", Path: writeTestStorePNG(t, root, d, "icon", 512, 512, true)},
			{ID: "feature", Locale: "en-US", Kind: "feature_graphic", Path: writeTestStorePNG(t, root, d, "feature", 1024, 500, true)},
			{ID: "shot1", Locale: "en-US", Kind: "phone_screenshot", Path: writeTestStorePNG(t, root, d, "shot1", 1080, 1920, true)},
			{ID: "shot2", Locale: "en-US", Kind: "phone_screenshot", Path: writeTestStorePNG(t, root, d, "shot2", 1080, 1920, true)},
		},
	}
	if findings := validateStoreAssets(root, d, nil, doc); len(findings) != 0 {
		t.Fatalf("valid Google assets rejected: %#v", findings)
	}
	doc.Assets[0].Path = writeTestStorePNG(t, root, d, "bad-icon", 256, 256, true)
	if findings := validateStoreAssets(root, d, nil, doc); !storeFindingsContain(findings, "asset.google_icon_dimensions") {
		t.Fatalf("invalid icon accepted: %#v", findings)
	}
}

func writeTestStorePNG(t *testing.T, root string, d *Deployment, id string, width, height int, opaque bool) string {
	t.Helper()
	rel := filepath.Join("store-assets", strconv.FormatInt(d.ID, 10), strconv.FormatInt(d.EnvironmentID, 10), id, id+".png")
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	alpha := uint8(255)
	if !opaque {
		alpha = 128
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 30, G: 60, B: 90, A: alpha})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return rel
}

func storeFindingsContain(findings []StoreFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
