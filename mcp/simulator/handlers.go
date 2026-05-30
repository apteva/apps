package main

// HTTP read endpoints backing the standalone SimulatorPanel. These
// mirror a subset of the MCP tools over plain GET/POST so the panel
// can use the dashboard's same-origin session instead of the MCP
// transport. Write-ish actions (boot, shutdown, stream-url) post here
// and delegate to the same code the tools call.
//
// Routes (sidecar-relative; reached at /api/apps/simulator<route>):
//   GET  /api/capabilities          → probeCapabilities
//   GET  /api/sims                  → list sims for the project
//   POST /api/sims/boot             → boot {platform,image,device_type}
//   POST /api/sims/<id>/shutdown    → shutdown
//   GET  /api/sims/<id>/screenshot  → PNG bytes
//   POST /api/sims/<id>/stream-url  → mint a stream URL
//   GET  /api/sims/<id>/logs?lines= → device logs

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, probeCapabilities(a.appCtx))
}

func (a *App) handleSimsList(w http.ResponseWriter, r *http.Request) {
	proj, err := resolveProjectFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sims, err := dbListSims(a.appCtx.AppDB(), proj)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	a.refreshListedSimStatuses(sims)
	writeJSON(w, http.StatusOK, map[string]any{"sims": sims})
}

func (a *App) handleSimsBootHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Platform   string `json:"platform"`
		Image      string `json:"image"`
		DeviceType string `json:"device_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := capabilityCheckFor(a.appCtx, body.Platform); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	var sim *Sim
	var err error
	switch body.Platform {
	case "android":
		img := orConfig(a.appCtx, body.Image, "android_image")
		dt := orConfig(a.appCtx, body.DeviceType, "android_device_type")
		sim, err = a.bootAndroid(a.appCtx, img, dt)
	case "ios":
		rt := orConfig(a.appCtx, body.Image, "ios_runtime")
		dt := orConfig(a.appCtx, body.DeviceType, "ios_device_type")
		sim, err = a.bootIOS(a.appCtx, rt, dt)
	default:
		writeErr(w, http.StatusBadRequest, errBadPlatform)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sim)
}

func (a *App) handleSimItem(w http.ResponseWriter, r *http.Request) {
	// /api/sims/<id>/<action>
	rest := strings.TrimPrefix(r.URL.Path, "/api/sims/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		writeErr(w, http.StatusBadRequest, errBadPath)
		return
	}
	simID, action := parts[0], parts[1]
	sim, err := dbGetSim(a.appCtx.AppDB(), simID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if sim == nil {
		writeErr(w, http.StatusNotFound, errNotFound)
		return
	}
	sim = a.refreshSimStatus(sim)

	switch action {
	case "shutdown":
		if p := a.sup.get(simID); p != nil {
			a.sup.shutdownProcess(p)
			a.sup.drop(simID)
		}
		switch sim.Platform {
		case "android":
			if sim.Serial != "" {
				_ = shutdownAndroidSim(sim.Serial)
			}
		case "ios":
			_ = shutdownIOSSim(simID)
		}
		_ = dbUpdateSim(a.appCtx.AppDB(), simID, map[string]any{"status": "shutdown", "pid": 0})
		_ = dbDeleteStreamToken(a.appCtx.AppDB(), simID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "screenshot":
		if sim.Status != "booted" {
			writeErr(w, http.StatusConflict, errNotBooted)
			return
		}
		var png []byte
		switch sim.Platform {
		case "android":
			png, err = androidScreenshot(sim.Serial)
		case "ios":
			png, err = iosScreenshot(sim.ID)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)

	case "stream-url":
		if sim.Status != "booted" {
			writeErr(w, http.StatusConflict, errNotBooted)
			return
		}
		stream, err := dbMintStreamToken(a.appCtx.AppDB(), simID, 1*time.Hour)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"stream_url": a.streamURL(a.appCtx, simID, stream.WSToken),
			"expires_at": stream.ExpiresAt,
		})

	case "logs":
		lines := 200
		if v := r.URL.Query().Get("lines"); v != "" {
			if n, e := strconv.Atoi(v); e == nil {
				lines = n
			}
		}
		var content string
		switch sim.Platform {
		case "android":
			content, err = androidLogs(sim.Serial, lines)
		case "ios":
			content, err = iosLogs(sim.ID, lines)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"content": content})

	default:
		writeErr(w, http.StatusNotFound, errBadPath)
	}
}

// ─── helpers ────────────────────────────────────────────────────────

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errNoProject
}

func orConfig(ctx *sdk.AppCtx, val, key string) string {
	return valueOrConfigDefault(ctx, val, key)
}

func (a *App) refreshListedSimStatuses(sims []Sim) {
	for i := range sims {
		refreshed := a.refreshSimStatus(&sims[i])
		if refreshed != nil {
			sims[i] = *refreshed
		}
	}
}

func (a *App) refreshSimStatus(sim *Sim) *Sim {
	if sim == nil || a == nil || a.appCtx == nil || a.appCtx.AppDB() == nil {
		return sim
	}
	if sim.Platform != "ios" || (sim.Status != "booting" && sim.Status != "booted") {
		return sim
	}
	state, err := simctlDeviceState(sim.ID)
	if err != nil {
		return sim
	}
	switch state {
	case "Booted":
		if sim.Status != "booted" {
			_ = dbUpdateSim(a.appCtx.AppDB(), sim.ID, map[string]any{"status": "booted", "error": ""})
			sim.Status = "booted"
			sim.Error = ""
		}
	case "Shutdown":
		if sim.Status != "shutdown" {
			_ = dbUpdateSim(a.appCtx.AppDB(), sim.ID, map[string]any{"status": "shutdown", "error": ""})
			sim.Status = "shutdown"
			sim.Error = ""
		}
	}
	return sim
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"error": err.Error()})
}

var (
	errBadPlatform = &simError{"platform must be android or ios"}
	errBadPath     = &simError{"bad path"}
	errNotFound    = &simError{"sim not found"}
	errNotBooted   = &simError{"sim not booted"}
	errNoProject   = &simError{"project_id required in query string when install scope=global"}
)
