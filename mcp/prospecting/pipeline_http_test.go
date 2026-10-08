package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPipelineHTTPUsesSameSavedRunAndIsolatesProjects(t *testing.T) {
	p := &placesPlatform{pages: map[string]any{}}
	ctx, profile := pipelineSetup(t, p)
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{}
	request := httptest.NewRequest(http.MethodPost, "/pipeline/runs?project_id=project-a", strings.NewReader(`{"profile_id":`+mustJSON(profile.ID)+`,"source":"google_places","qualify":false,"limit":2,"idempotency_key":"http-test"}`))
	response := httptest.NewRecorder()
	app.handlePipelineRuns(response, request)
	if response.Code != 200 {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var out struct {
		Run prospectingJob `json:"run"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Run.ID == 0 || out.Run.Status != "queued" {
		t.Fatalf("run=%+v", out.Run)
	}
	response = httptest.NewRecorder()
	app.handlePipelineRunItem(response, httptest.NewRequest(http.MethodGet, "/pipeline/runs/"+mustJSON(out.Run.ID)+"?project_id=other-project", nil))
	if response.Code == 200 {
		t.Fatal("HTTP leaked another project's run")
	}
	response = httptest.NewRecorder()
	app.handlePipelineRuns(response, httptest.NewRequest(http.MethodGet, "/pipeline/runs?project_id=project-a", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"google_places"`) {
		t.Fatalf("list=%s", response.Body.String())
	}
	response = httptest.NewRecorder()
	app.handleDiscoverySettings(response, httptest.NewRequest(http.MethodGet, "/settings?project_id=project-a", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), "api_key") {
		t.Fatalf("settings=%s", response.Body.String())
	}
}

func TestPlacesRunRejectsInvalidModesAndMissingConnectionsBeforeQueueing(t *testing.T) {
	p := &placesPlatform{}
	ctx, profile := pipelineSetup(t, p)
	for _, args := range []map[string]any{
		{"source": "unknown"}, {"source": "google_places", "crm_mode": "auto", "qualify": false}, {"source": "google_places", "limit": 21}, {"source": "google_places", "min_fit_score": 101},
		{"source": "google_places", "location_restriction": map[string]any{}, "location_bias": map[string]any{}},
	} {
		args["profile_id"] = profile.ID
		if _, err := (&App{}).toolRun(ctx, args); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	if _, err := (&App{}).toolDiscoverySettings(ctx, map[string]any{"places_connection_id": 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolRun(ctx, map[string]any{"profile_id": profile.ID, "source": "google_places"}); err == nil {
		t.Fatal("missing connection accepted")
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM prospecting_jobs`).Scan(&count)
	if count != 0 {
		t.Fatalf("invalid requests queued %d runs", count)
	}
}
