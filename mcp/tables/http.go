package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// HTTP surface backing the dashboard panel. Each route trampolines
// into the same handler an MCP tool would, so adding a route is a
// matter of building the args map and forwarding to the right tool —
// no behaviour duplicated.
//
// URL layout (all under the platform's /api/apps/tables/ proxy):
//
//   GET    /tables                     → tables_list
//   POST   /tables                     → tables_create
//   GET    /tables/{name}              → tables_describe
//   PATCH  /tables/{name}              → tables_alter ({add|rename|drop})
//   DELETE /tables/{name}              → tables_drop  (?confirm=true)
//   GET    /tables/{name}/rows         → rows_search  (limit/offset/order_by)
//   POST   /tables/{name}/rows         → rows_insert  ({rows:[...]} | {row:{...}})
//   POST   /tables/{name}/rows/search  → rows_search  (where in body)
//   PATCH  /tables/{name}/rows/{id}    → rows_update
//   DELETE /tables/{name}/rows/{id}    → rows_delete
//   POST   /tables/{name}/query        → tables_query

// globalCtx is set in OnMount; HTTP handlers reach for it because the
// SDK's Route.Handler signature is the bare http.HandlerFunc with no
// AppCtx parameter.
var globalCtx *sdk.AppCtx

// ─── handlers ──────────────────────────────────────────────────────

func (a *App) handleTablesCollection(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "app not yet mounted")
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolTablesList(globalCtx, injectProject(r, nil))
		writeToolResult(w, out, err)
	case http.MethodPost:
		body, err := readJSONBody(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.toolTablesCreate(globalCtx, injectProject(r, body))
		writeToolResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleTablesItem(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "app not yet mounted")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/tables/")
	if rest == "" {
		httpErr(w, http.StatusNotFound, "table name required")
		return
	}
	parts := strings.Split(rest, "/")
	tableName := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			out, err := a.toolTablesDescribe(globalCtx, injectProject(r, map[string]any{"name": tableName}))
			writeToolResult(w, out, err)
		case http.MethodPatch:
			body, err := readJSONBody(r)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			body["name"] = tableName
			out, err := a.toolTablesAlter(globalCtx, injectProject(r, body))
			writeToolResult(w, out, err)
		case http.MethodDelete:
			confirm := r.URL.Query().Get("confirm") == "true"
			out, err := a.toolTablesDrop(globalCtx, injectProject(r, map[string]any{"name": tableName, "confirm": confirm}))
			writeToolResult(w, out, err)
		default:
			httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH, or DELETE")
		}
		return
	}

	if parts[1] == "rows" {
		switch len(parts) {
		case 2:
			a.handleRowsCollection(w, r, tableName)
		case 3:
			if parts[2] == "search" {
				a.handleRowsSearch(w, r, tableName)
				return
			}
			id, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil {
				httpErr(w, http.StatusBadRequest, "row id must be integer")
				return
			}
			a.handleRowsItem(w, r, tableName, id)
		default:
			httpErr(w, http.StatusNotFound, "unknown rows path")
		}
		return
	}

	if parts[1] == "query" && len(parts) == 2 {
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		body, err := readJSONBody(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.toolTablesQuery(globalCtx, injectProject(r, body))
		writeToolResult(w, out, err)
		return
	}

	httpErr(w, http.StatusNotFound, "unknown path")
}

func (a *App) handleRowsCollection(w http.ResponseWriter, r *http.Request, tableName string) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{
			"table":    tableName,
			"limit":    parseIntQuery(r, "limit", 50),
			"offset":   parseIntQuery(r, "offset", 0),
			"order_by": r.URL.Query().Get("order_by"),
		}
		out, err := a.toolRowsSearch(globalCtx, injectProject(r, args))
		writeToolResult(w, out, err)
	case http.MethodPost:
		body, err := readJSONBody(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// Allow either {row: {...}} or {rows: [...]} so the panel
		// can create one row without wrapping in an array.
		if body["rows"] == nil {
			if single, ok := body["row"].(map[string]any); ok {
				body["rows"] = []any{single}
			}
		}
		body["table"] = tableName
		out, err := a.toolRowsInsert(globalCtx, injectProject(r, body))
		writeToolResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleRowsSearch(w http.ResponseWriter, r *http.Request, tableName string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	body["table"] = tableName
	out, err := a.toolRowsSearch(globalCtx, injectProject(r, body))
	writeToolResult(w, out, err)
}

func (a *App) handleRowsItem(w http.ResponseWriter, r *http.Request, tableName string, id int64) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolRowsGet(globalCtx, injectProject(r, map[string]any{
			"table":         tableName,
			"id":            id,
			"hydrate_files": r.URL.Query().Get("hydrate_files") == "true",
		}))
		writeToolResult(w, out, err)
	case http.MethodPatch:
		body, err := readJSONBody(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		args := injectProject(r, map[string]any{
			"table":  tableName,
			"id":     id,
			"fields": body,
		})
		out, err := a.toolRowsUpdate(globalCtx, args)
		writeToolResult(w, out, err)
	case http.MethodDelete:
		out, err := a.toolRowsDelete(globalCtx, injectProject(r, map[string]any{"table": tableName, "id": id}))
		writeToolResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH, or DELETE")
	}
}

// ─── helpers ───────────────────────────────────────────────────────

func readJSONBody(r *http.Request) (map[string]any, error) {
	if r.Body == nil {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(r.Body)
	var raw any
	if err := dec.Decode(&raw); err != nil {
		if err.Error() == "EOF" {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if m, ok := raw.(map[string]any); ok {
		return m, nil
	}
	return nil, errf("body must be a JSON object")
}

// injectProject overlays the request's project_id onto the args map
// so resolveProjectFromArgs picks it up. APTEVA_PROJECT_ID env wins
// (set per-install), but a fallback to ?project_id=... lets globally-
// scoped installs serve multiple projects on one sidecar.
func injectProject(r *http.Request, args map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		args["_project_id"] = v
	}
	return args
}

func parseIntQuery(r *http.Request, key string, def int) int {
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

func writeToolResult(w http.ResponseWriter, out any, err error) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}
