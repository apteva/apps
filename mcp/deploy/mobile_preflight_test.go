package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
