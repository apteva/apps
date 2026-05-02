package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// REST surface — mirror of the MCP tools.
//
//   /api/deployments                          collection
//   /api/deployments/<id-or-name>             one deployment + sub-actions
//   /api/builds/<id>                          build detail / log
//   /api/releases/<id>                        release detail / log

func (a *App) handleDeploymentsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.httpListDeployments(w, r)
	case http.MethodPost:
		a.httpCreateDeployment(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleDeploymentItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/deployments/")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "id or name required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	key := parts[0]
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}

	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := lookupDeploymentByKey(pid, key)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}

	switch {
	case tail == "":
		a.httpDeploymentDetail(w, r, d)
	case tail == "build":
		a.httpDeploymentBuild(w, r, d)
	case tail == "release":
		a.httpDeploymentRelease(w, r, d)
	case tail == "stop":
		a.httpDeploymentStop(w, r, d)
	case tail == "logs":
		a.httpDeploymentLogs(w, r, d)
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}

func (a *App) handleBuildItem(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/builds/")
	tail := ""
	if i := strings.Index(idStr, "/"); i >= 0 {
		tail = idStr[i+1:]
		idStr = idStr[:i]
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "build id required")
		return
	}
	build, err := dbGetBuild(globalCtx.AppDB(), id)
	if err != nil || build == nil {
		httpErr(w, http.StatusNotFound, "build not found")
		return
	}
	switch tail {
	case "":
		httpJSON(w, map[string]any{"build": build})
	case "log":
		body, _ := tailFile(build.LogPath, queryInt(r, "tail", 200))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}

func (a *App) handleReleaseItem(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/releases/")
	tail := ""
	if i := strings.Index(idStr, "/"); i >= 0 {
		tail = idStr[i+1:]
		idStr = idStr[:i]
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "release id required")
		return
	}
	rel, err := dbGetRelease(globalCtx.AppDB(), id)
	if err != nil || rel == nil {
		httpErr(w, http.StatusNotFound, "release not found")
		return
	}
	switch tail {
	case "":
		httpJSON(w, map[string]any{"release": rel})
	case "log":
		body, _ := tailFile(rel.LogPath, queryInt(r, "tail", 200))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}

// ─── Collection ────────────────────────────────────────────────────

func (a *App) httpListDeployments(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	include := r.URL.Query().Get("archived") == "1"
	rows, err := dbListDeployments(globalCtx.AppDB(), pid, include)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"deployments": rows, "count": len(rows)})
}

func (a *App) httpCreateDeployment(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		SourceKind  string `json:"source_kind"`
		SourceRef   string `json:"source_ref"`
		Framework   string `json:"framework"`
		BuildCmd    string `json:"build_cmd"`
		StartCmd    string `json:"start_cmd"`
		PortHint    int    `json:"port_hint"`
		EnvJSON     string `json:"env_json"`
		Domain      string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := validateName(body.Name); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := dbCreateDeployment(globalCtx.AppDB(), pid, CreateDeploymentInput{
		Name: body.Name, Description: body.Description,
		SourceKind: body.SourceKind, SourceRef: body.SourceRef,
		Framework: body.Framework,
		BuildCmd:  body.BuildCmd, StartCmd: body.StartCmd,
		PortHint: body.PortHint, EnvJSON: body.EnvJSON, Domain: body.Domain,
	})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emit("deploy.created", map[string]any{"deployment_id": d.ID, "name": d.Name, "source_kind": d.SourceKind})
	httpJSON(w, map[string]any{"deployment": d})
}

// ─── Item ──────────────────────────────────────────────────────────

