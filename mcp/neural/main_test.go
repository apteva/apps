package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func testApp(t *testing.T) *App {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "neural.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	raw, _ := embedded.ReadFile("migrations/001_init.sql")
	if _, err = db.Exec("PRAGMA foreign_keys=ON;" + string(raw)); err != nil {
		t.Fatal(err)
	}
	a := &App{db: db}
	m := a.Manifest()
	a.ctx = sdk.NewAppCtxForTest(&m, db, nil, nil, nil).WithProject("demo")
	return a
}
func call(t *testing.T, a *App, tool string, args map[string]any) map[string]any {
	t.Helper()
	out, err := a.perform("demo", tool, args)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}
func TestLifecycleIsolationAndPinnedDeployment(t *testing.T) {
	a := testApp(t)
	out := call(t, a, "experiments_create", map[string]any{"name": "XOR"})
	e := out["experiment"].(*Experiment)
	id := float64(e.ID)
	if _, err := a.perform("other", "experiments_get", map[string]any{"id": id}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-project read: %v", err)
	}
	call(t, a, "experiments_control", map[string]any{"id": id, "action": "start"})
	if err := a.tick("demo"); err != nil {
		t.Fatal(err)
	}
	call(t, a, "experiments_control", map[string]any{"id": id, "action": "pause"})
	before, _ := a.get("demo", e.ID)
	if err := a.tick("demo"); err != nil {
		t.Fatal(err)
	}
	after, _ := a.get("demo", e.ID)
	if after.State.Epoch != before.State.Epoch {
		t.Fatal("pause did not stop training")
	}
	call(t, a, "experiments_control", map[string]any{"id": id, "action": "step"})
	stepped, _ := a.get("demo", e.ID)
	if stepped.State.Epoch != before.State.Epoch+1 {
		t.Fatal("step must advance exactly one epoch")
	}
	v := call(t, a, "model_versions_create", map[string]any{"experiment_id": id})["version"].(Version)
	if _, err := a.perform("other", "deployments_create", map[string]any{"version_id": float64(v.ID)}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-project deployment: %v", err)
	}
	d := call(t, a, "deployments_create", map[string]any{"version_id": float64(v.ID)})["deployment"].(Deployment)
	args := map[string]any{"deployment_id": float64(d.ID), "x": .6, "y": .4}
	prediction := call(t, a, "predictions_create", args)
	call(t, a, "experiments_control", map[string]any{"id": id, "action": "start"})
	for i := 0; i < 10; i++ {
		if err := a.tick("demo"); err != nil {
			t.Fatal(err)
		}
	}
	pinned := call(t, a, "predictions_create", args)
	if !reflect.DeepEqual(prediction, pinned) {
		t.Fatal("training mutated a deployed model")
	}
	if _, err := a.perform("other", "predictions_create", args); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-project inference: %v", err)
	}
	// Reconstructing the app over the same durable DB recovers training.
	restarted := &App{db: a.db}
	current, _ := a.get("demo", e.ID)
	if err := restarted.tick("demo"); err != nil {
		t.Fatal(err)
	}
	recovered, _ := restarted.get("demo", e.ID)
	if recovered.State.Epoch != current.State.Epoch+5 {
		t.Fatal("restart did not continue training")
	}
}
func TestManifestAndToolsAgree(t *testing.T) {
	a := &App{}
	m := a.Manifest()
	if err := sdk.ValidateManifest(&m); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range a.MCPTools() {
		names[tool.Name] = true
		if tool.InputSchema == nil {
			t.Fatalf("missing schema: %s", tool.Name)
		}
	}
	for _, d := range m.Provides.MCPTools {
		if !names[d.Name] {
			t.Fatalf("missing handler: %s", d.Name)
		}
	}
}

func TestNativePanelPackage(t *testing.T) {
	m := (&App{}).Manifest()
	if len(m.Provides.UIPanels) != 1 {
		t.Fatal("expected one native project panel")
	}
	panel := m.Provides.UIPanels[0]
	if panel.Slot != "project.page" || !strings.HasSuffix(panel.Entry, ".mjs") {
		t.Fatalf("panel cannot load through AppProjectPage: %+v", panel)
	}
	path := strings.TrimPrefix(panel.Entry, "/")
	bundle, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing shipped panel %s: %v", path, err)
	}
	if !regexp.MustCompile(`from\s*["']react["']`).Match(bundle) {
		t.Fatal("panel must import the host's React through the import map")
	}
	if !regexp.MustCompile(`export\s*\{[^}]*\bas\s+default\b`).Match(bundle) {
		t.Fatal("native app loader requires a default component export")
	}
}
func TestHTTPValidationAndProjectGate(t *testing.T) {
	a := testApp(t)
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/rpc?project_id=other", `{"tool":"experiments_list"}`, 403},
		{"/rpc", `{"tool":"experiments_create","args":{"hidden":[999]}}`, 400},
		{"/rpc", `{"tool":"experiments_create","args":{"epochs":1.5}}`, 400},
		{"/rpc", `{"tool":"experiments_get","args":{"id":999}}`, 404},
		{"/rpc", `{"tool":"experiments_list"} {}`, 400},
		{"/rpc", `{"tool":"experiments_list"}`, 200},
	} {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.handleRPC(w, r)
		if w.Code != tc.status {
			t.Errorf("%s: got %d: %s", tc.body, w.Code, w.Body.String())
		}
	}
}
func TestPredictionEndpoint(t *testing.T) {
	a := testApp(t)
	e := call(t, a, "experiments_create", map[string]any{})["experiment"].(*Experiment)
	call(t, a, "experiments_control", map[string]any{"id": float64(e.ID), "action": "step"})
	v := call(t, a, "model_versions_create", map[string]any{"experiment_id": float64(e.ID)})["version"].(Version)
	call(t, a, "deployments_create", map[string]any{"version_id": float64(v.ID)})
	r := httptest.NewRequest("POST", "/deployments/1/predict", strings.NewReader(`{"x":0.5,"y":-0.4}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handlePrediction(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["version_id"] != float64(v.ID) {
		t.Fatal("missing pinned version identity")
	}
	r = httptest.NewRequest("POST", "/deployments/1/predict", strings.NewReader(`null`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	a.handlePrediction(w, r)
	if w.Code != 400 {
		t.Fatal("null prediction body must be rejected")
	}
}

func TestConcurrentTrainingAndInspection(t *testing.T) {
	a := testApp(t)
	e := call(t, a, "experiments_create", map[string]any{})["experiment"].(*Experiment)
	call(t, a, "experiments_control", map[string]any{"id": float64(e.ID), "action": "start"})
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				var err error
				if worker == 0 {
					err = a.tick("demo")
				} else {
					_, err = a.perform("demo", "experiments_get", map[string]any{"id": float64(e.ID)})
				}
				if err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	call(t, a, "experiments_control", map[string]any{"id": float64(e.ID), "action": "pause"})
	saved, _ := a.get("demo", e.ID)
	if saved.State.Epoch != 100 {
		t.Fatalf("lost updates: epoch %d", saved.State.Epoch)
	}
}
