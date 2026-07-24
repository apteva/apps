package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		httpError(w, http.StatusBadRequest, errors.New("invalid JSON"))
		return false
	}
	return true
}

func (a *App) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := a.svc.listDefinitions()
		if err != nil {
			httpError(w, 500, err)
			return
		}
		writeJSON(w, 200, rows)
	case http.MethodPost:
		var d Definition
		if !decodeBody(w, r, &d) {
			return
		}
		saved, err := a.svc.saveDefinition(&d)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		a.svc.ctx.Emit("environment.created", saved)
		writeJSON(w, 201, saved)
	default:
		httpError(w, 405, errors.New("GET or POST only"))
	}
}

func (a *App) handleEnvironment(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/environments/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			d, err := a.svc.getDefinition(id)
			if err != nil {
				httpError(w, 500, err)
				return
			}
			if d == nil {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, 200, d)
		case http.MethodPut, http.MethodPatch:
			var d Definition
			if !decodeBody(w, r, &d) {
				return
			}
			d.ID = id
			saved, err := a.svc.saveDefinition(&d)
			if err != nil {
				httpError(w, 400, err)
				return
			}
			writeJSON(w, 200, saved)
		case http.MethodDelete:
			if active, _ := a.svc.db.activeRun(id); active != nil {
				httpError(w, 409, errors.New("stop the environment before deleting it"))
				return
			}
			if err := a.svc.db.deleteDefinition(id); err != nil {
				httpError(w, 500, err)
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
		default:
			httpError(w, 405, errors.New("unsupported method"))
		}
		return
	}
	if r.Method != http.MethodPost {
		httpError(w, 405, errors.New("POST only"))
		return
	}
	switch parts[1] {
	case "start":
		run, err := a.svc.startDefinition(id)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 201, run)
	case "stop":
		if err := a.svc.stopDefinition(id); err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "restart":
		if err := a.svc.stopDefinition(id); err != nil {
			httpError(w, 400, err)
			return
		}
		run, err := a.svc.startDefinition(id)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 201, run)
	case "snapshot":
		var in struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		x, err := a.svc.snapshot(id, in.Description)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 201, x)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		rows, err := a.svc.db.listRuns()
		if err != nil {
			httpError(w, 500, err)
			return
		}
		for i := range rows {
			a.svc.decorateRun(&rows[i])
		}
		writeJSON(w, 200, rows)
		return
	}
	if r.Method == http.MethodPost {
		var in struct {
			Kind string          `json:"kind"`
			Spec EnvironmentSpec `json:"spec"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.Kind == "" {
			in.Kind = "eval"
		}
		run, err := a.svc.start("", in.Kind, in.Spec)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 201, run)
		return
	}
	httpError(w, 405, errors.New("GET or POST only"))
}
func (a *App) handleRun(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/"), "/")
	id := parts[0]
	run, err := a.svc.db.getRun(id)
	if err != nil {
		httpError(w, 500, err)
		return
	}
	if run == nil {
		http.NotFound(w, r)
		return
	}
	a.svc.decorateRun(run)
	if len(parts) >= 2 && parts[1] == "voice-calls" {
		if len(parts) == 2 && r.Method == http.MethodGet {
			calls, err := a.svc.db.listVoiceCalls(run.ID)
			if err != nil {
				httpError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, calls)
			return
		}
		if len(parts) == 2 && r.Method == http.MethodPost {
			var spec VoiceFixtureSpec
			if !decodeBody(w, r, &spec) {
				return
			}
			call, err := a.svc.runVoiceCall(r.Context(), run, spec)
			if err != nil {
				httpError(w, http.StatusBadGateway, err)
				return
			}
			writeJSON(w, http.StatusCreated, call)
			return
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			call, err := a.svc.db.getVoiceCall(parts[2])
			if err != nil {
				httpError(w, http.StatusInternalServerError, err)
				return
			}
			if call == nil || call.RunID != run.ID {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, http.StatusOK, call)
			return
		}
		httpError(w, http.StatusMethodNotAllowed, errors.New("unsupported voice call operation"))
		return
	}
	if len(parts) >= 2 && parts[1] == "fixtures" {
		if len(parts) == 2 && r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, run.WebFixtures)
			return
		}
		if len(parts) < 3 || parts[2] == "" {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			detail, err := a.svc.fixtureDetail(run.ID, parts[2])
			if err != nil {
				httpError(w, http.StatusNotFound, err)
				return
			}
			writeJSON(w, http.StatusOK, detail)
			return
		}
		if len(parts) == 4 && parts[3] == "reset" && r.Method == http.MethodPost {
			if err := a.svc.resetFixture(run.ID, parts[2]); err != nil {
				httpError(w, http.StatusBadRequest, err)
				return
			}
			detail, _ := a.svc.fixtureDetail(run.ID, parts[2])
			writeJSON(w, http.StatusOK, detail)
			return
		}
		httpError(w, http.StatusMethodNotAllowed, errors.New("unsupported fixture operation"))
		return
	}
	if len(parts) >= 2 && parts[1] == "protocol-fixtures" {
		if len(parts) == 2 && r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, run.ProtocolFixtures)
			return
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			fixture, err := a.svc.db.getProtocolFixture(run.ID, parts[2])
			if err != nil {
				httpError(w, http.StatusInternalServerError, err)
				return
			}
			if fixture == nil {
				http.NotFound(w, r)
				return
			}
			events, err := a.svc.db.listProtocolEvents(run.ID, parts[2], "")
			if err != nil {
				httpError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"fixture": fixture, "events": events})
			return
		}
		httpError(w, http.StatusMethodNotAllowed, errors.New("unsupported protocol fixture operation"))
		return
	}
	if len(parts) == 2 && parts[1] == "inspect" {
		if r.Method != http.MethodGet {
			httpError(w, http.StatusMethodNotAllowed, errors.New("GET only"))
			return
		}
		runtime, err := a.svc.runtime().GetRuntime(run.RuntimeID)
		if err != nil {
			httpError(w, http.StatusBadGateway, err)
			return
		}
		edge, err := a.svc.runtime().ListRuntimeEdgeCalls(run.RuntimeID)
		if err != nil {
			httpError(w, http.StatusBadGateway, err)
			return
		}
		out := map[string]any{"run": run, "runtime": runtime, "edge_calls": edge, "web_fixtures": run.WebFixtures}
		if agent := strings.TrimSpace(r.URL.Query().Get("agent")); agent != "" {
			events, err := a.svc.runtime().ListRuntimeAgentTelemetry(run.RuntimeID, agent, time.Time{}, 500)
			if err != nil {
				httpError(w, http.StatusBadGateway, err)
				return
			}
			out["telemetry"] = events
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if r.Method == http.MethodDelete || r.Method == http.MethodPost {
		if err := a.svc.stopRun(run); err != nil {
			httpError(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method == http.MethodGet {
		out := map[string]any{"run": run}
		if rt, err := a.svc.runtime().GetRuntime(run.RuntimeID); err == nil {
			out["runtime"] = rt
		}
		writeJSON(w, 200, out)
		return
	}
	httpError(w, 405, errors.New("unsupported method"))
}

func (a *App) handleVoiceRecording(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, errors.New("GET only"))
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/voice-recordings/"), "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	path, err := a.svc.voiceRecordingPath(parts[0], strings.TrimSuffix(parts[1], ".wav"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), file)
}

func (a *App) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, errors.New("GET only"))
		return
	}
	apps, err := a.svc.runtime().ListRuntimeCatalogApps(a.svc.ctx.CurrentProject())
	if err != nil {
		httpError(w, 502, err)
		return
	}
	connections, err := a.svc.ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: a.svc.ctx.CurrentProject()})
	if err != nil {
		httpError(w, 502, err)
		return
	}
	integrations, err := a.svc.runtime().ListRuntimeCatalogIntegrations()
	if err != nil {
		httpError(w, 502, err)
		return
	}
	managedMCPs, err := a.svc.runtime().ListRuntimeCatalogManagedMCPServers(a.svc.ctx.CurrentProject())
	if err != nil {
		httpError(w, 502, err)
		return
	}
	agents, err := a.svc.runtime().ListRuntimeCatalogAgents(a.svc.ctx.CurrentProject())
	if err != nil {
		httpError(w, 502, err)
		return
	}
	snapshots, err := a.svc.runtime().ListRuntimeSnapshots()
	if err != nil {
		httpError(w, 502, err)
		return
	}
	realtimeProviders, _ := a.svc.runtime().ListRuntimeRealtimeProviders(a.svc.ctx.CurrentProject())
	writeJSON(w, 200, map[string]any{"apps": apps, "connections": connections, "integrations": integrations, "managed_mcps": managedMCPs, "agents": agents, "snapshots": snapshots, "web_fixtures": webFixtureCatalog(), "realtime_providers": realtimeProviders})
}
func (a *App) handleCatalogItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, errors.New("GET only"))
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/catalog/"), "/"), "/")
	if len(parts) != 3 || parts[2] != "tools" {
		http.NotFound(w, r)
		return
	}
	switch parts[0] {
	case "apps":
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			httpError(w, 400, err)
			return
		}
		tools, err := a.svc.runtime().ListRuntimeCatalogAppTools(id)
		if err != nil {
			httpError(w, 502, err)
			return
		}
		writeJSON(w, 200, tools)
	case "integrations":
		tools, err := a.svc.runtime().ListRuntimeCatalogIntegrationTools(parts[1])
		if err != nil {
			httpError(w, 502, err)
			return
		}
		writeJSON(w, 200, tools)
	default:
		http.NotFound(w, r)
	}
}
func (a *App) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, errors.New("GET only"))
		return
	}
	rows, err := a.svc.runtime().ListRuntimeSnapshots()
	if err != nil {
		httpError(w, 502, err)
		return
	}
	writeJSON(w, 200, rows)
}
func (a *App) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httpError(w, 405, errors.New("DELETE only"))
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/snapshots/"), "/")
	if err := a.svc.runtime().DeleteRuntimeSnapshot(id); err != nil {
		httpError(w, 400, err)
		return
	}
	_ = a.svc.db.deleteSnapshot(id)
	_ = a.svc.db.deleteWebFixtureSnapshots(id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleLegacyImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	var in struct {
		MigrationID string       `json:"migration_id"`
		Definitions []Definition `json:"definitions"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.MigrationID == "" {
		httpError(w, http.StatusBadRequest, errors.New("migration_id required"))
		return
	}
	key := "legacy_import:" + in.MigrationID
	if done, err := a.svc.db.setting(key); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	} else if done != "" {
		writeJSON(w, http.StatusOK, map[string]any{"already_imported": true, "count": done})
		return
	}
	imported := 0
	for i := range in.Definitions {
		d := in.Definitions[i]
		d.DesiredState = "stopped"
		if existing, err := a.svc.db.getDefinition(d.ID); err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		} else if existing != nil {
			continue
		}
		if _, err := a.svc.saveDefinition(&d); err != nil {
			httpError(w, http.StatusBadRequest, errors.New("import "+d.ID+": "+err.Error()))
			return
		}
		imported++
	}
	if err := a.svc.db.setSetting(key, strconv.Itoa(imported)); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported})
}
