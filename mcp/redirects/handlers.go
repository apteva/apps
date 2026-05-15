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
// apexes does it manage." Mirrors the certs app's /api/_meta pattern.
// Returns {domains_available, domains} plus the public host the
// CNAME would point at, so the panel can show users what they'd
// have to configure if they manage DNS themselves.
func (a *App) handleMeta(w http.ResponseWriter, r *http.Request) {
	pid := projectFromRequest(r)
	names := domainsList(globalCtx, pid)
	httpJSON(w, map[string]any{
		"domains_available": len(names) > 0,
		"domains":           names,
		"public_host":       platformPublicHost(),
	})
}

// ─── REST handlers ─────────────────────────────────────────────────

func (a *App) httpListRedirects(w http.ResponseWriter, r *http.Request) {
	hostname := r.URL.Query().Get("hostname")
	project := projectFromRequest(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	rows, err := dbListRedirects(globalCtx.AppDB(), hostname, project, limit, offset)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"redirects": rows, "count": len(rows)})
}

func (a *App) httpCreateRedirect(w http.ResponseWriter, r *http.Request) {
	var body redirectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	in := body.toInput()
	if in.ProjectID == "" {
		in.ProjectID = projectFromRequest(r)
	}
	rule, err := dbInsertRedirect(globalCtx.AppDB(), in)
	if err != nil {
		switch {
		case errors.Is(err, ErrConflict):
			httpErr(w, http.StatusConflict, "conflict: a redirect already exists at this hostname+path+match")
		default:
			httpErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	// Best-effort glue — claim the hostname with routes (and CNAME via
	// domains when possible). Wire failures don't roll back the rule;
	// the panel surfaces a warning so the operator can retry.
	wireWarning := wireHostname(globalCtx, rule.ProjectID, rule.Hostname)
	httpJSON(w, map[string]any{"redirect": rule, "warning": wireWarning})
}

func (a *App) httpGetRedirect(w http.ResponseWriter, r *http.Request, id int64) {
	rule, err := dbGetRedirect(globalCtx.AppDB(), id)
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
	var body redirectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	in := body.toInput()
	rule, err := dbUpdateRedirect(globalCtx.AppDB(), id, in)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			httpErr(w, http.StatusNotFound, "redirect not found")
		case errors.Is(err, ErrConflict):
			httpErr(w, http.StatusConflict, "conflict: a redirect already exists at this hostname+path+match")
		default:
			httpErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	wireWarning := wireHostname(globalCtx, rule.ProjectID, rule.Hostname)
	httpJSON(w, map[string]any{"redirect": rule, "warning": wireWarning})
}

func (a *App) httpDeleteRedirect(w http.ResponseWriter, r *http.Request, id int64) {
	// Fetch first so we know which hostname we're touching (needed to
	// decide whether the route should be unregistered).
	existing, err := dbGetRedirect(globalCtx.AppDB(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpErr(w, http.StatusNotFound, "redirect not found")
			return
		}
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := dbDeleteRedirect(globalCtx.AppDB(), id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	maybeUnwireHostname(globalCtx, existing.Hostname, existing.ProjectID)
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

	// Async hit counter — never let a counter failure block the
	// redirect itself.
	go func(id int64) {
		if err := dbRecordHit(globalCtx.AppDB(), id); err != nil {
			globalCtx.Logger().Info("record hit", "id", id, "err", err.Error())
		}
	}(rule.ID)

	w.Header().Set("Location", target)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(rule.StatusCode)
}

// ─── request-body shape ────────────────────────────────────────────

// redirectBody is the JSON shape for both POST and PUT. Pointer types
// would have let PUT distinguish "leave field" vs "set empty" cleanly
// — but in practice all the fields here have safe "zero means absent"
// semantics (a status_code of 0 isn't valid; an empty hostname isn't
// valid), so plain values keep the body shape simple.
type redirectBody struct {
	Hostname      string `json:"hostname"`
	Path          string `json:"path"`
	MatchMode     string `json:"match_mode"`
	Destination   string `json:"destination"`
	StatusCode    int    `json:"status_code"`
	PreservePath  bool   `json:"preserve_path"`
	PreserveQuery bool   `json:"preserve_query"`
	ProjectID     string `json:"project_id"`
	Notes         string `json:"notes"`
}

func (b redirectBody) toInput() RedirectInput {
	return RedirectInput{
		Hostname:      b.Hostname,
		Path:          b.Path,
		MatchMode:     b.MatchMode,
		Destination:   b.Destination,
		StatusCode:    b.StatusCode,
		PreservePath:  b.PreservePath,
		PreserveQuery: b.PreserveQuery,
		ProjectID:     b.ProjectID,
		Notes:         b.Notes,
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
// Order: APTEVA_PROJECT_ID env (set when scope=project) > ?project_id
// query > X-Apteva-Project-ID header. Empty when scope=global with no
// project supplied.
func projectFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); v != "" {
		return v
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Apteva-Project-ID"); v != "" {
		return v
	}
	return ""
}
