package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// HTTP routes — panel data + a generic tools-dispatcher so the
// panel can invoke any MCP tool via a single POST.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/zones", Handler: a.handleZonesList},
		{Pattern: "/zones/", Handler: a.handleZoneItem},
		{Pattern: "/tools/call", Handler: a.handleToolsCall},
	}
}

func (a *App) handleZonesList(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := dbListZones(globalCtx.AppDB(), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"zones": rows})
}

func (a *App) handleZoneItem(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/zones/")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "zone hostname required")
		return
	}
	z, err := dbGetZoneByHostname(globalCtx.AppDB(), pid, strings.ToLower(rest))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if z == nil {
		httpErr(w, http.StatusNotFound, "zone not found")
		return
	}
	httpJSON(w, map[string]any{"zone": z})
}

// handleToolsCall is the generic panel-side dispatcher — same shape
// the domains/messaging apps use so the panel can POST any tool name
// without us minting an HTTP endpoint per tool.
func (a *App) handleToolsCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
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
	// Inject project_id from the query string so panel calls work
	// against global-scoped installs without each panel call having
	// to plumb _project_id by hand.
	if _, ok := body.Args["_project_id"]; !ok {
		if pid, err := resolveProjectFromRequest(r); err == nil {
			body.Args["_project_id"] = pid
		}
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
	out, err := handler(globalCtx, body.Args)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

// ─── request helpers ──────────────────────────────────────────────

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
