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
	"os"
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
	options, e := managerOptions(ctx.Config(), os.Getenv)
	if e != nil {
		return e
	}
	m, e := engine.OpenWithConfig(filepath.Join(root, "database"), options)
	if e != nil {
		return e
	}
	a.manager = m
	ctx.Logger().Info("database mounted", "durability_profile", options.SQLiteDurability, "aptevadb_sync_mode", options.Apteva.SyncMode, "aptevadb_cache_bytes", options.Apteva.CacheBytes, "aptevadb_mapped_cache_bytes", options.Apteva.MappedCacheBytes, "aptevadb_managed_memory_bytes", options.Apteva.ManagedMemoryBytes, "aptevadb_aggregate_disk_bytes", options.Apteva.AggregateDiskBytes, "aptevadb_max_groups", options.Apteva.AggregateMaxGroups, "aptevadb_memtable_bytes", options.Apteva.MemtableBytes, "aptevadb_total_cache_bytes", options.AptevaCacheBudgetBytes, "aptevadb_max_concurrent_queries", options.MaxConcurrentAptevaQueries, "aptevadb_aggregate_workers", options.Apteva.AggregateWorkers, "aptevadb_query_memory_bytes", options.Apteva.QueryWorkBytes)
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
	return []sdk.Route{{Pattern: "/operations/", Handler: a.handleHTTP}, {Method: "POST", Pattern: "/realtime/watch", Handler: a.handleWatch}}
}

type toolSpec struct {
	op, description string
	args, required  []string
}

var specs = []toolSpec{
	{"realtime_configure", "Enable or disable persistent native AptevaDB change capture for one database. Disabling ends resume history.", []string{"enabled", "retentionBytes", "retentionSeconds"}, []string{"database", "enabled"}},
	{"realtime_describe", "Inspect realtime support, capture policy and watcher usage.", nil, nil},
	{"changes", "Watch explicit collections for complete commit invalidations. Wait up to 20 seconds; repeat with the returned cursor. Optional initial snapshot reads find/count/aggregate atomically with the subscription boundary.", []string{"collections", "after", "initial", "waitMs"}, []string{"collections"}},
	{"databases_list", "List databases in the authenticated project or calling app's private scope.", nil, nil},
	{"database_create", "Create a named local database using sqlite (default), pebble or aptevadb. Repeating the same definition is safe.", []string{"adapter"}, []string{"database"}},
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
	{"find", "Find records with typed filters ({field,op,value}, and/or, {field,op:\"contains_ascii\",value} literal substring search, {fields:[...],op:\"like_contains_ascii\",value} multi-field wildcard search, and {exists:{collection,on:[{localField,foreignField,comparison?}] or anyOf:[{on:[...]}],where?}} or {not_exists:{...}} tests of related records), projection, ordering and cursor pagination. lookups [{as,collection,localField,select?}] nest the record of another collection whose single-field primary key equals localField (null when absent). Broad queries default to 30 seconds; timeoutMs can set up to 60 seconds.", []string{"where", "select", "orderBy", "limit", "cursor", "requireIndex", "timeoutMs", "lookups"}, []string{"collection"}},
	{"insert", "Atomically insert 1–1000 records. Integers use decimal strings; undeclared fields are rejected.", []string{"records"}, []string{"collection", "records"}},
	{"update", "Atomically patch records using set or increment. Exact key, where, or key plus a where guard; maxAffected defaults to 1.", []string{"key", "where", "set", "increment", "ifVersion", "maxAffected", "all", "requireIndex"}, []string{"collection"}},
	{"delete", "Atomically delete records by key, filter, or key plus a where guard. maxAffected defaults to 1; full deletion needs all=true.", []string{"key", "where", "ifVersion", "maxAffected", "all", "requireIndex"}, []string{"collection"}},
	{"upsert", "Atomically insert or patch records using their primary key or a named unique conflictIndex.", []string{"records", "conflictIndex"}, []string{"collection", "records"}},
	{"count", "Count matching records exactly; returns a decimal string. Filters accept {field,op:\"contains_ascii\",value} literal substring search, {fields:[text fields],op:\"like_contains_ascii\",value} wildcard search and exists/not_exists tests of related records. Defaults to a 30-second deadline, configurable up to 60 seconds with timeoutMs. diagnostics:true also reports what actually ran: plan, index, whether it was index-only, documents and index entries read, peak memory and timings.", []string{"where", "requireIndex", "timeoutMs", "diagnostics"}, []string{"collection"}},
	{"aggregate", "Group matching records and compute count, count_distinct, sum, avg, min or max. Metrics use {name,op,field?,where?}; a metric where counts only matching rows (conditional metrics in one pass). lookups [{as,collection,localField,select?}] join the record whose single-field primary key equals localField, so \"as.field\" works in where, groupBy and metrics without duplicating rows. buckets [{name,field,unit,timezone?,weekStart?}] derive hour/day/week/month/year keys from a datetime in an IANA timezone for groupBy. Integers/counts are decimal strings; no groups means one summary row. Defaults to 30 seconds; timeoutMs can set up to 60 seconds.", []string{"where", "groupBy", "metrics", "orderBy", "limit", "requireIndex", "timeoutMs", "lookups", "buckets"}, []string{"collection", "metrics"}},
	{"batch", "Run operations {op,args} in ONE database: either record writes (insert, update, delete, upsert) applied atomically, at most 1000 affected records; or reads (get, find, count, aggregate) answered from one consistent snapshot, which only needs read access. At most 100 operations; reads and writes cannot mix. timeoutMs applies to read batches.", []string{"operations", "timeoutMs"}, []string{"operations"}},
	{"explain", "Inspect the adapter's planned find access without executing it; not evidence of what a count did (use count with diagnostics:true).", []string{"where", "orderBy", "requireIndex"}, []string{"collection"}},
	{"access_grant", "Let a calling app use one existing project database with read or write access. Calling apps reach it with scope=\"project\". Repeating changes the access level.", []string{"app", "access"}, []string{"database", "app", "access"}},
	{"access_revoke", "Remove a calling app's access to a project database.", []string{"app"}, []string{"database", "app"}},
	{"access_list", "List calling apps allowed to use a project database.", nil, []string{"database"}},
}

