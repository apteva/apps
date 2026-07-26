package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerBackendBuildsSignedCapsuleWithoutGit(t *testing.T) {
	const token = "local-test-runner-token-0123456789abcdef"
	t.Setenv(defaultRunnerTokenEnv, token)

	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "index.html"), []byte("capsule-only build"), 0o644); err != nil {
		t.Fatal(err)
	}
	platform := &cloudBuildPlatform{provider: "none"}
	ctx := withCloudBuildContext(t, platform)
	app := &App{dataDir: t.TempDir(), retainRollbacks: 3}
	if err := os.MkdirAll(filepath.Join(app.dataDir, "builds"), 0o755); err != nil {
		t.Fatal(err)
	}

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		index := strings.Index(req.URL.Path, "/source-capsules/")
		if index < 0 {
			http.NotFound(w, req)
			return
		}
		req.URL.Path = req.URL.Path[index:]
		app.handleSourceCapsule(w, req)
	}))
	defer sourceServer.Close()

	runner, err := newCapsuleRunner(t.TempDir(), token, 1)
	if err != nil {
		t.Fatal(err)
	}
	runnerServer := httptest.NewServer(runner)
	defer runnerServer.Close()

	config := map[string]any{
		"runner_url": runnerServer.URL, "source_base_url": sourceServer.URL,
		"artifact_mode": "bundle",
	}
	configJSON, _ := json.Marshal(config)
	deployment, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "capsule-site", TargetKind: "service",
		SourceKind: "local", SourceRef: sourceDir, Framework: "static",
		BuildBackend: buildBackendRunner, BuildBackendJSON: string(configJSON),
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), deployment)
	if err != nil {
		t.Fatal(err)
	}
	build, err := app.submitCloudBuild(context.Background(), effectiveDeploymentForEnvironment(deployment, env))
	if err != nil {
		t.Fatal(err)
	}
	if build.BuildBackend != buildBackendRunner || build.ExternalJobID == "" || build.SourceSHA == "" {
		t.Fatalf("submitted build=%+v", build)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		build, err = dbGetBuild(ctx.AppDB(), build.ID)
		if err != nil {
			t.Fatal(err)
		}
		if build.Status == "succeeded" || build.Status == "failed" {
			break
		}
		if err := app.syncCloudBuild(context.Background(), build); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	build, err = dbGetBuild(ctx.AppDB(), build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if build.Status != "succeeded" {
		t.Fatalf("completed build=%+v", build)
	}
	body, err := os.ReadFile(filepath.Join(build.ArtifactPath, "index.html"))
	if err != nil || string(body) != "capsule-only build" {
		t.Fatalf("artifact body=%q err=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(app.buildDir(build.ID), sourceCapsuleFilename)); !os.IsNotExist(err) {
		t.Fatalf("terminal build retained source capsule: %v", err)
	}
	runner.mu.Lock()
	jobCount := len(runner.jobs)
	runner.mu.Unlock()
	if jobCount != 1 {
		t.Fatalf("runner jobs=%d", jobCount)
	}

	unauthorized, err := http.Get(runnerServer.URL + "/v1/jobs/" + build.ExternalJobID)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.StatusCode)
	}
}

func TestCapsuleRunnerIdempotencyAndCredentialsAreNotPersisted(t *testing.T) {
	const token = "runner-persistence-token-0123456789abcdef"
	sourceBody := runnerTestZip(t, map[string]string{"index.html": "ok"})
	sum := sha256.Sum256(sourceBody)
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(sourceBody)
	}))
	defer sourceServer.Close()
	dataDir := t.TempDir()
	runner, err := newCapsuleRunner(dataDir, token, 1)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(runner)
	defer server.Close()

	input := runnerJobRequest{
		Protocol: runnerProtocolVersion, IdempotencyKey: "same-job",
		Source: runnerSource{
			URL: sourceServer.URL, SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(sourceBody)), Format: sourceCapsuleFormat,
		},
		Build: runnerBuildSpec{
			BuildID: 7, ProjectID: "p1", Deployment: "site",
			Framework: "static", TargetConfigJSON: "{}",
		},
		Credentials: runnerCredentials{
			AppStore: map[string]string{"private_key": "TOP-SECRET-PRIVATE-KEY"},
		},
	}
	first := submitRunnerTestJob(t, server.URL, token, input)
	second := submitRunnerTestJob(t, server.URL, token, input)
	if first.ID == "" || second.ID != first.ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	state, err := os.ReadFile(filepath.Join(dataDir, "jobs", first.ID, runnerStateFilename))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(state, []byte("TOP-SECRET")) || bytes.Contains(state, []byte("private_key")) {
		t.Fatalf("runner persisted submitted credentials: %s", state)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		status := runner.jobs[first.ID].Response.Status
		runner.mu.Unlock()
		if status == "succeeded" || status == "failed" || status == "cancelled" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runner job did not reach a terminal state")
}

