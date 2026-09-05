package main

import (
	"encoding/json"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── HTTP routes (panel data + tool dispatch) ──────────────────────

func (a *App) handleDomainsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbDomainList(globalCtx.AppDB(), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"domains": out})
}

func (a *App) handleDomainItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/domains/")
	parts := strings.SplitN(rest, "/", 2)
	if parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "name required")
		return
	}
	name, err := normaliseDomainName(parts[0])
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := dbDomainGetByName(globalCtx.AppDB(), pid, name)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if len(parts) == 2 && parts[1] == "records" {
		out, err := a.toolDomainRecordsList(globalCtx, map[string]any{"domain": name, "_project_id": pid})
		if err != nil {
			httpErr(w, errorStatus(err), err.Error())
			return
		}
		httpJSON(w, out)
		return
	}
	httpJSON(w, map[string]any{"domain": d})
}

// handleConnectionsList feeds the panel's connection picker. It returns every
// compatible project connection for backwards compatibility with domains that
// were pinned before multi-bindings existed, and marks the connections selected
// in each multi-binding role so the UI can prioritize them.
func (a *App) handleConnectionsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	type conn struct {
		ID               int64  `json:"id"`
		AppSlug          string `json:"app_slug"`
		Name             string `json:"name"`
		Status           string `json:"status"`
		DNSBound         bool   `json:"dns_bound"`
		DNSDefault       bool   `json:"dns_default"`
		RegistrarBound   bool   `json:"registrar_bound"`
		RegistrarDefault bool   `json:"registrar_default"`
	}
	identity, err := globalCtx.PlatformAPI().WhoAmI()
	if err != nil || identity == nil {
		httpErr(w, 502, "could not read install bindings")
		return
	}
	dnsBound, dnsDefault, err := boundIDs(identity.Bindings["dns_provider"])
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	registrarBound, registrarDefault, err := boundIDs(identity.Bindings["registrar_provider"])
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	out := []conn{}
	{
		rows, err := globalCtx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: pid})
		if err != nil {
			httpErr(w, errorStatus(err), err.Error())
			return
		}
		for _, c := range rows {
			if !includes([]string{"porkbun", "namecheap", "ionos", "spaceship"}, c.AppSlug) {
				continue
			}
			out = append(out, conn{
				ID:               c.ID,
				AppSlug:          c.AppSlug,
				Name:             c.Name,
				Status:           c.Status,
				DNSBound:         dnsBound[c.ID],
				DNSDefault:       c.ID == dnsDefault,
				RegistrarBound:   registrarBound[c.ID],
				RegistrarDefault: c.ID == registrarDefault,
			})
		}
	}
	httpJSON(w, map[string]any{"connections": out})
}

// handleToolsCall — same generic dispatcher messaging uses, so the
// panel can call any tool via a single HTTP path.
func (a *App) handleToolsCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Tool == "" {
		httpErr(w, http.StatusBadRequest, "tool required")
		return
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}
	var handler sdk.ToolHandler
	for _, t := range a.MCPTools() {
		if t.Name == body.Tool {
			handler = t.Handler
			break
		}
	}
	if handler == nil {
		httpErr(w, http.StatusNotFound, "unknown tool: "+body.Tool)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	body.Args["_project_id"] = pid
	out, err := handler(globalCtx.WithProject(pid), body.Args)
	if err != nil {
		httpErr(w, errorStatus(err), err.Error())
		return
	}
	httpJSON(w, out)
}
