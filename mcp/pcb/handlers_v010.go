package main

import (
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// handleDesignV010 serves the Release workspace routes under
// /api/designs/{id}/{action}. GET reads take ?revision_id=; POST actions take
// a JSON body with the same arguments as the matching MCP tool.
func (a *App) handleDesignV010(w http.ResponseWriter, r *http.Request, app *sdk.AppCtx, id int64, action string) {
	args := map[string]any{}
	switch r.Method {
	case http.MethodGet:
		if rev, _ := strconv.ParseInt(r.URL.Query().Get("revision_id"), 10, 64); rev > 0 {
			args["revision_id"] = float64(rev)
		}
	case http.MethodPost:
		if err := decodeBody(r, &args); err != nil {
			writeHTTPError(w, err)
			return
		}
		if args == nil {
			args = map[string]any{}
		}
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
		return
	}
	args["design_id"] = float64(id)
	type handler func() (any, error)
	get := map[string]handler{
		"release-overview": func() (any, error) { return a.toolReleaseOverview(r.Context(), app, args) },
		"bringup":          func() (any, error) { return a.toolBringUpStatus(r.Context(), app, args) },
		"orders":           func() (any, error) { return a.toolOrdersList(r.Context(), app, args) },
		"fab-profiles":     func() (any, error) { return a.toolFabProfiles(r.Context(), app, args) },
	}
	post := map[string]handler{
		"bringup":         func() (any, error) { return a.toolBringUpRecord(r.Context(), app, args) },
		"orders":          func() (any, error) { return a.toolOrderPrepare(r.Context(), app, args) },
		"assembly":        func() (any, error) { return a.toolAssemblyGenerate(r.Context(), app, args) },
		"firmware-header": func() (any, error) { return a.toolFirmwareHeader(r.Context(), app, args) },
		"bom-source":      func() (any, error) { return a.toolBOMSource(r.Context(), app, args) },
	}
	table := get
	if r.Method == http.MethodPost {
		table = post
	}
	fn, ok := table[action]
	if !ok {
		allowed := []string{}
		if _, ok := get[action]; ok {
			allowed = append(allowed, http.MethodGet)
		}
		if _, ok := post[action]; ok {
			allowed = append(allowed, http.MethodPost)
		}
		writeMethodNotAllowed(w, allowed...)
		return
	}
	value, err := fn()
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

// handleOrder serves POST /api/orders/{id} (status, quote, order number).
func (a *App) handleOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/orders/"), "/"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, 400, "invalid order id")
		return
	}
	args := map[string]any{}
	if err := decodeBody(r, &args); err != nil {
		writeHTTPError(w, err)
		return
	}
	if args == nil {
		args = map[string]any{}
	}
	args["order_id"] = float64(id)
	value, err := a.toolOrderUpdate(r.Context(), a.requestCtx(r), args)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, value)
}
