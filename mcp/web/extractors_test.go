package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func testExtractorDefinition() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"defaults":       map[string]any{"start_url": "https://example.com", "max_pages": 2, "country": "US"},
		"presets": map[string]any{
			"de": map[string]any{"country": "DE", "viewport": map[string]any{"width": 1440, "height": 900}},
		},
		"browser":       map[string]any{"backend": "browserbase", "proxy_mode": "managed", "proxy_country": "{{country}}", "persist": false},
		"allowed_hosts": []any{"example.com"},
		"limits":        map[string]any{"max_pages": "{{max_pages}}", "max_items": 100, "max_duration_seconds": 60, "step_retries": 1},
		"steps": []any{
			map[string]any{"action": "goto", "url": "{{start_url}}"},
			map[string]any{"action": "extract", "items": "body", "fields": map[string]any{
				"name":  map[string]any{"selector": "h1", "type": "text", "required": true},
				"price": map[string]any{"selector": "p", "type": "text"},
			}},
		},
		"output_schema": map[string]any{"name": "string", "price": "string"},
	}
}

func TestExtractorCRUDIncrementsRevisionAndPreservesSnapshot(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	createdAny, err := app.toolExtractorSave(ctx, map[string]any{"name": "Products", "description": "Catalog", "definition": testExtractorDefinition()})
	if err != nil {
		t.Fatalf("create extractor: %v", err)
	}
	created := createdAny.(map[string]any)["extractor"].(*extractorRecord)
	if created.Revision != 1 || created.ID == 0 {
		t.Fatalf("created=%#v", created)
	}

	runAny, err := app.toolExtractorRun(ctx, map[string]any{"extractor_id": created.ID, "preset": "de", "input": map[string]any{"start_url": "https://example.com/products"}})
	if err != nil {
		t.Fatalf("queue extractor: %v", err)
	}
	runID := runAny.(map[string]any)["run_id"].(int64)

	definition := testExtractorDefinition()
	definition["defaults"].(map[string]any)["max_pages"] = 9
	updatedAny, err := app.toolExtractorSave(ctx, map[string]any{"id": created.ID, "name": created.Name, "expected_revision": 1, "definition": definition})
	if err != nil {
		t.Fatalf("update extractor: %v", err)
	}
	updated := updatedAny.(map[string]any)["extractor"].(*extractorRecord)
	if updated.Revision != 2 {
		t.Fatalf("revision=%d, want 2", updated.Revision)
	}

	run, err := getWebRun(ctx, runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	snapshot := run["definition_snapshot"].(map[string]any)
	defaults := snapshot["defaults"].(map[string]any)
	if got := intFromAny(defaults["max_pages"]); got != 2 {
		t.Fatalf("snapshot max_pages=%d, want original 2", got)
	}
	if run["extractor_revision"] != int64(1) {
		t.Fatalf("run revision=%v", run["extractor_revision"])
	}

	if _, err := app.toolExtractorSave(ctx, map[string]any{"id": created.ID, "name": created.Name, "expected_revision": 1, "definition": definition}); err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatalf("expected revision conflict, got %v", err)
	}
}

