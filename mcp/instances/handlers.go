package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// REST surface — mirror of the MCP tools, used by the panel + by
// other apps that prefer REST over CallApp.
//
//   GET    /api/instances                    list (optional ?provider=, ?status=)
//   POST   /api/instances                    create  {name, provider?, region?, size?, image?}
//   GET    /api/instances/<id>               one
//   DELETE /api/instances/<id>               destroy (local refused)
//   POST   /api/instances/<id>/run           {cmd, timeout_s?}
//   POST   /api/instances/<id>/upload        {path, content_b64}
//   POST   /api/instances/<id>/wait-ready    {timeout_s?}
//   GET    /api/instances/<id>/metrics       last vitals snapshot

func (a *App) handleInstancesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.httpList(w, r)
	case http.MethodPost:
		a.httpCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

// ─── catalog routes (panel-facing) ────────────────────────────────

func (a *App) handleListServerTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	types, err := listServerTypes(globalCtx, r.URL.Query().Get("provider"))
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, map[string]any{"server_types": types, "count": len(types)})
}

func (a *App) handleListLocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	locs, err := listLocations(globalCtx, r.URL.Query().Get("provider"))
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, map[string]any{"locations": locs, "count": len(locs)})
}

func (a *App) handleListImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	imgs, err := listImages(globalCtx, r.URL.Query().Get("provider"))
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, map[string]any{"images": imgs, "count": len(imgs)})
}

func (a *App) handleInstanceItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/instances/")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id, err := parseID(parts[0])
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	switch tail {
	case "":
		switch r.Method {
		case http.MethodGet:
			a.httpGet(w, r, id)
		case http.MethodDelete:
			a.httpDestroy(w, r, id)
		default:
			httpErr(w, http.StatusMethodNotAllowed, "GET or DELETE")
		}
	case "run":
		a.httpRun(w, r, id)
	case "upload":
		a.httpUpload(w, r, id)
	case "wait-ready":
		a.httpWaitReady(w, r, id)
	case "metrics":
		a.httpMetrics(w, r, id)
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}

// ─── handlers ─────────────────────────────────────────────────────

func (a *App) httpList(w http.ResponseWriter, r *http.Request) {
	rows, err := dbListInstances(globalCtx.AppDB(), r.URL.Query().Get("provider"), r.URL.Query().Get("status"))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]*Instance, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.stripSecrets())
	}
	httpJSON(w, map[string]any{"instances": out, "count": len(out)})
}

func (a *App) httpGet(w http.ResponseWriter, r *http.Request, id int64) {
	inst, err := dbGetInstance(globalCtx.AppDB(), id)
	if err != nil {
		if errors.Is(err, ErrInstanceNotFound) {
			httpErr(w, http.StatusNotFound, "instance not found")
		} else {
			httpErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httpJSON(w, map[string]any{"instance": inst.stripSecrets()})
}

func (a *App) httpCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Region   string `json:"region"`
		Size     string `json:"size"`
		Image    string `json:"image"`
		TagsJSON string `json:"tags_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.Name == "" {
		httpErr(w, http.StatusBadRequest, "name required")
		return
	}
	if body.Provider == "" {
		body.Provider = "hetzner"
	}
	if body.Provider == "local" {
		httpErr(w, http.StatusBadRequest, ErrLocalInstanceImmutable.Error())
		return
	}
	in := CreateInstanceInput{
		Name: body.Name, Provider: body.Provider,
		Region: body.Region, Size: body.Size, Image: body.Image,
		TagsJSON: body.TagsJSON,
	}
	switch body.Provider {
	case "hetzner":
		inst, err := hetznerProvision(globalCtx, in)
		if err != nil {
			httpErr(w, http.StatusBadGateway, err.Error())
			return
		}
		httpJSON(w, map[string]any{"instance": inst.stripSecrets()})
	default:
		httpErr(w, http.StatusBadRequest, "unsupported provider")
	}
}

func (a *App) httpDestroy(w http.ResponseWriter, r *http.Request, id int64) {
	if id == 0 {
		httpErr(w, http.StatusBadRequest, ErrLocalInstanceImmutable.Error())
		return
	}
	inst, err := dbGetInstance(globalCtx.AppDB(), id)
	if err != nil {
		httpErr(w, http.StatusNotFound, "instance not found")
		return
	}
	switch inst.Provider {
	case "hetzner":
		if err := hetznerDestroy(globalCtx, inst); err != nil {
			httpErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	if err := dbDeleteInstance(globalCtx.AppDB(), id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"destroyed": true, "id": id})
}

func (a *App) httpRun(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Cmd       string `json:"cmd"`
		TimeoutS  int    `json:"timeout_s"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	timeout := time.Duration(body.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	inst, err := dbGetInstance(globalCtx.AppDB(), id)
	if err != nil {
		httpErr(w, http.StatusNotFound, "instance not found")
		return
	}
	var output string
	var exit int
	if inst.IsLocal() {
		output, exit, err = runLocal(body.Cmd, timeout)
	} else {
		if inst.Status != "ready" {
			httpErr(w, http.StatusConflict, "instance not ready")
			return
		}
		output, exit, err = runSSH(inst, body.Cmd, timeout)
	}
	res := map[string]any{"id": id, "output": output, "exit_code": exit}
	if err != nil {
		res["error"] = err.Error()
	}
	httpJSON(w, res)
}

func (a *App) httpUpload(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Path       string `json:"path"`
		ContentB64 string `json:"content_b64"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Path == "" || body.ContentB64 == "" {
		httpErr(w, http.StatusBadRequest, "path and content_b64 required")
		return
	}
	inst, err := dbGetInstance(globalCtx.AppDB(), id)
	if err != nil {
		httpErr(w, http.StatusNotFound, "instance not found")
		return
	}
	var n int
	if inst.IsLocal() {
		n, err = uploadLocal(globalCtx, body.Path, body.ContentB64)
	} else {
		if inst.Status != "ready" {
			httpErr(w, http.StatusConflict, "instance not ready")
			return
		}
		n, err = uploadSSH(inst, body.Path, body.ContentB64)
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"id": id, "path": body.Path, "bytes_written": n})
}

func (a *App) httpWaitReady(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		TimeoutS int `json:"timeout_s"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	timeout := time.Duration(body.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	inst, err := dbGetInstance(globalCtx.AppDB(), id)
	if err != nil {
		httpErr(w, http.StatusNotFound, "instance not found")
		return
	}
	if inst.IsLocal() || inst.Status == "ready" {
		httpJSON(w, map[string]any{"ready": true, "id": id, "status": inst.Status})
		return
	}
	if err := probeSSHReady(inst, timeout); err != nil {
		httpErr(w, http.StatusGatewayTimeout, err.Error())
		return
	}
	_ = dbUpdateInstance(globalCtx.AppDB(), id, map[string]any{"status": "ready", "ready_at": nowUTC()})
	httpJSON(w, map[string]any{"ready": true, "id": id, "status": "ready"})
}

func (a *App) httpMetrics(w http.ResponseWriter, r *http.Request, id int64) {
	inst, err := dbGetInstance(globalCtx.AppDB(), id)
	if err != nil {
		httpErr(w, http.StatusNotFound, "instance not found")
		return
	}
	m, err := collectMetrics(inst)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"instance_id": id, "metrics": m})
}
