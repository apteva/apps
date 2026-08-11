package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestBuildArtifactDownloadServesCanonicalMobileFile(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	t.Setenv("APTEVA_PROJECT_ID", "")
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	deployment, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "apteva", TargetKind: "android", SourceKind: "local", SourceRef: t.TempDir(), Framework: "android",
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := dbGetEnvironmentByName(ctx.AppDB(), deployment.ID, defaultEnvironmentName)
	if err != nil || environment == nil {
		t.Fatalf("production environment: %v", err)
	}
	build, err := dbCreateBuildForEnv(ctx.AppDB(), deployment.ID, environment.ID, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := t.TempDir()
	bundle := []byte("signed-aab")
	if err := os.WriteFile(filepath.Join(artifactDir, "app.aab"), bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := artifactManifest{Platform: "android", Primary: "app.aab", VersionName: "0.1.0", VersionCode: "1"}
	manifestJSON, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(artifactDir, artifactManifestFilename), manifestJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{
		"status": "succeeded", "artifact_path": artifactDir, "artifact_size": int64(len(bundle)), "artifact_manifest_json": string(manifestJSON),
	}); err != nil {
		t.Fatal(err)
	}
	build, _ = dbGetBuild(ctx.AppDB(), build.ID)
	app := &App{dataDir: t.TempDir()}

	requestPath := "/api/builds/" + strconv.FormatInt(build.ID, 10) + "/artifact?project_id=p1"
	recorder := httptest.NewRecorder()
	app.handleBuildItem(recorder, httptest.NewRequest(http.MethodGet, requestPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("download status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("content-type=%q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, "apteva-0.1.0-1.aab") {
		t.Fatalf("content-disposition=%q", got)
	}
	if !bytes.Equal(recorder.Body.Bytes(), bundle) {
		t.Fatalf("body=%q", recorder.Body.Bytes())
	}

	headRecorder := httptest.NewRecorder()
	app.handleBuildItem(headRecorder, httptest.NewRequest(http.MethodHead, requestPath, nil))
	if headRecorder.Code != http.StatusOK || headRecorder.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d body=%q", headRecorder.Code, headRecorder.Body.String())
	}

	detailRecorder := httptest.NewRecorder()
	detailPath := "/api/builds/" + strconv.FormatInt(build.ID, 10) + "?project_id=p1"
	app.handleBuildItem(detailRecorder, httptest.NewRequest(http.MethodGet, detailPath, nil))
	var detail struct {
		Build Build `json:"build"`
	}
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Build.ArtifactDownloadURL != buildArtifactDownloadURL(build.ID, "p1") {
		t.Fatalf("artifact_download_url=%q", detail.Build.ArtifactDownloadURL)
	}

	toolResult, err := app.toolArtifactDownload(ctx, map[string]any{"_project_id": "p1", "build_id": float64(build.ID)})
	if err != nil {
		t.Fatal(err)
	}
	toolMap := toolResult.(map[string]any)
	if toolMap["filename"] != "apteva-0.1.0-1.aab" || toolMap["artifact_download_url"] == "" {
		t.Fatalf("tool result=%v", toolMap)
	}

	otherProject := httptest.NewRecorder()
	app.handleBuildItem(otherProject, httptest.NewRequest(http.MethodGet,
		"/api/builds/"+strconv.FormatInt(build.ID, 10)+"/artifact?project_id=p2", nil))
	if otherProject.Code != http.StatusNotFound {
		t.Fatalf("cross-project status=%d body=%s", otherProject.Code, otherProject.Body.String())
	}
}

func TestBuildArtifactDownloadStreamsServiceDirectoryAsZip(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	t.Setenv("APTEVA_PROJECT_ID", "")
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	deployment, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "site", TargetKind: "service", SourceKind: "local", SourceRef: t.TempDir(), Framework: "static",
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, _ := dbGetEnvironmentByName(ctx.AppDB(), deployment.ID, defaultEnvironmentName)
	build, err := dbCreateBuildForEnv(ctx.AppDB(), deployment.ID, environment.ID, "static", "")
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(artifactDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "index.html"), []byte("home"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "assets", "app.css"), []byte("css"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{
		"status": "succeeded", "artifact_path": artifactDir, "artifact_size": int64(7),
	}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	path := "/api/builds/" + strconv.FormatInt(build.ID, 10) + "/artifact?project_id=p1"
	(&App{}).handleBuildItem(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("download status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, "site-build-") || !strings.Contains(got, ".zip") {
		t.Fatalf("content-disposition=%q", got)
	}
	archive, err := zip.NewReader(bytes.NewReader(recorder.Body.Bytes()), int64(recorder.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(reader)
		_ = reader.Close()
		files[entry.Name] = string(body)
	}
	if files["index.html"] != "home" || files["assets/app.css"] != "css" {
		t.Fatalf("zip files=%v", files)
	}
}

func TestBuildArtifactDownloadReportsPrunedArtifact(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	t.Setenv("APTEVA_PROJECT_ID", "")
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	deployment, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "site", SourceKind: "local", SourceRef: t.TempDir(), Framework: "static",
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, _ := dbGetEnvironmentByName(ctx.AppDB(), deployment.ID, defaultEnvironmentName)
	build, _ := dbCreateBuildForEnv(ctx.AppDB(), deployment.ID, environment.ID, "static", "")
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{"status": "succeeded", "artifact_path": ""}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	path := "/api/builds/" + strconv.FormatInt(build.ID, 10) + "/artifact?project_id=p1"
	(&App{}).handleBuildItem(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusGone {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