func TestExtractorRunExecutesAndStoresBoundedArtifacts(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	createdAny, err := app.toolExtractorSave(ctx, map[string]any{"name": "Products", "definition": testExtractorDefinition()})
	if err != nil {
		t.Fatal(err)
	}
	created := createdAny.(map[string]any)["extractor"].(*extractorRecord)
	runAny, err := app.toolExtractorRun(ctx, map[string]any{"extractor_id": created.ID, "preset": "de", "input": map[string]any{"start_url": "https://example.com/products"}})
	if err != nil {
		t.Fatal(err)
	}
	runID := runAny.(map[string]any)["run_id"].(int64)
	queued, err := claimExtractorRun(ctx)
	if err != nil || queued == nil {
		t.Fatalf("claim: run=%#v err=%v", queued, err)
	}
	if err := app.executeExtractorRun(context.Background(), ctx, queued); err != nil {
		t.Fatalf("execute: %v", err)
	}

	run, err := getWebRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run["status"] != "completed" {
		t.Fatalf("status=%v error=%v", run["status"], run["error"])
	}
	out := run["output"].(map[string]any)
	if intFromAny(out["item_count"]) != 1 || intFromAny(out["page_count"]) != 1 {
		t.Fatalf("output=%#v", out)
	}
	items := out["items"].([]any)
	item := items[0].(map[string]any)
	if item["name"] != "Hello" {
		t.Fatalf("item=%#v", item)
	}
	for _, key := range []string{"dataset_artifact_id", "csv_artifact_id", "trace_artifact_id", "screenshot_artifact_id"} {
		if intFromAny(out[key]) == 0 {
			t.Errorf("missing %s in %#v", key, out)
		}
	}
	if countCalls(plat, "computer", "browser_close") != 1 {
		t.Fatalf("browser close calls=%d, want 1", countCalls(plat, "computer", "browser_close"))
	}
	open := plat.lastCall("computer", "browser_open")
	if open["backend"] != "browserbase" || open["proxy_country"] != "DE" || open["proxy_mode"] != "managed" {
		t.Fatalf("rendered browser args=%#v", open)
	}
	if _, legacy := open["proxy"]; legacy {
		t.Fatalf("extractor used legacy provider-coupled proxy flag: %#v", open)
	}
}

