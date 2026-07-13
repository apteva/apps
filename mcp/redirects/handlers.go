package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// REST surface — mirror of the MCP tools used by the panel and by
// anything that prefers HTTP over CallApp.
//
// Path scheme:
//   GET    /api/redirects                       list (optional ?hostname=, ?project_id=, ?limit, ?offset)
//   POST   /api/redirects                       create  {hostname, destination, ...}
//   GET    /api/redirects/<id>                  one rule
//   PUT    /api/redirects/<id>                  update  {…fields to change}
//   DELETE /api/redirects/<id>                  remove

func (a *App) handleRedirectsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.httpListRedirects(w, r)
	case http.MethodPost:
		a.httpCreateRedirect(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleRedirectItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/redirects/")
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.httpGetRedirect(w, r, id)
	case http.MethodPut:
		a.httpUpdateRedirect(w, r, id)
	case http.MethodDelete:
		a.httpDeleteRedirect(w, r, id)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PUT, or DELETE")
	}
}

// handleMeta surfaces install-time context the panel needs before
// drawing the add form — chiefly "is domains installed, and which
// apexes does it manage." Returns {domains_available, domains} plus
// the public host/IP the DNS record would point at.
func (a *App) handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	pid := projectFromRequest(r)
	names := domainsList(globalCtx, pid)
	ingress := map[string]string{}
	if globalCtx != nil && globalCtx.PlatformAPI() != nil {
		if routes, err := globalCtx.PlatformAPI().ListIngressRoutes(); err == nil {
			for _, route := range routes {
				if route.ProjectID == pid || route.ProjectID == "" {
					status := route.Status
					if status == "" {
						status = "active"
					}
					ingress[normaliseHostname(route.Hostname)] = status
				}
			}
		}
	}
	httpJSON(w, map[string]any{
		"domains_available": len(names) > 0,
		"domains":           names,
		"public_host":       platformPublicHost(),
		"ingress":           ingress,
	})
}

func (a *App) handleRedirectTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Hostname string `json:"hostname"`
		Path     string `json:"path"`
		Query    string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	host := normaliseHostname(body.Hostname)
	if err := validateHostname(host); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rule, err := matchRedirectInProject(globalCtx.AppDB(), host, body.Path, projectFromRequest(r))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rule == nil {
		httpJSON(w, map[string]any{"matched": false})
		return
	}
	httpJSON(w, map[string]any{"matched": true, "redirect": rule, "location": applyRule(rule, body.Path, body.Query), "status_code": rule.StatusCode})
}

// ─── REST handlers ─────────────────────────────────────────────────

func (a *App) httpListRedirects(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("hostname")
	project := projectFromRequest(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 250 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := dbListRedirects(globalCtx.AppDB(), hostname, project, limit, offset)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := dbCountRedirects(globalCtx.AppDB(), hostname, project)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"redirects": rows, "count": len(rows), "total": total, "limit": limit, "offset": offset})
}

func (a *App) httpCreateRedirect(w http.ResponseWriter, r *http.Request) {
	var body redirectCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	in := body.toInput(projectFromRequest(r))
	rule, err := dbInsertRedirect(globalCtx.AppDB(), in)
	if err != nil {
		switch {
		case errors.Is(err, ErrConflict):
			httpErr(w, http.StatusConflict, "conflict: a redirect already exists at this hostname+path+match")
		case errors.Is(err, ErrHostnameOwned):
			httpErr(w, http.StatusConflict, err.Error())
		default:
			httpErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	// Best-effort glue — claim the hostname with platform ingress and
	// wire DNS via domains when possible. Failures don't roll back the rule;
	// the panel surfaces a warning so the operator can retry.
	wireWarning := wireHostname(globalCtx, rule.ProjectID, rule.Hostname)
	emitRuleChange(globalCtx, "rule.created", rule)
	httpJSON(w, map[string]any{"redirect": rule, "warning": wireWarning})
}

func (a *App) httpGetRedirect(w http.ResponseWriter, r *http.Request, id int64) {
	rule, err := dbGetRedirect(globalCtx.AppDB(), id, projectFromRequest(r))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpErr(w, http.StatusNotFound, "redirect not found")
			return
		}
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"redirect": rule})
}

func (a *App) httpUpdateRedirect(w http.ResponseWriter, r *http.Request, id int64) {
	var body redirectPatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	projectID := projectFromRequest(r)
	existing, err := dbGetRedirect(globalCtx.AppDB(), id, projectID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpErr(w, http.StatusNotFound, "redirect not found")
			return
		}
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	rule, err := dbUpdateRedirect(globalCtx.AppDB(), id, projectID, body.toPatch())
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			httpErr(w, http.StatusNotFound, "redirect not found")
		case errors.Is(err, ErrConflict):
			httpErr(w, http.StatusConflict, "conflict: a redirect already exists at this hostname+path+match")
		case errors.Is(err, ErrHostnameOwned):
			httpErr(w, http.StatusConflict, err.Error())
		default:
			httpErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if existing.Hostname != rule.Hostname || existing.ProjectID != rule.ProjectID {
		maybeUnwireHostname(globalCtx, existing.Hostname, existing.ProjectID)
	}
	wireWarning := wireHostname(globalCtx, rule.ProjectID, rule.Hostname)
	emitRuleChange(globalCtx, "rule.updated", rule)
	httpJSON(w, map[string]any{"redirect": rule, "warning": wireWarning})
}

