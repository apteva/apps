package main

import (
	"context"
	"errors"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func object(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func field(kind string) map[string]any { return map[string]any{"type": kind} }
func (a *App) MCPTools() []sdk.Tool {
	id := map[string]any{"type": "integer", "minimum": 1}
	str := field("string")
	assertion := object(map[string]any{"path": map[string]any{"type": "string", "description": "JSON pointer into runner output; /response/total for functions, /status_code or /json/total for HTTP. Empty selects the whole output."}, "op": map[string]any{"type": "string", "enum": []string{"equals", "not_equals", "exists", "contains", "lt", "lte", "gt", "gte"}}, "value": map[string]any{}}, "path", "op")
	definition := object(map[string]any{
		"function": str, "event": map[string]any{}, "url": str, "method": str, "headers": map[string]any{"type": "object", "additionalProperties": str}, "body": str,
		"timeout_ms": map[string]any{"type": "integer", "minimum": 100, "maximum": 30000, "default": 10000},
		"assertions": map[string]any{"type": "array", "items": assertion, "minItems": 1, "maxItems": 50},
	}, "assertions")
	specs := []struct {
		name, description string
		schema            map[string]any
	}{
		{"tests_suites_list", "List active suites in the current project.", object(map[string]any{})},
		{"tests_suite_save", "Create (omit id) or replace a suite's name, description and environment label. Environment is descriptive; targets belong to each check.", object(map[string]any{"id": id, "name": str, "description": str, "environment": str}, "name")},
		{"tests_suite_archive", "Archive a suite. Existing queued runs and history remain.", object(map[string]any{"id": id}, "id")},
		{"tests_checks_list", "List all enabled and disabled checks in a suite.", object(map[string]any{"suite_id": id}, "suite_id")},
		{"tests_check_save", "Create (omit id) or fully replace a check. Function checks invoke the active version; HTTP checks do not follow redirects. Requires at least one assertion.", object(map[string]any{"id": id, "suite_id": id, "name": str, "kind": map[string]any{"type": "string", "enum": []string{"function", "http"}}, "enabled": map[string]any{"type": "boolean", "default": true}, "definition": definition}, "suite_id", "name", "kind", "definition")},
		{"tests_check_delete", "Delete a check. Prior runs retain their immutable definitions and results.", object(map[string]any{"id": id}, "id")},
		{"tests_run", "Queue a suite run. Returns immediately; poll tests_run_get. Optional request_key deduplicates retries. Jobs may call this tool with suite_id.", object(map[string]any{"suite_id": id, "request_key": str}, "suite_id")},
		{"tests_runs_list", "List the most recent 100 runs, optionally filtered by suite_id.", object(map[string]any{"suite_id": id})},
		{"tests_run_get", "Get a run's status, check snapshots, assertion outcomes and saved evidence.", object(map[string]any{"id": id}, "id")},
	}
	tools := make([]sdk.Tool, 0, len(specs))
	for _, spec := range specs {
		name := spec.name
		spec.schema["properties"].(map[string]any)["project_id"] = map[string]any{"type": "string", "description": "Project for a global install; must match authenticated scope when present."}
		tools = append(tools, sdk.Tool{Name: name, Description: spec.description, InputSchema: spec.schema, HandlerCtx: func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			requested, _ := args["project_id"].(string)
			scopedApp, err := scoped(ctx, app, strings.TrimSpace(requested))
			if err != nil {
				return nil, err
			}
			return a.dispatch(ctx, scopedApp, name, args)
		}})
	}
	return tools
}
func (a *App) dispatch(ctx context.Context, app *sdk.AppCtx, name string, args map[string]any) (any, error) {
	s := store{app.AppDB(), app.CurrentProject()}
	var ids struct {
		ID         int64  `json:"id"`
		SuiteID    int64  `json:"suite_id"`
		RequestKey string `json:"request_key"`
	}
	if err := decodeArgs(args, &ids); err != nil {
		return nil, err
	}
	switch name {
	case "tests_suites_list":
		v, e := s.suites()
		return map[string]any{"suites": v}, e
	case "tests_suite_save":
		var v Suite
		if e := decodeArgs(args, &v); e != nil {
			return nil, e
		}
		v, e := s.saveSuite(v)
		return map[string]any{"suite": v}, e
	case "tests_suite_archive":
		return map[string]any{"archived": true}, s.archiveSuite(ids.ID)
	case "tests_checks_list":
		v, e := s.checks(ids.SuiteID)
		return map[string]any{"checks": v}, e
	case "tests_check_save":
		v := Check{Enabled: true}
		if e := decodeArgs(args, &v); e != nil {
			return nil, e
		}
		v, e := s.saveCheck(v)
		return map[string]any{"check": v}, e
	case "tests_check_delete":
		return map[string]any{"deleted": true}, s.deleteCheck(ids.ID)
	case "tests_run":
		key := ids.RequestKey
		if key != "" {
			key = "request:" + key
		} else if c := sdk.CallerFrom(ctx); c != nil && c.ToolCallID != "" {
			key = "tool:" + c.ToolCallID
		}
		v, e := s.queue(ids.SuiteID, "tool", key)
		return map[string]any{"run": v}, e
	case "tests_runs_list":
		v, e := s.runs(ids.SuiteID)
		return map[string]any{"runs": v}, e
	case "tests_run_get":
		v, e := s.run(ids.ID)
		return map[string]any{"run": v}, e
	}
	return nil, errors.New("unknown tool")
}
