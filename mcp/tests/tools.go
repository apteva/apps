package main

import (
	"context"
	"errors"
	"fmt"
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
	releaseArgs := map[string]any{"suite_id": id, "candidate_digest": str, "suite_revision": str, "fixture_revision": str}
	candidate := map[string]any{"type": "object", "description": "Deploy candidate resolved when the run is queued: {deployment_id, environment?, build: \"live\" | \"latest_succeeded\" | build ID}.", "properties": map[string]any{"deployment_id": id, "environment": str, "build": map[string]any{}}}
	assertion := object(map[string]any{"path": map[string]any{"type": "string", "description": "JSON pointer into runner output; /response/total for functions, /status_code or /json/total for HTTP. Empty selects the whole output."}, "op": map[string]any{"type": "string", "enum": []string{"equals", "not_equals", "exists", "contains", "lt", "lte", "gt", "gte"}}, "value": map[string]any{}}, "path", "op")
	definition := object(map[string]any{
		"function": str, "event": map[string]any{}, "url": str, "target": str, "path": str, "method": str,
		"header_refs": map[string]any{"type": "object", "description": "Header name → {ref: \"credentials.<alias>.<field>\", prefix?}. Needs a named target."},
		"budget_ms":   map[string]any{"type": "integer", "minimum": 0, "description": "Fail the check when it takes longer than this."},
		"diagnostics": map[string]any{"description": "Existing-mode browser checks (Computer 0.14.0+): \"off\", or {fail_on?: [exceptions, target_requests], fail_origins?: [origin], allow?: [{path_prefix, status?, origin?}], console_errors?: record|fail}. Default: page exceptions and target-origin requests with status >= 400 or no response fail the check."},
		"session":     map[string]any{"type": "string", "enum": []string{"none"}, "description": "Opt out of the suite session, e.g. to test sign-in itself."}, "headers": map[string]any{"type": "object", "additionalProperties": str}, "body": str,
		"timeout_ms": map[string]any{"type": "integer", "minimum": 100, "maximum": 30000, "default": 10000},
		"assertions": map[string]any{"type": "array", "items": assertion, "minItems": 1, "maxItems": 50},
		"steps":      map[string]any{"type": "array", "items": field("object"), "description": "Scenario and browser steps. Isolated runs with simulated integrations (Environments 0.12.0+) also take {kind: \"integration_trigger\", slug, trigger, input?, app?, role?} (fires a provider event such as incoming_call and waits for its webhooks; output {result, deliveries}; never in parallel groups, cannot poll) and {kind: \"integration_state\", slug, app?, role?, poll_every_ms?} (output {collections, deliveries})."},
		"code":       map[string]any{"type": "object", "description": "Code checks (kind code, Workspaces 0.8.0+): {source: {from: \"candidate\" | \"code\", repository?, subdir?}, runtime?: {profile: \"bun-test\"}, prepare?: {argv, env?, network?: registry_only|none|default, timeout_s?, working_directory?}, test: {argv, env?, timeout_s?, working_directory?}, results: {junit, coverage?}, timeout_s? (60–3600, default 900), retry_flaky?, use_cache?}. argv runs without a shell; \"$TESTS_OUT\" in argv and env is replaced by the results directory; results paths are relative to it. prepare runs as the builder user (may install dependencies); test runs as the runner user without network. Assertions are optional and address the output, e.g. /summary/failed or /coverage/lines_pct."},
	})
	specs := []struct {
		name, description string
		schema            map[string]any
	}{
		{"tests_suite_get", "Get a suite including its frozen release configuration and OpenAPI contract.", object(map[string]any{"id": id}, "id")},
		{"tests_suites_list", "List active suites in the current project.", object(map[string]any{})},
		{"tests_suite_save", "Create (omit id) or replace a suite. Optional target runs it against deployed endpoints (mode existing) or an isolated environment Tests generates and pins (mode isolated). credentials maps aliases to entries in the encrypted credentials setting; session signs in once per run. release keeps the hand-written isolated configuration.", object(map[string]any{"id": id, "name": str, "description": str, "environment": str, "release": map[string]any{"type": []string{"object", "null"}}, "target": map[string]any{"type": []string{"object", "null"}}, "credentials": map[string]any{"type": []string{"object", "null"}}, "session": map[string]any{"type": []string{"object", "null"}}, "release_blocking": map[string]any{"type": "boolean", "description": "Include the suite in tests_release_verdicts: a candidate passes only when every release-blocking suite passed for it."}}, "name")},
		{"tests_suite_archive", "Archive a suite. Existing queued runs and history remain.", object(map[string]any{"id": id}, "id")},
		{"tests_checks_list", "List all enabled and disabled checks in a suite.", object(map[string]any{"suite_id": id}, "suite_id")},
		{"tests_check_save", "Create (omit id) or fully replace a check or scenario. Standalone Function checks invoke the active version; HTTP checks do not follow redirects. Requires at least one assertion.", object(map[string]any{"id": id, "suite_id": id, "name": str, "kind": map[string]any{"type": "string", "enum": []string{"function", "http", "scenario", "browser", "code"}}, "enabled": map[string]any{"type": "boolean", "default": true}, "definition": definition}, "suite_id", "name", "kind", "definition")},
		{"tests_check_delete", "Delete a check. Prior runs retain their immutable definitions and results.", object(map[string]any{"id": id}, "id")},
		{"tests_run", "Queue a suite run. Returns immediately; poll tests_run_get. Optional request_key deduplicates retries (a retry reuses the build resolved first). Pass candidate to resolve a Deploy build, or candidate_digest. Pass check_names to run only those checks (enabled or not) plus the session setup check; such runs never produce a release verdict and leave enabled flags unchanged. Jobs may call this tool with suite_id.", object(map[string]any{"suite_id": id, "request_key": str, "candidate_digest": str, "candidate": candidate, "suite_revision": str, "fixture_revision": str,
			"check_names": map[string]any{"type": "array", "items": str, "maxItems": 50, "description": "Run only these checks by name; the session setup check is added automatically."}}, "suite_id")},
		{"tests_runs_list", "List the most recent 100 runs, optionally filtered by suite_id.", object(map[string]any{"suite_id": id})},
		{"tests_run_cancel", "Cancel a queued or running test.", object(map[string]any{"id": id}, "id")},
		{"tests_release_spec", "Resolve the current suite and fixture digests for release policy configuration.", object(map[string]any{"suite_id": id}, "suite_id")},
		{"tests_release_verdict", "Verify a stored verdict for an exact candidate and pinned suite and fixture revisions.", object(releaseArgs, "suite_id", "candidate_digest", "suite_revision", "fixture_revision")},
		{"tests_run_get", "Get a run's status, check snapshots, assertion outcomes and saved evidence.", object(map[string]any{"id": id}, "id")},
		{"tests_release_verdicts", "Verdict for a candidate across every release-blocking suite: allowed only when each one has a passing, matching run for this candidate digest (and source digest, when given).", object(map[string]any{"candidate_digest": str, "source_digest": str}, "candidate_digest")},
		{"tests_cases_list", "List test cases code checks reported, newest first. Filter by run, suite, check, status (failing = failed or error), file or case_key.", object(map[string]any{"run_id": id, "suite_id": id, "check_id": id, "status": map[string]any{"type": "string", "enum": []string{"passed", "failed", "error", "skipped", "flaky", "failing"}}, "file": str, "case_key": str, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "offset": map[string]any{"type": "integer", "minimum": 0}})},
		{"tests_case_history", "Per-case history in a suite: runs, failures, flaky reruns, the last 20 outcomes (p passed, f failed, e error, s skipped, y flaky) and quarantine state. unstable=true keeps cases that failed or flaked.", object(map[string]any{"suite_id": id, "case_key": str, "unstable": field("boolean"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}, "suite_id")},
		{"tests_quarantine_set", "Quarantine a case (failures are recorded but do not fail its code check) for days (1–90, default 14) or until expires_at, with a reason. remove=true lifts it. Every quarantine expires.", object(map[string]any{"suite_id": id, "case_key": str, "reason": str, "days": map[string]any{"type": "integer", "minimum": 1, "maximum": 90}, "expires_at": str, "remove": field("boolean")}, "suite_id", "case_key")},
		{"tests_quarantine_list", "List quarantined cases, optionally for one suite; include_expired adds lapsed entries.", object(map[string]any{"suite_id": id, "include_expired": field("boolean")})},
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
		QueueOptions
	}
	if err := decodeArgsLimit(args, &ids, maxSuitePayload); err != nil {
		return nil, err
	}
	switch name {
	case "tests_suites_list":
		v, e := s.suites()
		return map[string]any{"suites": v}, e
	case "tests_suite_get":
		// Archived suites remain readable from saved-run dashboard links.
		v, e := scanSuite(s.db.QueryRow(`SELECT `+suiteColumns+` FROM test_suites WHERE project_id=? AND id=?`, s.project, ids.ID))
		return map[string]any{"suite": v}, e
	case "tests_suite_save":
		var v Suite
		if e := decodeArgsLimit(args, &v, maxSuitePayload); e != nil {
			return nil, e
		}
		if v.Target != nil && v.Target.Environment != nil {
			v.Target.Environment.Pinned = nil // Tests owns the pin.
		}
		if e := validateSuiteExtensions(&v); e != nil {
			return nil, e
		}
		if v.Target != nil && v.Target.Candidate != nil {
			if e := checkDeployBinding(ctx, app, v.Target.Candidate); e != nil {
				return nil, e
			}
		}
		var previous *Suite
		if v.ID != 0 {
			if old, e := s.getSuite(v.ID); e == nil {
				previous = &old
			}
		}
		if e := a.pinEnvironment(ctx, app, &v, previous); e != nil {
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
		if v.Enabled && hasBrowser(v) {
			suite, e := s.getSuite(v.SuiteID)
			if e != nil {
				return nil, e
			}
			if e = a.checkBrowserBinding(ctx, app, suite); e != nil {
				return nil, fmt.Errorf("browser checks need Computer: %w", e)
			}
			if suite.Target != nil && suite.Target.Mode == "existing" && usesReadPaths(v) && !computerSupportsReads(ctx, app) {
				return nil, errors.New(computerReadVersionMessage)
			}
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
		v, e := a.queue(ctx, app, ids.SuiteID, "tool", key, ids.QueueOptions)
		return map[string]any{"run": v}, e
	case "tests_run_cancel":
		v, e := s.cancel(ids.ID)
		return map[string]any{"run": v}, e
	case "tests_release_spec":
		return a.releaseSpec(ctx, app, ids.SuiteID)
	case "tests_release_verdict":
		return s.verdict(ids.SuiteID, ids.QueueOptions)
	case "tests_release_verdicts":
		return a.releaseVerdicts(ctx, app, ids.CandidateDigest, ids.SourceDigest)
	case "tests_cases_list":
		var f caseFilter
		if e := decodeArgs(args, &f); e != nil {
			return nil, e
		}
		v, e := s.cases(f)
		return map[string]any{"cases": v}, e
	case "tests_case_history":
		var f struct {
			SuiteID  int64  `json:"suite_id"`
			CaseKey  string `json:"case_key"`
			Unstable bool   `json:"unstable"`
			Limit    int    `json:"limit"`
		}
		if e := decodeArgs(args, &f); e != nil {
			return nil, e
		}
		v, e := s.caseHistories(f.SuiteID, f.CaseKey, f.Unstable, f.Limit)
		return map[string]any{"cases": v}, e
	case "tests_quarantine_set":
		var req quarantineRequest
		if e := decodeArgs(args, &req); e != nil {
			return nil, e
		}
		v, e := s.setQuarantine(req, callerLabel(ctx))
		if req.Remove {
			return map[string]any{"removed": e == nil}, e
		}
		return map[string]any{"quarantine": v}, e
	case "tests_quarantine_list":
		var f struct {
			SuiteID        int64 `json:"suite_id"`
			IncludeExpired bool  `json:"include_expired"`
		}
		if e := decodeArgs(args, &f); e != nil {
			return nil, e
		}
		v, e := s.quarantineList(f.SuiteID, f.IncludeExpired)
		return map[string]any{"quarantine": v}, e
	case "tests_runs_list":
		v, e := s.runs(ids.SuiteID)
		return map[string]any{"runs": v}, e
	case "tests_run_get":
		v, e := s.run(ids.ID)
		return map[string]any{"run": v}, e
	}
	return nil, errors.New("unknown tool")
}
