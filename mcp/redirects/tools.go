package main

import (
	"errors"
	"os"

	sdk "github.com/apteva/app-sdk"
)

// MCPTools — agent-facing surface. Each tool's REST twin lives in
// handlers.go; the underlying logic in store.go is shared.

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "redirect_add",
			Description: "Create a redirect rule. Inbound (hostname, path) → destination URL with a 30x. " +
				"Args: hostname (req), destination (req URL), path? (default '/'), " +
				"match? ('exact'|'prefix', default 'exact'), " +
				"status_code? (301|302|307|308, default 302), " +
				"preserve_path? (default false; only valid for match='prefix'), " +
				"preserve_query? (default true), notes?, project_id? (when scope=global).",
			InputSchema: schemaObject(map[string]any{
				"hostname":       map[string]any{"type": "string"},
				"destination":    map[string]any{"type": "string"},
				"path":           map[string]any{"type": "string"},
				"match":          map[string]any{"type": "string"},
				"status_code":    map[string]any{"type": "integer"},
				"preserve_path":  map[string]any{"type": "boolean"},
				"preserve_query": map[string]any{"type": "boolean"},
				"notes":          map[string]any{"type": "string"},
				"project_id":     map[string]any{"type": "string"},
			}, []string{"hostname", "destination"}),
			Handler: a.toolRedirectAdd,
		},
		{
			Name: "redirect_update",
			Description: "Update a redirect rule by id. Same fields as redirect_add; only fields you pass are changed. " +
				"Set status_code to 0 to skip; empty strings on path/match leave the field alone.",
			InputSchema: schemaObject(map[string]any{
				"id":             map[string]any{"type": "integer"},
				"hostname":       map[string]any{"type": "string"},
				"destination":    map[string]any{"type": "string"},
				"path":           map[string]any{"type": "string"},
				"match":          map[string]any{"type": "string"},
				"status_code":    map[string]any{"type": "integer"},
				"preserve_path":  map[string]any{"type": "boolean"},
				"preserve_query": map[string]any{"type": "boolean"},
				"notes":          map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolRedirectUpdate,
		},
		{
			Name:        "redirect_remove",
			Description: "Delete a redirect rule by id. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolRedirectRemove,
		},
		{
			Name: "redirect_list",
			Description: "List redirect rules. Args: hostname? (filter), project_id?, limit? (default 100), offset? (default 0).",
			InputSchema: schemaObject(map[string]any{
				"hostname":   map[string]any{"type": "string"},
				"project_id": map[string]any{"type": "string"},
				"limit":      map[string]any{"type": "integer"},
				"offset":     map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolRedirectList,
		},
		{
			Name:        "redirect_get",
			Description: "Fetch one rule by id. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolRedirectGet,
		},
		{
			Name: "redirect_test",
			Description: "Dry-run a redirect lookup. Returns the rule that would fire plus the computed Location, or null when nothing matches. " +
				"Args: hostname (req), path? (default '/'), query? (raw query string).",
			InputSchema: schemaObject(map[string]any{
				"hostname": map[string]any{"type": "string"},
				"path":     map[string]any{"type": "string"},
				"query":    map[string]any{"type": "string"},
			}, []string{"hostname"}),
			Handler: a.toolRedirectTest,
		},
	}
}

// ─── handlers ─────────────────────────────────────────────────────

func (a *App) toolRedirectAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	in := inputFromArgs(args)
	if in.ProjectID == "" {
		in.ProjectID = projectFromArgs(args)
	}
	rule, err := dbInsertRedirect(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	warning := wireHostname(ctx, rule.ProjectID, rule.Hostname)
	return map[string]any{"redirect": rule, "warning": warning}, nil
}

func (a *App) toolRedirectUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	in := inputFromArgs(args)
	rule, err := dbUpdateRedirect(ctx.AppDB(), id, in)
	if err != nil {
		return nil, err
	}
	warning := wireHostname(ctx, rule.ProjectID, rule.Hostname)
	return map[string]any{"redirect": rule, "warning": warning}, nil
}

func (a *App) toolRedirectRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	existing, err := dbGetRedirect(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := dbDeleteRedirect(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	maybeUnwireHostname(ctx, existing.Hostname, existing.ProjectID)
	return map[string]any{"removed": true}, nil
}

func (a *App) toolRedirectList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	hostname := strArg(args, "hostname")
	project := strArg(args, "project_id")
	if project == "" {
		project = projectFromArgs(args)
	}
	limit := intArg(args, "limit", 100)
	offset := intArg(args, "offset", 0)
	rows, err := dbListRedirects(ctx.AppDB(), hostname, project, limit, offset)
	if err != nil {
		return nil, err
	}
	return map[string]any{"redirects": rows, "count": len(rows)}, nil
}

func (a *App) toolRedirectGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	r, err := dbGetRedirect(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"redirect": r}, nil
}

func (a *App) toolRedirectTest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	host := strArg(args, "hostname")
	if host == "" {
		return nil, errors.New("hostname required")
	}
	path := strArg(args, "path")
	if path == "" {
		path = "/"
	}
	query := strArg(args, "query")
	rule, err := matchRedirect(ctx.AppDB(), host, path)
	if err != nil {
		return nil, err
	}
	if rule == nil {
		return map[string]any{"matched": false}, nil
	}
	return map[string]any{
		"matched":     true,
		"redirect":    rule,
		"location":    applyRule(rule, path, query),
		"status_code": rule.StatusCode,
	}, nil
}

// ─── arg helpers ──────────────────────────────────────────────────

func inputFromArgs(args map[string]any) RedirectInput {
	preservePathSet, preservePathVal := boolArg(args, "preserve_path")
	preserveQuerySet, preserveQueryVal := boolArg(args, "preserve_query")
	in := RedirectInput{
		Hostname:    strArg(args, "hostname"),
		Path:        strArg(args, "path"),
		MatchMode:   strArg(args, "match"),
		Destination: strArg(args, "destination"),
		StatusCode:  intArg(args, "status_code", 0),
		ProjectID:   strArg(args, "project_id"),
		Notes:       strArg(args, "notes"),
	}
	// preserve_path: default false; agent must opt in.
	if preservePathSet {
		in.PreservePath = preservePathVal
	}
	// preserve_query: default true; agent must opt out.
	if preserveQuerySet {
		in.PreserveQuery = preserveQueryVal
	} else {
		in.PreserveQuery = true
	}
	return in
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return def
}

// boolArg returns (set, value). "set" lets callers distinguish "field
// absent" from "field present and false" — needed because false is the
// default for some flags and the explicit default for others.
func boolArg(args map[string]any, key string) (bool, bool) {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return true, b
		}
	}
	return false, false
}

// projectFromArgs handles the global-scope case where the agent passes
// _project_id explicitly. For project-scoped installs the platform
// injects APTEVA_PROJECT_ID; we honour that first.
func projectFromArgs(args map[string]any) string {
	if v := os.Getenv("APTEVA_PROJECT_ID"); v != "" {
		return v
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v
	}
	return ""
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