func TestExtractorSelectorClickUsesComputerAndAssertsRedirect(t *testing.T) {
	plat := newFakePlatform()
	plat.selectorRedirectURL = "https://digilo.co/register?aff=qa"
	ctx, app := newTestCtx(t, plat)
	definition := map[string]any{
		"schema_version": 1,
		"browser":        map[string]any{"backend": "browserbase", "proxy_mode": "managed", "proxy_country": "FR"},
		"allowed_hosts":  []any{"marcoschwartz.com", "go.marcoschwartz.com", "digilo.co"},
		"limits":         map[string]any{"max_duration_seconds": 60, "step_retries": 0},
		"steps": []any{
			map[string]any{"action": "goto", "url": "https://marcoschwartz.com/digilo-review"},
			map[string]any{"action": "click", "locator": map[string]any{"selector": `a[href="https://go.marcoschwartz.com/digilo"]`}},
			map[string]any{"action": "assert_url", "host": "digilo.co", "path_prefix": "/register"},
		},
		"output_schema": map[string]any{},
	}
	createdAny, err := app.toolExtractorSave(ctx, map[string]any{"name": "Digilo redirect QA", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["extractor"].(*extractorRecord).ID
	runAny, err := app.toolExtractorRun(ctx, map[string]any{"extractor_id": id})
	if err != nil {
		t.Fatal(err)
	}
	queued, _ := claimExtractorRun(ctx)
	if err := app.executeExtractorRun(context.Background(), ctx, queued); err != nil {
		t.Fatal(err)
	}
	run, _ := getWebRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	if run["status"] != "completed" {
		t.Fatalf("run=%#v", run)
	}
	out := run["output"].(map[string]any)
	if out["current_url"] != plat.selectorRedirectURL {
		t.Fatalf("current_url=%v", out["current_url"])
	}
	open := plat.lastCall("computer", "browser_open")
	if open["proxy_mode"] != "managed" || open["proxy_country"] != "FR" {
		t.Fatalf("proxy args=%#v", open)
	}
	if intFromAny(open["timeout"]) < 60 {
		t.Fatalf("browserbase timeout=%v, want at least 60 seconds", open["timeout"])
	}
	foundSelector := false
	for _, call := range plat.callsSnapshot() {
		if call.app == "computer" && call.tool == "computer_use" && call.args["action"] == "click" {
			if call.args["selector"] == `a[href="https://go.marcoschwartz.com/digilo"]` {
				foundSelector = true
			}
			if call.args["coordinate"] != nil || call.args["label"] != nil {
				t.Fatalf("selector click unexpectedly used visual targeting: %#v", call.args)
			}
		}
	}
	if !foundSelector {
		t.Fatal("selector was not passed directly to Computer")
	}
}

func TestExtractorForwardsProviderNeutralComputerProxyProfile(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	definition := map[string]any{
		"schema_version": 1,
		"browser": map[string]any{
			"backend": "local", "proxy_mode": "profile", "proxy_profile": "qa-fr",
			"proxy_country": "FR", "proxy_sticky": "session",
		},
		"allowed_hosts": []any{"example.com"},
		"limits":        map[string]any{"max_duration_seconds": 60, "step_retries": 0},
		"steps": []any{
			map[string]any{"action": "goto", "url": "https://example.com/qa"},
			map[string]any{"action": "assert_url", "host": "example.com", "path_prefix": "/qa"},
		},
		"output_schema": map[string]any{},
	}
	createdAny, err := app.toolExtractorSave(ctx, map[string]any{"name": "Profile proxy QA", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["extractor"].(*extractorRecord).ID
	runAny, _ := app.toolExtractorRun(ctx, map[string]any{"extractor_id": id})
	queued, _ := claimExtractorRun(ctx)
	_ = app.executeExtractorRun(context.Background(), ctx, queued)
	run, _ := getWebRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	if run["status"] != "completed" {
		t.Fatalf("run=%#v", run)
	}
	open := plat.lastCall("computer", "browser_open")
	if open["backend"] != "local" || open["proxy_mode"] != "profile" || open["proxy_profile"] != "qa-fr" || open["proxy_country"] != "FR" || open["proxy_sticky"] != "session" {
		t.Fatalf("computer proxy contract not forwarded: %#v", open)
	}
}

func TestExtractorFailsClosedWhenComputerResolvesWrongProxyCountry(t *testing.T) {
	plat := newFakePlatform()
	plat.proxyCountryOverride = "DE"
	ctx, app := newTestCtx(t, plat)
	definition := testExtractorDefinition()
	definition["defaults"].(map[string]any)["country"] = "FR"
	definition["limits"].(map[string]any)["step_retries"] = 0
	createdAny, err := app.toolExtractorSave(ctx, map[string]any{"name": "Proxy validation", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["extractor"].(*extractorRecord).ID
	runAny, _ := app.toolExtractorRun(ctx, map[string]any{"extractor_id": id})
	queued, _ := claimExtractorRun(ctx)
	_ = app.executeExtractorRun(context.Background(), ctx, queued)
	run, _ := getWebRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	if run["status"] != "failed" || !strings.Contains(stringFromAny(run["error"]), `proxy country "DE", expected "FR"`) {
		t.Fatalf("run=%#v", run)
	}
	if countCalls(plat, "computer", "browser_open") != 1 || countCalls(plat, "computer", "browser_close") != 1 {
		t.Fatalf("opens=%d closes=%d", countCalls(plat, "computer", "browser_open"), countCalls(plat, "computer", "browser_close"))
	}
}

func TestExtractorPaginatesWithSemanticRegionBeforeCoordinateFallback(t *testing.T) {
	plat := newFakePlatform()
	plat.extractorPagination = true
	ctx, app := newTestCtx(t, plat)
	definition := testExtractorDefinition()
	definition["browser"].(map[string]any)["proxy_mode"] = "none"
	delete(definition["browser"].(map[string]any), "proxy_country")
	definition["steps"] = []any{
		map[string]any{"action": "goto", "url": "{{start_url}}"},
		map[string]any{"action": "extract", "items": "article", "fields": map[string]any{"name": map[string]any{"selector": "h1", "type": "text"}}},
		map[string]any{"action": "paginate", "locator": map[string]any{"text": "Next", "role": "link"}, "max_pages": 2},
	}
	definition["output_schema"] = map[string]any{"name": "string"}
	createdAny, err := app.toolExtractorSave(ctx, map[string]any{"name": "Paged products", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["extractor"].(*extractorRecord).ID
	runAny, _ := app.toolExtractorRun(ctx, map[string]any{"extractor_id": id, "input": map[string]any{}})
	queued, _ := claimExtractorRun(ctx)
	_ = app.executeExtractorRun(context.Background(), ctx, queued)
	run, _ := getWebRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	out := run["output"].(map[string]any)
	if run["status"] != "completed" || intFromAny(out["page_count"]) != 2 || intFromAny(out["item_count"]) != 2 {
		t.Fatalf("run=%#v", run)
	}
	foundCoordinate := false
	for _, call := range plat.callsSnapshot() {
		if call.app == "computer" && call.tool == "computer_use" && call.args["action"] == "click" && stringFromAny(call.args["coordinate"]) != "" {
			foundCoordinate = true
		}
	}
	if !foundCoordinate {
		t.Fatal("pagination did not resolve the semantic region to a coordinate fallback")
	}
}

func TestExtractorScheduledDeliveryIsIdempotent(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	createdAny, err := app.toolExtractorSave(ctx, map[string]any{"name": "Products", "definition": testExtractorDefinition()})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["extractor"].(*extractorRecord).ID
	args := map[string]any{"extractor_id": id, "schedule_key": "sched_test", "trigger_bucket": "2026-08-10T12:00:00Z", "input": map[string]any{}}
	firstAny, err := app.toolExtractorRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	secondAny, err := app.toolExtractorRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(map[string]any)
	second := secondAny.(map[string]any)
	if first["run_id"] != second["run_id"] || second["duplicate"] != true {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM web_runs WHERE kind='extractor'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestExtractorCancellationAndRetryUseSnapshot(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	createdAny, _ := app.toolExtractorSave(ctx, map[string]any{"name": "Products", "definition": testExtractorDefinition()})
	id := createdAny.(map[string]any)["extractor"].(*extractorRecord).ID
	runAny, _ := app.toolExtractorRun(ctx, map[string]any{"extractor_id": id, "input": map[string]any{}})
	runID := runAny.(map[string]any)["run_id"].(int64)
	cancelAny, err := app.toolWebRunCancel(ctx, map[string]any{"id": runID})
	if err != nil || cancelAny.(map[string]any)["cancel_requested"] != true {
		t.Fatalf("cancel=%#v err=%v", cancelAny, err)
	}
	if queued, err := claimExtractorRun(ctx); err != nil || queued != nil {
		t.Fatalf("cancelled run was claimable: %#v err=%v", queued, err)
	}
	retryAny, err := app.toolWebRunRetry(ctx, map[string]any{"id": runID})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	retryID := retryAny.(map[string]any)["run_id"].(int64)
	var original, retried string
	if err := ctx.AppDB().QueryRow(`SELECT definition_snapshot_json FROM web_runs WHERE id=?`, runID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT definition_snapshot_json FROM web_runs WHERE id=?`, retryID).Scan(&retried); err != nil {
		t.Fatal(err)
	}
	if original != retried {
		t.Fatal("retry did not preserve the immutable definition snapshot")
	}
}

func TestExtractorRejectsDisallowedHostBeforeBrowserOpen(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	createdAny, _ := app.toolExtractorSave(ctx, map[string]any{"name": "Products", "definition": testExtractorDefinition()})
	id := createdAny.(map[string]any)["extractor"].(*extractorRecord).ID
	runAny, _ := app.toolExtractorRun(ctx, map[string]any{"extractor_id": id, "input": map[string]any{"start_url": "https://evil.example.net"}})
	queued, _ := claimExtractorRun(ctx)
	_ = app.executeExtractorRun(context.Background(), ctx, queued)
	run, _ := getWebRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	if run["status"] != "failed" || !strings.Contains(stringFromAny(run["error"]), "allowed_hosts") {
		t.Fatalf("run=%#v", run)
	}
	if countCalls(plat, "computer", "browser_open") != 0 {
		t.Fatal("browser opened for a disallowed host")
	}
}

func TestExtractorNeverFallsBackFromRequestedBackend(t *testing.T) {
	plat := newFakePlatform()
	plat.openBackendOverride = "local"
	ctx, app := newTestCtx(t, plat)
	definition := testExtractorDefinition()
	definition["browser"].(map[string]any)["backend"] = "browserbase"
	createdAny, err := app.toolExtractorSave(ctx, map[string]any{"name": "Cloud products", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["extractor"].(*extractorRecord).ID
	runAny, _ := app.toolExtractorRun(ctx, map[string]any{"extractor_id": id, "input": map[string]any{}})
	queued, _ := claimExtractorRun(ctx)
	_ = app.executeExtractorRun(context.Background(), ctx, queued)
	run, _ := getWebRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	if run["status"] != "failed" || !strings.Contains(stringFromAny(run["error"]), "expected \"browserbase\"") {
		t.Fatalf("run=%#v", run)
	}
	if countCalls(plat, "computer", "browser_open") != 1 || countCalls(plat, "computer", "browser_close") != 1 {
		t.Fatalf("opens=%d closes=%d", countCalls(plat, "computer", "browser_open"), countCalls(plat, "computer", "browser_close"))
	}
}

func TestExtractorScheduleDelegatesToJobs(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	createdAny, _ := app.toolExtractorSave(ctx, map[string]any{"name": "Products", "definition": testExtractorDefinition()})
	id := createdAny.(map[string]any)["extractor"].(*extractorRecord).ID
	out, err := app.toolExtractorSchedule(ctx, map[string]any{"extractor_id": id, "schedule": map[string]any{"kind": "every", "every_seconds": 8640}, "timezone": "Europe/Berlin"})
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	result := out.(map[string]any)
	if !strings.HasPrefix(stringFromAny(result["schedule_key"]), "sched_") {
		t.Fatalf("result=%#v", result)
	}
	call := plat.lastCall("jobs", "jobs_schedule")
	if call["owner_app"] != "web" || call["timezone"] != "Europe/Berlin" {
		t.Fatalf("jobs args=%#v", call)
	}
	target := call["target"].(map[string]any)
	if target["app"] != "web" || target["tool"] != "web_extractor_run" {
		t.Fatalf("target=%#v", target)
	}
}

func TestMoneyParsingHandlesCommaAndDotLocales(t *testing.T) {
	for input, want := range map[string]float64{"€1.234,56": 1234.56, "$1,234.56": 1234.56, "29.95 EUR": 29.95} {
		got, err := parseExtractorNumber(input)
		if err != nil || got != want {
			t.Errorf("parse %q=%v,%v want %v", input, got, err, want)
		}
	}
}

func TestExtractorCSVNeutralizesFormulaStrings(t *testing.T) {
	b, err := encodeExtractorCSV([]map[string]any{{"name": "=HYPERLINK(\"https://bad.test\")", "price": -2.5}}, map[string]string{"name": "string", "price": "number"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "'=HYPERLINK") {
		t.Fatalf("formula string was not neutralized: %q", text)
	}
	if !strings.Contains(text, "-2.5") {
		t.Fatalf("numeric value was changed: %q", text)
	}
}

func TestExtractorManifestDeclaresRuntimeSurface(t *testing.T) {
	manifest := (&App{}).Manifest()
	wantTools := []string{"web_extractor_save", "web_extractor_run", "web_run_get", "web_run_cancel", "web_run_retry", "web_extractor_schedule"}
	names := toolNames(manifest.Provides.MCPTools)
	for _, want := range wantTools {
		if !containsString(names, want) {
			t.Errorf("manifest missing %s", want)
		}
	}
	if len(manifest.Provides.Workers) != 1 || manifest.Provides.Workers[0].Name != "extractor-runner" {
		t.Fatalf("workers=%#v", manifest.Provides.Workers)
	}
	dependencies := make([]string, 0, len(manifest.Requires.Apps))
	for _, dependency := range manifest.Requires.Apps {
		dependencies = append(dependencies, dependency.Name)
	}
	for _, want := range []string{"computer", "storage", "jobs"} {
		if !containsString(dependencies, want) {
			t.Errorf("manifest dependencies %v missing %s", dependencies, want)
		}
	}
	runtimeTools := (&App{}).MCPTools()
	runtimeNames := make([]string, 0, len(runtimeTools))
	for _, tool := range runtimeTools {
		runtimeNames = append(runtimeNames, tool.Name)
	}
	for _, want := range names {
		if !containsString(runtimeNames, want) {
			t.Errorf("manifest tool %s missing at runtime", want)
		}
	}
	_, _ = json.Marshal(manifest)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
