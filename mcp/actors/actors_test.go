package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func testActorDefinition() map[string]any {
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

func TestActorCRUDIncrementsRevisionAndPreservesSnapshot(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	createdAny, err := app.toolActorSave(ctx, map[string]any{"name": "Products", "description": "Catalog", "definition": testActorDefinition()})
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	created := createdAny.(map[string]any)["actor"].(*actorRecord)
	if created.Revision != 1 || created.ID == 0 {
		t.Fatalf("created=%#v", created)
	}

	runAny, err := app.toolActorRun(ctx, map[string]any{"actor_id": created.ID, "preset": "de", "input": map[string]any{"start_url": "https://example.com/products"}})
	if err != nil {
		t.Fatalf("queue actor: %v", err)
	}
	runID := runAny.(map[string]any)["run_id"].(int64)

	definition := testActorDefinition()
	definition["defaults"].(map[string]any)["max_pages"] = 9
	updatedAny, err := app.toolActorSave(ctx, map[string]any{"id": created.ID, "name": created.Name, "expected_revision": 1, "definition": definition})
	if err != nil {
		t.Fatalf("update actor: %v", err)
	}
	updated := updatedAny.(map[string]any)["actor"].(*actorRecord)
	if updated.Revision != 2 {
		t.Fatalf("revision=%d, want 2", updated.Revision)
	}

	run, err := getActorRun(ctx, runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	snapshot := run["definition_snapshot"].(map[string]any)
	defaults := snapshot["defaults"].(map[string]any)
	if got := intFromAny(defaults["max_pages"]); got != 2 {
		t.Fatalf("snapshot max_pages=%d, want original 2", got)
	}
	if run["actor_revision"] != int64(1) {
		t.Fatalf("run revision=%v", run["actor_revision"])
	}

	if _, err := app.toolActorSave(ctx, map[string]any{"id": created.ID, "name": created.Name, "expected_revision": 1, "definition": definition}); err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatalf("expected revision conflict, got %v", err)
	}
}

func TestActorRunExecutesAndStoresBoundedArtifacts(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	createdAny, err := app.toolActorSave(ctx, map[string]any{"name": "Products", "definition": testActorDefinition()})
	if err != nil {
		t.Fatal(err)
	}
	created := createdAny.(map[string]any)["actor"].(*actorRecord)
	runAny, err := app.toolActorRun(ctx, map[string]any{"actor_id": created.ID, "preset": "de", "input": map[string]any{"start_url": "https://example.com/products"}})
	if err != nil {
		t.Fatal(err)
	}
	runID := runAny.(map[string]any)["run_id"].(int64)
	queued, err := claimActorRun(ctx)
	if err != nil || queued == nil {
		t.Fatalf("claim: run=%#v err=%v", queued, err)
	}
	if err := app.executeActorRun(context.Background(), ctx, queued); err != nil {
		t.Fatalf("execute: %v", err)
	}

	run, err := getActorRun(ctx, runID)
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
		t.Fatalf("actor used legacy provider-coupled proxy flag: %#v", open)
	}
}

