package main

import (
	"errors"

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
			}, []string{"hostname", "destination"}),
			Handler: a.toolRedirectAdd,
		},
		{
			Name: "redirect_update",
			Description: "Update a redirect rule by id. Same fields as redirect_add; only fields you pass are changed. " +
				"Boolean false and an empty notes string are applied explicitly when provided.",
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
			Name:        "redirect_list",
			Description: "List redirect rules in the current project. Args: hostname? (filter), limit? (default 50), offset? (default 0).",
			InputSchema: schemaObject(map[string]any{
				"hostname": map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
				"offset":   map[string]any{"type": "integer"},
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
	in.ProjectID = ctx.CurrentProject()
	rule, err := dbInsertRedirect(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	warning := wireHostname(ctx, rule.ProjectID, rule.Hostname)
	emitRuleChange(ctx, "rule.created", rule)
	return map[string]any{"redirect": rule, "warning": warning}, nil
}

func (a *App) toolRedirectUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	projectID := ctx.CurrentProject()
	existing, err := dbGetRedirect(ctx.AppDB(), id, projectID)
	if err != nil {
		return nil, err
	}
	rule, err := dbUpdateRedirect(ctx.AppDB(), id, projectID, patchFromArgs(args))
	if err != nil {
		return nil, err
	}
	if existing.Hostname != rule.Hostname || existing.ProjectID != rule.ProjectID {
		maybeUnwireHostname(ctx, existing.Hostname, existing.ProjectID)
	}
	warning := wireHostname(ctx, rule.ProjectID, rule.Hostname)
	emitRuleChange(ctx, "rule.updated", rule)
	return map[string]any{"redirect": rule, "warning": warning}, nil
}

func (a *App) toolRedirectRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	existing, err := dbDeleteRedirect(ctx.AppDB(), id, ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	hitLastEmit.Delete(existing.ID)
	maybeUnwireHostname(ctx, existing.Hostname, existing.ProjectID)
	emitRuleChange(ctx, "rule.removed", existing)
	return map[string]any{"removed": true}, nil
}

func (a *App) toolRedirectList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	hostname := strArg(args, "hostname")
	project := ctx.CurrentProject()
	limit := intArg(args, "limit", 50)
	offset := intArg(args, "offset", 0)
	rows, err := dbListRedirects(ctx.AppDB(), hostname, project, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := dbCountRedirects(ctx.AppDB(), hostname, project)
	if err != nil {
		return nil, err
	}
	return map[string]any{"redirects": rows, "count": len(rows), "total": total, "limit": limit, "offset": offset}, nil
}

func (a *App) toolRedirectGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	r, err := dbGetRedirect(ctx.AppDB(), id, ctx.CurrentProject())
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
	rule, err := matchRedirectInProject(ctx.AppDB(), host, path, ctx.CurrentProject())
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

func patchFromArgs(args map[string]any) RedirectPatch {
	return RedirectPatch{
		Hostname: stringPtrArg(args, "hostname"), Path: stringPtrArg(args, "path"),
		MatchMode: stringPtrArg(args, "match"), Destination: stringPtrArg(args, "destination"),
		StatusCode: intPtrArg(args, "status_code"), PreservePath: boolPtrArg(args, "preserve_path"),
		PreserveQuery: boolPtrArg(args, "preserve_query"), Notes: stringPtrArg(args, "notes"),
	}
}

func stringPtrArg(args map[string]any, key string) *string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func intPtrArg(args map[string]any, key string) *int {
	if _, ok := args[key]; !ok {
		return nil
	}
	v := intArg(args, key, 0)
	return &v
}

func boolPtrArg(args map[string]any, key string) *bool {
	set, value := boolArg(args, key)
	if !set {
		return nil
	}
	return &value
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

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
