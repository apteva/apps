package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// REST surface — mirror of the MCP tools, used by:
//   • the panel (browser → /api/apps/routes/api/routes/* with session auth)
//   • apteva-server's route cache (server → same endpoints with API key auth)
//   • sidecars that prefer REST over CallApp (the platform middleware
//     forwards X-Apteva-App-Install-ID on these requests)
//
// Path scheme:
//   GET    /api/routes                        list (optional ?owner=)
//   POST   /api/routes                        upsert {hostname, target, ...}
//   GET    /api/routes/<hostname>             one route
//   DELETE /api/routes/<hostname>             remove

func (a *App) handleRoutesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.httpListRoutes(w, r)
	case http.MethodPost:
		a.httpUpsertRoute(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleRouteItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/routes/")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "hostname required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.httpGetRoute(w, r, rest)
	case http.MethodDelete:
		a.httpDeleteRoute(w, r, rest)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or DELETE")
	}
}

// ─── handlers ─────────────────────────────────────────────────────

func (a *App) httpListRoutes(w http.ResponseWriter, r *http.Request) {
	var filter *int64
	if v := r.URL.Query().Get("owner"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			filter = &n
		}
	}
	routes, err := dbListRoutes(globalCtx.AppDB(), filter)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"routes": routes, "count": len(routes)})
}

func (a *App) httpUpsertRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hostname  string `json:"hostname"`
		Target    string `json:"target"`
		CertFQDN  string `json:"cert_fqdn"`
		AllowHTTP bool   `json:"allow_http"`
		OwnerKind string `json:"owner_kind"` // panel can override; sidecars usually leave blank
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	owner := callerInstallID(r)
	// owner_kind: trust the platform's middleware-set header for
	// sidecar calls; for panel calls (owner=0) tag as "manual".
	kind := body.OwnerKind
	if kind == "" {
		if owner == 0 {
			kind = "manual"
		} else {
			kind = ownerKindForInstallID(globalCtx, owner)
		}
	}
	in := RegisterInput{
		Hostname:       body.Hostname,
		Target:         body.Target,
		OwnerInstallID: owner,
		OwnerKind:      kind,
		CertFQDN:       body.CertFQDN,
		AllowHTTP:      body.AllowHTTP,
	}
	if in.CertFQDN == "" {
		in.CertFQDN = in.Hostname
	}
	route, action, err := dbUpsertRoute(globalCtx.AppDB(), in)
	if err != nil {
		switch {
		case errors.Is(err, ErrHostnameOwnedElsewhere):
			httpErr(w, http.StatusConflict, "hostname_in_use_by_other_owner")
		default:
			httpErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	a.emitRouteChanged(globalCtx, action, route)
	httpJSON(w, map[string]any{"route": route, "action": action})
}

func (a *App) httpGetRoute(w http.ResponseWriter, r *http.Request, hostname string) {
	route, err := dbGetRouteByHostname(globalCtx.AppDB(), hostname)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if route == nil {
		httpErr(w, http.StatusNotFound, "route not found")
		return
	}
	httpJSON(w, map[string]any{"route": route})
}

func (a *App) httpDeleteRoute(w http.ResponseWriter, r *http.Request, hostname string) {
	owner := callerInstallID(r)
	removed, err := dbDeleteRouteByHostname(globalCtx.AppDB(), hostname, owner)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotOwner):
			// For the panel (owner=0) we want to allow deleting ANY route —
			// the panel is admin UI. We re-attempt with the route's actual
			// owner so the schema check still happens but the panel can
			// clean up.
			if owner == 0 {
				existing, _ := dbGetRouteByHostname(globalCtx.AppDB(), hostname)
				if existing != nil {
					removed, err = dbDeleteRouteByHostname(globalCtx.AppDB(), hostname, existing.OwnerInstallID)
				}
			}
			if err != nil {
				httpErr(w, http.StatusForbidden, "not_owner")
				return
			}
		default:
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if removed {
		a.emitRouteChanged(globalCtx, "removed", &Route{Hostname: hostname, OwnerInstallID: owner})
	}
	httpJSON(w, map[string]any{"removed": removed})
}
