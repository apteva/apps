package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func signingSibling(t *testing.T, d *Deployment, project, bundle, backend, config string) *Deployment {
	t.Helper()
	next, err := dbCreateDeployment(globalCtx.AppDB(), project, CreateDeploymentInput{Name: "sibling-" + bundle, TargetKind: "ios", SourceKind: "local", SourceRef: d.SourceRef, Framework: "ios", BuildBackend: backend, BuildBackendJSON: config, TargetConfigJSON: `{"bundle_id":"` + bundle + `","scheme":"Example"}`})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := dbGetEnvironmentByName(globalCtx.AppDB(), next.ID, defaultEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	return effectiveDeploymentForEnvironment(next, environment)
}

func appleImportRequest(t *testing.T, archive []byte, preview bool) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("keystore", "recovery.zip")
	if err != nil {
		t.Fatal(err)
	}
	file.Write(archive)
	if preview {
		form.WriteField("inspect_only", "true")
	}
	form.Close()
	request := httptest.NewRequest(http.MethodPost, "/mobile-signing/import", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	return request
}

func TestAppleRecoveryImportUsesSelectedAccountAndNewProfile(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	_, original := newIOSSigningDeployment(t, platform)
	app := &App{dataDir: t.TempDir()}
	first, err := app.setupMobileSigning(t.Context(), original, "", false)
	if err != nil {
		t.Fatal(err)
	}
	archive, _, err := app.exportMobileSigningRecovery(original)
	if err != nil {
		t.Fatal(err)
	}
	target := signingSibling(t, original, original.ProjectID, "com.example.imported", "local", "{}")
	recorder := httptest.NewRecorder()
	app.httpDeploymentMobileSigningImport(recorder, appleImportRequest(t, archive, true), target)
	if recorder.Code != 200 {
		t.Fatalf("preview: %d %s", recorder.Code, recorder.Body.String())
	}
	if platform.profileSeq != 1 || strings.Contains(recorder.Body.String(), "PRIVATE KEY") {
		t.Fatal("preview mutated provisioning or exposed the key")
	}
	stored, _ := dbGetMobileSigningIdentity(globalCtx.AppDB(), target.ProjectID, "ios", "issuer-secret", "com.example.imported")
	if stored != nil {
		t.Fatal("preview saved the identity")
	}
	recorder = httptest.NewRecorder()
	app.httpDeploymentMobileSigningImport(recorder, appleImportRequest(t, archive, false), target)
	if recorder.Code != 200 {
		t.Fatalf("import: %d %s", recorder.Code, recorder.Body.String())
	}
	var result mobileSigningSetupResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.Identity.Source != "imported" || result.Setup.AppleCertificateID != first.Setup.AppleCertificateID || result.Setup.AppleProfileID == first.Setup.AppleProfileID {
		t.Fatalf("import did not reuse certificate with a new profile: %+v", result.Setup)
	}
	if platform.certificateSeq != 1 {
		t.Fatal("import created another Apple certificate")
	}
	credentials, err := app.iosSigningCredentials(target)
	if err != nil {
		t.Fatal(err)
	}
	if credentials["certificate_private_key"] == "" || credentials["provisioning_profile_base64"] == "" {
		t.Fatal("imported identity is not available for builds")
	}
	if strings.Contains(recorder.Body.String(), credentials["certificate_private_key"]) {
		t.Fatal("import exposed private signing material")
	}
	delete(platform.certificates, first.Setup.AppleCertificateID)
	recorder = httptest.NewRecorder()
	app.httpDeploymentMobileSigningImport(recorder, appleImportRequest(t, archive, true), target)
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "not active") {
		t.Fatal("unregistered certificate was accepted")
	}
}

func TestAppleCertificateReusedAcrossAppsButNotProjects(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	_, original := newIOSSigningDeployment(t, platform)
	app := &App{dataDir: t.TempDir()}
	first, err := app.setupMobileSigning(t.Context(), original, "", false)
	if err != nil {
		t.Fatal(err)
	}
	sibling := signingSibling(t, original, original.ProjectID, "com.example.sibling", "local", "{}")
	next, err := app.setupMobileSigning(t.Context(), sibling, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if next.Identity.ID == first.Identity.ID || next.Identity.Source != first.Identity.Source || next.Setup.AppleCertificateID != first.Setup.AppleCertificateID || next.Setup.AppleProfileID == first.Setup.AppleProfileID {
		t.Fatalf("incorrect certificate reuse: %+v", next.Setup)
	}
	if platform.certificateSeq != 1 || platform.profileSeq != 2 {
		t.Fatal("reuse created a certificate or shared an app profile")
	}
	foreign := signingSibling(t, original, "other-project", "com.example.foreign", "local", "{}")
	_, err = app.setupMobileSigning(t.Context(), foreign, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if platform.certificateSeq != 2 {
		t.Fatal("reused a private identity across project scopes")
	}
}

func TestAppleImportRejectsMismatchedPrivateKey(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	_, d := newIOSSigningDeployment(t, platform)
	app := &App{dataDir: t.TempDir()}
	first, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	archive, _, err := app.exportMobileSigningRecovery(d)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := appleSigningMaterial(archive, "")
	if err != nil {
		t.Fatal(err)
	}
	replacement, _, _, err := generateAppleDistributionKey(d, "com.example.other")
	if err != nil {
		t.Fatal(err)
	}
	payload.PrivateKeyPEM = replacement
	bad := testAppleRecoveryArchive(t, payload.CertificatePEM, payload.PrivateKeyPEM)
	if _, _, err := appleSigningMaterial(bad, ""); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong private key accepted: %v", err)
	}
	if first.Identity == nil {
		t.Fatal("missing fixture")
	}
}

func testAppleRecoveryArchive(t *testing.T, certificate, key string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, value := range map[string]string{"certificate.pem": certificate, "distribution-private-key.pem": key} {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		file.Write([]byte(value))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestReadyAppleBundleCannotChangeWhileSwitchingProviders(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	_, deployment := newIOSSigningDeployment(t, platform)
	app := &App{dataDir: t.TempDir()}
	if _, err := app.setupMobileSigning(t.Context(), deployment, "", false); err != nil {
		t.Fatal(err)
	}
	deployment.BuildBackend = "local"
	deployment.BuildBackendJSON = "{}"
	deployment.TargetConfigJSON = `{"bundle_id":"com.example.changed"}`
	if _, err := app.setupMobileSigning(t.Context(), deployment, "", false); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("provider switch bypassed ready identity protection: %v", err)
	}
	if platform.certificateSeq != 1 || platform.profileSeq != 1 {
		t.Fatal("changed Apple resources for an immutable identity")
	}
}
