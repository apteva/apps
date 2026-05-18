package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	case tail == "restart":
		a.httpDeploymentRestart(w, r, d)
	case tail == "logs":
		a.httpDeploymentLogs(w, r, d)
	case tail == "attach-domain":
		a.httpDeploymentAttachDomain(w, r, d)
	case tail == "detach-domain":
		a.httpDeploymentDetachDomain(w, r, d)
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
	domainArg := strings.TrimSpace(body.Domain)
	domainsOn := domainArg != "" && a.domainsAvailable(globalCtx)
	in := CreateDeploymentInput{
		Name: body.Name, Description: body.Description,
		SourceKind: body.SourceKind, SourceRef: body.SourceRef,
		Framework: body.Framework,
		BuildCmd:  body.BuildCmd, StartCmd: body.StartCmd,
		PortHint: body.PortHint, EnvJSON: body.EnvJSON,
	}
	if !domainsOn {
		in.Domain = domainArg
	}
	d, err := dbCreateDeployment(globalCtx.AppDB(), pid, in)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emit("deploy.created", map[string]any{"deployment_id": d.ID, "name": d.Name, "source_kind": d.SourceKind})
	resp := map[string]any{"deployment": d}
	if domainsOn {
		attachRes, err := a.attachDomain(globalCtx, d, attachDomainSpec{FQDN: domainArg})
		if err != nil {
			resp["domain_error"] = err.Error()
		} else {
			d, _ = dbGetDeployment(globalCtx.AppDB(), pid, d.ID)
			resp["deployment"] = d
			resp["attach"] = attachRes
		}
	}
	httpJSON(w, resp)
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
	case http.MethodPatch:
		a.httpDeploymentPatch(w, r, d)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH or DELETE")
	}
}

// httpDeploymentPatch mutates an allowlist of deployment fields
// without delete+recreate. The fields are exactly the ones in
// dbUpdateDeployment; unknown keys are ignored (silently — clearer
// API than rejecting). New values take effect on the NEXT release
// build/restart; the live process keeps its env until restarted.
// Use POST /restart (or deploy_restart) to apply config without a
// fresh build.
func (a *App) httpDeploymentPatch(w http.ResponseWriter, r *http.Request, d *Deployment) {
	body, err := patchBodyFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body) == 0 {
		httpErr(w, http.StatusBadRequest, "no mutable fields in body")
		return
	}
	if err := dbUpdateDeployment(globalCtx.AppDB(), d.ProjectID, d.ID, body); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emit("deploy.updated", map[string]any{
		"deployment_id": d.ID, "name": d.Name,
		"fields": keysOf(body),
	})
	fresh, _ := dbGetDeployment(globalCtx.AppDB(), d.ProjectID, d.ID)
	httpJSON(w, map[string]any{
		"deployment": fresh,
		"applied":    keysOf(body),
		"note":       "new values take effect on the next build/release. Call POST /restart to apply now without rebuilding.",
	})
}

// httpDeploymentRestart re-spawns the current release with whatever
// config the deployment row now holds. Stops the live release
// authoritatively (port-free guarantee), then runs runRelease with
// the same build_id, so a config-only change (env_json, port_hint,
// start_cmd) takes effect without a rebuild. Falls back to error if
// there's nothing to restart.
func (a *App) httpDeploymentRestart(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if d.CurrentReleaseID == nil {
		httpErr(w, http.StatusBadRequest, "no current release to restart — run /build or /release first")
		return
	}
	rel, err := dbGetRelease(globalCtx.AppDB(), *d.CurrentReleaseID)
	if err != nil || rel == nil {
		httpErr(w, http.StatusInternalServerError, "current release missing")
		return
	}
	build, err := dbGetBuild(globalCtx.AppDB(), rel.BuildID)
	if err != nil || build == nil {
		httpErr(w, http.StatusInternalServerError, "build for current release missing")
		return
	}
	// Authoritative stop so the port is genuinely free before the
	// respawn binds — same guarantee operator-driven stop has.
	if err := a.stopReleaseAuthoritative(rel, 5*time.Second); err != nil {
		httpErr(w, http.StatusInternalServerError, "stop: "+err.Error())
		return
	}
	a.markStopped(rel.ID)
	// Re-fetch the deployment so runRelease sees the latest env_json /
	// port_hint / start_cmd / etc. — that's the whole point of restart.
	fresh, _ := dbGetDeployment(globalCtx.AppDB(), d.ProjectID, d.ID)
	if fresh == nil {
		fresh = d
	}
	newRel, err := a.runRelease(fresh, build)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "release: "+err.Error())
		return
	}
	emit("deploy.restarted", map[string]any{
		"deployment_id": d.ID, "release_id": newRel.ID, "build_id": build.ID,
	})
	httpJSON(w, map[string]any{
		"release": newRel,
		"url":     a.deploymentURL(fresh, newRel),
	})
}

func patchBodyFromRequest(r *http.Request) (map[string]any, error) {
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	// Filter to the allowlist. PortHint comes in as float64 from JSON;
	// the SQL driver doesn't mind, but coerce to int for clarity.
	allow := map[string]bool{
		"description": true, "framework": true,
		"build_cmd": true, "start_cmd": true,
		"port_hint": true, "env_json": true,
		"source_extra_json": true,
	}
	out := map[string]any{}
	for k, v := range raw {
		if !allow[k] {
			continue
		}
		if k == "port_hint" {
			if f, ok := v.(float64); ok {
				out[k] = int(f)
				continue
			}
		}
		out[k] = v
	}
	return out, nil
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
	rel, _ := dbGetRelease(globalCtx.AppDB(), rid)
	// Authoritative stop: don't return until the port is actually free
	// (or report the failure). Fixes the orphan class where runtime.Stop
	// was a no-op (registry handle missing) and the process kept serving.
	if err := a.stopReleaseAuthoritative(rel, 5*time.Second); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
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

func (a *App) httpDeploymentAttachDomain(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		FQDN   string `json:"fqdn"`
		Target string `json:"target"`
		Type   string `json:"type"`
		TTL    int    `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	attachRes, err := a.attachDomain(globalCtx, d, attachDomainSpec{
		FQDN: body.FQDN, Target: body.Target, Type: body.Type, TTL: body.TTL,
	})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, _ := dbGetDeployment(globalCtx.AppDB(), d.ProjectID, d.ID)
	httpJSON(w, map[string]any{"deployment": out, "attach": attachRes})
}

func (a *App) httpDeploymentDetachDomain(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	res := map[string]any{"detached": true, "id": d.ID, "fqdn": d.Domain}
	if err := a.detachDomain(globalCtx, d); err != nil {
		res["registrar_error"] = err.Error()
	}
	out, _ := dbGetDeployment(globalCtx.AppDB(), d.ProjectID, d.ID)
	res["deployment"] = out
	httpJSON(w, res)
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
