package main

import (
	"bytes"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"io"
	"math"
	_ "modernc.org/sqlite"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

//go:embed apteva.yaml
var manifestYAML []byte

// All writes share a lock; revisions protect against stale clients.
type App struct {
	ctx    *sdk.AppCtx
	writes sync.Mutex
}

func main() { sdk.Run(&App{}) }
func (a *App) Manifest() sdk.Manifest {
	m, e := sdk.ParseManifest(manifestYAML)
	if e != nil {
		panic(e)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("editorial requires its own database")
	}
	a.ctx = ctx
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/items", Handler: a.http}, {Pattern: "/items/", Handler: a.http}, {Pattern: "/releases", Handler: a.http}, {Pattern: "/releases/", Handler: a.http}, {Pattern: "/settings", Handler: a.http}, {Pattern: "/integrations", Handler: a.http}}
}
func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func number(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		if v >= 0 && v < math.MaxInt64 && v == math.Trunc(v) {
			return int64(v)
		}
	case json.Number:
		n, e := v.Int64()
		if e == nil {
			return n
		}
	case string:
		n, e := strconv.ParseInt(v, 10, 64)
		if e == nil {
			return n
		}
	}
	return -1
}
func object(p map[string]any, required ...string) map[string]any {
	if p == nil {
		p = map[string]any{}
	}
	r := map[string]any{"type": "object", "properties": p, "additionalProperties": false}
	if len(required) > 0 {
		r["required"] = required
	}
	return r
}
func properties(keys []string) map[string]any {
	p := map[string]any{}
	for _, k := range keys {
		t := map[string]any{"type": "string"}
		switch k {
		case "id", "revision", "item_id", "external_id", "limit", "offset":
			t = map[string]any{"type": "integer", "minimum": 0}
		case "sources", "attachments", "tags", "statuses", "formats", "channels":
			t = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		case "fields", "results":
			t = map[string]any{"type": "object"}
		case "archived":
			t = map[string]any{"type": "boolean"}
		}
		p[k] = t
	}
	return p
}
func (a *App) MCPTools() []sdk.Tool {
	specs := []struct {
		name, description string
		schema            map[string]any
	}{
		{"items_list", "List planning items and releases with pagination. archived: false, true or all.", object(properties([]string{"q", "status", "format", "owner", "campaign", "approval", "limit", "offset"}))},
		{"items_get", "Read an item, releases and latest 100 history entries.", object(properties([]string{"id"}), "id")},
		{"items_create", "Create a planning item. Dates: YYYY-MM-DD or RFC3339. No external action.", object(properties(itemFields), "title")},
		{"items_update", "Patch with current revision. Content changes invalidate an existing approval.", object(map[string]any{"id": properties([]string{"id"})["id"], "revision": properties([]string{"revision"})["revision"], "patch": object(properties(itemFields))}, "id", "revision", "patch")},
		{"releases_create", "Plan a release; never schedules delivery. Optional app/external_id links an existing record.", object(properties(append(append([]string{}, releaseFields...), "item_id")), "item_id", "channel")},
		{"releases_update", "Update using current revision. Does not change the linked publisher record.", object(map[string]any{"id": properties([]string{"id"})["id"], "revision": properties([]string{"revision"})["revision"], "patch": object(properties(releaseFields))}, "id", "revision", "patch")},
		{"releases_refresh", "Refresh linked results. Social exposes latest 200 posts; missing posts preserve prior results.", object(properties([]string{"id"}), "id")},
		{"settings_get", "Read formats, statuses and channel suggestions.", object(nil)},
		{"settings_update", "Configure formats, statuses and channels. Keep values used by existing items.", object(properties([]string{"revision", "statuses", "formats", "channels"}), "revision", "statuses", "formats", "channels")},
		{"integrations", "Check optional bindings; pass app social or campaigns to browse existing records. Never publishes.", object(properties([]string{"app"}))},
	}
	out := []sdk.Tool{}
	for _, s := range specs {
		operation := s.name
		if operation == "items_list" {
			s.schema["properties"].(map[string]any)["archived"] = map[string]any{"type": "string", "enum": []string{"false", "true", "all"}}
		}
		out = append(out, sdk.Tool{Name: "editorial_" + s.name, Description: s.description, InputSchema: s.schema, Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) { return a.dispatch(ctx, operation, args) }})
	}
	return out
}
func (a *App) dispatch(ctx *sdk.AppCtx, op string, args map[string]any) (any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("app not mounted")
	}
	pid := strings.TrimSpace(ctx.CurrentProject())
	if pid == "" {
		return nil, invalid("a project context is required")
	}
	argsCopy := map[string]any{}
	for k, v := range args {
		if k != "project_id" && k != "_project_id" {
			argsCopy[k] = v
		}
	}
	args = argsCopy
	db := ctx.AppDB()
	switch op {
	case "items_list":
		return listItems(db, pid, args)
	case "items_get":
		return itemDetail(db, pid, number(args, "id"))
	case "settings_get":
		return getSettings(db, pid)
	case "integrations":
		return integrations(ctx, args)
	case "releases_refresh":
		return a.refresh(ctx, number(args, "id"))
	}
	a.writes.Lock()
	defer a.writes.Unlock()
	switch op {
	case "items_create":
		return saveItem(db, pid, 0, 0, args)
	case "items_update", "releases_update":
		id, revision := number(args, "id"), number(args, "revision")
		if id <= 0 || revision <= 0 {
			return nil, invalid("positive id and revision required")
		}
		patch, ok := args["patch"].(map[string]any)
		if !ok {
			return nil, invalid("patch object required")
		}
		if op == "items_update" {
			return saveItem(db, pid, id, revision, patch)
		}
		return saveRelease(db, pid, id, 0, revision, patch, false)
	case "releases_create":
		id := number(args, "item_id")
		if id <= 0 {
			return nil, invalid("item_id is required")
		}
		delete(args, "item_id")
		return saveRelease(db, pid, 0, id, 0, args, false)
	case "settings_update":
		return saveSettings(db, pid, args)
	}
	return nil, invalid("unknown operation")
}
func (a *App) http(w http.ResponseWriter, r *http.Request) {
	ctx := a.ctx
	if ctx == nil {
		respond(w, nil, errors.New("app not mounted"))
		return
	}
	// Pinned installs cannot be switched by a query parameter. On global installs
	// the gateway's authenticated project header takes precedence.
	pid := ctx.CurrentProject()
	if pid == "" {
		pid = r.Header.Get("X-Apteva-Project-Id")
		if pid == "" {
			pid = r.URL.Query().Get("project_id")
		}
	}
	ctx = ctx.WithProject(pid)
	path := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	op := ""
	args := map[string]any{}
	if r.Method == http.MethodGet {
		for k, v := range r.URL.Query() {
			args[k] = v[0]
		}
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if e != nil {
			respond(w, nil, invalid("request body exceeds 2 MB"))
			return
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if e = decoder.Decode(&args); e != nil || args == nil {
				respond(w, nil, invalid("expected a JSON object"))
				return
			}
			if e = decoder.Decode(new(any)); e != io.EOF {
				respond(w, nil, invalid("expected one JSON object"))
				return
			}
		}
	}
	switch {
	case len(path) == 1 && path[0] == "items" && r.Method == "GET":
		op = "items_list"
	case len(path) == 1 && path[0] == "items" && r.Method == "POST":
		op = "items_create"
	case len(path) == 2 && path[0] == "items" && r.Method == "GET":
		op = "items_get"
		args["id"] = path[1]
	case len(path) == 2 && path[0] == "items" && r.Method == "PATCH":
		op = "items_update"
		args["id"] = path[1]
	case len(path) == 1 && path[0] == "releases" && r.Method == "POST":
		op = "releases_create"
	case len(path) == 2 && path[0] == "releases" && r.Method == "PATCH":
		op = "releases_update"
		args["id"] = path[1]
	case len(path) == 3 && path[0] == "releases" && path[2] == "refresh" && r.Method == "POST":
		op = "releases_refresh"
		args["id"] = path[1]
	case len(path) == 1 && path[0] == "settings" && r.Method == "GET":
		op = "settings_get"
	case len(path) == 1 && path[0] == "settings" && r.Method == "PATCH":
		op = "settings_update"
	case len(path) == 1 && path[0] == "integrations" && r.Method == "GET":
		op = "integrations"
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	result, e := a.dispatch(ctx, op, args)
	respond(w, result, e)
}
func respond(w http.ResponseWriter, result any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		code := 500
		var v validationError
		switch {
		case errors.As(e, &v):
			code = 400
		case errors.Is(e, sql.ErrNoRows):
			code = 404
		case errors.Is(e, errConflict):
			code = 409
		}
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"error": e.Error()})
		return
	}
	json.NewEncoder(w).Encode(result)
}
