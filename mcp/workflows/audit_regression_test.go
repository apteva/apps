package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestHTTPExternalTargetDoesNotReceiveAppToken(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "must-not-leak")
	var authorization string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	result := runHTTPStep(context.Background(), &StepDef{
		Kind: "http",
		URL:  srv.URL,
	}, nil)
	if result.Status != "ok" {
		t.Fatalf("runHTTPStep: %+v", result)
	}
	if authorization != "" {
		t.Fatalf("external target received Authorization %q", authorization)
	}
}

func TestHTTPAppRelativeTargetReceivesAppToken(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "sidecar-token")
	var authorization, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)

	result := runHTTPStep(context.Background(), &StepDef{
		Kind: "http",
		App:  "tables",
		Path: "/rows",
	}, nil)
	if result.Status != "ok" {
		t.Fatalf("runHTTPStep: %+v", result)
	}
	if authorization != "Bearer sidecar-token" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if path != "/api/apps/tables/rows" {
		t.Fatalf("path = %q", path)
	}
}

func TestWorkflowTemplateEnvironmentRedactsCredentials(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "secret")
	t.Setenv("SOME_API_KEY", "secret")
	t.Setenv("SAFE_WORKFLOW_VALUE", "visible")
	env := readEnv()
	if env["APTEVA_APP_TOKEN"] != "" || env["SOME_API_KEY"] != "" {
		t.Fatalf("credential leaked into workflow environment: %+v", env)
	}
	if env["SAFE_WORKFLOW_VALUE"] != "visible" {
		t.Fatalf("safe value missing: %+v", env)
	}
}

func TestSourceUpdateRefreshesDenormalizedEventTrigger(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	wf, err := buildAndCreateWorkflow(ctx, testProj, map[string]any{
		"name": "changes-trigger",
		"source": `name: changes-trigger
trigger: { kind: manual }
steps:
  - id: ack
    kind: emit
    topic: ack
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := updateAndRehashWorkflow(ctx, testProj, wf.ID, map[string]any{
		"source": `name: changes-trigger
trigger:
  kind: event
  source: tables
  topic: row.updated
steps:
  - id: ack
    kind: emit
    topic: ack
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TriggerKind != "event" ||
		!strings.Contains(updated.TriggerJSON, `"source":"tables"`) ||
		!strings.Contains(updated.TriggerJSON, `"topic":"row.updated"`) {
		t.Fatalf("trigger not refreshed: %+v", updated)
	}
}

func TestCreateHonorsDisabledStatus(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	wf, err := buildAndCreateWorkflow(ctx, testProj, map[string]any{
		"name":   "disabled-workflow",
		"status": "disabled",
		"source": `name: disabled-workflow
steps:
  - id: ack
    kind: emit
    topic: ack
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if wf.Status != "disabled" {
		t.Fatalf("status = %q, want disabled", wf.Status)
	}
	if _, err := RunWorkflow(context.Background(), ctx, testProj, wf, nil, runOptions{triggerKind: "manual"}); err == nil {
		t.Fatal("disabled workflow executed")
	}
}

func TestRepoBackedWorkflowResolvesSourceBeforeRun(t *testing.T) {
	platform := &stubPlatform{results: map[string]any{
		"code:code_read_file": map[string]any{
			"content": `name: repo-backed
steps:
  - id: ack
    kind: emit
    topic: ack
`,
		},
	}}
	ctx := newRunCtx(t, platform)
	repoID := int64(99123)
	wf, err := dbCreateWorkflow(ctx.AppDB(), testProj, &Workflow{
		Name:        "repo-backed",
		SourceKind:  "repo",
		RepoID:      &repoID,
		RepoPath:    "automations/repo-backed.yaml",
		SourceHash:  "repo-backed-test-" + time.Now().UTC().Format(time.RFC3339Nano),
		TriggerKind: "manual",
		Status:      "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := RunWorkflow(context.Background(), ctx, testProj, wf, nil, runOptions{triggerKind: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != "completed" {
		t.Fatalf("run = %+v", run)
	}
	if len(platform.calls) != 1 || platform.calls[0].app != "code" ||
		platform.calls[0].tool != "code_read_file" {
		t.Fatalf("platform calls = %+v", platform.calls)
	}
}

func TestCancelStopsActiveHTTPRun(t *testing.T) {
	requestStarted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	wf := mustCreateWorkflow(t, ctx, `name: cancellable
steps:
  - id: wait
    kind: http
    url: `+srv.URL+`
`)
	result := make(chan *Run, 1)
	go func() {
		run, _ := RunWorkflow(context.Background(), ctx, testProj, wf, nil, runOptions{triggerKind: "manual"})
		result <- run
	}()

	select {
	case <-requestStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP step did not start")
	}
	var runID int64
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := dbListRuns(ctx.AppDB(), testProj, wf.ID, 1)
		if len(runs) == 1 {
			runID = runs[0].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runID == 0 {
		t.Fatal("active run not found")
	}
	requested, err := requestRunCancellation(ctx.AppDB(), testProj, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !requested {
		t.Fatal("active cancellation was not delivered")
	}
	select {
	case run := <-result:
		if run == nil || run.Status != "cancelled" {
			t.Fatalf("run = %+v, want cancelled", run)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled run did not stop")
	}
}