func TestActorForwardsTemplatedComputerEnvironmentAndAuditsPreset(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	definition := map[string]any{
		"schema_version": 1,
		"defaults": map[string]any{
			"start_url": "https://example.com", "country": "FR",
			"width": 390, "height": 844, "locale": "fr-FR",
			"languages": []any{"fr-FR", "fr"}, "timezone": "Europe/Paris",
			"latitude": 48.8566, "longitude": 2.3522, "scale": 3,
			"mobile": true, "touch": true, "touch_points": 5,
			"user_agent": "QA Mobile Browser",
		},
		"presets": map[string]any{
			"fr_mobile": map[string]any{"country": "FR"},
		},
		"browser": map[string]any{
			"backend": "browserbase", "proxy_mode": "managed", "proxy_country": "{{country}}",
			"viewport": map[string]any{"width": "{{width}}", "height": "{{height}}"},
			"environment": map[string]any{
				"user_agent": "{{user_agent}}", "locale": "{{locale}}", "languages": "{{languages}}",
				"timezone": "{{timezone}}", "device_scale_factor": "{{scale}}",
				"mobile": "{{mobile}}", "touch": "{{touch}}", "max_touch_points": "{{touch_points}}",
				"geolocation": map[string]any{
					"latitude": "{{latitude}}", "longitude": "{{longitude}}", "accuracy": 50, "permission": "grant",
				},
			},
		},
		"allowed_hosts": []any{"example.com"},
		"limits":        map[string]any{"max_duration_seconds": 60, "step_retries": 0},
		"steps":         []any{map[string]any{"action": "goto", "url": "{{start_url}}"}},
		"output_schema": map[string]any{},
	}
	createdAny, err := app.toolActorSave(ctx, map[string]any{"name": "Environment QA", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	runAny, err := app.toolActorRun(ctx, map[string]any{"actor_id": id, "preset": "fr_mobile"})
	if err != nil {
		t.Fatal(err)
	}
	queued, _ := claimActorRun(ctx)
	if err := app.executeActorRun(context.Background(), ctx, queued); err != nil {
		t.Fatal(err)
	}

	open := plat.lastCall("computer", "browser_open")
	environment, ok := open["environment"].(map[string]any)
	if !ok {
		t.Fatalf("environment was not forwarded: %#v", open)
	}
	if environment["locale"] != "fr-FR" || environment["timezone"] != "Europe/Paris" || environment["mobile"] != true || environment["touch"] != true {
		t.Fatalf("environment=%#v", environment)
	}
	languages := stringSliceFromAny(environment["languages"])
	if len(languages) != 2 || languages[0] != "fr-FR" || languages[1] != "fr" {
		t.Fatalf("languages=%#v", environment["languages"])
	}
	location := environment["geolocation"].(map[string]any)
	if latitude, _ := numericValue(location["latitude"]); latitude != 48.8566 {
		t.Fatalf("geolocation=%#v", location)
	}

	run, _ := getActorRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	out := run["output"].(map[string]any)
	if out["preset"] != "fr_mobile" {
		t.Fatalf("preset audit=%#v", out)
	}
	browser := out["browser_config"].(map[string]any)
	if _, ok := browser["environment"].(map[string]any); !ok {
		t.Fatalf("browser audit=%#v", browser)
	}
}

func TestActorRejectsInvalidBrowserEnvironment(t *testing.T) {
	for name, environment := range map[string]map[string]any{
		"unknown field":                {"canvas_noise": true},
		"invalid timezone":             {"timezone": "Paris/Definitely-Not-Real"},
		"touch points without touch":   {"touch": false, "max_touch_points": 5},
		"touch points with no setting": {"max_touch_points": 5},
		"invalid latitude":             {"geolocation": map[string]any{"latitude": 91, "longitude": 2.3}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, app := newTestCtx(t, newFakePlatform())
			definition := testActorDefinition()
			definition["browser"].(map[string]any)["environment"] = environment
			if _, err := app.toolActorSave(ctx, map[string]any{"name": "Invalid environment", "definition": definition}); err == nil || !strings.Contains(err.Error(), "browser.environment") {
				t.Fatalf("want environment validation error, got %v", err)
			}
		})
	}
}

func TestActorSelectorClickUsesComputerAndAssertsRedirect(t *testing.T) {
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
	createdAny, err := app.toolActorSave(ctx, map[string]any{"name": "Digilo redirect QA", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	runAny, err := app.toolActorRun(ctx, map[string]any{"actor_id": id})
	if err != nil {
		t.Fatal(err)
	}
	queued, _ := claimActorRun(ctx)
	if err := app.executeActorRun(context.Background(), ctx, queued); err != nil {
		t.Fatal(err)
	}
	run, _ := getActorRun(ctx, runAny.(map[string]any)["run_id"].(int64))
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

func TestActorForwardsProviderNeutralComputerProxyProfile(t *testing.T) {
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
	createdAny, err := app.toolActorSave(ctx, map[string]any{"name": "Profile proxy QA", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	runAny, _ := app.toolActorRun(ctx, map[string]any{"actor_id": id})
	queued, _ := claimActorRun(ctx)
	_ = app.executeActorRun(context.Background(), ctx, queued)
	run, _ := getActorRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	if run["status"] != "completed" {
		t.Fatalf("run=%#v", run)
	}
	open := plat.lastCall("computer", "browser_open")
	if open["backend"] != "local" || open["proxy_mode"] != "profile" || open["proxy_profile"] != "qa-fr" || open["proxy_country"] != "FR" || open["proxy_sticky"] != "session" {
		t.Fatalf("computer proxy contract not forwarded: %#v", open)
	}
}

func TestActorFailsClosedWhenComputerResolvesWrongProxyCountry(t *testing.T) {
	plat := newFakePlatform()
	plat.proxyCountryOverride = "DE"
	ctx, app := newTestCtx(t, plat)
	definition := testActorDefinition()
	definition["defaults"].(map[string]any)["country"] = "FR"
	definition["limits"].(map[string]any)["step_retries"] = 0
	createdAny, err := app.toolActorSave(ctx, map[string]any{"name": "Proxy validation", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	runAny, _ := app.toolActorRun(ctx, map[string]any{"actor_id": id})
	queued, _ := claimActorRun(ctx)
	_ = app.executeActorRun(context.Background(), ctx, queued)
	run, _ := getActorRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	if run["status"] != "failed" || !strings.Contains(stringFromAny(run["error"]), `proxy country "DE", expected "FR"`) {
		t.Fatalf("run=%#v", run)
	}
	if countCalls(plat, "computer", "browser_open") != 1 || countCalls(plat, "computer", "browser_close") != 1 {
		t.Fatalf("opens=%d closes=%d", countCalls(plat, "computer", "browser_open"), countCalls(plat, "computer", "browser_close"))
	}
}

func TestActorPaginatesWithSemanticRegionBeforeCoordinateFallback(t *testing.T) {
	plat := newFakePlatform()
	plat.actorPagination = true
	ctx, app := newTestCtx(t, plat)
	definition := testActorDefinition()
	definition["browser"].(map[string]any)["proxy_mode"] = "none"
	delete(definition["browser"].(map[string]any), "proxy_country")
	definition["steps"] = []any{
		map[string]any{"action": "goto", "url": "{{start_url}}"},
		map[string]any{"action": "extract", "items": "article", "fields": map[string]any{"name": map[string]any{"selector": "h1", "type": "text"}}},
		map[string]any{"action": "paginate", "locator": map[string]any{"text": "Next", "role": "link"}, "max_pages": 2},
	}
	definition["output_schema"] = map[string]any{"name": "string"}
	createdAny, err := app.toolActorSave(ctx, map[string]any{"name": "Paged products", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	runAny, _ := app.toolActorRun(ctx, map[string]any{"actor_id": id, "input": map[string]any{}})
	queued, _ := claimActorRun(ctx)
	_ = app.executeActorRun(context.Background(), ctx, queued)
	run, _ := getActorRun(ctx, runAny.(map[string]any)["run_id"].(int64))
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

func TestActorScheduledDeliveryIsIdempotent(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	createdAny, err := app.toolActorSave(ctx, map[string]any{"name": "Products", "definition": testActorDefinition()})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	args := map[string]any{"actor_id": id, "schedule_key": "sched_test", "trigger_bucket": "2026-08-10T12:00:00Z", "input": map[string]any{}}
	firstAny, err := app.toolActorRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	secondAny, err := app.toolActorRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(map[string]any)
	second := secondAny.(map[string]any)
	if first["run_id"] != second["run_id"] || second["duplicate"] != true {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_runs WHERE kind='actor'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestActorCancellationAndRetryUseSnapshot(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	createdAny, _ := app.toolActorSave(ctx, map[string]any{"name": "Products", "definition": testActorDefinition()})
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	runAny, _ := app.toolActorRun(ctx, map[string]any{"actor_id": id, "input": map[string]any{}})
	runID := runAny.(map[string]any)["run_id"].(int64)
	cancelAny, err := app.toolActorRunCancel(ctx, map[string]any{"id": runID})
	if err != nil || cancelAny.(map[string]any)["cancel_requested"] != true {
		t.Fatalf("cancel=%#v err=%v", cancelAny, err)
	}
	if queued, err := claimActorRun(ctx); err != nil || queued != nil {
		t.Fatalf("cancelled run was claimable: %#v err=%v", queued, err)
	}
	retryAny, err := app.toolActorRunRetry(ctx, map[string]any{"id": runID})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	retryID := retryAny.(map[string]any)["run_id"].(int64)
	var original, retried string
	if err := ctx.AppDB().QueryRow(`SELECT definition_snapshot_json FROM actors_runs WHERE id=?`, runID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT definition_snapshot_json FROM actors_runs WHERE id=?`, retryID).Scan(&retried); err != nil {
		t.Fatal(err)
	}
	if original != retried {
		t.Fatal("retry did not preserve the immutable definition snapshot")
	}
}

func TestActorRejectsDisallowedHostBeforeBrowserOpen(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	createdAny, _ := app.toolActorSave(ctx, map[string]any{"name": "Products", "definition": testActorDefinition()})
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	runAny, _ := app.toolActorRun(ctx, map[string]any{"actor_id": id, "input": map[string]any{"start_url": "https://evil.example.net"}})
	queued, _ := claimActorRun(ctx)
	_ = app.executeActorRun(context.Background(), ctx, queued)
	run, _ := getActorRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	if run["status"] != "failed" || !strings.Contains(stringFromAny(run["error"]), "allowed_hosts") {
		t.Fatalf("run=%#v", run)
	}
	if countCalls(plat, "computer", "browser_open") != 0 {
		t.Fatal("browser opened for a disallowed host")
	}
}

func TestActorNeverFallsBackFromRequestedBackend(t *testing.T) {
	plat := newFakePlatform()
	plat.openBackendOverride = "local"
	ctx, app := newTestCtx(t, plat)
	definition := testActorDefinition()
	definition["browser"].(map[string]any)["backend"] = "browserbase"
	createdAny, err := app.toolActorSave(ctx, map[string]any{"name": "Cloud products", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	runAny, _ := app.toolActorRun(ctx, map[string]any{"actor_id": id, "input": map[string]any{}})
	queued, _ := claimActorRun(ctx)
	_ = app.executeActorRun(context.Background(), ctx, queued)
	run, _ := getActorRun(ctx, runAny.(map[string]any)["run_id"].(int64))
	if run["status"] != "failed" || !strings.Contains(stringFromAny(run["error"]), "expected \"browserbase\"") {
		t.Fatalf("run=%#v", run)
	}
	if countCalls(plat, "computer", "browser_open") != 1 || countCalls(plat, "computer", "browser_close") != 1 {
		t.Fatalf("opens=%d closes=%d", countCalls(plat, "computer", "browser_open"), countCalls(plat, "computer", "browser_close"))
	}
}

func TestActorScheduleDelegatesToJobs(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	createdAny, _ := app.toolActorSave(ctx, map[string]any{"name": "Products", "definition": testActorDefinition()})
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	out, err := app.toolActorSchedule(ctx, map[string]any{"actor_id": id, "schedule": map[string]any{"kind": "every", "every_seconds": 8640}, "timezone": "Europe/Berlin"})
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	result := out.(map[string]any)
	if !strings.HasPrefix(stringFromAny(result["schedule_key"]), "sched_") {
		t.Fatalf("result=%#v", result)
	}
	call := plat.lastCall("jobs", "jobs_schedule")
	if call["owner_app"] != "actors" || call["timezone"] != "Europe/Berlin" {
		t.Fatalf("jobs args=%#v", call)
	}
	target := call["target"].(map[string]any)
	if target["app"] != "actors" || target["tool"] != "actors_run" {
		t.Fatalf("target=%#v", target)
	}
}

func TestActorScheduleForwardsRandomScheduleUnchanged(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	createdAny, _ := app.toolActorSave(ctx, map[string]any{"name": "Random products", "definition": testActorDefinition()})
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	schedule := map[string]any{
		"kind": "random", "period": "day", "runs_per_period": float64(5),
		"window_start": "08:00", "window_end": "22:00", "min_spacing_minutes": float64(60),
	}
	if _, err := app.toolActorSchedule(ctx, map[string]any{"actor_id": id, "schedule": schedule, "timezone": "Europe/Paris"}); err != nil {
		t.Fatal(err)
	}
	call := plat.lastCall("jobs", "jobs_schedule")
	forwarded := call["schedule"].(map[string]any)
	for key, want := range schedule {
		if forwarded[key] != want {
			t.Fatalf("schedule.%s=%v, want %v; full schedule=%#v", key, forwarded[key], want, forwarded)
		}
	}
	if call["timezone"] != "Europe/Paris" {
		t.Fatalf("timezone=%v, want Europe/Paris", call["timezone"])
	}
}

func TestActorScheduleRotatesPresetPoolDeterministically(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	definition := testActorDefinition()
	definition["presets"].(map[string]any)["fr"] = map[string]any{"country": "FR"}
	createdAny, _ := app.toolActorSave(ctx, map[string]any{"name": "Rotating profiles", "definition": definition})
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	pool := []any{"fr", "de", "fr"}
	if _, err := app.toolActorSchedule(ctx, map[string]any{
		"actor_id":    id,
		"preset_pool": pool,
		"schedule":    map[string]any{"kind": "random", "period": "day", "runs_per_period": 5, "window_start": "08:00", "window_end": "22:00"},
	}); err != nil {
		t.Fatal(err)
	}
	call := plat.lastCall("jobs", "jobs_schedule")
	target := call["target"].(map[string]any)
	targetInput := target["input"].(map[string]any)
	forwarded := stringSliceFromAny(targetInput["preset_pool"])
	if len(forwarded) != 2 || forwarded[0] != "fr" || forwarded[1] != "de" {
		t.Fatalf("preset_pool=%#v", targetInput["preset_pool"])
	}
	scheduleKey := stringFromAny(targetInput["schedule_key"])
	bucket := "2026-08-10T12:00:00Z"
	delivery := copyArgs(targetInput)
	delivery["trigger_bucket"] = bucket
	firstAny, err := app.toolActorRun(ctx, delivery)
	if err != nil {
		t.Fatal(err)
	}
	secondAny, err := app.toolActorRun(ctx, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if firstAny.(map[string]any)["run_id"] != secondAny.(map[string]any)["run_id"] || secondAny.(map[string]any)["duplicate"] != true {
		t.Fatalf("deliveries were not idempotent: first=%#v second=%#v", firstAny, secondAny)
	}
	var inputJSON string
	if err := ctx.AppDB().QueryRow(`SELECT input_json FROM actors_runs WHERE id=?`, firstAny.(map[string]any)["run_id"]).Scan(&inputJSON); err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatal(err)
	}
	want := selectScheduledPreset([]string{"fr", "de"}, scheduleKey, bucket)
	if input["preset"] != want {
		t.Fatalf("selected preset=%v, want %s; input=%#v", input["preset"], want, input)
	}
}

func TestActorScheduleRejectsInvalidPresetPool(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	createdAny, _ := app.toolActorSave(ctx, map[string]any{"name": "Products", "definition": testActorDefinition()})
	id := createdAny.(map[string]any)["actor"].(*actorRecord).ID
	schedule := map[string]any{"kind": "every", "every_seconds": 3600}
	if _, err := app.toolActorSchedule(ctx, map[string]any{"actor_id": id, "preset_pool": []any{"missing"}, "schedule": schedule}); err == nil || !strings.Contains(err.Error(), "unknown preset") {
		t.Fatalf("unknown pool error=%v", err)
	}
	if _, err := app.toolActorSchedule(ctx, map[string]any{"actor_id": id, "preset": "de", "preset_pool": []any{"de"}, "schedule": schedule}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("preset/pool error=%v", err)
	}
}

func TestMoneyParsingHandlesCommaAndDotLocales(t *testing.T) {
	for input, want := range map[string]float64{"€1.234,56": 1234.56, "$1,234.56": 1234.56, "29.95 EUR": 29.95} {
		got, err := parseActorNumber(input)
		if err != nil || got != want {
			t.Errorf("parse %q=%v,%v want %v", input, got, err, want)
		}
	}
}

func TestActorCSVNeutralizesFormulaStrings(t *testing.T) {
	b, err := encodeActorCSV([]map[string]any{{"name": "=HYPERLINK(\"https://bad.test\")", "price": -2.5}}, map[string]string{"name": "string", "price": "number"})
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

func TestActorManifestDeclaresRuntimeSurface(t *testing.T) {
	manifest := (&App{}).Manifest()
	wantTools := []string{"actors_save", "actors_run", "actors_run_get", "actors_run_cancel", "actors_run_retry", "actors_schedule"}
	names := toolNames(manifest.Provides.MCPTools)
	for _, want := range wantTools {
		if !containsString(names, want) {
			t.Errorf("manifest missing %s", want)
		}
	}
	if len(manifest.Provides.Workers) != 1 || manifest.Provides.Workers[0].Name != "actor-runner" {
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
