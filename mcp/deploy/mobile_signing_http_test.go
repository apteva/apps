package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestAndroidSigningImportAndRecoveryHTTP(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android", TargetKind: "android", SourceKind: "local", SourceRef: t.TempDir(),
		Framework: "android", BuildBackend: "local",
		TargetConfigJSON: `{"package_name":"com.example.android"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbGetEnvironmentByName(ctx.AppDB(), d.ID, defaultEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, env)
	_, generated, err := generateAndroidSigningIdentity("p1", "com.example.android")
	if err != nil {
		t.Fatal(err)
	}
	pfx, _ := base64.StdEncoding.DecodeString(generated.KeystoreBase64)

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, _ := form.CreateFormFile("keystore", "upload.p12")
	_, _ = file.Write(pfx)
	_ = form.WriteField("store_password", generated.StorePassword)
	_ = form.WriteField("key_password", generated.KeyPassword)
	_ = form.WriteField("key_alias", generated.KeyAlias)
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPost, "/mobile-signing/import", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	recorder := httptest.NewRecorder()
	app := &App{dataDir: t.TempDir()}
	app.httpDeploymentMobileSigningImport(recorder, request, d)
	if recorder.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result mobileSigningSetupResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.Identity == nil || result.Identity.Source != "imported" {
		t.Fatalf("import result=%+v", result)
	}
	if strings.Contains(recorder.Body.String(), generated.StorePassword) ||
		strings.Contains(recorder.Body.String(), generated.KeystoreBase64) {
		t.Fatal("import response exposed signing secrets")
	}
	statusRecorder := httptest.NewRecorder()
	app.httpDeploymentMobileSigning(statusRecorder, httptest.NewRequest(http.MethodGet, "/mobile-signing", nil), d)
	if statusRecorder.Code != http.StatusOK || strings.Contains(statusRecorder.Body.String(), generated.StorePassword) {
		t.Fatalf("status response is invalid or exposed secrets: status=%d body=%s", statusRecorder.Code, statusRecorder.Body.String())
	}
	var status struct {
		Signing map[string]any `json:"signing"`
	}
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Signing["package_name"] != "com.example.android" || status.Signing["provider_ready"] != true {
		t.Fatalf("signing summary=%v", status.Signing)
	}

	recoveryRequest := httptest.NewRequest(http.MethodPost, "/mobile-signing/recovery", strings.NewReader(`{"confirm":true}`))
	recoveryRecorder := httptest.NewRecorder()
	app.httpDeploymentMobileSigningRecovery(recoveryRecorder, recoveryRequest, d)
	if recoveryRecorder.Code != http.StatusOK || recoveryRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("recovery status=%d headers=%v body=%s", recoveryRecorder.Code, recoveryRecorder.Header(), recoveryRecorder.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(recoveryRecorder.Body.Bytes()), int64(recoveryRecorder.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range archive.File {
		reader, openErr := entry.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		files[entry.Name], _ = io.ReadAll(reader)
		_ = reader.Close()
	}
	if !bytes.Equal(files["upload-keystore.p12"], pfx) ||
		!bytes.Contains(files["credentials.json"], []byte(generated.StorePassword)) {
		t.Fatalf("recovery archive is incomplete: files=%v", files)
	}
}
