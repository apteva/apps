package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func decodeBody(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		httpError(w, 400, errors.New("invalid JSON"))
		return false
	}
	return true
}

func (a *App) handleSuites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := a.svc.db.listSuites()
		if err != nil {
			httpError(w, 500, err)
			return
		}
		writeJSON(w, 200, rows)
	case http.MethodPost:
		var item Suite
		if !decodeBody(w, r, &item) {
			return
		}
		saved, err := a.svc.saveSuite(&item, true)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 201, saved)
	default:
		httpError(w, 405, errors.New("GET or POST only"))
	}
}

func (a *App) handleSuite(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/suites/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) > 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := a.svc.db.getSuite(id)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		if item == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, item)
	case http.MethodPut, http.MethodPatch:
		var item Suite
		if !decodeBody(w, r, &item) {
			return
		}
		item.ID = id
		saved, err := a.svc.saveSuite(&item, false)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 200, saved)
	case http.MethodDelete:
		if err := a.svc.db.deleteSuite(id); err != nil {
			httpError(w, 409, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		httpError(w, 405, errors.New("unsupported method"))
	}
}

func (a *App) handleCases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, errors.New("POST only"))
		return
	}
	var item Case
	if !decodeBody(w, r, &item) {
		return
	}
	saved, err := a.svc.saveCase(&item, true)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, 201, saved)
}

func (a *App) handleCase(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/cases/")
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	switch r.Method {
	case http.MethodGet:
		item, err := a.svc.db.getCase(id)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		if item == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, item)
	case http.MethodPut, http.MethodPatch:
		var item Case
		if !decodeBody(w, r, &item) {
			return
		}
		item.ID = id
		saved, err := a.svc.saveCase(&item, false)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 200, saved)
	case http.MethodDelete:
		if err := a.svc.db.deleteCase(id); err != nil {
			httpError(w, 409, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		httpError(w, 405, errors.New("unsupported method"))
	}
}

type experimentInput struct {
	SuiteID        string   `json:"suite_id"`
	Name           string   `json:"name"`
	Targets        []Target `json:"targets"`
	Repetitions    int      `json:"repetitions"`
	BaselineTarget int      `json:"baseline_target"`
	JudgeModel     string   `json:"judge_model"`
}

func (a *App) handleExperiments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := a.svc.db.listExperiments(limit)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		writeJSON(w, 200, rows)
	case http.MethodPost:
		var input experimentInput
		if !decodeBody(w, r, &input) {
			return
		}
		item, err := a.svc.createExperiment(input.SuiteID, input.Name, "manual", input.Targets, input.Repetitions, input.BaselineTarget, input.JudgeModel)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 201, item)
	default:
		httpError(w, 405, errors.New("GET or POST only"))
	}
}

func (a *App) handleExperiment(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/experiments/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		if err := a.svc.db.cancelExperiment(id); err != nil {
			httpError(w, 400, err)
			return
		}
		item, _ := a.svc.db.getExperiment(id)
		writeJSON(w, 200, item)
		return
	}
	if len(parts) == 2 && parts[1] == "compare" && r.Method == http.MethodGet {
		item, err := a.svc.db.getExperiment(id)
		if err != nil {
			httpError(w, 500, err)
			return
		}
		if item == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, item.Summary)
		return
	}
	if len(parts) != 1 || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	item, err := a.svc.db.getExperiment(id)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if item == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, 200, item)
}

func (a *App) handleRun(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/runs/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	run, err := a.svc.db.getRun(parts[0])
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if run == nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 3 && parts[1] == "recordings" && r.Method == http.MethodGet {
		if run.VoiceCall == nil || (parts[2] != "receptionist" && parts[2] != "caller" && parts[2] != "caller-delivered") {
			http.NotFound(w, r)
			return
		}
		var recording struct {
			Data string `json:"data"`
		}
		if err := a.svc.ctx.PlatformAPI().CallAppResult("environments", "environment_voice_recording_get", map[string]any{"id": run.VoiceCall.ID, "speaker": parts[2]}, &recording); err != nil {
			httpError(w, http.StatusBadGateway, err)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(recording.Data)
		if err != nil {
			httpError(w, http.StatusBadGateway, errors.New("invalid environment recording"))
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Cache-Control", "private, max-age=300")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return
	}
	if len(parts) == 2 && parts[1] == "retry" && r.Method == http.MethodPost {
		retried, err := a.svc.db.retryRun(run)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 201, retried)
		return
	}
	if len(parts) != 1 || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, 200, run)
}

func (a *App) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, errors.New("GET only"))
		return
	}
	value, err := a.svc.catalog()
	if err != nil {
		httpError(w, 502, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *App) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, errors.New("POST only"))
		return
	}
	var input map[string]any
	if !decodeBody(w, r, &input) {
		return
	}
	value, err := a.svc.createEnvironment(input)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, 201, value)
}

func (a *App) handleEnvironmentTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, errors.New("GET only"))
		return
	}
	parts := pathParts(r.URL.Path, "/api/environment-tools/")
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	installID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || installID <= 0 {
		httpError(w, 400, errors.New("valid app install id required"))
		return
	}
	tools, err := a.svc.ctx.RuntimeAPI().ListRuntimeCatalogAppTools(installID)
	if err != nil {
		httpError(w, 502, err)
		return
	}
	writeJSON(w, 200, tools)
}

func (a *App) handleAgentCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, errors.New("GET only"))
		return
	}
	parts := pathParts(r.URL.Path, "/api/agent-capabilities/")
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	agentID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || agentID <= 0 {
		httpError(w, 400, errors.New("valid agent id required"))
		return
	}
	capabilities, err := a.svc.ctx.RuntimeAPI().GetRuntimeAgentCapabilities(agentID)
	if err != nil {
		httpError(w, 502, err)
		return
	}
	writeJSON(w, 200, capabilities)
}

func (a *App) handleSuggestion(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/suggestions/")
	if len(parts) != 2 || parts[1] != "apply" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	updated, err := a.svc.applySuggestion(parts[0])
	if err != nil {
		httpError(w, 409, err)
		return
	}
	writeJSON(w, 200, updated)
}

func pathParts(path, prefix string) []string {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return nil
	}
	return strings.Split(rest, "/")
}
