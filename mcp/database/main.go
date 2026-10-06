package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/database/engine"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct{ manager *engine.Manager }

func main() { sdk.Run(&App{}) }
func (a *App) Manifest() sdk.Manifest {
	m, e := sdk.ParseManifest(manifestYAML)
	if e != nil {
		panic(e)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error {
	root := ctx.DataDir()
	if root == "" {
		return errors.New("APTEVA_DATA_DIR is required")
	}
	m, e := engine.Open(filepath.Join(root, "database"))
	if e != nil {
		return e
	}
	a.manager = m
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.manager != nil {
		return a.manager.Close()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{{Pattern: "/operations/", Handler: a.handleHTTP}}
}

type toolSpec struct {
	op, description string
	args, required  []string
}

var specs = []toolSpec{
	{"databases_list", "List databases in the authenticated project or calling app's private scope.", nil, nil},
	{"database_create", "Create a named local database using sqlite (default) or pebble. Repeating the same definition is safe.", []string{"adapter"}, []string{"database"}},
	{"database_describe", "Describe a named database.", nil, nil},
	{"database_drop", "Permanently delete one database and all collections; confirm=true is required.", []string{"confirm"}, []string{"database", "confirm"}},
	{"collections_list", "List collections in a database.", nil, nil},
	{"collection_create", "Create a collection with explicit typed fields and optional composite primaryKey; defaults to UUID id.", []string{"fields", "primaryKey"}, []string{"collection", "fields"}},
	{"collection_describe", "Describe fields, primary key, indexes and schema version.", nil, []string{"collection"}},
	{"collection_drop", "Permanently delete a collection and records; confirm=true is required.", []string{"confirm"}, []string{"collection", "confirm"}},
	{"indexes_list", "List generic secondary indexes on a collection.", nil, []string{"collection"}},
	{"index_create", "Create a single-field or compound ordered index, optionally unique. Builds atomically; duplicates abort a unique build.", []string{"index"}, []string{"collection", "index"}},
	{"index_drop", "Drop a secondary index; confirm=true is required.", []string{"name", "confirm"}, []string{"collection", "name", "confirm"}},
	{"get", "Get a record by its exact primary-key object.", []string{"key"}, []string{"collection", "key"}},
	{"find", "Find records with typed filters, projection, ordering and cursor pagination. Broad queries default to 30 seconds; timeoutMs can set up to 60 seconds.", []string{"where", "select", "orderBy", "limit", "cursor", "requireIndex", "timeoutMs"}, []string{"collection"}},
	{"insert", "Atomically insert 1–1000 records. Integers use decimal strings; undeclared fields are rejected.", []string{"records"}, []string{"collection", "records"}},
	{"update", "Atomically patch records using set or increment. Exact key or where required; maxAffected defaults to 1.", []string{"key", "where", "set", "increment", "ifVersion", "maxAffected", "all", "requireIndex"}, []string{"collection"}},
	{"delete", "Atomically delete records by key or filter. maxAffected defaults to 1; full deletion needs all=true.", []string{"key", "where", "ifVersion", "maxAffected", "all", "requireIndex"}, []string{"collection"}},
	{"upsert", "Atomically insert or patch records using their primary key or a named unique conflictIndex.", []string{"records", "conflictIndex"}, []string{"collection", "records"}},
	{"count", "Count matching records exactly; returns a decimal string. Defaults to a 30-second deadline, configurable up to 60 seconds with timeoutMs.", []string{"where", "requireIndex", "timeoutMs"}, []string{"collection"}},
	{"aggregate", "Group matching records and compute count, sum, avg, min or max. Integers/counts are decimal strings; no groups means one summary row. Metrics use {name,op,field?}. Defaults to 30 seconds; timeoutMs can set up to 60 seconds.", []string{"where", "groupBy", "metrics", "orderBy", "limit", "requireIndex", "timeoutMs"}, []string{"collection", "metrics"}},
	{"batch", "Atomically execute record writes across collections in ONE database. Operations are {op,args}; at most 100 operations and 1000 affected records.", []string{"operations"}, []string{"operations"}},
	{"explain", "Inspect the adapter's query access path without executing it.", []string{"where", "orderBy", "requireIndex"}, []string{"collection"}},
}

func permission(op string) string {
	switch op {
	case "databases_list", "database_describe", "collections_list", "collection_describe", "indexes_list", "get", "find", "count", "aggregate", "explain":
		return "database.read"
	case "insert", "update", "delete", "upsert", "batch":
		return "database.write"
	}
	return "database.manage"
}
func scope(project string, caller *sdk.Caller) string {
	owner := ""
	if caller != nil && caller.AppInstallID > 0 {
		owner = fmt.Sprint(caller.AppInstallID)
	}
	b, _ := json.Marshal([]string{project, owner})
	return string(b)
}
func (a *App) MCPTools() []sdk.Tool {
	out := []sdk.Tool{}
	for _, s := range specs {
		s := s
		out = append(out, sdk.Tool{Name: "db_" + s.op, Description: s.description, InputSchema: inputSchema(s), HandlerCtx: func(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
			caller := sdk.CallerFrom(ctx)
			if caller == nil || caller.ProjectID == "" {
				return nil, &engine.Error{Code: "permission_denied", Message: "authenticated caller and project are required"}
			}
			if caller.SubjectID != "" {
				return nil, &engine.Error{Code: "permission_denied", Message: "delegated-user database access is not enabled"}
			}
			clean := map[string]any{}
			for k, v := range args {
				if k == "project_id" || k == "_project_id" {
					if v != caller.ProjectID {
						return nil, &engine.Error{Code: "permission_denied", Message: "project override is not allowed"}
					}
					continue
				}
				clean[k] = v
			}
			r, e := parseArgs(clean, s)
			if e != nil {
				return nil, e
			}
			db := r.Database
			if db == "" {
				db = "default"
			}
			if s.op != "databases_list" && !caller.Allows(permission(s.op), "database/"+db) {
				return nil, &engine.Error{Code: "permission_denied", Message: "database operation is not granted"}
			}
			result, e := a.run(ctx, scope(caller.ProjectID, caller), s.op, r)
			if e == nil && s.op == "databases_list" {
				filtered := []engine.DatabaseInfo{}
				for _, d := range result.([]engine.DatabaseInfo) {
					if caller.Allows("database.read", "database/"+d.Name) {
						filtered = append(filtered, d)
					}
				}
				result = filtered
			}
			return result, e
		}})
	}
	return out
}
func parseArgs(args map[string]any, s toolSpec) (engine.Request, error) {
	var r engine.Request
	allowed := map[string]bool{"database": true, "collection": true}
	for _, k := range s.args {
		allowed[k] = true
	}
	for k := range args {
		if !allowed[k] {
			return r, engine.Invalid("unsupported argument %s for %s", k, s.op)
		}
	}
	for _, k := range s.required {
		if args[k] == nil {
			return r, engine.Invalid("%s is required", k)
		}
	}
	b, e := json.Marshal(args)
	if e != nil {
		return r, e
	}
	if len(b) > 8<<20 {
		return r, engine.Invalid("request exceeds 8 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if e = d.Decode(&r); e != nil {
		return r, engine.Invalid("invalid request: %v", e)
	}
	return r, nil
}
func (a *App) run(ctx context.Context, scope, op string, r engine.Request) (any, error) {
	if a.manager == nil {
		return nil, &engine.Error{Code: "storage_error", Message: "app is not mounted"}
	}
	timeout, e := engine.OperationTimeout(op, r.TimeoutMS)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, e := a.manager.Execute(ctx, scope, op, r)
	var known *engine.Error
	if e != nil && !errors.As(e, &known) {
		if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
			e = &engine.Error{Code: "resource_limit", Message: "operation deadline exceeded"}
		} else {
			e = &engine.Error{Code: "storage_error", Message: "local database operation failed"}
		}
	}
	return out, e
}

// HTTP is the admin dashboard surface behind SDK bearer authentication. The
// gateway strips/rebuilds project headers. Agents and sibling apps use MCP,
// where SDK caller grants and private app namespaces are enforced.
func (a *App) handleHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	for _, h := range []string{"X-Apteva-Caller-Agent", "X-Apteva-Caller-Instance", "X-Apteva-Subject-ID", sdk.HeaderBoundCallerInstallID} {
		if r.Header.Get(h) != "" {
			httpResult(w, nil, &engine.Error{Code: "permission_denied", Message: "use MCP for scoped caller access"})
			return
		}
	}
	project := r.Header.Get("X-Apteva-Project-ID")
	if project == "" {
		httpResult(w, nil, &engine.Error{Code: "permission_denied", Message: "trusted project header required"})
		return
	}
	op := strings.TrimPrefix(r.URL.Path, "/operations/")
	var spec *toolSpec
	for _, s := range specs {
		if s.op == op {
			x := s
			spec = &x
		}
	}
	if spec == nil {
		w.WriteHeader(404)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	d.UseNumber()
	var args map[string]any
	if e := d.Decode(&args); e != nil {
		httpResult(w, nil, engine.Invalid("invalid JSON body"))
		return
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		httpResult(w, nil, engine.Invalid("expected one JSON object"))
		return
	}
	req, e := parseArgs(args, *spec)
	if e != nil {
		httpResult(w, nil, e)
		return
	}
	out, e := a.run(r.Context(), scope(project, nil), op, req)
	httpResult(w, out, e)
}
func httpResult(w http.ResponseWriter, v any, e error) {
	if e != nil {
		status := 400
		var known *engine.Error
		if errors.As(e, &known) {
			switch known.Code {
			case "permission_denied":
				status = 403
			case "not_found":
				status = 404
			case "unique_conflict", "version_conflict", "schema_conflict":
				status = 409
			case "storage_error":
				status = 500
			}
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": e})
		return
	}
	json.NewEncoder(w).Encode(v)
}

func inputSchema(s toolSpec) map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	obj := func() map[string]any { return map[string]any{"type": "object"} }
	arr := func(items any) map[string]any { return map[string]any{"type": "array", "items": items} }
	order := map[string]any{"type": "object", "properties": map[string]any{"field": str(), "direction": map[string]any{"type": "string", "enum": []string{"asc", "desc"}}}, "required": []string{"field"}}
	all := map[string]any{"database": str(), "collection": str(), "adapter": map[string]any{"type": "string", "enum": []string{"sqlite", "pebble"}}, "name": str(), "confirm": map[string]any{"type": "boolean"}, "all": map[string]any{"type": "boolean"}, "requireIndex": map[string]any{"type": "boolean"}, "key": obj(), "set": obj(), "increment": obj(), "records": arr(obj()), "where": obj(), "select": arr(str()), "primaryKey": arr(str()), "groupBy": arr(str()), "orderBy": arr(order), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "cursor": str(), "conflictIndex": str(), "ifVersion": map[string]any{"type": "integer", "minimum": 1}, "maxAffected": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "fields": arr(map[string]any{"type": "object", "properties": map[string]any{"name": str(), "type": map[string]any{"type": "string", "enum": []string{"text", "integer", "number", "boolean", "datetime", "json"}}, "nullable": map[string]any{"type": "boolean"}}, "required": []string{"name", "type"}}), "index": map[string]any{"type": "object", "properties": map[string]any{"name": str(), "fields": arr(order), "unique": map[string]any{"type": "boolean"}}, "required": []string{"name", "fields"}}, "metrics": arr(map[string]any{"type": "object", "properties": map[string]any{"name": str(), "op": map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "min", "max"}}, "field": str()}, "required": []string{"name", "op"}}), "operations": arr(map[string]any{"type": "object", "properties": map[string]any{"op": map[string]any{"type": "string", "enum": []string{"insert", "update", "delete", "upsert"}}, "args": obj()}, "required": []string{"op", "args"}})}
	all["timeoutMs"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 60000, "description": "Query deadline in milliseconds; omitted or 0 defaults to 30000. An earlier caller deadline takes precedence."}
	props := map[string]any{"database": all["database"]}
	if s.op != "batch" && !strings.HasPrefix(s.op, "database") {
		props["collection"] = all["collection"]
	}
	for _, k := range s.args {
		props[k] = all[k]
	}
	return map[string]any{"type": "object", "properties": props, "required": s.required, "additionalProperties": false}
}
