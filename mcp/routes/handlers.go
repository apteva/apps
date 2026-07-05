package main

import (
	"encoding/json"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// REST surface — mirror of the MCP tools, used by:
//   • the panel (browser → /api/apps/routes/api/routes/* with session auth)
//   • sidecars that prefer REST over CallApp
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
	routes, err := globalCtx.PlatformAPI().ListIngressRoutes()
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
		TLSMode   string `json:"tls_mode"`
		TLS       string `json:"tls"`
		OwnerKind string `json:"owner_kind"` // panel can override; sidecars usually leave blank
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	route, err := globalCtx.PlatformAPI().ExposeIngress(sdkIngressRequest(body.Hostname, body.Target, body.OwnerKind, body.CertFQDN, body.TLSMode, body.TLS, body.AllowHTTP))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, routeResponse(route, "exposed"))
}

func (a *App) httpGetRoute(w http.ResponseWriter, r *http.Request, hostname string) {
	routes, err := globalCtx.PlatformAPI().ListIngressRoutes()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range routes {
		if strings.EqualFold(routes[i].Hostname, hostname) {
			httpJSON(w, routeResponse(&routes[i], ""))
			return
		}
	}
	httpErr(w, http.StatusNotFound, "route not found")
}

func (a *App) httpDeleteRoute(w http.ResponseWriter, r *http.Request, hostname string) {
	if err := globalCtx.PlatformAPI().UnexposeIngress(hostname); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"removed": true, "hostname": strings.ToLower(strings.TrimSpace(hostname))})
}

func sdkIngressRequest(hostname, target, ownerKind, certFQDN, tlsMode, tls string, allowHTTP bool) sdk.IngressExposeRequest {
	return sdk.IngressExposeRequest{
		Hostname:  hostname,
		Target:    target,
		OwnerKind: firstNonEmpty(ownerKind, "routes"),
		CertFQDN:  certFQDN,
		TLSMode:   tlsMode,
		TLS:       tls,
		AllowHTTP: allowHTTP,
	}
}
