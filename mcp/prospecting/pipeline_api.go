package main

import (
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) pipelineTools() []sdk.Tool {
	properties := map[string]any{
		"profile_id": sInteger(), "source": map[string]any{"type": "string", "enum": []string{"web", "google_places"}},
		"query": sString(), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}, "qualify": sBoolean(),
		"crm_mode": map[string]any{"type": "string", "enum": []string{"review", "auto"}}, "min_fit_score": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}, "min_confidence_score": map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"list_ids":  map[string]any{"type": "array", "items": map[string]any{"oneOf": []any{sString(), sInteger()}}},
		"max_pages": map[string]any{"type": "integer", "minimum": 1, "maximum": 5}, "max_places_requests": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
		"concurrency": map[string]any{"type": "integer", "minimum": 1, "maximum": 4}, "target_leads": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}, "new_only": sBoolean(),
		"included_type": sString(), "location_restriction": map[string]any{"type": "object"}, "location_bias": map[string]any{"type": "object"}, "engine": sString(), "fallback_engine": sString(), "idempotency_key": sString(),
	}
	return []sdk.Tool{
		{Name: "prospecting_run", Description: "Start a saved, resumable run: discover with source web or google_places, automatically add prospects, optionally qualify their websites, and write matching prospects to CRM when crm_mode=auto. REAL CRM WRITE in auto mode. Returns a queued run; poll prospecting_run_get. Defaults: source=web, limit=20, qualify=true, crm_mode=review, min_fit_score=70, min_confidence_score=60, max_pages=2, max_places_requests=3. concurrency defaults to 3 (maximum 4). Optional target_leads stops qualification once that many distinct published emails with websites are saved; limit remains the discovery candidate budget. new_only excludes businesses already saved in other profiles. Use idempotency_key for safe request retries. No message is sent.", InputSchema: schemaObject(properties, []string{"profile_id"}), Handler: a.toolRun},
		{Name: "prospecting_run_get", Description: "Read a saved run, stage counts, prospect ids, reasons, errors and CRM contact ids. Args: id.", InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}), Handler: a.toolRunGet},
		{Name: "prospecting_run_list", Description: "List saved pipeline runs for the current project. Args: limit?.", InputSchema: schemaObject(map[string]any{"limit": sInteger()}, nil), Handler: a.toolRunList},
		{Name: "prospecting_run_resume", Description: "Resume an interrupted run or retry failed qualification/CRM steps. Completed handoffs are preserved. Args: id.", InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}), Handler: a.toolRunResume},
		{Name: "prospecting_settings", Description: "Read or update project discovery settings. Args: places_connection_id? (0 disconnects), daily_places_request_limit? (1–1000, default 100). No credentials are returned.", InputSchema: schemaObject(map[string]any{"places_connection_id": sInteger(), "daily_places_request_limit": sInteger()}, nil), Handler: a.toolDiscoverySettings},
		{Name: "prospecting_places_connections", Description: "List accessible Google Places connections for settings. Does not return API keys.", InputSchema: schemaObject(nil, nil), Handler: a.toolPlacesConnections},
	}
}

func (a *App) handleDiscoverySettings(w http.ResponseWriter, r *http.Request) {
	args := map[string]any{}
	if r.Method == http.MethodPatch {
		var err error
		args, err = decodeBody(r)
		if err != nil {
			writeError(w, err, 400)
			return
		}
	} else if r.Method != http.MethodGet {
		http.Error(w, "GET or PATCH required", 405)
		return
	}
	out, err := a.toolDiscoverySettings(requestCtx(r), args)
	respond(w, out, err)
}
func (a *App) handlePlacesConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	out, err := a.toolPlacesConnections(requestCtx(r), nil)
	respond(w, out, err)
}
func (a *App) handlePipelineRuns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolRunList(requestCtx(r), map[string]any{"limit": queryInt64(r, "limit")})
		respond(w, out, err)
	case http.MethodPost:
		args, err := decodeBody(r)
		if err != nil {
			writeError(w, err, 400)
			return
		}
		out, err := a.toolRun(requestCtx(r), args)
		respond(w, out, err)
	default:
		http.Error(w, "GET or POST required", 405)
	}
}
func (a *App) handlePipelineRunItem(w http.ResponseWriter, r *http.Request) {
	id, action := pathIDAction(r.URL.Path, "/pipeline/runs/")
	if id == 0 {
		http.Error(w, "run id required", 400)
		return
	}
	args := map[string]any{"id": id}
	if r.Method == http.MethodGet && action == "" {
		out, err := a.toolRunGet(requestCtx(r), args)
		respond(w, out, err)
	} else if r.Method == http.MethodPost && action == "resume" {
		out, err := a.toolRunResume(requestCtx(r), args)
		respond(w, out, err)
	} else {
		http.Error(w, "unsupported run operation", 405)
	}
}