func TestExtractRunnerSourceRejectsTraversalAndSymlinks(t *testing.T) {
	var traversal bytes.Buffer
	writer := zip.NewWriter(&traversal)
	entry, err := writer.Create("../escape")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("bad"))
	_ = writer.Close()
	path := filepath.Join(t.TempDir(), "traversal.zip")
	if err := os.WriteFile(path, traversal.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractRunnerSource(path, t.TempDir()); err == nil {
		t.Fatal("expected path traversal rejection")
	}

	var symlink bytes.Buffer
	writer = zip.NewWriter(&symlink)
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err = writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("/tmp/target"))
	_ = writer.Close()
	path = filepath.Join(t.TempDir(), "symlink.zip")
	if err := os.WriteFile(path, symlink.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractRunnerSource(path, t.TempDir()); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestRunnerConfigRequiresHTTPSOutsideLoopback(t *testing.T) {
	t.Setenv(defaultRunnerTokenEnv, "runner-config-token-0123456789abcdef")
	if err := validateBuildBackendSelection(buildBackendRunner, `{"runner_url":"http://127.0.0.1:9075"}`); err != nil {
		t.Fatal(err)
	}
	if err := validateBuildBackendSelection(buildBackendRunner, `{"runner_url":"https://runner.example.test"}`); err != nil {
		t.Fatal(err)
	}
	if err := validateBuildBackendSelection(buildBackendRunner, `{"runner_url":"http://runner.example.test"}`); err == nil {
		t.Fatal("expected non-loopback HTTP runner rejection")
	}
	if err := validateBuildBackendSelection(buildBackendRunner, `{"runner_url":"https://runner.example.test","source_mode":"repository"}`); err == nil {
		t.Fatal("expected repository source mode rejection")
	}
}

func TestCapsuleRunnerMarksActiveJobFailedAfterRestart(t *testing.T) {
	dataDir := t.TempDir()
	id := strings.Repeat("a", 32)
	jobDir := filepath.Join(dataDir, "jobs", id)
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := runnerJob{
		Response: runnerJobResponse{
			Protocol: runnerProtocolVersion, ID: id, Status: "running",
			CreatedAt: time.Now().Add(-time.Minute).Format(time.RFC3339Nano),
		},
		IdempotencyKey: "restart-job",
	}
	body, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(jobDir, runnerStateFilename), body, 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := newCapsuleRunner(dataDir, "runner-restart-token-0123456789abcdef", 1)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	loaded := runner.jobs[id].Response
	runner.mu.Unlock()
	if loaded.Status != "failed" || !strings.Contains(loaded.Error, "restarted") {
		t.Fatalf("loaded job=%+v", loaded)
	}
}

func TestCapsuleRunnerCancellationStopsBuildProcess(t *testing.T) {
	const token = "runner-cancellation-token-0123456789abcdef"
	sourceBody := runnerTestZip(t, map[string]string{"index.html": "ok"})
	sum := sha256.Sum256(sourceBody)
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(sourceBody)
	}))
	defer sourceServer.Close()
	runner, err := newCapsuleRunner(t.TempDir(), token, 1)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(runner)
	defer server.Close()
	input := runnerJobRequest{
		Protocol: runnerProtocolVersion, IdempotencyKey: "cancel-job",
		Source: runnerSource{
			URL: sourceServer.URL, SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(sourceBody)), Format: sourceCapsuleFormat,
		},
		Build: runnerBuildSpec{
			BuildID: 8, Deployment: "slow-site", Framework: "static",
			BuildCmd: "sleep 10", TargetConfigJSON: "{}",
		},
	}
	job := submitRunnerTestJob(t, server.URL, token, input)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		status := runner.jobs[job.ID].Response.Status
		runner.mu.Unlock()
		if status == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	request, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/jobs/"+job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cancel status=%d", response.StatusCode)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(runner.sem) == 0 {
			runner.mu.Lock()
			status := runner.jobs[job.ID].Response.Status
			runner.mu.Unlock()
			if status != "cancelled" {
				t.Fatalf("cancelled job status=%s", status)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cancelled build process did not release runner capacity")
}

func runnerTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(entry, body)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func submitRunnerTestJob(t *testing.T, baseURL, token string, input runnerJobRequest) runnerJobResponse {
	t.Helper()
	body, _ := json.Marshal(input)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/jobs", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit status=%d body=%s", resp.StatusCode, responseBody)
	}
	var output runnerJobResponse
	if err := json.NewDecoder(resp.Body).Decode(&output); err != nil {
		t.Fatal(err)
	}
	return output
}
