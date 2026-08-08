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
//   POST /api/sims/<id>/input       → send tap/swipe/key/text input
//   GET  /api/sims/<id>/logs?lines= → device logs
//   POST /api/run                   → multipart source archive build/install/launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.configuredCapabilities(a.appCtx))
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
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Platform   string `json:"platform"`
		Image      string `json:"image"`
		DeviceType string `json:"device_type"`
		HostID     *int64 `json:"host_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	projectID, err := resolveProjectFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	requestCtx := a.appCtx.WithProject(projectID)
	args := map[string]any{}
	if body.HostID != nil {
		args["host_id"] = *body.HostID
	}
	hostID := resolvedHostID(requestCtx, args, body.Platform)
	if err := a.capabilityCheckForHost(requestCtx, body.Platform, hostID, false, false); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	var sim *Sim
	switch body.Platform {
	case "android":
		img := orConfig(requestCtx, body.Image, "android_image")
		dt := orConfig(requestCtx, body.DeviceType, "android_device_type")
		sim, err = a.bootOnHost(requestCtx, body.Platform, img, dt, hostID)
	case "ios":
		rt := orConfig(requestCtx, body.Image, "ios_runtime")
		dt := orConfig(requestCtx, body.DeviceType, "ios_device_type")
		sim, err = a.bootOnHost(requestCtx, body.Platform, rt, dt, hostID)
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

func (a *App) handleRunHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSourceArchiveBytes+(10<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("parse multipart upload: %w", err))
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	framework := strings.TrimSpace(r.FormValue("framework"))
	if framework != "android" && framework != "ios" {
		writeErr(w, http.StatusBadRequest, errBadPlatform)
		return
	}
	proj, err := resolveProjectFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	requestCtx := a.appCtx.WithProject(proj)
	hostArgs := map[string]any{}
	if raw := strings.TrimSpace(r.FormValue("host_id")); raw != "" {
		hostArgs["host_id"] = raw
	}
	hostID := resolvedHostID(requestCtx, hostArgs, framework)
	if err := a.capabilityCheckForHost(requestCtx, framework, hostID, true, true); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	file, header, err := r.FormFile("source")
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("source archive required: %w", err))
		return
	}
	defer file.Close()
	upload, err := os.CreateTemp("", "apteva-sim-source-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	uploadPath := upload.Name()
	defer os.Remove(uploadPath)
	n, copyErr := io.Copy(upload, io.LimitReader(file, maxSourceArchiveBytes+1))
	closeErr := upload.Close()
	if copyErr != nil || closeErr != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("read source archive: %w", errors.Join(copyErr, closeErr)))
		return
	}
	if n > maxSourceArchiveBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Errorf("source archive exceeds %d bytes", maxSourceArchiveBytes))
		return
	}

	sim, err := a.ensureBootedSimOnHost(requestCtx, framework, hostID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("boot: %w", err))
		return
	}
	unlockRun := a.lockSimRun(sim.ID)
	defer unlockRun()
	if err := dbStopActiveSimRuns(requestCtx.AppDB(), sim.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	run, err := dbInsertSimRun(requestCtx.AppDB(), SimRun{
		SimID: sim.ID, ProjectID: proj, SourceApp: "panel",
		SourceRef: header.Filename, Framework: framework, Status: "building",
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
		RunnerKind: sim.RunnerKind, InstanceID: sim.InstanceID,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	srcDir := filepath.Join(os.TempDir(), "apteva-sim-upload-"+randHex(8))
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		a.failRun(requestCtx, run.ID, "extract: "+err.Error())
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer os.RemoveAll(srcDir)
	buildRoot, _, err := extractSourceUploadFile(uploadPath, header.Filename, srcDir)
	if err != nil {
		a.failRun(requestCtx, run.ID, "extract: "+err.Error())
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var (
		br         *buildResult
		artifactID string
	)
	if sim.IsRemote() {
		sourceB64, archiveErr := sourceDirToTarGzB64(buildRoot)
		if archiveErr != nil {
			a.failRun(requestCtx, run.ID, "archive: "+archiveErr.Error())
			writeErr(w, http.StatusInternalServerError, archiveErr)
			return
		}
		br, artifactID, err = a.buildForSim(r.Context(), requestCtx, sim, buildParams{
			Framework: framework, SourceTGZB64: sourceB64,
			Module: r.FormValue("android_module"), Scheme: r.FormValue("ios_scheme"),
			BuildCmd: r.FormValue("build_cmd"), SimUDID: sim.NativeID(), SimRunID: run.ID,
		})
	} else {
		br, err = a.runBuildFromSourceDir(r.Context(), requestCtx, buildRoot, buildParams{
			Framework: framework,
			Module:    r.FormValue("android_module"),
			Scheme:    r.FormValue("ios_scheme"),
			BuildCmd:  r.FormValue("build_cmd"),
			SimUDID:   sim.NativeID(),
			SimRunID:  run.ID,
		})
		if err == nil {
			artifactID = filepath.Base(br.ArtifactPath)
		}
	}
	if err != nil {
		a.failRun(requestCtx, run.ID, "build: "+err.Error())
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = dbUpdateSimRun(requestCtx.AppDB(), run.ID, map[string]any{
		"status":        "installing",
		"bundle_id":     br.BundleID,
		"artifact_path": br.ArtifactPath,
		"artifact_id":   artifactID,
		"log_path":      fmt.Sprintf("%d.log", run.ID),
	})
	if err := a.installAndLaunchForSim(requestCtx, sim, br, artifactID); err != nil {
		a.failRun(requestCtx, run.ID, "install/launch: "+err.Error())
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = dbUpdateSimRun(requestCtx.AppDB(), run.ID, map[string]any{"status": "running"})
	_ = a.cleanupStorage(requestCtx.AppDB())
	stream, err := dbMintStreamToken(requestCtx.AppDB(), sim.ID, time.Hour)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sim_id":        sim.ID,
		"sim_run_id":    run.ID,
		"platform":      sim.Platform,
		"bundle_id":     br.BundleID,
		"artifact_path": br.ArtifactPath,
		"artifact_id":   artifactID,
		"runner":        sim.RunnerKind,
		"instance_id":   sim.InstanceID,
		"stream_url":    a.streamURL(requestCtx, sim.ID, stream.WSToken),
		"status":        "running",
	})
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
	projectID, err := resolveProjectFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if sim.ProjectID != projectID {
		writeErr(w, http.StatusNotFound, errNotFound)
		return
	}
	sim = a.refreshSimStatus(sim)

	switch action {
	case "shutdown":
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, errBadMethod)
			return
		}
		if err := a.shutdownSim(a.appCtx.WithProject(projectID), sim); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "screenshot":
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, errBadMethod)
			return
		}
		if sim.Status != "booted" {
			writeErr(w, http.StatusConflict, errNotBooted)
			return
		}
		png, err := a.screenshotSim(a.appCtx.WithProject(projectID), sim)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)

	case "stream-url":
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, errBadMethod)
			return
		}
		if sim.Status != "booted" {
			writeErr(w, http.StatusConflict, errNotBooted)
			return
		}
		if err := a.capabilityCheckForHost(a.appCtx.WithProject(projectID), sim.Platform, sim.InstanceID, false, true); err != nil {
			writeErr(w, http.StatusConflict, err)
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

	case "input":
		if sim.Status != "booted" {
			writeErr(w, http.StatusConflict, errNotBooted)
			return
		}
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, errBadMethod)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var ev inputEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := a.inputForSim(a.appCtx.WithProject(projectID), sim, ev); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "logs":
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, errBadMethod)
			return
		}
		lines := 200
		if v := r.URL.Query().Get("lines"); v != "" {
			if n, e := strconv.Atoi(v); e == nil {
				lines = normalizeLogLines(n)
			}
		}
		content, err := a.logsForSim(a.appCtx.WithProject(projectID), sim, lines)
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
	if sim.IsRemote() {
		return a.refreshRemoteSim(a.appCtx.WithProject(sim.ProjectID), sim)
	}
	if sim.Platform != "ios" || (sim.Status != "booting" && sim.Status != "booted") {
		return sim
	}
	state, err := simctlDeviceState(sim.NativeID())
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
	errBadMethod   = &simError{"method not allowed"}
	errNotFound    = &simError{"sim not found"}
	errNotBooted   = &simError{"sim not booted"}
	errNoProject   = &simError{"project_id required in query string when install scope=global"}
)
