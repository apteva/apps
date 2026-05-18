// MCP tool handlers + REST endpoints for site CRUD.
//
// Tools: sites_create / list / get / update / archive / set_default
// REST:  /admin/sites          GET, POST
//        /admin/sites/:slug    GET, PATCH, DELETE
//        /admin/sites/:slug/set-default  POST

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ── MCP tools ────────────────────────────────────────────────────

func (a *App) toolSitesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	site, err := dbCreateSite(ctx.AppDB(), pid,
		asString(args["slug"]),
		asString(args["name"]),
		asString(args["hostname"]))
	if err != nil {
		return nil, err
	}
	return map[string]any{"site": site}, nil
}

func (a *App) toolSitesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	includeArchived, _ := args["include_archived"].(bool)
	sites, err := dbListSites(ctx.AppDB(), pid, includeArchived)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sites": sites}, nil
}

func (a *App) toolSitesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if id, ok := asInt64(args["id"]); ok && id > 0 {
		s, err := dbGetSite(ctx.AppDB(), pid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"site": s}, nil
	}
	slug := asString(args["slug"])
	if slug == "" {
		return nil, errors.New("id or slug required")
	}
	s, err := dbGetSiteBySlug(ctx.AppDB(), pid, slug)
	if err != nil {
		return nil, err
	}
	return map[string]any{"site": s}, nil
}

func (a *App) toolSitesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	var name, hostname *string
	if v, ok := args["name"].(string); ok {
		name = &v
	}
	if v, ok := args["hostname"].(string); ok {
		hostname = &v
	}
	s, err := dbUpdateSite(ctx.AppDB(), pid, id, name, hostname)
	if err != nil {
		return nil, err
	}
	invalidatePageCache()
	return map[string]any{"site": s}, nil
}

func (a *App) toolSitesArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbArchiveSite(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	invalidatePageCache()
	return map[string]any{"ok": true, "id": id}, nil
}

func (a *App) toolSitesSetDefault(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbSetDefaultSite(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id}, nil
}

// ── REST handlers ────────────────────────────────────────────────

func (a *App) handleHTTPSites(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		includeArchived := r.URL.Query().Get("include_archived") == "true"
		sites, err := dbListSites(ctx.AppDB(), pid, includeArchived)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"sites": sites})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolSitesCreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPSiteItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/sites/")
	parts := strings.SplitN(rest, "/", 2)
	slug := parts[0]
	if slug == "" {
		httpErr(w, http.StatusBadRequest, "site slug required")
		return
	}
	site, err := dbGetSiteBySlug(ctx.AppDB(), pid, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, "site not found")
		return
	}
	if len(parts) == 2 && parts[1] == "set-default" {
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if err := dbSetDefaultSite(ctx.AppDB(), pid, site.ID); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s, _ := dbGetSite(ctx.AppDB(), pid, site.ID)
		httpJSON(w, map[string]any{"site": s})
		return
	}
	switch r.Method {
	case http.MethodGet:
		httpJSON(w, map[string]any{"site": site})
	case http.MethodPatch, http.MethodPut:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		body["id"] = site.ID
		out, err := a.toolSitesUpdate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodDelete:
		if err := dbArchiveSite(ctx.AppDB(), pid, site.ID); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"ok": true})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