func (a *App) httpDeleteRedirect(w http.ResponseWriter, r *http.Request, id int64) {
	// Fetch first so we know which hostname we're touching (needed to
	// decide whether the route should be unregistered).
	projectID := projectFromRequest(r)
	existing, err := dbDeleteRedirect(globalCtx.AppDB(), id, projectID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpErr(w, http.StatusNotFound, "redirect not found")
		} else {
			httpErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	hitLastEmit.Delete(existing.ID)
	maybeUnwireHostname(globalCtx, existing.Hostname, existing.ProjectID)
	emitRuleChange(globalCtx, "rule.removed", existing)
	httpJSON(w, map[string]any{"removed": true})
}

// ─── public catch-all ─────────────────────────────────────────────

// handlePublicRedirect is the actual redirect runtime. Inbound HTTP
// for any hostname routed to this sidecar lands here.
func (a *App) handlePublicRedirect(w http.ResponseWriter, r *http.Request) {
	host := inboundHost(r)
	if host == "" {
		httpErr(w, http.StatusBadRequest, "missing host header")
		return
	}
	rule, err := matchRedirect(globalCtx.AppDB(), host, r.URL.Path)
	if err != nil {
		globalCtx.Logger().Warn("matchRedirect", "host", host, "path", r.URL.Path, "err", err.Error())
		httpErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if rule == nil {
		// No rule for this (host, path). 404 with a tiny body so the
		// browser shows something useful but no app branding.
		httpErr(w, http.StatusNotFound, "no redirect configured for "+host+r.URL.Path)
		return
	}
	target := applyRule(rule, r.URL.Path, r.URL.RawQuery)

	// Bounded, batched analytics: redirect latency never waits on SQLite
	// and traffic spikes cannot create an unbounded goroutine backlog.
	a.enqueueHit(globalCtx, rule)

	w.Header().Set("Location", target)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(rule.StatusCode)
}

// ─── request-body shape ────────────────────────────────────────────

type redirectCreateBody struct {
	Hostname      string `json:"hostname"`
	Path          string `json:"path"`
	MatchMode     string `json:"match_mode"`
	Destination   string `json:"destination"`
	StatusCode    int    `json:"status_code"`
	PreservePath  bool   `json:"preserve_path"`
	PreserveQuery *bool  `json:"preserve_query"`
	Notes         string `json:"notes"`
}

func (b redirectCreateBody) toInput(projectID string) RedirectInput {
	preserveQuery := true
	if b.PreserveQuery != nil {
		preserveQuery = *b.PreserveQuery
	}
	return RedirectInput{
		Hostname:      b.Hostname,
		Path:          b.Path,
		MatchMode:     b.MatchMode,
		Destination:   b.Destination,
		StatusCode:    b.StatusCode,
		PreservePath:  b.PreservePath,
		PreserveQuery: preserveQuery,
		ProjectID:     projectID,
		Notes:         b.Notes,
	}
}

type redirectPatchBody struct {
	Hostname      *string `json:"hostname"`
	Path          *string `json:"path"`
	MatchMode     *string `json:"match_mode"`
	Destination   *string `json:"destination"`
	StatusCode    *int    `json:"status_code"`
	PreservePath  *bool   `json:"preserve_path"`
	PreserveQuery *bool   `json:"preserve_query"`
	Notes         *string `json:"notes"`
}

func (b redirectPatchBody) toPatch() RedirectPatch {
	return RedirectPatch{
		Hostname: b.Hostname, Path: b.Path, MatchMode: b.MatchMode,
		Destination: b.Destination, StatusCode: b.StatusCode,
		PreservePath: b.PreservePath, PreserveQuery: b.PreserveQuery, Notes: b.Notes,
	}
}

// ─── helpers ──────────────────────────────────────────────────────

// inboundHost prefers Host but falls back to forwarded headers if the
// proxy has rewritten Host. Strips ports — rules are matched on the
// bare hostname only.
func inboundHost(r *http.Request) string {
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	if h := r.Header.Get("X-Original-Host"); h != "" {
		host = h
	}
	if i := strings.Index(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.ToLower(strings.TrimSpace(host))
}

// projectFromRequest resolves the owning project_id for the request.
// The proxy owns X-Apteva-Project-ID and strips client-supplied values.
// Prefer it over the query string for global installs; direct local dev
// can still use ?project_id when no trusted header is present.
func projectFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		return v
	}
	return ""
}
