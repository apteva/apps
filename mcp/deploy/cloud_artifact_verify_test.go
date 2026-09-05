package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAndroidCloudBuildRejectsStaleUnsignedAdapterArtifact(t *testing.T) {
	app, deployment, platform, input, _ := newAndroidCloudSigningFixture(t)
	bundle := filepath.Join(t.TempDir(), "app.aab")
	writeUnsignedTestAAB(t, bundle)
	platform.artifactURL = serveCloudArtifactArchive(t, bundle, artifactManifest{
		Platform: "android", Primary: "app.aab",
		CertificateSHA256: input.CertificateSHA256,
	})

	build, err := app.submitCloudBuild(context.Background(), deployment)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.syncCloudBuild(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetBuild(globalCtx.AppDB(), build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "failed" || !strings.Contains(fresh.Error, "build adapter is stale or incompatible") {
		t.Fatalf("stale unsigned artifact was not rejected: %+v", fresh)
	}
}

func TestAndroidCloudBuildVerifiesManagedSignerBeforeSuccess(t *testing.T) {
	app, deployment, platform, input, payload := newAndroidCloudSigningFixture(t)
	privateKey, certificate := decodeGeneratedAndroidIdentity(t, payload)
	bundle := filepath.Join(t.TempDir(), "app.aab")
	writeTestSignedAAB(t, bundle, privateKey, certificate, testAndroidManifest("com.example.android", "1.0", "1"), testAndroidManifest("com.example.android", "1.0", "1"))
	platform.artifactURL = serveCloudArtifactArchive(t, bundle, artifactManifest{
		Platform: "android", Primary: "app.aab",
		SigningContract: mobileSigningArtifactContractVersion,
		SigningVerified: true, CertificateSHA256: input.CertificateSHA256,
	})

	build, err := app.submitCloudBuild(context.Background(), deployment)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.syncCloudBuild(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetBuild(globalCtx.AppDB(), build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "succeeded" {
		t.Fatalf("verified artifact did not succeed: %+v", fresh)
	}
	var manifest artifactManifest
	if err := json.Unmarshal([]byte(fresh.ArtifactManifestJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.SigningVerified || manifest.SigningContract != mobileSigningArtifactContractVersion ||
		normalizeCertificateFingerprint(manifest.CertificateSHA256) != normalizeCertificateFingerprint(input.CertificateSHA256) {
		t.Fatalf("verified manifest=%+v", manifest)
	}
}

func newAndroidCloudSigningFixture(
	t *testing.T,
) (*App, *Deployment, *cloudBuildPlatform, mobileSigningIdentityInput, mobileSigningSecretPayload) {
	t.Helper()
	platform := &cloudBuildPlatform{provider: "codemagic"}
	ctx := withCloudBuildContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-signing", TargetKind: "android", SourceKind: "code", SourceRef: "repo-1",
		Framework: "android", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"cm-app","workflow_id":"apteva-mobile-capsule","branch":"main","artifact_mode":"file","artifact_file":"app.aab"}`,
		TargetConfigJSON: `{"package_name":"com.example.android","version_name":"1.0","version_code":"1"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, environment)
	app := &App{dataDir: t.TempDir(), retainRollbacks: 3}
	t.Setenv(mobileSigningVaultKeyFileEnv, filepath.Join(app.dataDir, "vault-key"))
	input, payload, err := generateAndroidSigningIdentity("p1", "com.example.android")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.createMobileSigningIdentity(ctx.AppDB(), input, payload); err != nil {
		t.Fatal(err)
	}
	return app, d, platform, input, payload
}

func writeUnsignedTestAAB(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("base/manifest/AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write(testAndroidManifest("com.example.android", "1.0", "1"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func serveCloudArtifactArchive(t *testing.T, bundlePath string, manifest artifactManifest) string {
	t.Helper()
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, body := range map[string][]byte{
		"app.aab": bundle, artifactManifestFilename: manifestJSON,
	} {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write(body); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/zip")
		_, _ = response.Write(archive.Bytes())
	}))
	t.Cleanup(server.Close)
	return server.URL
}
