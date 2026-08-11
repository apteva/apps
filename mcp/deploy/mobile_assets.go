package main

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"sort"
	"strings"
)

type storeAssetMetadata struct {
	MIME     string
	Format   string
	Width    int
	Height   int
	HasAlpha bool
}

func inspectStoreAssetFile(path string) (storeAssetMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return storeAssetMetadata{}, err
	}
	defer file.Close()
	header := make([]byte, 512)
	n, _ := file.Read(header)
	metadata := storeAssetMetadata{MIME: http.DetectContentType(header[:n])}
	if _, err := file.Seek(0, 0); err != nil {
		return metadata, err
	}
	img, format, err := image.Decode(file)
	if err != nil {
		return metadata, err
	}
	metadata.Format = strings.ToLower(format)
	metadata.Width = img.Bounds().Dx()
	metadata.Height = img.Bounds().Dy()
	if metadata.Format == "png" {
		bounds := img.Bounds()
		for y := bounds.Min.Y; y < bounds.Max.Y && !metadata.HasAlpha; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				_, _, _, alpha := img.At(x, y).RGBA()
				if alpha != 0xffff {
					metadata.HasAlpha = true
					break
				}
			}
		}
	}
	return metadata, nil
}

func validateStoreAssets(dataDir string, d *Deployment, build *Build, doc StoreDocument) []StoreFinding {
	var findings []StoreFinding
	add := func(mediaKind, assetID, code, locale, field, message, action string) {
		findings = append(findings, StoreFinding{
			Code: code, Severity: "error", Scope: "media", MediaKind: mediaKind, AssetID: assetID, Locale: locale, Field: field,
			Message: message, Action: action, Automatable: true,
		})
	}
	counts := map[string]int{}
	for _, asset := range doc.Assets {
		locale := defaultStr(strings.TrimSpace(asset.Locale), doc.DefaultLocale)
		if _, ok := doc.Localizations[locale]; !ok && asset.Kind != "review_attachment" {
			add(asset.Kind, asset.ID, "asset.locale_unknown", locale, "locale", "Asset references a locale that is not configured.", "Add the locale or assign the asset to an existing locale.")
		}
		path, err := resolveStoreAssetPath(dataDir, d, asset.Path)
		if err != nil {
			add(asset.Kind, asset.ID, "asset.unavailable", locale, "path", err.Error(), "Upload the asset again.")
			continue
		}
		if asset.Kind == "app_preview" || asset.Kind == "review_attachment" {
			continue
		}
		metadata, err := inspectStoreAssetFile(path)
		if err != nil {
			add(asset.Kind, asset.ID, "asset.invalid_image", locale, "path", "Asset is not a valid PNG or JPEG image.", "Upload a valid store image.")
			continue
		}
		if metadata.Format != "png" && metadata.Format != "jpeg" {
			add(asset.Kind, asset.ID, "asset.unsupported_format", locale, "path", "Store images must be PNG or JPEG.", "Convert the image to PNG or JPEG.")
			continue
		}
		counts[locale+"\x00"+asset.Kind]++
		if d.TargetKind == "ios" {
			if metadata.HasAlpha {
				add(asset.Kind, asset.ID, "asset.apple_alpha", locale, "path", "App Store screenshots cannot contain transparency.", "Export the image without an alpha channel.")
			}
			if asset.Kind == "phone_screenshot" || asset.Kind == "tablet_screenshot" {
				target := appleScreenshotDisplayTargetForSize(asset, metadata.Width, metadata.Height)
				if !appleScreenshotSizeAllowed(target, metadata.Width, metadata.Height) {
					add(asset.Kind, asset.ID, "asset.apple_dimensions", locale, "display_target",
						fmt.Sprintf("%s screenshot dimensions %d x %d do not match %s.", asset.Kind, metadata.Width, metadata.Height, target),
						"Upload an image with dimensions accepted for the selected Apple display target.")
				}
			}
		} else {
			validateGoogleAssetDimensions(asset, metadata, locale, add)
		}
	}
	defaultLocale := defaultStr(doc.DefaultLocale, "en-US")
	phoneCount := counts[defaultLocale+"\x00phone_screenshot"]
	tabletCount := counts[defaultLocale+"\x00tablet_screenshot"]
	if d.TargetKind == "ios" {
		if phoneCount == 0 {
			add("phone_screenshot", "", "screenshots.iphone_required", defaultLocale, "assets", "At least one valid iPhone screenshot is required for the default locale.", "Upload an iPhone screenshot.")
		}
		if iosRequiresIPadScreenshots(d, build) && tabletCount == 0 {
			add("tablet_screenshot", "", "screenshots.ipad_required", defaultLocale, "assets", "This binary supports iPad, so a valid 13-inch iPad screenshot is required.", "Upload a 13-inch iPad screenshot.")
		}
	} else {
		if phoneCount+tabletCount < 2 {
			add("phone_screenshot", "", "screenshots.google_minimum", defaultLocale, "assets", "Google Play requires at least two valid phone or tablet screenshots in the default locale.", "Upload at least two screenshots.")
		}
		if counts[defaultLocale+"\x00icon"] == 0 {
			add("icon", "", "icon.google_required", defaultLocale, "assets", "Google Play requires a valid 512 x 512 store icon.", "Upload a 512 x 512 store icon.")
		}
		if counts[defaultLocale+"\x00feature_graphic"] == 0 {
			add("feature_graphic", "", "feature_graphic.google_required", defaultLocale, "assets", "Google Play requires a valid 1024 x 500 feature graphic.", "Upload a 1024 x 500 feature graphic.")
		}
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Code < findings[j].Code })
	return findings
}

