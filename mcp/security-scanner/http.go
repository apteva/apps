package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/overview", Handler: a.handleOverview},
		{Pattern: "/targets", Handler: a.handleTargets},
		{Pattern: "/targets/", Handler: a.handleTargetItem},
		{Pattern: "/settings", Handler: a.handleSettings},
		{Pattern: "/scope-check", Handler: a.handleScopeCheck},
		{Pattern: "/scans", Handler: a.handleScans},
		{Pattern: "/scans/", Handler: a.handleScanItem},
		{Pattern: "/hosts", Handler: a.handleHosts},
		{Pattern: "/findings", Handler: a.handleFindings},
		{Pattern: "/findings/", Handler: a.handleFindingItem},
		{Pattern: "/report", Handler: a.handleReport},
		{Pattern: "/diff", Handler: a.handleDiff},
	}
}

// requireSession lets only signed-in dashboard users (who carry the
// platform session cookie) change scope or limits. Agents and API-key
// callers can read these settings but not edit them.
func requireSession(w http.ResponseWriter, r *http.Request) bool {
	if c, err := r.Cookie("session"); err == nil && c.Value != "" {
		return true
	}
	http.Error(w, "only a signed-in person can change the allowlist or limits (use the Security panel)", http.StatusForbidden)
	return false
}

func requestCtx(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return globalCtx.WithProject(pid)
	}
	return globalCtx
}

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	out, err := a.toolOverview(requestCtx(r), nil)
	respond(w, out, err)
}

func (a *App) handleTargets(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolTargetsList(ctx, nil)
		respond(w, out, err)
	case http.MethodPost:
		if !requireSession(w, r) {
			return
		}
		args, err := decodeBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		kind := defaultString(stringArg(args, "kind"), "allow")
		values := stringSliceArg(args, "values")
		if v := stringArg(args, "value"); v != "" {
			values = append(values, v)
		}
		if len(values) == 0 {
			writeError(w, errors.New("value required"), http.StatusBadRequest)
			return
		}
		added := []*Target{}
		for _, v := range values {
			t, err := addTarget(ctx.AppDB(), ctx.CurrentProject(), kind, v, stringArg(args, "note"))
			if err != nil {
				writeError(w, err, http.StatusBadRequest)
				return
			}
			added = append(added, t)
		}
		writeJSON(w, map[string]any{"added": added})
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTargetItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE required", http.StatusMethodNotAllowed)
		return
	}
	if !requireSession(w, r) {
		return
	}
	ctx := requestCtx(r)
	id, _ := pathIDAction(r.URL.Path, "/targets/")
	err := removeTarget(ctx.AppDB(), ctx.CurrentProject(), id)
	respond(w, map[string]any{"removed": err == nil, "id": id}, err)
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	switch r.Method {
	case http.MethodGet:
		s, err := getSettings(ctx.AppDB(), ctx.CurrentProject())
		respond(w, map[string]any{"settings": s, "ceilings": map[string]int{"rate_pps": maxRatePPS, "parallel_hosts": maxParallelHosts, "max_addresses": maxAddressLimit}}, err)
	case http.MethodPut, http.MethodPatch:
		if !requireSession(w, r) {
			return
		}
		args, err := decodeBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		s, err := saveSettings(ctx.AppDB(), ctx.CurrentProject(), args)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"settings": s})
	default:
		http.Error(w, "GET or PUT required", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleScopeCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	args, err := decodeBody(r)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	out, err := a.toolScopeCheck(requestCtx(r), args)
	respond(w, out, err)
}

func (a *App) handleScans(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := a.toolScansList(ctx, map[string]any{"status": r.URL.Query().Get("status"), "limit": limit})
		respond(w, out, err)
	case http.MethodPost:
		args, err := decodeBody(r)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		startedBy := "API"
		if c, err := r.Cookie("session"); err == nil && c.Value != "" {
			startedBy = "panel user"
		}
		out, err := a.startScan(ctx, args, startedBy)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleScanItem(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	if strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/scans/"), "/") == "stop-all" && r.Method == http.MethodPost {
		writeJSON(w, map[string]any{"cancelled": a.scans.cancelAll(ctx.AppDB(), ctx.CurrentProject())})
		return
	}
	id, action := pathIDAction(r.URL.Path, "/scans/")
	switch {
	case r.Method == http.MethodGet && action == "":
		out, err := a.toolScanStatus(ctx, map[string]any{"id": id})
		respond(w, out, err)
	case r.Method == http.MethodPost && action == "cancel":
		out, err := a.toolScanCancel(ctx, map[string]any{"id": id})
		respond(w, out, err)
	default:
		http.Error(w, "unsupported scan operation", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleHosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := a.toolHostsSearch(requestCtx(r), map[string]any{"q": q.Get("q"), "include_closed": q.Get("include_closed") == "true", "limit": limit})
	respond(w, out, err)
}

func (a *App) handleFindings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := map[string]any{}
	for _, k := range []string{"severity", "status", "address", "cve", "q"} {
		if v := q.Get(k); v != "" {
			args[k] = v
		}
	}
	for _, k := range []string{"port", "scan_id", "limit", "offset"} {
		if v := q.Get(k); v != "" {
			args[k] = v
		}
	}
	out, err := a.toolFindingsSearch(requestCtx(r), args)
	respond(w, out, err)
}

func (a *App) handleFindingItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		http.Error(w, "PATCH required", http.StatusMethodNotAllowed)
		return
	}
	args, err := decodeBody(r)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	id, _ := pathIDAction(r.URL.Path, "/findings/")
	args["id"] = id
	out, err := a.toolFindingsUpdate(requestCtx(r), args)
	respond(w, out, err)
}

func (a *App) handleReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := a.toolReport(requestCtx(r), map[string]any{"scan_id": q.Get("scan_id"), "include_info": q.Get("include_info") == "true"})
	if err == nil && q.Get("format") == "md" {
		m := out.(map[string]any)
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="security-report.md"`)
		_, _ = io.WriteString(w, m["markdown"].(string))
		return
	}
	respond(w, out, err)
}

func (a *App) handleDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := a.toolDiff(requestCtx(r), map[string]any{"scan_id": q.Get("scan_id"), "against_scan_id": q.Get("against_scan_id")})
	respond(w, out, err)
}

func respond(w http.ResponseWriter, out any, err error) {
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

func decodeBody(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	args := map[string]any{}
	if len(strings.TrimSpace(string(body))) == 0 {
		return args, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&args); err != nil {
		return nil, errors.New("invalid JSON body")
	}
	return args, nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error, status int) {
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
		err = errors.New("not found")
	}
	http.Error(w, err.Error(), status)
}

func pathIDAction(path, prefix string) (int64, string) {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	return id, action
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
