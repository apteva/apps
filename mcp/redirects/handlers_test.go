package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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

func TestPublicRedirectEmitsEveryHitWithResolvedTarget(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	recorder := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(recorder))
	globalCtx = ctx
	rule, err := dbInsertRedirect(ctx.AppDB(), RedirectInput{
		Hostname:      "go.example.com",
		Path:          "/blog",
		MatchMode:     "prefix",
		Destination:   "https://new.example.com/articles",
		PreservePath:  true,
		PreserveQuery: true,
		ProjectID:     "project-a",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	requests := []struct {
		url    string
		target string
	}{
		{"http://go.example.com/blog/launch?source=email", "https://new.example.com/articles/launch?source=email"},
		{"http://go.example.com/blog/sale?source=ads", "https://new.example.com/articles/sale?source=ads"},
		{"http://go.example.com/blog/cart?source=social", "https://new.example.com/articles/cart?source=social"},
	}
	for _, request := range requests {
		req := httptest.NewRequest(http.MethodGet, request.url, nil)
		rec := httptest.NewRecorder()
		(&App{}).handlePublicRedirect(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != request.target {
			t.Fatalf("location=%q want=%q", got, request.target)
		}
	}
	events := recorder.EventsByTopic("rule.hit")
	if len(events) != len(requests) {
		t.Fatalf("hit events=%+v", events)
	}
	for i, event := range events {
		payload, ok := event.Data.(map[string]any)
		if !ok || payload["id"] != rule.ID || payload["rule_id"] != rule.ID ||
			payload["destination"] != rule.Destination || payload["target"] != requests[i].target ||
			payload["hits_total"] != int64(i+1) || payload["day_hits"] != int64(i+1) {
			t.Fatalf("hit payload %d=%#v", i, event.Data)
		}
		at, _ := payload["at"].(string)
		if date, _ := payload["date"].(string); len(at) < 10 || date != at[:10] {
			t.Fatalf("hit date/at mismatch: %#v", payload)
		}
	}
	stored, err := dbGetRedirect(ctx.AppDB(), rule.ID, "project-a")
	if err != nil || stored.Hits != int64(len(requests)) {
		t.Fatalf("stored hits=%+v err=%v", stored, err)
	}
}

func TestPublicRedirectDoesNotRedirectWhenCountersCannotPersist(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	recorder := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(recorder))
	globalCtx = ctx
	rule, err := dbInsertRedirect(ctx.AppDB(), RedirectInput{
		Hostname: "go.example.com", Destination: "https://example.com/landing", ProjectID: "project-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`DROP TABLE redirect_daily_stats`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://go.example.com/", nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicRedirect(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" {
		t.Fatalf("status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if events := recorder.EventsByTopic("rule.hit"); len(events) != 0 {
		t.Fatalf("emitted unpersisted hit: %+v", events)
	}
	stored, err := dbGetRedirect(ctx.AppDB(), rule.ID, "project-a")
	if err != nil || stored.Hits != 0 {
		t.Fatalf("counter transaction did not roll back: rule=%+v err=%v", stored, err)
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

func TestRedirectStatsToolAndHTTPAreProjectScoped(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"))
	globalCtx = ctx
	ruleA, err := dbInsertRedirect(ctx.AppDB(), RedirectInput{
		Hostname: "a.example.com", Destination: "https://example.com/a", ProjectID: "project-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	ruleB, err := dbInsertRedirect(ctx.AppDB(), RedirectInput{
		Hostname: "b.example.com", Destination: "https://example.com/b", ProjectID: "project-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339, "2026-07-13T12:10:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbRecordHit(ctx.AppDB(), ruleA.ID, "project-a", at); err != nil {
		t.Fatal(err)
	}
	if _, err := dbRecordHit(ctx.AppDB(), ruleB.ID, "project-b", at); err != nil {
		t.Fatal(err)
	}

	result, err := (&App{}).toolRedirectStats(ctx, map[string]any{
		"project_id": "project-b", "from": "2026-07-13", "to": "2026-07-13",
	})
	if err != nil {
		t.Fatal(err)
	}
	toolStats := result.(map[string]any)["stats"].([]RedirectStat)
	if len(toolStats) != 1 || toolStats[0].RuleID != ruleA.ID {
		t.Fatalf("tool stats leaked project data: %+v", toolStats)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/redirects/stats?project_id=project-b&from=2026-07-13&to=2026-07-13", nil)
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	rec := httptest.NewRecorder()
	(&App{}).handleRedirectStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Stats []RedirectStat `json:"stats"`
		Total int            `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || len(payload.Stats) != 1 || payload.Stats[0].RuleID != ruleA.ID {
		t.Fatalf("HTTP stats leaked project data: %+v", payload)
	}
}