func validateGoogleAssetDimensions(asset StoreAsset, metadata storeAssetMetadata, locale string, add func(string, string, string, string, string, string, string)) {
	switch asset.Kind {
	case "icon":
		if metadata.Width != 512 || metadata.Height != 512 {
			add(asset.Kind, asset.ID, "asset.google_icon_dimensions", locale, "path", "Google Play icons must be exactly 512 x 512 pixels.", "Upload a 512 x 512 icon.")
		}
	case "feature_graphic":
		if metadata.Width != 1024 || metadata.Height != 500 {
			add(asset.Kind, asset.ID, "asset.google_feature_dimensions", locale, "path", "Google Play feature graphics must be exactly 1024 x 500 pixels.", "Upload a 1024 x 500 feature graphic.")
		}
	case "phone_screenshot", "tablet_screenshot", "tv_screenshot", "wear_screenshot", "automotive_screenshot":
		short, long := metadata.Width, metadata.Height
		if short > long {
			short, long = long, short
		}
		if short < 320 || long > 3840 || long > short*2 {
			add(asset.Kind, asset.ID, "asset.google_screenshot_dimensions", locale, "path",
				fmt.Sprintf("Google Play screenshot dimensions %d x %d are outside the supported range.", metadata.Width, metadata.Height),
				"Use dimensions from 320 to 3840 pixels with the long side no more than twice the short side.")
		}
	}
}

func appleScreenshotDisplayTarget(asset StoreAsset) string {
	if strings.TrimSpace(asset.DisplayTarget) != "" {
		return strings.ToUpper(strings.TrimSpace(asset.DisplayTarget))
	}
	if asset.Kind == "tablet_screenshot" {
		return "APP_IPAD_PRO_3GEN_129"
	}
	return appleScreenshotDisplayTargetForSize(asset, asset.Width, asset.Height)
}

func appleScreenshotDisplayTargetForSize(asset StoreAsset, width, height int) string {
	if strings.TrimSpace(asset.DisplayTarget) != "" {
		target := strings.ToUpper(strings.TrimSpace(asset.DisplayTarget))
		if width <= 0 || height <= 0 || appleScreenshotSizeAllowed(target, width, height) {
			return target
		}
	}
	if asset.Kind == "tablet_screenshot" {
		return "APP_IPAD_PRO_3GEN_129"
	}
	if width > height {
		width, height = height, width
	}
	for _, target := range []string{"APP_IPHONE_69", "APP_IPHONE_67", "APP_IPHONE_65", "APP_IPHONE_63", "APP_IPHONE_61", "APP_IPHONE_55", "APP_IPHONE_47"} {
		if appleScreenshotSizeAllowed(target, width, height) {
			return target
		}
	}
	return "APP_IPHONE_69"
}

func appleScreenshotSizeAllowed(target string, width, height int) bool {
	if width > height {
		width, height = height, width
	}
	target = strings.ToUpper(strings.TrimSpace(target))
	allowed := map[string][][2]int{
		"APP_IPHONE_69":         {{1260, 2736}, {1290, 2796}, {1320, 2868}},
		"APP_IPHONE_67":         {{1290, 2796}, {1284, 2778}},
		"APP_IPHONE_65":         {{1242, 2688}, {1284, 2778}},
		"APP_IPHONE_63":         {{1179, 2556}, {1206, 2622}},
		"APP_IPHONE_61":         {{1080, 2340}, {1125, 2436}, {1170, 2532}},
		"APP_IPHONE_55":         {{1242, 2208}},
		"APP_IPHONE_47":         {{750, 1334}},
		"APP_IPAD_PRO_13":       {{2048, 2732}, {2064, 2752}},
		"APP_IPAD_PRO_3GEN_129": {{2048, 2732}, {2064, 2752}},
		"APP_IPAD_PRO_129":      {{2048, 2732}},
	}
	for _, size := range allowed[target] {
		if width == size[0] && height == size[1] {
			return true
		}
	}
	return false
}

func iosRequiresIPadScreenshots(d *Deployment, build *Build) bool {
	var families []string
	if d != nil {
		if cfg, err := parseMobileTargetConfig(d.TargetConfigJSON); err == nil {
			families = append(families, cfg.DeviceFamilies...)
		}
	}
	if build != nil {
		if manifest, err := readArtifactManifest(build); err == nil {
			families = append(families, manifest.DeviceFamilies...)
		}
	}
	for _, family := range families {
		family = strings.ToLower(strings.TrimSpace(family))
		if family == "ipad" || family == "2" || family == "iphone,ipad" || family == "universal" {
			return true
		}
	}
	return false
}
