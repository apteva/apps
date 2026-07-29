package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"howett.net/plist"
)

func TestIOSSourcePreflightRejectsMissingStoreMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "project.yml"), []byte(`
name: Example
targets:
  Example:
    type: application
    platform: iOS
`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Deployment{
		TargetKind: "ios", Framework: "ios",
		TargetConfigJSON: `{"bundle_id":"com.example.app","version_name":"1.0","build_number":"1"}`,
	}
	err := validateMobileSource(root, d, cloudBuildConfig{ArtifactMode: "store_upload"})
	if err == nil || !strings.Contains(err.Error(), ".appiconset") {
		t.Fatalf("error=%v", err)
	}
}

func TestIOSSourcePreflightAcceptsAppIconAndOrientations(t *testing.T) {
	root := t.TempDir()
	iconDir := filepath.Join(root, "Example", "Assets.xcassets", "AppIcon.appiconset")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iconDir, "AppIcon.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iconDir, "Contents.json"), []byte(`{
  "images":[{"filename":"AppIcon.png","idiom":"universal","platform":"ios","size":"1024x1024"}],
  "info":{"author":"xcode","version":1}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "project.yml"), []byte(`
name: Example
targets:
  Example:
    type: application
    platform: iOS
    settings:
      base:
        INFOPLIST_KEY_UISupportedInterfaceOrientations: UIInterfaceOrientationPortrait
`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Deployment{
		TargetKind: "ios", Framework: "ios",
		TargetConfigJSON: `{"bundle_id":"com.example.app","version_name":"1.0","build_number":"1"}`,
	}
	if err := validateMobileSource(root, d, cloudBuildConfig{ArtifactMode: "store_upload"}); err != nil {
		t.Fatal(err)
	}
}

