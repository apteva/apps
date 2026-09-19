package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func saveFixtureActor(t *testing.T, ctx *sdk.AppCtx, app *App, definition map[string]any) *actorRecord {
	t.Helper()
	out, err := app.toolActorSave(ctx, map[string]any{"name": "fixture", "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["actor"].(*actorRecord)
}

func executeNext(t *testing.T, ctx *sdk.AppCtx, app *App) map[string]any {
	t.Helper()
	queued, err := claimActorRun(ctx)
	if err != nil || queued == nil {
		t.Fatalf("claim=%v err=%v", queued, err)
	}
	if err := app.executeActorRun(context.Background(), ctx, queued); err != nil {
		t.Fatal(err)
	}
	run, err := getActorRun(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestStandaloneManifestAndAssets(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if manifest.Name != "actors" {
		t.Fatal(manifest.Name)
	}
	for _, dep := range manifest.Requires.Apps {
		if dep.Name == "web" {
			t.Fatal("Actors must not depend on Web")
		}
	}
	declared := toolNames(manifest.Provides.MCPTools)
	for _, tool := range app.MCPTools() {
		if !strings.HasPrefix(tool.Name, "actors_") || !containsString(declared, tool.Name) {
			t.Fatalf("undeclared/foreign tool %s", tool.Name)
		}
	}
	for _, file := range []string{"ui/ActorsPanel.mjs", "ui/icon.svg", "skills/how-to-use-actors.md", "examples/page-reader.json"} {
		if _, err := os.Stat(file); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNamedOperationTaskPinsRevisionAndDatasetIsProjectScoped(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	ctx = ctx.WithProject("project-a")
	def := testActorDefinition()
	def["operations"] = map[string]any{"read": map[string]any{"steps": def["steps"], "output_schema": def["output_schema"]}}
	delete(def, "steps")
	delete(def, "output_schema")
	rec := saveFixtureActor(t, ctx, app, def)
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err == nil {
		t.Fatal("missing named operation accepted")
	}
	out, err := app.toolTaskSave(ctx, map[string]any{"name": "daily", "actor_id": rec.ID, "operation": "read"})
	if err != nil {
		t.Fatal(err)
	}
	task := out.(map[string]any)["task"].(actorTask)
	// Revision 2 deliberately removes the old operation. The task must still run v1.
	if _, err := app.toolActorSave(ctx, map[string]any{"id": rec.ID, "name": rec.Name, "expected_revision": 1, "definition": testActorDefinition()}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolTaskRun(ctx, map[string]any{"id": task.ID}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, app)
	if run["status"] != "completed" || run["actor_revision"] != int64(1) {
		t.Fatalf("run=%v", run)
	}
	data, err := app.toolDatasetRead(ctx, map[string]any{"run_id": run["id"], "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	page := data.(map[string]any)
	if len(page["items"].([]map[string]any)) != 1 {
		t.Fatal(page)
	}
	next, err := app.toolDatasetRead(ctx, map[string]any{"run_id": run["id"], "after": page["next_cursor"]})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.(map[string]any)["items"].([]map[string]any)) != 0 {
		t.Fatal("cursor repeated an item")
	}
	other := ctx.WithProject("project-b")
	if _, err := app.toolDatasetRead(other, map[string]any{"run_id": run["id"]}); err == nil {
		t.Fatal("cross-project dataset access")
	}
	if _, err := app.toolTaskRun(other, map[string]any{"id": task.ID}); err == nil {
		t.Fatal("cross-project task access")
	}
	if _, err := app.toolActorDelete(ctx, map[string]any{"id": rec.ID}); err == nil {
		t.Fatal("deleted actor referenced by task")
	}
	for _, call := range plat.callsSnapshot() {
		if call.app == "web" {
			t.Fatal("Web called")
		}
	}
}

func TestSavedContextAndFillUseComputerDirectly(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	def := testActorDefinition()
	def["browser"].(map[string]any)["context_id"] = "saved-account"
	steps := def["steps"].([]any)
	def["steps"] = append(steps, map[string]any{"action": "fill", "locator": map[string]any{"selector": "input[name=q]"}, "text": "hello"})
	rec := saveFixtureActor(t, ctx, app, def)
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, app)
	if run["status"] != "completed" {
		t.Fatal(run)
	}
	open := plat.lastCall("computer", "browser_open")
	if open["context_id"] != "saved-account" || open["persist"] != true {
		t.Fatal(open)
	}
	found := false
	for _, call := range plat.callsSnapshot() {
		if call.tool == "computer_use" && call.args["action"] == "set_text" {
			found = true
			if call.args["text"] != "hello" || call.args["mode"] != "replace" {
				t.Fatal(call.args)
			}
		}
	}
	if !found {
		t.Fatal("no fill call")
	}
	var locks int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_context_locks`).Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("locks=%d err=%v", locks, err)
	}
}

func TestContextLeaseExcludesOtherRuns(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	e := &actorExecution{app: app, ctx: ctx, run: &actorQueuedRun{ID: 1}, deadline: time.Now().Add(time.Minute), definition: actorDefinition{Browser: actorBrowser{ContextID: "account"}}}
	if err := e.lockContext(); err != nil {
		t.Fatal(err)
	}
	e.run = &actorQueuedRun{ID: 2}
	if err := e.lockContext(); err == nil {
		t.Fatal("concurrent account use accepted")
	}
	e.ctx = ctx.WithProject("other")
	if err := e.lockContext(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedRunRetainsCommittedDataset(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	def := testActorDefinition()
	def["steps"] = append(def["steps"].([]any), map[string]any{"action": "assert_element", "locator": map[string]any{"selector": "#missing"}})
	rec := saveFixtureActor(t, ctx, app, def)
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, app)
	if run["status"] != "failed" {
		t.Fatal(run)
	}
	data, err := app.toolDatasetRead(ctx, map[string]any{"run_id": run["id"]})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.(map[string]any)["items"].([]map[string]any)) != 1 {
		t.Fatal(data)
	}
}

func TestScheduleOwnershipAndPinnedOperation(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	rec := saveFixtureActor(t, ctx, app, testActorDefinition())
	if _, err := app.toolActorUnschedule(ctx, map[string]any{"job_id": 999}); err == nil {
		t.Fatal("cancelled another app's job")
	}
	if countCalls(plat, "jobs", "jobs_cancel") != 0 {
		t.Fatal("foreign job cancellation dispatched")
	}
	if _, err := app.toolActorSchedule(ctx, map[string]any{"actor_id": rec.ID, "operation": "missing", "schedule": map[string]any{"kind": "every", "every_seconds": 60}}); err == nil {
		t.Fatal("unknown operation scheduled")
	}
	if _, err := app.toolActorSchedule(ctx, map[string]any{"actor_id": rec.ID, "operation": "run", "schedule": map[string]any{"kind": "every", "every_seconds": 60}}); err != nil {
		t.Fatal(err)
	}
	call := plat.lastCall("jobs", "jobs_schedule")
	input := mapFromAny(mapFromAny(call["target"])["input"])
	if intFromAny(input["revision"]) != 1 || input["operation"] != "run" {
		t.Fatal(input)
	}
}

func TestHTTPTaskAndDatasetRoutes(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	rec := saveFixtureActor(t, ctx, app, testActorDefinition())
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Method+" "+route.Pattern, route.Handler)
	}
	body, _ := json.Marshal(map[string]any{"name": "task", "actor_id": rec.ID})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/tasks", strings.NewReader(string(body))))
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/tasks/1/run", strings.NewReader("{}")))
	if w.Code != 200 {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	executeNext(t, ctx, app)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/runs/1/dataset", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Hello") {
		t.Fatalf("dataset: %d %s", w.Code, w.Body.String())
	}
}

func TestDeletedActorIDsAreNotReused(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	first := saveFixtureActor(t, ctx, app, testActorDefinition())
	if _, err := app.toolActorDelete(ctx, map[string]any{"id": first.ID}); err != nil {
		t.Fatal(err)
	}
	second := saveFixtureActor(t, ctx, app, testActorDefinition())
	if second.ID <= first.ID {
		t.Fatal("historical identity reused")
	}
}

func TestSubmissionIdempotencyRejectsChangedInput(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	rec := saveFixtureActor(t, ctx, app, testActorDefinition())
	args := map[string]any{"actor_id": rec.ID, "idempotency_key": "request-1", "input": map[string]any{"start_url": "https://example.com"}}
	first, err := app.toolActorRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.toolActorRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["run_id"] != second.(map[string]any)["run_id"] || second.(map[string]any)["duplicate"] != true {
		t.Fatal(second)
	}
	args["input"] = map[string]any{"start_url": "https://example.com/different"}
	if _, err := app.toolActorRun(ctx, args); err == nil || !strings.Contains(err.Error(), "idempotency conflict") {
		t.Fatal(err)
	}
}

func TestCancellationStopsInFlightComputerCall(t *testing.T) {
	plat := newFakePlatform()
	plat.blockExtract = make(chan struct{})
	ctx, app := newTestCtx(t, plat)
	rec := saveFixtureActor(t, ctx, app, testActorDefinition())
	out, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["run_id"].(int64)
	queued, err := claimActorRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.executeActorRun(context.Background(), ctx, queued) }()
	select {
	case <-plat.blockExtract:
	case <-time.After(3 * time.Second):
		t.Fatal("browser extraction never started")
	}
	if _, err := app.toolActorRunCancel(ctx, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight call was not cancelled")
	}
	run, err := getActorRun(ctx, id)
	if err != nil || run["status"] != "cancelled" {
		t.Fatalf("run=%v err=%v", run, err)
	}
	if countCalls(plat, "computer", "browser_close") != 1 {
		t.Fatal("session not closed")
	}
}

func TestUncertainClickIsNotRetried(t *testing.T) {
	plat := newFakePlatform()
	plat.failAction = "click"
	ctx, app := newTestCtx(t, plat)
	def := testActorDefinition()
	def["steps"] = []any{map[string]any{"action": "goto", "url": "https://example.com"}, map[string]any{"action": "click", "locator": map[string]any{"selector": "button"}}}
	rec := saveFixtureActor(t, ctx, app, def)
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, app)
	if run["status"] != "failed" {
		t.Fatal(run)
	}
	clicks := 0
	for _, call := range plat.callsSnapshot() {
		if call.tool == "computer_use" && call.args["action"] == "click" {
			clicks++
		}
	}
	if clicks != 1 {
		t.Fatalf("clicks=%d", clicks)
	}
}

func TestGenericExampleIsExecutable(t *testing.T) {
	raw, err := os.ReadFile("examples/page-reader.json")
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	ctx, app := newTestCtx(t, newFakePlatform())
	saved, err := app.toolActorSave(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	rec := saved.(map[string]any)["actor"].(*actorRecord)
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "read"}); err != nil {
		t.Fatal(err)
	}
	if run := executeNext(t, ctx, app); run["status"] != "completed" {
		t.Fatal(run)
	}
}

func TestDatasetRejectsOversizedPageAtomically(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	rec := saveFixtureActor(t, ctx, app, testActorDefinition())
	out, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["run_id"].(int64)
	err = persistDatasetPage(ctx, id, []map[string]any{{"text": "small"}, {"text": strings.Repeat("x", maxActorItemBytes+1)}})
	if err == nil {
		t.Fatal("oversized item accepted")
	}
	data, err := app.toolDatasetRead(ctx, map[string]any{"run_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.(map[string]any)["items"].([]map[string]any)) != 0 {
		t.Fatal("partial page committed")
	}
}
