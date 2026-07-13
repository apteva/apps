package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestPanelContainsSafeManagementWorkflows(t *testing.T) {
	source, err := os.ReadFile("ui/RedirectsPanel.tsx")
	if err != nil {
		t.Fatalf("read panel: %v", err)
	}
	text := string(source)
	for _, required := range []string{"EditDialog", "TestDialog", "ConfirmDialog", "PAGE_SIZE", "total", "ingress"} {
		if !strings.Contains(text, required) {
			t.Errorf("panel missing %q", required)
		}
	}
}

func TestHTTPCreateIgnoresBodyProjectID(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	globalCtx = ctx
	req := httptest.NewRequest(http.MethodPost, "/api/redirects?project_id=project-a", strings.NewReader(`{
		"hostname":"go.example.com",
		"destination":"https://example.com",
		"project_id":"project-b"
	}`))
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	rec := httptest.NewRecorder()
	(&App{}).httpCreateRedirect(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Redirect Redirect `json:"redirect"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Redirect.ProjectID != "project-a" {
		t.Fatalf("project_id=%q, want project-a", payload.Redirect.ProjectID)
	}
	if !payload.Redirect.PreserveQuery {
		t.Fatalf("omitted preserve_query should default true")
	}
}

func TestHTTPItemCannotCrossProjectBoundary(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	globalCtx = ctx
	rule, err := dbInsertRedirect(ctx.AppDB(), RedirectInput{
		Hostname: "go.example.com", Destination: "https://example.com", ProjectID: "project-b",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/redirects/1?project_id=project-a", nil)
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	rec := httptest.NewRecorder()
	(&App{}).httpDeleteRedirect(rec, req, rule.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := dbGetRedirect(ctx.AppDB(), rule.ID, "project-b"); err != nil {
		t.Fatalf("owner rule was removed: %v", err)
	}
}

func TestHTTPListReturnsTotal(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	globalCtx = ctx
	for i, path := range []string{"/a", "/b", "/c"} {
		_, err := dbInsertRedirect(ctx.AppDB(), RedirectInput{
			Hostname: "go.example.com", Path: path, Destination: "https://example.com", ProjectID: "project-a",
		})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/redirects?project_id=project-a&limit=2", nil)
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	rec := httptest.NewRecorder()
	(&App{}).httpListRedirects(rec, req)
	var payload struct {
		Count int `json:"count"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Count != 2 || payload.Total != 3 {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestMCPListIgnoresSpoofedProjectID(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"))
	for _, input := range []RedirectInput{
		{Hostname: "a.example.com", Destination: "https://example.com/a", ProjectID: "project-a"},
		{Hostname: "b.example.com", Destination: "https://example.com/b", ProjectID: "project-b"},
	} {
		if _, err := dbInsertRedirect(ctx.AppDB(), input); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	result, err := (&App{}).toolRedirectList(ctx, map[string]any{"project_id": "project-b"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	rows := result.(map[string]any)["redirects"].([]*Redirect)
	if len(rows) != 1 || rows[0].ProjectID != "project-a" {
		t.Fatalf("cross-project rows leaked: %+v", rows)
	}
}