func TestIOSSourcePreflightAcceptsXcodeProjectExplicitInfoPlist(t *testing.T) {
	root, d := newIOSPreflightFixture(t)
	projectDir := filepath.Join(root, "Example.xcodeproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(projectDir, "project.pbxproj"), `
		buildSettings = {
			INFOPLIST_FILE = Example/Info.plist;
		};
`)
	writeIOSPlist(t, filepath.Join(root, "Example", "Info.plist"), plist.XMLFormat, map[string]any{
		"UISupportedInterfaceOrientations": []string{"UIInterfaceOrientationPortrait"},
	})

	if err := validateMobileSource(root, d, cloudBuildConfig{ArtifactMode: "store_upload"}); err != nil {
		t.Fatal(err)
	}
}

func TestIOSSourcePreflightAcceptsProjectYAMLExplicitInfoPlist(t *testing.T) {
	root, d := newIOSPreflightFixture(t)
	writeTestFile(t, filepath.Join(root, "project.yml"), `
name: Example
targets:
  Example:
    type: application
    platform: iOS
    settings:
      base:
        INFOPLIST_FILE: $(SRCROOT)/Example/Info.plist
`)
	writeIOSPlist(t, filepath.Join(root, "Example", "Info.plist"), plist.XMLFormat, map[string]any{
		"UISupportedInterfaceOrientations~ipad": []string{"UIInterfaceOrientationLandscapeLeft"},
	})

	if err := validateMobileSource(root, d, cloudBuildConfig{ArtifactMode: "store_upload"}); err != nil {
		t.Fatal(err)
	}
}

func TestIOSSourcePreflightRecognizesQualifiedOrientationKeys(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "plist iPhone", key: "UISupportedInterfaceOrientations~iphone", value: "UIInterfaceOrientationPortrait"},
		{name: "plist iPad", key: "UISupportedInterfaceOrientations~ipad", value: "UIInterfaceOrientationLandscapeRight"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeIOSPlist(t, filepath.Join(root, "Info.plist"), plist.XMLFormat, map[string]any{
				test.key: []string{test.value},
			})
			ok, err := hasIOSSupportedOrientations(root)
			if err != nil || !ok {
				t.Fatalf("orientations=%v err=%v", ok, err)
			}
		})
	}

	for _, key := range []string{
		"INFOPLIST_KEY_UISupportedInterfaceOrientations_iPhone",
		"INFOPLIST_KEY_UISupportedInterfaceOrientations_iPad",
	} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "project.yml"), "settings:\n  "+key+": UIInterfaceOrientationPortrait\n")
			ok, err := hasIOSSupportedOrientations(root)
			if err != nil || !ok {
				t.Fatalf("orientations=%v err=%v", ok, err)
			}
		})
	}
}

func TestIOSSourcePreflightRejectsEmptyOrMissingOrientations(t *testing.T) {
	for _, test := range []struct {
		name string
		body map[string]any
	}{
		{name: "empty array", body: map[string]any{"UISupportedInterfaceOrientations": []string{}}},
		{name: "missing key", body: map[string]any{"CFBundleDisplayName": "Example"}},
		{name: "invalid value", body: map[string]any{"UISupportedInterfaceOrientations": []string{"portrait"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeIOSPlist(t, filepath.Join(root, "Info.plist"), plist.XMLFormat, test.body)
			ok, err := hasIOSSupportedOrientations(root)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Fatal("expected orientations to be rejected")
			}
		})
	}
}

func TestIOSSourcePreflightIgnoresMalformedUnrelatedPlist(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "project.yml"), `
settings:
  INFOPLIST_FILE: ${PROJECT_DIR}/Example/Info.plist
`)
	writeIOSPlist(t, filepath.Join(root, "Example", "Info.plist"), plist.XMLFormat, map[string]any{
		"UISupportedInterfaceOrientations": []string{"UIInterfaceOrientationPortraitUpsideDown"},
	})
	writeTestFile(t, filepath.Join(root, "Broken.plist"), "not a property list")

	ok, err := hasIOSSupportedOrientations(root)
	if err != nil || !ok {
		t.Fatalf("orientations=%v err=%v", ok, err)
	}
}

func TestIOSSourcePreflightIgnoresGeneratedDirectories(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "project.yml"), "name: Example\n")
	for _, dir := range []string{"build", "DerivedData", ".build", "Pods", "node_modules"} {
		writeIOSPlist(t, filepath.Join(root, dir, "Info.plist"), plist.XMLFormat, map[string]any{
			"UISupportedInterfaceOrientations": []string{"UIInterfaceOrientationPortrait"},
		})
	}

	ok, err := hasIOSSupportedOrientations(root)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("generated plist must not satisfy source preflight")
	}
}

func TestIOSSourcePreflightAcceptsBinaryPlist(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "project.yml"), `
settings:
  INFOPLIST_FILE: Example/Info.plist
`)
	writeIOSPlist(t, filepath.Join(root, "Example", "Info.plist"), plist.BinaryFormat, map[string]any{
		"UISupportedInterfaceOrientations": []string{"UIInterfaceOrientationLandscapeLeft"},
	})

	ok, err := hasIOSSupportedOrientations(root)
	if err != nil || !ok {
		t.Fatalf("orientations=%v err=%v", ok, err)
	}
}

func TestIOSSourcePreflightRejectsEmptyGeneratedOrientationSetting(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "project.yml"), `
settings:
  INFOPLIST_KEY_UISupportedInterfaceOrientations: ""
`)
	ok, err := hasIOSSupportedOrientations(root)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("empty generated orientation setting must be rejected")
	}
}

func TestAndroidSourcePreflightValidatesStoreIdentity(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "app", "src", "main")
	iconDir := filepath.Join(mainDir, "res", "mipmap-anydpi-v26")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(root, "settings.gradle.kts"):    `include(":app")`,
		filepath.Join(mainDir, "AndroidManifest.xml"): `<manifest xmlns:android="http://schemas.android.com/apk/res/android"><application android:icon="@mipmap/ic_launcher"/></manifest>`,
		filepath.Join(iconDir, "ic_launcher.xml"):     `<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android"/>`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := &Deployment{
		TargetKind: "android", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.app","version_name":"1.0","version_code":"7"}`,
	}
	if err := validateMobileSource(root, d, cloudBuildConfig{ArtifactMode: "store_upload"}); err != nil {
		t.Fatal(err)
	}
	d.TargetConfigJSON = `{"package_name":"com.example.app"}`
	err := validateMobileSource(root, d, cloudBuildConfig{ArtifactMode: "store_upload"})
	if err == nil || !strings.Contains(err.Error(), "version_code") {
		t.Fatalf("error=%v", err)
	}
}

func newIOSPreflightFixture(t *testing.T) (string, *Deployment) {
	t.Helper()
	root := t.TempDir()
	iconDir := filepath.Join(root, "Example", "Assets.xcassets", "AppIcon.appiconset")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(iconDir, "AppIcon.png"), "png")
	writeTestFile(t, filepath.Join(iconDir, "Contents.json"), `{
  "images":[{"filename":"AppIcon.png","idiom":"universal","platform":"ios","size":"1024x1024"}],
  "info":{"author":"xcode","version":1}
}`)
	return root, &Deployment{
		TargetKind: "ios", Framework: "ios",
		TargetConfigJSON: `{"bundle_id":"com.example.app","version_name":"1.0","build_number":"1"}`,
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeIOSPlist(t *testing.T, path string, format int, value map[string]any) {
	t.Helper()
	body, err := plist.Marshal(value, format)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