func (a *App) httpDeploymentDetail(w http.ResponseWriter, r *http.Request, d *Deployment) {
	switch r.Method {
	case http.MethodGet:
		builds, _ := dbListBuilds(globalCtx.AppDB(), d.ID, 10)
		releases, _ := dbListReleases(globalCtx.AppDB(), d.ID, 10)
		var current *Release
		if d.CurrentReleaseID != nil {
			current, _ = dbGetRelease(globalCtx.AppDB(), *d.CurrentReleaseID)
		}
		httpJSON(w, map[string]any{
			"deployment":      d,
			"builds":          builds,
			"releases":        releases,
			"current_release": current,
			"url":             a.deploymentURL(d, current),
		})
	case http.MethodDelete:
		if d.CurrentReleaseID != nil {
			if rr := a.registry.Get(*d.CurrentReleaseID); rr != nil {
				_ = a.runtime.Stop(rr)
			}
			a.markStopped(*d.CurrentReleaseID)
		}
		_ = dbDeleteDeployment(globalCtx.AppDB(), d.ProjectID, d.ID)
		emit("deploy.destroyed", map[string]any{"deployment_id": d.ID, "name": d.Name})
		httpJSON(w, map[string]any{"destroyed": true, "id": d.ID})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or DELETE")
	}
}

func (a *App) httpDeploymentBuild(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Release bool `json:"release"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	build, err := a.runBuild(d)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	res := map[string]any{"build": build}
	if body.Release && build.Status == "succeeded" {
		rel, err := a.runRelease(d, build)
		if err != nil {
			res["release_error"] = err.Error()
		} else {
			res["release"] = rel
			res["url"] = a.deploymentURL(d, rel)
		}
	}
	httpJSON(w, res)
}

func (a *App) httpDeploymentRelease(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		BuildID int64 `json:"build_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.BuildID == 0 {
		httpErr(w, http.StatusBadRequest, "build_id required")
		return
	}
	build, err := dbGetBuild(globalCtx.AppDB(), body.BuildID)
	if err != nil || build == nil || build.DeploymentID != d.ID {
		httpErr(w, http.StatusBadRequest, "build does not belong to deployment")
		return
	}
	rel, err := a.runRelease(d, build)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"release": rel, "url": a.deploymentURL(d, rel)})
}

func (a *App) httpDeploymentStop(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if d.CurrentReleaseID == nil {
		httpJSON(w, map[string]any{"stopped": false, "reason": "no live release"})
		return
	}
	rid := *d.CurrentReleaseID
	if rr := a.registry.Get(rid); rr != nil {
		_ = a.runtime.Stop(rr)
	}
	a.markStopped(rid)
	_ = dbSetCurrentRelease(globalCtx.AppDB(), d.ID, nil)
	httpJSON(w, map[string]any{"stopped": true, "release_id": rid})
}

func (a *App) httpDeploymentLogs(w http.ResponseWriter, r *http.Request, d *Deployment) {
	// Default: tail current release's log; ?build_id= or ?release_id= overrides.
	tail := queryInt(r, "tail", 200)
	if bid := queryInt(r, "build_id", 0); bid != 0 {
		b, err := dbGetBuild(globalCtx.AppDB(), int64(bid))
		if err != nil || b == nil || b.DeploymentID != d.ID {
			httpErr(w, http.StatusNotFound, "build not found")
			return
		}
		body, _ := tailFile(b.LogPath, tail)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
		return
	}
	rid := queryInt(r, "release_id", 0)
	if rid == 0 && d.CurrentReleaseID != nil {
		rid = int(*d.CurrentReleaseID)
	}
	if rid == 0 {
		httpErr(w, http.StatusNotFound, "no release to read logs from")
		return
	}
	rel, err := dbGetRelease(globalCtx.AppDB(), int64(rid))
	if err != nil || rel == nil || rel.DeploymentID != d.ID {
		httpErr(w, http.StatusNotFound, "release not found")
		return
	}
	body, _ := tailFile(rel.LogPath, tail)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

// ─── helpers ──────────────────────────────────────────────────────

func lookupDeploymentByKey(projectID, key string) (*Deployment, error) {
	if id, err := strconv.ParseInt(key, 10, 64); err == nil && id > 0 {
		d, err := dbGetDeployment(globalCtx.AppDB(), projectID, id)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, errNotFound("deployment", key)
		}
		return d, nil
	}
	d, err := dbGetDeploymentByName(globalCtx.AppDB(), projectID, key)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, errNotFound("deployment", key)
	}
	return d, nil
}

type notFoundErr struct{ kind, key string }

func (e *notFoundErr) Error() string { return e.kind + " " + e.key + " not found" }
func errNotFound(kind, key string) error { return &notFoundErr{kind: kind, key: key} }

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
