package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// envHandler returns its code label plus two env values so tests can see
// both the version and the layered stage vars.
func envHandler(label string) string {
	return `export default async () => "` + label + `:" + (process.env.A || "-") + ":" + (process.env.B || "-");`
}

func stageTestApp(t *testing.T) (*sdk.AppCtx, *App) {
	t.Helper()
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	return ctx, mountApp(t, ctx)
}

func mustStageSet(t *testing.T, app *App, ctx *sdk.AppCtx, args map[string]any) *Stage {
	t.Helper()
	out, err := app.toolStageSet(ctx, args)
	if err != nil {
		t.Fatalf("stage_set %v: %v", args, err)
	}
	return out.(map[string]any)["stage"].(*Stage)
}

func deployCode(t *testing.T, app *App, ctx *sdk.AppCtx, name, source string, activate bool) *FunctionVersion {
	t.Helper()
	out, err := app.toolDeploy(ctx, map[string]any{"name": name, "source": source, "activate": activate})
	if err != nil {
		t.Fatalf("deploy %s: %v", name, err)
	}
	return out.(map[string]any)["version"].(*FunctionVersion)
}

func invokeTarget(t *testing.T, app *App, ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	t.Helper()
	out, err := app.toolInvoke(ctx, args)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func mustInvokeResponse(t *testing.T, app *App, ctx *sdk.AppCtx, args map[string]any) string {
	t.Helper()
	out, err := invokeTarget(t, app, ctx, args)
	if err != nil {
		t.Fatalf("invoke %v: %v", args, err)
	}
	if out["status"] != "ok" {
		t.Fatalf("invoke %v: %+v", args, out)
	}
	return out["response"].(string)
}

func getStage(t *testing.T, ctx *sdk.AppCtx, fnID int64, name string) *Stage {
	t.Helper()
	st, err := dbGetStage(ctx.AppDB(), testProj, fnID, name)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A release: build v2 without going live, promote staging, test, promote
// production. The active (unstaged) path never moves and stage vars are
// layered over the function env.
func TestStagePromoteInvokeAndUnstagedUnchanged(t *testing.T) {
	ctx, app := stageTestApp(t)
	fn := createFn(t, app, ctx, map[string]any{"name": "rel", "source": envHandler("v1"), "env": map[string]any{"A": "base-a", "B": "base-b"}})
	v1Active := *fn.ActiveVersionID

	for _, stage := range []string{"staging", "production"} {
		st := mustStageSet(t, app, ctx, map[string]any{"function": "rel", "stage": stage, "version": 1, "vars": map[string]any{"B": stage + "-b"}})
		if st.Version != 1 || st.Rev != 1 || st.Status != "active" || st.Vars["B"] != "[redacted]" {
			t.Fatalf("created stage: %+v", st)
		}
	}

	v2 := deployCode(t, app, ctx, "rel", envHandler("v2"), false)
	if v2.Version != 2 || v2.BuildStatus != "ready" {
		t.Fatalf("v2: %+v", v2)
	}
	current, _ := dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")
	if *current.ActiveVersionID != v1Active {
		t.Fatal("activate=false moved the active version")
	}
	if st := getStage(t, ctx, fn.ID, "staging"); st.Version != 1 || st.Rev != 1 {
		t.Fatalf("deploy moved a stage: %+v", st)
	}

	if got := mustInvokeResponse(t, app, ctx, map[string]any{"name": "rel"}); got != `"v1:base-a:base-b"` {
		t.Fatalf("unstaged = %s", got)
	}
	if got := mustInvokeResponse(t, app, ctx, map[string]any{"name": "rel", "stage": "staging"}); got != `"v1:base-a:staging-b"` {
		t.Fatalf("staging before promote = %s", got)
	}
	if got := mustInvokeResponse(t, app, ctx, map[string]any{"name": "rel", "version": 2}); got != `"v2:base-a:base-b"` {
		t.Fatalf("pinned version = %s", got)
	}

	out, err := app.toolStagePromoteMany(ctx, map[string]any{"stage": "staging", "items": []any{map[string]any{"function": "rel", "version": 2, "expected_rev": 1}}})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	item := out.(map[string]any)["items"].([]map[string]any)[0]
	if item["changed"] != true || item["version"] != 2 || *(item["previous_version"].(*int)) != 1 || item["rev"] != int64(2) {
		t.Fatalf("promote result: %+v", item)
	}
	if got := mustInvokeResponse(t, app, ctx, map[string]any{"name": "rel", "stage": "staging"}); got != `"v2:base-a:staging-b"` {
		t.Fatalf("staging after promote = %s", got)
	}
	if got := mustInvokeResponse(t, app, ctx, map[string]any{"name": "rel", "stage": "production"}); got != `"v1:base-a:production-b"` {
		t.Fatalf("production before its promote = %s", got)
	}
	if got := mustInvokeResponse(t, app, ctx, map[string]any{"name": "rel"}); got != `"v1:base-a:base-b"` {
		t.Fatalf("unstaged after promote = %s", got)
	}

	// Promoting to the version a stage already runs is an idempotent no-op.
	out, err = app.toolStagePromoteMany(ctx, map[string]any{"stage": "staging", "items": []any{map[string]any{"function": fn.ID, "version": 2}}})
	if err != nil || out.(map[string]any)["items"].([]map[string]any)[0]["changed"] != false {
		t.Fatalf("repeat promote: %v %+v", err, out)
	}
	if st := getStage(t, ctx, fn.ID, "staging"); st.Rev != 2 || *st.PreviousVersion != 1 {
		t.Fatalf("repeat promote changed the stage: %+v", st)
	}

	// Invocation history records stage + version.
	rows, err := dbListInvocations(ctx.AppDB(), testProj, fn.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	var staged, unstaged int
	for _, inv := range rows {
		switch inv.Stage {
		case "staging":
			staged++
			if inv.VersionID == nil {
				t.Fatal("staged invocation without version")
			}
		case "":
			unstaged++
		}
	}
	if staged != 2 || unstaged < 2 {
		t.Fatalf("invocation stages: staged=%d unstaged=%d", staged, unstaged)
	}
	logs, err := app.toolLogs(ctx, map[string]any{"invocation_id": rows[len(rows)-2].ID})
	if err != nil || logs.(map[string]any)["stage"] != "staging" {
		t.Fatalf("logs stage: %v %+v", err, logs)
	}

	// functions_get shows the stages with their versions.
	got, err := app.toolGet(ctx, map[string]any{"name": "rel"})
	if err != nil {
		t.Fatal(err)
	}
	stages := got.(map[string]any)["function"].(*Function).Stages
	if len(stages) != 2 || stages[0].Name != "production" || stages[1].Version != 2 || stages[1].Vars["B"] != "[redacted]" {
		t.Fatalf("get stages: %+v", stages)
	}
}

func TestStagePromoteManyIsAllOrNothing(t *testing.T) {
	ctx, app := stageTestApp(t)
	a := createFn(t, app, ctx, map[string]any{"name": "atom-a", "source": envHandler("a1")})
	b := createFn(t, app, ctx, map[string]any{"name": "atom-b", "source": envHandler("b1")})
	deployCode(t, app, ctx, "atom-a", envHandler("a2"), false)
	deployCode(t, app, ctx, "atom-b", envHandler("b2"), false)
	for _, name := range []string{"atom-a", "atom-b"} {
		mustStageSet(t, app, ctx, map[string]any{"function": name, "stage": "staging", "version": 1})
	}
	mustStageSet(t, app, ctx, map[string]any{"function": "atom-a", "stage": "solo", "version": 1})

	for name, items := range map[string][]any{
		"missing version":  {map[string]any{"function": "atom-a", "version": 2}, map[string]any{"function": "atom-b", "version": 99}},
		"missing function": {map[string]any{"function": "atom-a", "version": 2}, map[string]any{"function": "nope", "version": 1}},
		"duplicate":        {map[string]any{"function": "atom-a", "version": 2}, map[string]any{"function": a.ID, "version": 2}},
		"stale rev":        {map[string]any{"function": "atom-a", "version": 2}, map[string]any{"function": "atom-b", "version": 2, "expected_rev": 7}},
	} {
		if _, err := app.toolStagePromoteMany(ctx, map[string]any{"stage": "staging", "items": items}); err == nil {
			t.Fatalf("%s: promote succeeded", name)
		}
		for _, id := range []int64{a.ID, b.ID} {
			if st := getStage(t, ctx, id, "staging"); st.Version != 1 || st.Rev != 1 || st.PreviousVersionID != nil {
				t.Fatalf("%s: partial promote: %+v", name, st)
			}
		}
	}
	// A stage missing on one function also aborts everything.
	if _, err := app.toolStagePromoteMany(ctx, map[string]any{"stage": "solo", "items": []any{map[string]any{"function": "atom-a", "version": 2}, map[string]any{"function": "atom-b", "version": 2}}}); err == nil || !errors.Is(err, errStageNotFound) {
		t.Fatalf("missing stage: %v", err)
	}
	if st := getStage(t, ctx, a.ID, "solo"); st.Version != 1 {
		t.Fatalf("partial promote on missing stage: %+v", st)
	}
	if _, err := app.toolStagePromoteMany(ctx, map[string]any{"stage": "staging", "items": []any{map[string]any{"function": "atom-a", "version": 2}, map[string]any{"function": "atom-b", "version": 2}}}); err != nil {
		t.Fatalf("valid promote: %v", err)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if st := getStage(t, ctx, id, "staging"); st.Version != 2 || st.Rev != 2 || *st.PreviousVersion != 1 {
			t.Fatalf("promoted stage: %+v", st)
		}
	}
}

func TestStageRollbackAndRevConflict(t *testing.T) {
	ctx, app := stageTestApp(t)
	fn := createFn(t, app, ctx, map[string]any{"name": "rb-stage", "source": envHandler("v1")})
	other := createFn(t, app, ctx, map[string]any{"name": "rb-other", "source": envHandler("o1")})
	deployCode(t, app, ctx, "rb-stage", envHandler("v2"), false)
	mustStageSet(t, app, ctx, map[string]any{"function": "rb-stage", "stage": "production", "version": 1})
	mustStageSet(t, app, ctx, map[string]any{"function": "rb-other", "stage": "production", "version": 1})

	// Wrong expected_rev on settings and promotion: conflict, nothing changes.
	_, err := app.toolStageSet(ctx, map[string]any{"function": "rb-stage", "stage": "production", "vars": map[string]any{"A": "x"}, "expected_rev": 5})
	if !errors.Is(err, errStageConflict) || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("set conflict: %v", err)
	}
	_, err = app.toolStagePromoteMany(ctx, map[string]any{"stage": "production", "items": []any{map[string]any{"function": "rb-stage", "version": 2, "expected_rev": 3}}})
	if !errors.Is(err, errStageConflict) {
		t.Fatalf("promote conflict: %v", err)
	}
	if _, err = app.toolStageSet(ctx, map[string]any{"function": "rb-stage", "stage": "fresh", "version": 1, "expected_rev": 1}); !errors.Is(err, errStageConflict) {
		t.Fatalf("create with nonzero expected_rev: %v", err)
	}
	if st := getStage(t, ctx, fn.ID, "production"); st.Rev != 1 || st.Version != 1 || len(st.Vars) != 0 {
		t.Fatalf("conflict changed stage: %+v", st)
	}
	// Settings update with the right rev bumps it; the same settings again do not.
	st := mustStageSet(t, app, ctx, map[string]any{"function": "rb-stage", "stage": "production", "vars": map[string]any{"A": "x"}, "expected_rev": 1})
	if st.Rev != 2 {
		t.Fatalf("rev after set: %d", st.Rev)
	}
	if st = mustStageSet(t, app, ctx, map[string]any{"function": "rb-stage", "stage": "production", "vars": map[string]any{"A": "x"}}); st.Rev != 2 {
		t.Fatalf("idempotent set bumped rev: %d", st.Rev)
	}
	if _, err = app.toolStageSet(ctx, map[string]any{"function": "rb-stage", "stage": "production", "version": 2}); err == nil {
		t.Fatal("stage_set changed an existing stage's version")
	}

	// Rollback with one function lacking a previous version changes nothing.
	if _, err = app.toolStagePromoteMany(ctx, map[string]any{"stage": "production", "items": []any{map[string]any{"function": "rb-stage", "version": 2}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = app.toolStageRollback(ctx, map[string]any{"stage": "production", "functions": []any{"rb-stage", "rb-other"}}); err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Fatalf("rollback without previous: %v", err)
	}
	if st = getStage(t, ctx, fn.ID, "production"); st.Version != 2 || st.Rev != 3 {
		t.Fatalf("failed rollback changed stage: %+v", st)
	}
	out, err := app.toolStageRollback(ctx, map[string]any{"stage": "production", "functions": []any{map[string]any{"function": "rb-stage", "expected_rev": 3}}})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if item := out.(map[string]any)["items"].([]map[string]any)[0]; item["version"] != 1 || *(item["previous_version"].(*int)) != 2 || item["rev"] != int64(4) {
		t.Fatalf("rollback result: %+v", item)
	}
	if got := mustInvokeResponse(t, app, ctx, map[string]any{"name": "rb-stage", "stage": "production"}); got != `"v1:x:-"` {
		t.Fatalf("after rollback = %s", got)
	}
	if st = getStage(t, ctx, other.ID, "production"); st.Rev != 1 {
		t.Fatalf("other function touched: %+v", st)
	}

	// Delete is idempotent.
	out, err = app.toolStageDelete(ctx, map[string]any{"function": "rb-stage", "stage": "production"})
	if err != nil || out.(map[string]any)["deleted"] != true {
		t.Fatalf("delete: %v %+v", err, out)
	}
	out, err = app.toolStageDelete(ctx, map[string]any{"function": "rb-stage", "stage": "production"})
	if err != nil || out.(map[string]any)["deleted"] != false {
		t.Fatalf("second delete: %v %+v", err, out)
	}
}

func TestStageInvocationRefusals(t *testing.T) {
	ctx, app := stageTestApp(t)
	createFn(t, app, ctx, map[string]any{"name": "gate", "source": envHandler("v1")})
	mustStageSet(t, app, ctx, map[string]any{"function": "gate", "stage": "production", "version": 1, "status": "paused"})

	if _, err := invokeTarget(t, app, ctx, map[string]any{"name": "gate", "stage": "production"}); !errors.Is(err, errStagePaused) {
		t.Fatalf("paused stage invoked: %v", err)
	}
	if _, err := invokeTarget(t, app, ctx, map[string]any{"name": "gate", "stage": "missing"}); !errors.Is(err, errStageNotFound) {
		t.Fatalf("unknown stage: %v", err)
	}
	if _, err := invokeTarget(t, app, ctx, map[string]any{"name": "gate", "stage": "production", "version": 1}); err == nil {
		t.Fatal("stage and version accepted together")
	}
	if _, err := invokeTarget(t, app, ctx, map[string]any{"name": "gate", "stage": "Bad_Name"}); err == nil {
		t.Fatal("invalid stage name accepted")
	}
	if _, err := app.toolStageSet(ctx, map[string]any{"function": "gate", "stage": strings.Repeat("a", 41), "version": 1}); err == nil {
		t.Fatal("41-character stage name accepted")
	}
	if _, err := app.toolStageSet(ctx, map[string]any{"function": "gate", "stage": "qa"}); err == nil {
		t.Fatal("stage created without version")
	}
	if _, err := app.toolStageSet(ctx, map[string]any{"function": "gate", "stage": "qa", "version": 1, "vars": map[string]any{"APTEVA_APP_TOKEN": "x"}}); err == nil {
		t.Fatal("reserved var accepted")
	}
	if _, err := app.toolStageSet(ctx, map[string]any{"function": "gate", "stage": "qa", "version": 1, "limits": map[string]any{"bogus": 1}}); err == nil {
		t.Fatal("unknown limit accepted")
	}
	mustStageSet(t, app, ctx, map[string]any{"function": "gate", "stage": "production", "status": "active"})
	if got := mustInvokeResponse(t, app, ctx, map[string]any{"name": "gate", "stage": "production"}); got != `"v1:-:-"` {
		t.Fatalf("resumed stage = %s", got)
	}
}

// Workers are keyed by stage, version and effective settings: the active
// configuration, each stage, and a stage before/after a settings change
// never share a worker process.
func TestStageWorkersAreNotShared(t *testing.T) {
	ctx, app := stageTestApp(t)
	src := `const worker = Math.random().toString(36).slice(2); export default async () => worker + ":" + (process.env.B || "-");`
	createFn(t, app, ctx, map[string]any{"name": "pool", "source": src, "limits": map[string]any{"concurrency": 1}})
	mustStageSet(t, app, ctx, map[string]any{"function": "pool", "stage": "one", "version": 1, "vars": map[string]any{"B": "one"}})
	mustStageSet(t, app, ctx, map[string]any{"function": "pool", "stage": "two", "version": 1, "vars": map[string]any{"B": "two"}})
	worker := func(args map[string]any) (string, string) {
		parts := strings.Split(strings.Trim(mustInvokeResponse(t, app, ctx, args), `"`), ":")
		return parts[0], parts[1]
	}
	active1, _ := worker(map[string]any{"name": "pool"})
	active2, _ := worker(map[string]any{"name": "pool"})
	if active1 != active2 {
		t.Fatal("active worker was not reused (test needs function_version isolation)")
	}
	one1, b1 := worker(map[string]any{"name": "pool", "stage": "one"})
	one2, _ := worker(map[string]any{"name": "pool", "stage": "one"})
	two, b2 := worker(map[string]any{"name": "pool", "stage": "two"})
	if one1 != one2 || b1 != "one" || b2 != "two" {
		t.Fatalf("stage workers: %s %s %s/%s", one1, one2, b1, b2)
	}
	if one1 == active1 || two == active1 || two == one1 {
		t.Fatal("a worker was shared across stage settings")
	}
	mustStageSet(t, app, ctx, map[string]any{"function": "pool", "stage": "one", "vars": map[string]any{"B": "changed"}})
	one3, b3 := worker(map[string]any{"name": "pool", "stage": "one"})
	if one3 == one1 || b3 != "changed" {
		t.Fatalf("stage settings change reused a worker: %s/%s", one3, b3)
	}
	if again, _ := worker(map[string]any{"name": "pool"}); again != active1 {
		t.Fatal("staged calls disturbed the active worker")
	}
}

// Nested functions_invoke inherits the caller's stage when the callee has
// a stage of that name; otherwise the callee runs its active version.
func TestNestedCallInheritsStage(t *testing.T) {
	ctx, app := stageTestApp(t)
	createFn(t, app, ctx, map[string]any{"name": "child", "source": `export default async () => process.env.LEVEL || "active";`})
	createFn(t, app, ctx, map[string]any{"name": "caller", "source": `export default async (e, c) => (await c.call("functions", "functions_invoke", e.args || {name: "child", event: {}})).response;`})
	mustStageSet(t, app, ctx, map[string]any{"function": "child", "stage": "staging", "version": 1, "vars": map[string]any{"LEVEL": "staging"}})
	for _, stage := range []string{"staging", "qa"} {
		mustStageSet(t, app, ctx, map[string]any{"function": "caller", "stage": stage, "version": 1})
	}
	call := func(stage string, args map[string]any) string {
		in := map[string]any{"name": "caller", "event": map[string]any{}}
		if stage != "" {
			in["stage"] = stage
		}
		if args != nil {
			in["event"] = map[string]any{"args": args}
		}
		return mustInvokeResponse(t, app, ctx, in)
	}
	if got := call("staging", nil); !strings.Contains(got, "staging") {
		t.Fatalf("inherited stage: %s", got)
	}
	if got := call("", nil); !strings.Contains(got, "active") {
		t.Fatalf("unstaged caller: %s", got)
	}
	if got := call("qa", nil); !strings.Contains(got, "active") {
		t.Fatalf("callee without the caller's stage: %s", got)
	}
	if got := call("", map[string]any{"name": "child", "stage": "staging", "event": map[string]any{}}); !strings.Contains(got, "staging") {
		t.Fatalf("explicit nested stage: %s", got)
	}
	mustStageSet(t, app, ctx, map[string]any{"function": "child", "stage": "staging", "status": "paused"})
	out, err := invokeTarget(t, app, ctx, map[string]any{"name": "caller", "stage": "staging", "event": map[string]any{}})
	if err != nil || out["status"] == "ok" || !strings.Contains(out["error"].(string), "paused") {
		t.Fatalf("paused inherited stage: %v %+v", err, out)
	}
}

// Identical content returns the existing ready version instead of a copy;
// activating identical older content reuses that version.
func TestDeployDedupesIdenticalContent(t *testing.T) {
	ctx, app := stageTestApp(t)
	fn := createFn(t, app, ctx, map[string]any{"name": "dedupe", "source": envHandler("v1")})
	same := deployCode(t, app, ctx, "dedupe", envHandler("v1"), true)
	if same.Version != 1 || !same.Reused {
		t.Fatalf("identical active deploy: %+v", same)
	}
	v2 := deployCode(t, app, ctx, "dedupe", envHandler("v2"), false)
	again := deployCode(t, app, ctx, "dedupe", envHandler("v2"), false)
	if again.Version != v2.Version || !again.Reused {
		t.Fatalf("identical inactive deploy: %+v", again)
	}
	back := deployCode(t, app, ctx, "dedupe", envHandler("v2"), true)
	current, _ := dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")
	if back.Version != 2 || !back.Reused || *current.ActiveVersionID != v2.ID {
		t.Fatalf("activate identical version: %+v active=%d", back, *current.ActiveVersionID)
	}
	versions, _ := dbListVersions(ctx.AppDB(), testProj, fn.ID, 50)
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(versions))
	}
}

func TestPruneKeepsReferencedVersions(t *testing.T) {
	ctx, app := stageTestApp(t)
	fn := createFn(t, app, ctx, map[string]any{"name": "prune", "source": envHandler("v1")})
	for _, label := range []string{"v2", "v3", "v4", "v5"} {
		deployCode(t, app, ctx, "prune", envHandler(label), false)
	}
	mustStageSet(t, app, ctx, map[string]any{"function": "prune", "stage": "production", "version": 2})
	if _, err := app.toolStagePromoteMany(ctx, map[string]any{"stage": "production", "items": []any{map[string]any{"function": "prune", "version": 3}}}); err != nil {
		t.Fatal(err)
	}
	// active=v1, production=v3 (previous v2), newest=v5 → only v4 is free.
	if _, err := app.toolVersionsPrune(ctx, map[string]any{"function": "prune", "keep": 0}); err == nil {
		t.Fatal("keep=0 accepted")
	}
	out, err := app.toolVersionsPrune(ctx, map[string]any{"function": "prune", "keep": 1})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted := out.(map[string]any)["deleted"].([]int); len(deleted) != 1 || deleted[0] != 4 {
		t.Fatalf("deleted = %v", deleted)
	}
	versions, _ := dbListVersions(ctx.AppDB(), testProj, fn.ID, 50)
	got := []int{}
	for _, v := range versions {
		got = append(got, v.Version)
	}
	if len(got) != 4 || got[0] != 5 || got[1] != 3 || got[2] != 2 || got[3] != 1 {
		t.Fatalf("remaining versions = %v", got)
	}
	for _, args := range []map[string]any{{"name": "prune"}, {"name": "prune", "stage": "production"}, {"name": "prune", "version": 5}} {
		mustInvokeResponse(t, app, ctx, args)
	}
}

func TestDeleteFunctionRemovesStages(t *testing.T) {
	ctx, app := stageTestApp(t)
	fn := createFn(t, app, ctx, map[string]any{"name": "gone", "source": envHandler("v1")})
	mustStageSet(t, app, ctx, map[string]any{"function": "gone", "stage": "production", "version": 1})
	if _, err := app.toolDelete(ctx, map[string]any{"name": "gone"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM function_stages WHERE function_id=?`, fn.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stages left: %d %v", n, err)
	}
}

// Migration 012 applies to a populated pre-1.22 database without touching
// existing rows, and existing invocations read back with no stage.
func TestStageMigrationOnExistingDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "existing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	apply := func(name string) {
		b, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(string(b)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err = tx.Exec(`INSERT INTO _migrations(filename) VALUES(?)`, name); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`CREATE TABLE _migrations(filename TEXT PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_init.sql", "002_versions.sql", "003_function_url.sql", "004_retention_indexes.sql"} {
		apply(name)
	}
	if err = migrateExecutionIdentity(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"006_capacity.sql", "007_active_work.sql", "008_invocation_principals.sql", "009_invocation_history_index.sql", "010_storage_retention.sql", "011_worker_isolation.sql"} {
		apply(name)
	}
	if _, err = db.Exec(`INSERT INTO functions(id,project_id,name,runtime,source_hash,active_version_id) VALUES(1,'p','legacy','node','h',1);
		INSERT INTO function_versions(id,project_id,function_id,version,source_hash,build_status) VALUES(1,'p',1,1,'h','ready');
		INSERT INTO function_invocations(id,project_id,function_id,status,trigger_kind,response_body,version_id) VALUES(1,'p',1,'ok','manual','kept',1);`); err != nil {
		t.Fatal(err)
	}
	apply("012_stages.sql")
	apply("013_response_storage.sql")

	inv, err := dbGetInvocation(db, "p", 1)
	if err != nil || inv == nil || inv.ResponseBody != "kept" || inv.Stage != "" {
		t.Fatalf("existing invocation: %v %+v", err, inv)
	}
	stages, err := dbListStages(db, "p", 1)
	if err != nil || len(stages) != 0 {
		t.Fatalf("stages on upgraded db: %v %v", stages, err)
	}
	if _, err = db.Exec(`INSERT INTO function_stages(project_id,function_id,name,version_id,status,created_at,updated_at) VALUES('p',1,'production',1,'active','t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO function_stages(project_id,function_id,name,version_id,status,created_at,updated_at) VALUES('p',1,'production',1,'active','t','t')`); err == nil {
		t.Fatal("duplicate (function_id, name) accepted")
	}
	if _, err = db.Exec(`INSERT INTO function_stages(project_id,function_id,name,version_id,status,created_at,updated_at) VALUES('p',1,'other',1,'bogus','t','t')`); err == nil {
		t.Fatal("invalid status accepted")
	}
	var integrity string
	if err = db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity: %s %v", integrity, err)
	}
}

func TestStageToolsInManifest(t *testing.T) {
	declared := map[string]bool{}
	for _, tool := range (&App{}).Manifest().Provides.MCPTools {
		declared[tool.Name] = true
	}
	registered := map[string]bool{}
	for _, tool := range (&App{}).MCPTools() {
		registered[tool.Name] = true
		if !declared[tool.Name] {
			t.Errorf("tool %s missing from apteva.yaml", tool.Name)
		}
	}
	for name := range declared {
		if !registered[name] {
			t.Errorf("apteva.yaml declares unregistered tool %s", name)
		}
	}
	for _, name := range []string{"functions_stage_set", "functions_stage_list", "functions_stage_delete", "functions_stage_promote_many", "functions_stage_rollback", "functions_versions_prune"} {
		if !registered[name] {
			t.Errorf("missing %s", name)
		}
	}
}

// The HTTP routes mirror the stage tools and accept ?stage= on invoke.
func TestStageHTTPRoutes(t *testing.T) {
	ctx, app := stageTestApp(t)
	fn := createFn(t, app, ctx, map[string]any{"name": "http-stage", "source": envHandler("v1")})
	deployCode(t, app, ctx, "http-stage", envHandler("v2"), false)
	do := func(handler func(http.ResponseWriter, *http.Request), method, path, body string) *httptest.ResponseRecorder {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		req := httptest.NewRequest(method, path+sep+"project_id="+testProj, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		handler(rr, req)
		return rr
	}
	base := "/functions/" + strconv.FormatInt(fn.ID, 10)
	if rr := do(app.handleHTTPFunctionItem, http.MethodPost, base+"/stages", `{"stage":"staging","version":1,"vars":{"A":"http"}}`); rr.Code != 200 || strings.Contains(rr.Body.String(), `"http"`) {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(app.handleHTTPStages, http.MethodPost, "/stages/promote", `{"stage":"staging","items":[{"function":"http-stage","version":2,"expected_rev":9}]}`); rr.Code != http.StatusConflict {
		t.Fatalf("conflict: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(app.handleHTTPStages, http.MethodPost, "/stages/promote", `{"stage":"staging","items":[{"function":"http-stage","version":2}]}`); rr.Code != 200 {
		t.Fatalf("promote: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(app.handleHTTPInvokeByName, http.MethodPost, "/fn/http-stage?stage=staging", `{}`); rr.Code != 200 || rr.Body.String() != `"v2:http:-"` {
		t.Fatalf("invoke stage: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(app.handleHTTPInvokeByName, http.MethodPost, "/fn/http-stage?stage=nope", `{}`); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown stage: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(app.handleHTTPInvokeByName, http.MethodPost, "/fn/http-stage", `{}`); rr.Body.String() != `"v1:-:-"` {
		t.Fatalf("unstaged: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(app.handleHTTPStages, http.MethodPost, "/stages/rollback", `{"stage":"staging","functions":["http-stage"]}`); rr.Code != 200 {
		t.Fatalf("rollback: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(app.handleHTTPFunctionItem, http.MethodGet, base+"/stages", ``); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"version":1`) || !strings.Contains(rr.Body.String(), `"[redacted]"`) {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(app.handleHTTPFunctionItem, http.MethodPost, base+"/versions/prune", `{"keep":1}`); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"deleted":[]`) {
		t.Fatalf("prune: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(app.handleHTTPFunctionItem, http.MethodDelete, base+"/stages/staging", ``); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"deleted":true`) {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
}

// 1.23.0: an authenticated invocation can name a stage or pin a version;
// admission stays by function id; without either the active version runs.
func TestAuthenticatedInvokeRunsStage(t *testing.T) {
	ctx, app := stageTestApp(t)
	fn := createFn(t, app, ctx, map[string]any{"name": "authstage", "source": envHandler("v1"), "env": map[string]any{"A": "base"}, "invocation_policy": principalPolicy()})
	deployCode(t, app, ctx, "authstage", envHandler("v2"), false)
	mustStageSet(t, app, ctx, map[string]any{"function": "authstage", "stage": "staging", "version": 2, "vars": map[string]any{"B": "stg"}})
	mustStageSet(t, app, ctx, map[string]any{"function": "authstage", "stage": "production", "version": 1, "status": "paused"})

	call := func(extra map[string]any) (map[string]any, error) {
		args := principalArgs(fn)
		for k, v := range extra {
			args[k] = v
		}
		out, err := app.toolInvokeAuthenticated(trustedCaller(), ctx, args)
		if err != nil {
			return nil, err
		}
		return out.(map[string]any), nil
	}
	if out, err := call(nil); err != nil || out["response"] != `"v1:base:-"` {
		t.Fatalf("default = %v %v", out, err)
	}
	if out, err := call(map[string]any{"stage": "staging"}); err != nil || out["response"] != `"v2:base:stg"` {
		t.Fatalf("staging = %v %v", out, err)
	}
	if out, err := call(map[string]any{"version": 2}); err != nil || out["response"] != `"v2:base:-"` {
		t.Fatalf("version pin = %v %v", out, err)
	}
	if _, err := call(map[string]any{"stage": "production"}); !errors.Is(err, errStagePaused) {
		t.Fatalf("paused stage: %v", err)
	}
	if _, err := call(map[string]any{"stage": "missing"}); !errors.Is(err, errStageNotFound) {
		t.Fatalf("unknown stage: %v", err)
	}
	if _, err := call(map[string]any{"stage": "staging", "version": 2}); err == nil {
		t.Fatal("stage and version accepted together")
	}
	// Admission is by function id: a principal scoped to another function is
	// denied on every stage.
	other := principalArgs(fn, fn.ID+1000)
	other["stage"] = "staging"
	if _, err := app.toolInvokeAuthenticated(trustedCaller(), ctx, other); !errors.Is(err, errInvocationDenied) {
		t.Fatalf("out-of-scope principal on a stage: %v", err)
	}
	if got := authenticatedInvocationTool(app).InputSchema["properties"].(map[string]any); got["stage"] == nil || got["version"] == nil {
		t.Fatalf("schema lacks stage/version: %v", got)
	}
}

// 1.23.0: a Function URL runs its configured stage; the public caller
// cannot pick one.
func TestFunctionURLStage(t *testing.T) {
	ctx, app := stageTestApp(t)
	fn := createFn(t, app, ctx, map[string]any{"name": "urlstage", "source": envHandler("v1"), "function_url": map[string]any{"enabled": true}})
	deployCode(t, app, ctx, "urlstage", envHandler("v2"), false)
	mustStageSet(t, app, ctx, map[string]any{"function": "urlstage", "stage": "staging", "version": 2, "vars": map[string]any{"B": "stg"}})
	cur, err := dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	token := cur.FunctionURL.Token
	hit := func(query string) (int, string) {
		req := httptest.NewRequest("POST", "/url/urlstage/"+token+"?project_id="+testProj+query, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		app.handleHTTPInvokeByFunctionURL(w, req)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	if code, body := hit("&stage=staging"); code != 200 || !strings.Contains(body, "v1:-:-") {
		t.Fatalf("query must not pick a stage: %d %s", code, body)
	}
	if _, err := app.toolUpdate(ctx, map[string]any{"id": fn.ID, "function_url": map[string]any{"stage": "staging"}}); err != nil {
		t.Fatal(err)
	}
	if code, body := hit(""); code != 200 || !strings.Contains(body, "v2:-:stg") {
		t.Fatalf("configured stage: %d %s", code, body)
	}
	mustStageSet(t, app, ctx, map[string]any{"function": "urlstage", "stage": "staging", "status": "paused"})
	if code, _ := hit(""); code != http.StatusConflict {
		t.Fatalf("paused stage over URL: %d", code)
	}
	if _, err := app.toolUpdate(ctx, map[string]any{"id": fn.ID, "function_url": map[string]any{"stage": "Bad_Name"}}); err == nil {
		t.Fatal("invalid url stage accepted")
	}
	if _, err := app.toolUpdate(ctx, map[string]any{"id": fn.ID, "function_url": map[string]any{"stage": ""}}); err != nil {
		t.Fatal(err)
	}
	if code, body := hit(""); code != 200 || !strings.Contains(body, "v1:-:-") {
		t.Fatalf("cleared stage: %d %s", code, body)
	}
}
