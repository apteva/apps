package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestAppleBuildNumbersAreReservedPerPlatform(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	for _, kind := range []string{"ios", "macos"} {
		d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{Name: kind, TargetKind: kind, SourceKind: "local", SourceRef: "/src", Framework: kind})
		if err != nil {
			t.Fatal(err)
		}
		env, err := dbEnsureProductionEnvironment(db, d)
		if err != nil {
			t.Fatal(err)
		}
		build, err := dbCreateBuildForEnv(db, d.ID, env.ID, kind, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := dbReserveMobileVersion(db, d, build.ID, mobileVersionAllocation{
			Platform: kind, Provider: "app_store_connect", AppKey: "shared-app-id", VersionName: "1.0",
		}, 4)
		if err != nil || got != 5 {
			t.Fatalf("%s: next=%d err=%v", kind, got, err)
		}
	}
}

func TestMacMigrationPreservesSigningRevisions(t *testing.T) {
	db := openSchemaDBThrough(t, len(testMigrationFiles)-1)
	defer db.Close()
	_, err := db.Exec(`INSERT INTO mobile_signing_identities
		(project_id,platform,application_identifier,format,encrypted_payload,source,created_at,updated_at)
		VALUES ('p1','ios','com.example.app','pem',X'01','generated','now','now')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO signing_identity_revisions
		(identity_id,revision,identity_json,encrypted_payload,created_at)
		VALUES (1,1,'{}',X'02','now')`)
	if err != nil {
		t.Fatal(err)
	}
	applyTestMigration(t, db, "migrations/014_macos_platform.sql")
	var identities, revisions int
	if err := db.QueryRow(`SELECT count(*) FROM mobile_signing_identities`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM signing_identity_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if identities != 1 || revisions != 1 {
		t.Fatalf("migration lost signing data: identities=%d revisions=%d", identities, revisions)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left broken foreign keys")
	}
}

func TestMacPlatformContract(t *testing.T) {
	platform, ok := appPlatformFor("macos")
	if !ok || platform.ApplePlatform != "MAC_OS" || platform.ArtifactExt != ".pkg" || platform.Profile != "MAC_APP_STORE" {
		t.Fatalf("macOS contract: %+v", platform)
	}
	if _, err := builderFor("macos"); err != nil {
		t.Fatal(err)
	}
	if channel, err := normalizeMobileChannel("macos", "production"); err != nil || channel != "production" {
		t.Fatalf("channel=%s err=%v", channel, err)
	}
	if !mediaKindSupported("macos", "desktop_screenshot") || mediaKindSupported("macos", "phone_screenshot") {
		t.Fatal("wrong macOS media kinds")
	}
	if !appleScreenshotSizeAllowed("APP_DESKTOP", 2880, 1800) || appleScreenshotSizeAllowed("APP_DESKTOP", 1290, 2796) {
		t.Fatal("wrong Mac screenshot sizes")
	}
	if mode := resolvedCloudArtifactMode(cloudBuildConfig{SourceMode: "bundle"}, &Deployment{TargetKind: "macos"}); mode != "store_upload" {
		t.Fatalf("macOS capsule build needs a runner-side App Store upload, got %q", mode)
	}
	if mode := resolvedCloudArtifactMode(cloudBuildConfig{SourceMode: "bundle"}, &Deployment{TargetKind: "ios"}); mode != "file" {
		t.Fatalf("iOS capsule build mode changed: %q", mode)
	}
}

func TestMacCloudArtifactRejectsOtherPlatformBeforeInspectingPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.pkg"), []byte("not a package"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeArtifactManifest(root, artifactManifest{Platform: "ios", Primary: "app.pkg"}); err != nil {
		t.Fatal(err)
	}
	err := (&App{}).verifyStagedCloudMobileArtifact(&Deployment{TargetKind: "macos"}, nil, root)
	if err == nil || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("cross-platform package accepted: %v", err)
	}
}

type dependencyExportPlatform struct {
	tk.BasePlatformClient
	archives map[string][]byte
}

func (p *dependencyExportPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	slug, _ := args["slug"].(string)
	archive := p.archives[slug]
	sum := sha256.Sum256(archive)
	value := sourceReceipt{SHA256: hex.EncodeToString(sum[:]), ZipB64: base64.StdEncoding.EncodeToString(archive)}
	body, _ := json.Marshal(value)
	return json.Unmarshal(body, out)
}

func codeArchive(t *testing.T, name, contents string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	f, err := w.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestCodeDependencySnapshotsPreserveSiblingLayout(t *testing.T) {
	p := &dependencyExportPlatform{archives: map[string][]byte{
		"apple-app":  codeArchive(t, "project.yml", "packages:\n  SDK:\n    path: ../apteva-client-sdk\n"),
		"client-sdk": codeArchive(t, "Package.swift", "// pinned SDK snapshot"),
	}}
	d := &Deployment{ProjectID: "p1", SourceKind: "code", SourceRef: "apple-app", SourceExtraJSON: `{"dependencies":[{"slug":"client-sdk","path":"apteva-client-sdk"}]}`}
	root := t.TempDir()
	if err := fetchCodeSourceWithDependencies(d, root, sourceConfig{CacheDir: t.TempDir()}, p); err != nil {
		t.Fatal(err)
	}
	app, err := sourceBuildRoot(d, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(app, "project.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(app, "..", "apteva-client-sdk", "Package.swift")); err != nil {
		t.Fatal(err)
	}
	capsulePath := filepath.Join(t.TempDir(), "source.zip")
	if _, _, err := writeSourceCapsule(root, capsulePath); err != nil {
		t.Fatal(err)
	}
	capsule, err := zip.OpenReader(capsulePath)
	if err != nil {
		t.Fatal(err)
	}
	defer capsule.Close()
	found := map[string]bool{}
	for _, file := range capsule.File {
		found[file.Name] = true
	}
	if !found["app/project.yml"] || !found["apteva-client-sdk/Package.swift"] {
		t.Fatalf("capsule lost sibling sources: %#v", found)
	}
	contract, err := cloudBuildContractVariables(cloudBuildConfig{SourceMode: "bundle"}, d, &Build{ID: 7}, &sourceCapsule{URL: "https://example.test/source", SHA256: "digest", Size: 1, Format: "zip-v1"})
	if err != nil || contract["APTEVA_SOURCE_BUILD_SUBDIR"] != "app" {
		t.Fatalf("remote source layout: subdir=%q err=%v", contract["APTEVA_SOURCE_BUILD_SUBDIR"], err)
	}
	if _, err := sourceBuildSubdir(&Deployment{SourceKind: "code", SourceExtraJSON: `{"dependencies":[{"slug":"x","path":"../escape"}]}`}); err == nil {
		t.Fatal("accepted escaping dependency path")
	}
}