// requestPermission is permission for one call: a batch of only reads needs
// read access.
func requestPermission(op string, r engine.Request) string {
	if engine.QueryKind(op, r) == "read_batch" {
		return "database.read"
	}
	return permission(op)
}
func permission(op string) string {
	switch op {
	case "changes":
		return "database.watch"
	case "realtime_describe":
		return "database.read"
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
			namespace := "project"
			if caller.AppInstallID > 0 {
				namespace = "app"
			}
			for k, v := range args {
				if k == "project_id" || k == "_project_id" {
					if v != caller.ProjectID {
						return nil, &engine.Error{Code: "permission_denied", Message: "project override is not allowed"}
					}
					continue
				}
				if k == "scope" {
					if v != "app" && v != "project" {
						return nil, engine.Invalid("scope must be app or project")
					}
					if v == "app" && caller.AppInstallID == 0 {
						return nil, engine.Invalid("only calling apps have a private app scope")
					}
					namespace = v.(string)
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
			projectScope := scope(caller.ProjectID, nil)
			var accessible map[string]string
			if caller.AppInstallID > 0 {
				if strings.HasPrefix(s.op, "access_") {
					return nil, &engine.Error{Code: "permission_denied", Message: "calling apps cannot change database access"}
				}
				if namespace == "project" {
					if accessible, e = a.appProjectAccess(ctx, caller, projectScope, s.op, requestPermission(s.op, r), db); e != nil {
						return nil, e
					}
				}
			}
			if s.op != "databases_list" && !caller.Allows(requestPermission(s.op, r), "database/"+db) {
				return nil, &engine.Error{Code: "permission_denied", Message: "database operation is not granted"}
			}
			if s.op == "changes" && !caller.Allows("database.read", "database/"+db) {
				return nil, &engine.Error{Code: "permission_denied", Message: "watch also requires database.read"}
			}
			r.WatchCaller = fmt.Sprintf("agent:%d/app:%d", caller.AgentID, caller.AppInstallID)
			target := scope(caller.ProjectID, caller)
			if namespace == "project" {
				target = projectScope
			}
			result, e := a.run(ctx, target, s.op, r)
			if e == nil && s.op == "databases_list" {
				filtered := []engine.DatabaseInfo{}
				for _, d := range result.([]engine.DatabaseInfo) {
					if accessible != nil && accessible[d.Name] == "" {
						continue
					}
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

// A calling app reaches a project-owned database only through an operator's
// access grant. It can read, watch and (with write access) change records, but
// never manage schema, realtime capture or the database itself.
func (a *App) appProjectAccess(ctx context.Context, caller *sdk.Caller, projectScope, op, need, db string) (map[string]string, error) {
	denied := &engine.Error{Code: "permission_denied", Message: fmt.Sprintf("app %s has no access to project database %s; a project operator can grant it with db_access_grant", caller.AppName, db)}
	if a.manager == nil {
		return nil, &engine.Error{Code: "storage_error", Message: "app is not mounted"}
	}
	if caller.AppName == "" {
		return nil, denied
	}
	if op == "databases_list" {
		return a.manager.AppAccessible(ctx, projectScope, caller.AppName)
	}
	if need == "database.manage" {
		return nil, &engine.Error{Code: "permission_denied", Message: "calling apps cannot manage project databases"}
	}
	level, e := a.manager.AppAccessLevel(ctx, projectScope, db, caller.AppName)
	if e != nil {
		return nil, &engine.Error{Code: "storage_error", Message: "database access lookup failed"}
	}
	if level == "" || need == "database.write" && level != "write" {
		return nil, denied
	}
	return nil, nil
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
	timeout, e := engine.OperationTimeout(engine.QueryKind(op, r), r.TimeoutMS)
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
			case "unique_conflict", "version_conflict", "schema_conflict", "resume_invalid":
				status = 409
			case "history_expired":
				status = 410
			case "resource_limit":
				status = 429
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
	all := map[string]any{"database": str(), "collection": str(), "adapter": map[string]any{"type": "string", "enum": []string{"sqlite", "pebble", "aptevadb"}}, "name": str(), "confirm": map[string]any{"type": "boolean"}, "all": map[string]any{"type": "boolean"}, "requireIndex": map[string]any{"type": "boolean"}, "key": obj(), "set": obj(), "increment": obj(), "records": arr(obj()), "where": obj(), "select": arr(str()), "primaryKey": arr(str()), "groupBy": arr(str()), "orderBy": arr(order), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "cursor": str(), "conflictIndex": str(), "ifVersion": map[string]any{"type": "integer", "minimum": 1}, "maxAffected": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "fields": arr(map[string]any{"type": "object", "properties": map[string]any{"name": str(), "type": map[string]any{"type": "string", "enum": []string{"text", "integer", "number", "boolean", "datetime", "json"}}, "nullable": map[string]any{"type": "boolean"}}, "required": []string{"name", "type"}}), "index": map[string]any{"type": "object", "properties": map[string]any{"name": str(), "fields": arr(order), "unique": map[string]any{"type": "boolean"}}, "required": []string{"name", "fields"}}, "metrics": arr(map[string]any{"type": "object", "properties": map[string]any{"name": str(), "op": map[string]any{"type": "string", "enum": []string{"count", "count_distinct", "sum", "avg", "min", "max"}}, "field": str(), "where": obj()}, "required": []string{"name", "op"}}), "operations": arr(map[string]any{"type": "object", "properties": map[string]any{"op": map[string]any{"type": "string", "enum": []string{"insert", "update", "delete", "upsert", "get", "find", "count", "aggregate"}}, "args": obj()}, "required": []string{"op", "args"}})}
	all["lookups"] = map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "object", "properties": map[string]any{"as": str(), "collection": str(), "localField": str(), "select": arr(str())}, "required": []string{"as", "collection", "localField"}, "additionalProperties": false}}
	all["buckets"] = map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "object", "properties": map[string]any{"name": str(), "field": str(), "unit": map[string]any{"type": "string", "enum": []string{"hour", "day", "week", "month", "year"}}, "timezone": map[string]any{"type": "string", "description": "IANA timezone such as Europe/Paris; defaults to UTC."}, "weekStart": map[string]any{"type": "string", "enum": []string{"monday", "sunday"}}}, "required": []string{"name", "field", "unit"}, "additionalProperties": false}}
	all["timeoutMs"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 60000, "description": "Query deadline in milliseconds; omitted or 0 defaults to 30000. An earlier caller deadline takes precedence."}
	all["collections"] = map[string]any{"type": "array", "items": str(), "minItems": 1, "maxItems": 16}
	all["after"] = str()
	all["diagnostics"] = map[string]any{"type": "boolean", "description": "Count only: report what the count actually did."}
	all["initial"] = map[string]any{"type": "object", "properties": map[string]any{"op": map[string]any{"type": "string", "enum": []string{"find", "count", "aggregate"}}, "args": obj()}, "required": []string{"op", "args"}, "additionalProperties": false}
	all["enabled"] = map[string]any{"type": "boolean"}
	all["retentionBytes"] = map[string]any{"type": "integer", "minimum": 1048576, "maximum": 268435456}
	all["retentionSeconds"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 86400}
	all["waitMs"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 20000}
	all["app"] = map[string]any{"type": "string", "description": "Installed app name, for example graphql."}
	all["access"] = map[string]any{"type": "string", "enum": []string{"read", "write"}}
	props := map[string]any{"database": all["database"], "scope": map[string]any{"type": "string", "enum": []string{"app", "project"}, "description": "Calling apps only: app (default) uses the app's private databases; project uses a project database an operator granted with db_access_grant."}}
	if s.op != "batch" && !strings.HasPrefix(s.op, "database") && !strings.HasPrefix(s.op, "access_") {
		props["collection"] = all["collection"]
	}
	for _, k := range s.args {
		props[k] = all[k]
	}
	return map[string]any{"type": "object", "properties": props, "required": s.required, "additionalProperties": false}
}
