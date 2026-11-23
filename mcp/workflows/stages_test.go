package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── helpers ───────────────────────────────────────────────────────

const stagedEventV1 = `name: staged
trigger:
  kind: event
  source: tables
  topic: row.inserted
steps:
  - id: out
    kind: emit
    topic: staged.out
    input:
      stage: "{{ stage.name }}"
      url: "{{ stage.vars.url }}"
      version: "{{ stage.version }}"
      marker: v1
`

func stagedEventSource(marker string) string {
	return strings.Replace(stagedEventV1, "marker: v1", "marker: "+marker, 1)
}

func newStageCtx(t *testing.T) (*sdk.AppCtx, *tk.EmitRecorder) {
	t.Helper()
	t.Setenv("APTEVA_GATEWAY_URL", "http://example.invalid")
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEmitter(rec))
	return ctx, rec
}

func pargs(kv ...any) map[string]any {
	m := map[string]any{"_project_id": testProj}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func createWF(t *testing.T, ctx *sdk.AppCtx, name, src string) *Workflow {
	t.Helper()
	out, err := (&App{}).toolCreate(ctx, pargs("name", name, "source", src))
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return out.(map[string]any)["workflow"].(*Workflow)
}

func updateWF(t *testing.T, ctx *sdk.AppCtx, wf *Workflow, src string) *Workflow {
	t.Helper()
	out, err := (&App{}).toolUpdate(ctx, pargs("id", float64(wf.ID), "source", src))
	if err != nil {
		t.Fatalf("update %s: %v", wf.Name, err)
	}
	return out.(map[string]any)["workflow"].(*Workflow)
}

func stageSet(ctx *sdk.AppCtx, kv ...any) (*Stage, bool, error) {
	out, err := (&App{}).toolStageSet(ctx, pargs(kv...))
	if err != nil {
		return nil, false, err
	}
	m := out.(map[string]any)
	return m["stage"].(*Stage), m["changed"].(bool), nil
}

func mustStage(t *testing.T, ctx *sdk.AppCtx, wf *Workflow, name string) *Stage {
	t.Helper()
	st, err := dbGetStage(ctx.AppDB(), wf.ID, name)
	if err != nil || st == nil {
		t.Fatalf("stage %s/%s: %v %v", wf.Name, name, st, err)
	}
	return st
}

func waitRuns(t *testing.T, ctx *sdk.AppCtx, wfID int64, want int) []*Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var runs []*Run
	for time.Now().Before(deadline) {
		runs, _ = dbListRuns(ctx.AppDB(), testProj, wfID, 50)
		done := len(runs) >= want
		for _, r := range runs {
			if r.Status == "running" || r.Status == "pending" {
				done = false
			}
		}
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return runs
}

func dispatch(ctx *sdk.AppCtx, wf *Workflow, seq int, extra string) {
	mgr := newEventTrigger(ctx)
	ln := &eventLane{key: laneKey{source: "tables", projectID: testProj}, workflows: []*Workflow{wf}}
	frame := fmt.Sprintf(`{"topic":"row.inserted","app":"tables","project_id":"%s","seq":%d,"data":{"env":"staging"}%s}`,
		testProj, seq, extra)
	mgr.dispatchFrame(ln, fmt.Sprint(seq), frame)
}

func intp(n int) *int { return &n }

// ─── versions ──────────────────────────────────────────────────────

func TestStages_EveryDefinitionChangeFreezesAVersion(t *testing.T) {
	ctx, _ := newStageCtx(t)
	wf := createWF(t, ctx, "frozen", stagedEventV1)
	wf = updateWF(t, ctx, wf, stagedEventSource("v2"))
	if wf.Version != 2 {
		t.Fatalf("head version = %d, want 2", wf.Version)
	}
	// Status-only change: no new version.
	if _, err := (&App{}).toolUpdate(ctx, pargs("id", float64(wf.ID), "status", "active")); err != nil {
		t.Fatal(err)
	}
	vs, err := dbListVersions(ctx.AppDB(), wf.ID, 10)
	if err != nil || len(vs) != 2 {
		t.Fatalf("versions = %d (%v), want 2", len(vs), err)
	}
	v1, _ := dbGetVersion(ctx.AppDB(), wf.ID, 1)
	v2, _ := dbGetVersion(ctx.AppDB(), wf.ID, 2)
	if !strings.Contains(v1.Source, "marker: v1") || !strings.Contains(v2.Source, "marker: v2") {
		t.Fatalf("version sources not frozen: v1=%q v2=%q", v1.Source, v2.Source)
	}
	if !strings.Contains(v1.TriggerJSON, `"source":"tables"`) {
		t.Fatalf("v1 trigger not frozen: %q", v1.TriggerJSON)
	}
}

// ─── stage_set ─────────────────────────────────────────────────────

func TestStages_SetValidation(t *testing.T) {
	ctx, _ := newStageCtx(t)
	wf := createWF(t, ctx, "setval", stagedEventV1)
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging"); err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Fatalf("create without version: %v", err)
	}
	for _, bad := range []string{"", "Staging", "-x", "has space", strings.Repeat("a", 41)} {
		if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", bad, "version", float64(1)); err == nil {
			t.Errorf("stage name %q accepted", bad)
		}
	}
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(7)); err == nil || !strings.Contains(err.Error(), "no version 7") {
		t.Fatalf("missing version: %v", err)
	}
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(1), "status", "off"); err == nil {
		t.Fatal("bad status accepted")
	}
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(1),
		"filter", map[string]any{"when": "ventes"}); err == nil {
		t.Fatal("invalid filter.when accepted")
	}
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(1),
		"filter", map[string]any{"bogus": 1}); err == nil {
		t.Fatal("unknown filter key accepted")
	}
	st, changed, err := stageSet(ctx, "workflow", float64(wf.ID), "stage", "staging", "version", float64(1),
		"vars", map[string]any{"url": "https://s"}, "filter", map[string]any{"source_install_id": "42"})
	if err != nil || !changed {
		t.Fatalf("create: %v changed=%v", err, changed)
	}
	if st.Rev != 1 || st.Status != "active" || st.PreviousVersion != nil || st.Filter == nil || *st.Filter.SourceInstallID != 42 {
		t.Fatalf("unexpected stage: %+v", st)
	}
	// Same values again → no-op, rev unchanged.
	st2, changed, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(1),
		"vars", map[string]any{"url": "https://s"})
	if err != nil || changed || st2.Rev != 1 {
		t.Fatalf("idempotent set: err=%v changed=%v rev=%d", err, changed, st2.Rev)
	}
	// Clearing the filter bumps rev.
	st3, changed, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "filter", nil)
	if err != nil || !changed || st3.Filter != nil || st3.Rev != 2 {
		t.Fatalf("clear filter: err=%v changed=%v %+v", err, changed, st3)
	}
}

func TestStages_EditNeverMovesStage(t *testing.T) {
	ctx, _ := newStageCtx(t)
	wf := createWF(t, ctx, "nomove", stagedEventV1)
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "production", "version", float64(1)); err != nil {
		t.Fatal(err)
	}
	updateWF(t, ctx, wf, stagedEventSource("v2"))
	updateWF(t, ctx, wf, stagedEventSource("v3"))
	if st := mustStage(t, ctx, wf, "production"); st.Version != 1 || st.Rev != 1 {
		t.Fatalf("edit moved stage: %+v", st)
	}
}

func TestStages_RevConflict(t *testing.T) {
	ctx, _ := newStageCtx(t)
	wf := createWF(t, ctx, "revc", stagedEventV1)
	updateWF(t, ctx, wf, stagedEventSource("v2"))
	// expected_rev 0 = must not exist yet.
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(1), "expected_rev", float64(0)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(1), "expected_rev", float64(0)); err == nil {
		// Same values, but the stage now exists at rev 1 — still a conflict.
		t.Fatal("expected_rev 0 on existing stage accepted")
	}
	_, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(2), "expected_rev", float64(5))
	if err == nil || !strings.Contains(err.Error(), "rev conflict") {
		t.Fatalf("want rev conflict, got %v", err)
	}
	if st := mustStage(t, ctx, wf, "staging"); st.Version != 1 || st.Rev != 1 {
		t.Fatalf("conflict changed the stage: %+v", st)
	}
	st, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(2), "expected_rev", float64(1))
	if err != nil || st.Version != 2 || st.Rev != 2 || st.PreviousVersion == nil || *st.PreviousVersion != 1 {
		t.Fatalf("matching rev: %v %+v", err, st)
	}
	// Delete with stale rev refused; correct rev deletes; repeat is a no-op.
	if _, err := (&App{}).toolStageDelete(ctx, pargs("workflow", wf.Name, "stage", "staging", "expected_rev", float64(1))); err == nil {
		t.Fatal("stale delete accepted")
	}
	out, err := (&App{}).toolStageDelete(ctx, pargs("workflow", wf.Name, "stage", "staging", "expected_rev", float64(2)))
	if err != nil || out.(map[string]any)["deleted"] != true {
		t.Fatalf("delete: %v %v", out, err)
	}
	out, err = (&App{}).toolStageDelete(ctx, pargs("workflow", wf.Name, "stage", "staging"))
	if err != nil || out.(map[string]any)["deleted"] != false {
		t.Fatalf("repeat delete: %v %v", out, err)
	}
}

// ─── promote_many / rollback ───────────────────────────────────────

func promoteMany(ctx *sdk.AppCtx, stage string, items ...map[string]any) (map[string]any, error) {
	raw := make([]any, len(items))
	for i := range items {
		raw[i] = items[i]
	}
	out, err := (&App{}).toolStagePromoteMany(ctx, pargs("stage", stage, "items", raw))
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func TestStages_PromoteManyIsAllOrNothing(t *testing.T) {
	ctx, _ := newStageCtx(t)
	a := createWF(t, ctx, "pm-a", stagedEventV1)
	b := createWF(t, ctx, "pm-b", stagedEventV1)
	updateWF(t, ctx, a, stagedEventSource("v2"))
	updateWF(t, ctx, b, stagedEventSource("v2"))

	// Creates missing stages.
	if _, err := promoteMany(ctx, "staging",
		map[string]any{"workflow": "pm-a", "version": float64(1)},
		map[string]any{"workflow": float64(b.ID), "version": float64(1)}); err != nil {
		t.Fatal(err)
	}
	// One bad item (missing version) → nothing moves.
	_, err := promoteMany(ctx, "staging",
		map[string]any{"workflow": "pm-a", "version": float64(2)},
		map[string]any{"workflow": "pm-b", "version": float64(99)})
	if err == nil || !strings.Contains(err.Error(), "items[1]") {
		t.Fatalf("want items[1] error, got %v", err)
	}
	// One rev conflict → nothing moves.
	_, err = promoteMany(ctx, "staging",
		map[string]any{"workflow": "pm-a", "version": float64(2), "expected_rev": float64(1)},
		map[string]any{"workflow": "pm-b", "version": float64(2), "expected_rev": float64(9)})
	if err == nil || !strings.Contains(err.Error(), "rev conflict") {
		t.Fatalf("want rev conflict, got %v", err)
	}
	// Unknown workflow → nothing moves.
	if _, err = promoteMany(ctx, "staging",
		map[string]any{"workflow": "pm-a", "version": float64(2)},
		map[string]any{"workflow": "nope", "version": float64(2)}); err == nil {
		t.Fatal("unknown workflow accepted")
	}
	for _, wf := range []*Workflow{a, b} {
		if st := mustStage(t, ctx, wf, "staging"); st.Version != 1 || st.Rev != 1 {
			t.Fatalf("%s moved after failed batch: %+v", wf.Name, st)
		}
	}
	out, err := promoteMany(ctx, "staging",
		map[string]any{"workflow": "pm-a", "version": float64(2), "expected_rev": float64(1)},
		map[string]any{"workflow": "pm-b", "version": float64(2)})
	if err != nil || out["changed"] != 2 {
		t.Fatalf("promote: %v %v", out, err)
	}
	// Retry of the same promote is a no-op: previous_version survives.
	out, err = promoteMany(ctx, "staging",
		map[string]any{"workflow": "pm-a", "version": float64(2)},
		map[string]any{"workflow": "pm-b", "version": float64(2)})
	if err != nil || out["changed"] != 0 {
		t.Fatalf("retry promote: %v %v", out, err)
	}
	for _, wf := range []*Workflow{a, b} {
		st := mustStage(t, ctx, wf, "staging")
		if st.Version != 2 || st.PreviousVersion == nil || *st.PreviousVersion != 1 || st.Rev != 2 {
			t.Fatalf("%s after promote: %+v", wf.Name, st)
		}
	}
}

func TestStages_PromoteRefusesInvalidVersion(t *testing.T) {
	ctx, _ := newStageCtx(t)
	wf := createWF(t, ctx, "pinv", stagedEventV1)
	// A frozen version whose definition no longer validates (e.g.
	// imported by an older runtime). Promotion must refuse it.
	if _, err := ctx.AppDB().Exec(`INSERT INTO workflow_versions (workflow_id, version, source, source_hash, trigger_kind)
		VALUES (?, 2, 'name: x\nsteps: []', 'h', 'manual')`, wf.ID); err != nil {
		t.Fatal(err)
	}
	_, err := promoteMany(ctx, "production", map[string]any{"workflow": wf.Name, "version": float64(2)})
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("want invalid-version refusal, got %v", err)
	}
	if st, _ := dbGetStage(ctx.AppDB(), wf.ID, "production"); st != nil {
		t.Fatalf("stage created despite refusal: %+v", st)
	}
}

func TestStages_Rollback(t *testing.T) {
	ctx, _ := newStageCtx(t)
	a := createWF(t, ctx, "rb-a", stagedEventV1)
	b := createWF(t, ctx, "rb-b", stagedEventV1)
	updateWF(t, ctx, a, stagedEventSource("v2"))
	updateWF(t, ctx, b, stagedEventSource("v2"))
	if _, err := promoteMany(ctx, "production",
		map[string]any{"workflow": "rb-a", "version": float64(1)},
		map[string]any{"workflow": "rb-b", "version": float64(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := promoteMany(ctx, "production", map[string]any{"workflow": "rb-a", "version": float64(2)}); err != nil {
		t.Fatal(err)
	}
	// rb-b has no previous version → whole rollback refused.
	_, err := (&App{}).toolStageRollback(ctx, pargs("stage", "production", "workflows", []any{"rb-a", "rb-b"}))
	if err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Fatalf("want no-previous refusal, got %v", err)
	}
	if st := mustStage(t, ctx, a, "production"); st.Version != 2 {
		t.Fatalf("rb-a moved by failed rollback: %+v", st)
	}
	// Stale expected_rev → refused.
	if _, err := (&App{}).toolStageRollback(ctx, pargs("stage", "production",
		"workflows", []any{map[string]any{"workflow": "rb-a", "expected_rev": float64(1)}})); err == nil {
		t.Fatal("stale rollback accepted")
	}
	out, err := (&App{}).toolStageRollback(ctx, pargs("stage", "production",
		"workflows", []any{map[string]any{"workflow": "rb-a", "expected_rev": float64(2)}}))
	if err != nil {
		t.Fatal(err)
	}
	st := out.(map[string]any)["stages"].([]*Stage)[0]
	if st.Version != 1 || st.PreviousVersion == nil || *st.PreviousVersion != 2 || st.Rev != 3 {
		t.Fatalf("after rollback: %+v", st)
	}
	// Retrying with the same expected_rev is refused, not re-applied.
	if _, err := (&App{}).toolStageRollback(ctx, pargs("stage", "production",
		"workflows", []any{map[string]any{"workflow": "rb-a", "expected_rev": float64(2)}})); err == nil {
		t.Fatal("retried rollback re-applied")
	}
}

// ─── dispatch ──────────────────────────────────────────────────────

func TestStages_UnstagedEventDispatchUnchanged(t *testing.T) {
	ctx, _ := newStageCtx(t)
	wf := createWF(t, ctx, "unstaged", stagedEventV1)
	dispatch(ctx, wf, 1, "")
	runs := waitRuns(t, ctx, wf.ID, 1)
	if len(runs) != 1 || runs[0].Stage != "" || runs[0].WorkflowVersion != 1 || runs[0].Status != "completed" {
		t.Fatalf("unstaged runs: %+v", runs)
	}
	if strings.Contains(runs[0].InputJSON, "source_install_id") {
		t.Fatalf("input gained source_install_id without the frame carrying it: %s", runs[0].InputJSON)
	}
}

func TestStages_TwoStagesOneEventTwoRuns(t *testing.T) {
	ctx, rec := newStageCtx(t)
	wf := createWF(t, ctx, "two", stagedEventV1)
	wf = updateWF(t, ctx, wf, stagedEventSource("v2"))
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "production", "version", float64(1),
		"vars", map[string]any{"url": "https://prod"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(2),
		"vars", map[string]any{"url": "https://staging"}); err != nil {
		t.Fatal(err)
	}
	dispatch(ctx, wf, 1, "")
	runs := waitRuns(t, ctx, wf.ID, 2)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	byStage := map[string]*Run{}
	for _, r := range runs {
		byStage[r.Stage] = r
	}
	if byStage["production"] == nil || byStage["production"].WorkflowVersion != 1 ||
		byStage["staging"] == nil || byStage["staging"].WorkflowVersion != 2 {
		t.Fatalf("stage/version mapping wrong: %+v", runs)
	}
	got := map[string]map[string]any{}
	for _, ev := range rec.EventsByTopic("staged.out") {
		d := ev.Data.(map[string]any)
		got[d["stage"].(string)] = d
	}
	if p := got["production"]; p == nil || p["url"] != "https://prod" || p["marker"] != "v1" || p["version"] != float64(1) {
		t.Fatalf("production output: %+v", p)
	}
	if s := got["staging"]; s == nil || s["url"] != "https://staging" || s["marker"] != "v2" || s["version"] != float64(2) {
		t.Fatalf("staging output: %+v", s)
	}
	// A redelivered frame (same seq on the same lane) starts no new
	// runs: dedupe stays per (event, stage).
	mgr := newEventTrigger(ctx)
	ln := &eventLane{key: laneKey{source: "tables", projectID: testProj}, workflows: []*Workflow{wf}}
	frame := fmt.Sprintf(`{"topic":"row.inserted","app":"tables","project_id":"%s","seq":5,"data":{}}`, testProj)
	mgr.dispatchFrame(ln, "5", frame)
	mgr.dispatchFrame(ln, "5", frame)
	if runs := waitRuns(t, ctx, wf.ID, 4); len(runs) != 4 {
		t.Fatalf("duplicate frame produced %d runs, want 4 total", len(runs))
	}
}

func TestStages_PausedStageSkipped(t *testing.T) {
	ctx, _ := newStageCtx(t)
	wf := createWF(t, ctx, "paused", stagedEventV1)
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(1), "status", "paused"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "production", "version", float64(1)); err != nil {
		t.Fatal(err)
	}
	dispatch(ctx, wf, 1, "")
	runs := waitRuns(t, ctx, wf.ID, 1)
	time.Sleep(100 * time.Millisecond)
	runs, _ = dbListRuns(ctx.AppDB(), testProj, wf.ID, 10)
	if len(runs) != 1 || runs[0].Stage != "production" {
		t.Fatalf("paused stage ran: %+v", runs)
	}
	// All stages paused → no fallback to head.
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "production", "status", "paused"); err != nil {
		t.Fatal(err)
	}
	dispatch(ctx, wf, 2, "")
	time.Sleep(150 * time.Millisecond)
	if runs, _ = dbListRuns(ctx.AppDB(), testProj, wf.ID, 10); len(runs) != 1 {
		t.Fatalf("all-paused workflow ran: %d runs", len(runs))
	}
}

func TestStages_FilterWhenAndSourceInstallID(t *testing.T) {
	ctx, _ := newStageCtx(t)
	wf := createWF(t, ctx, "filtered", stagedEventV1)
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(1),
		"filter", map[string]any{"when": "input.data.env == 'staging'"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "production", "version", float64(1),
		"filter", map[string]any{"when": "input.data.env == 'production'", "source_install_id": float64(42)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "pinned", "version", float64(1),
		"filter", map[string]any{"source_install_id": float64(42)}); err != nil {
		t.Fatal(err)
	}
	// env=staging, no source_install_id: staging matches; pinned's
	// install clause is ignored (absent) so it matches too.
	dispatch(ctx, wf, 1, "")
	runs := waitRuns(t, ctx, wf.ID, 2)
	time.Sleep(100 * time.Millisecond)
	runs, _ = dbListRuns(ctx.AppDB(), testProj, wf.ID, 10)
	stages := map[string]int{}
	for _, r := range runs {
		stages[r.Stage]++
	}
	if len(runs) != 2 || stages["staging"] != 1 || stages["pinned"] != 1 {
		t.Fatalf("absent install id: %+v", stages)
	}
	// source_install_id 7 ≠ 42: pinned filtered out; staging still matches.
	dispatch(ctx, wf, 2, `,"source_install_id":7`)
	waitRuns(t, ctx, wf.ID, 3)
	time.Sleep(100 * time.Millisecond)
	runs, _ = dbListRuns(ctx.AppDB(), testProj, wf.ID, 10)
	stages = map[string]int{}
	for _, r := range runs {
		stages[r.Stage]++
	}
	if len(runs) != 3 || stages["staging"] != 2 || stages["pinned"] != 1 {
		t.Fatalf("install id 7: %+v", stages)
	}
	if !strings.Contains(runs[0].InputJSON, `"source_install_id":7`) {
		t.Fatalf("input should carry source_install_id when present: %s", runs[0].InputJSON)
	}
	// source_install_id "42" (string form) matches pinned.
	dispatch(ctx, wf, 3, `,"source_install_id":"42"`)
	waitRuns(t, ctx, wf.ID, 5)
	time.Sleep(100 * time.Millisecond)
	runs, _ = dbListRuns(ctx.AppDB(), testProj, wf.ID, 10)
	stages = map[string]int{}
	for _, r := range runs {
		stages[r.Stage]++
	}
	if len(runs) != 5 || stages["pinned"] != 2 || stages["production"] != 0 {
		t.Fatalf("install id 42: %+v", stages)
	}
	// The platform stamps the publisher as install_id (AppEvent.install_id).
	// install_id 7 ≠ 42: pinned filtered out, staging matches.
	dispatch(ctx, wf, 4, `,"install_id":7`)
	waitRuns(t, ctx, wf.ID, 6)
	time.Sleep(100 * time.Millisecond)
	runs, _ = dbListRuns(ctx.AppDB(), testProj, wf.ID, 20)
	stages = map[string]int{}
	for _, r := range runs {
		stages[r.Stage]++
	}
	if len(runs) != 6 || stages["pinned"] != 2 || stages["staging"] != 4 {
		t.Fatalf("platform install_id 7: %+v", stages)
	}
	if !strings.Contains(runs[0].InputJSON, `"source_install_id":7`) {
		t.Fatalf("input should carry the platform install_id as source_install_id: %s", runs[0].InputJSON)
	}
	// install_id 0 (older platforms) counts as absent: pinned matches.
	dispatch(ctx, wf, 5, `,"install_id":0`)
	waitRuns(t, ctx, wf.ID, 8)
	time.Sleep(100 * time.Millisecond)
	runs, _ = dbListRuns(ctx.AppDB(), testProj, wf.ID, 20)
	stages = map[string]int{}
	for _, r := range runs {
		stages[r.Stage]++
	}
	if len(runs) != 8 || stages["pinned"] != 3 || stages["staging"] != 5 {
		t.Fatalf("platform install_id 0: %+v", stages)
	}
}

func TestStages_VersionTriggerDrivesStagedDispatch(t *testing.T) {
	ctx, _ := newStageCtx(t)
	wf := createWF(t, ctx, "trigmove", stagedEventV1)
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "production", "version", float64(1)); err != nil {
		t.Fatal(err)
	}
	// Head now listens to a different app and topic; production is
	// still on v1 (tables/row.inserted) and must keep firing there.
	moved := strings.Replace(stagedEventV1, "source: tables", "source: crm", 1)
	wf = updateWF(t, ctx, wf, moved)
	sources, err := eventSourcesForWorkflow(ctx.AppDB(), wf)
	if err != nil || len(sources) != 1 || sources[0] != "tables" {
		t.Fatalf("sources = %v %v, want [tables]", sources, err)
	}
	all, err := dbListEventTriggeredWorkflowsAll(ctx.AppDB())
	if err != nil || len(all) != 1 {
		t.Fatalf("event workflows = %d %v", len(all), err)
	}
	dispatch(ctx, wf, 1, "")
	runs := waitRuns(t, ctx, wf.ID, 1)
	if len(runs) != 1 || runs[0].Stage != "production" || runs[0].WorkflowVersion != 1 {
		t.Fatalf("runs: %+v", runs)
	}
}

// ─── manual runs, templates, replay ────────────────────────────────

const stagedManual = `name: manual-staged
steps:
  - id: gate
    kind: branch
    when: "stage.vars.enabled == true"
    else: { end: true }
  - id: out
    kind: emit
    topic: manual.out
    input:
      who: "{{ stage.name }} via {{ stage.vars.url }}"
      in: "{{ input.x }}"
`

func TestStages_ManualRunTemplatesAndReplay(t *testing.T) {
	ctx, rec := newStageCtx(t)
	wf := createWF(t, ctx, "manual-staged", stagedManual)
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(1),
		"vars", map[string]any{"url": "https://s", "enabled": true}); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolRun(ctx, pargs("name", wf.Name, "stage", "staging", "input", map[string]any{"x": "hi"}))
	if err != nil {
		t.Fatal(err)
	}
	run := out.(map[string]any)["run"].(*Run)
	if run.Status != "completed" || run.Stage != "staging" || run.WorkflowVersion != 1 {
		t.Fatalf("staged manual run: %+v", run)
	}
	evs := rec.EventsByTopic("manual.out")
	if len(evs) != 1 || evs[0].Data.(map[string]any)["who"] != "staging via https://s" {
		t.Fatalf("stage templates: %+v", evs)
	}
	// Unknown stage → clear error; no stage → head run without stage.* values.
	if _, err := (&App{}).toolRun(ctx, pargs("name", wf.Name, "stage", "nope")); err == nil {
		t.Fatal("unknown stage accepted")
	}
	out, err = (&App{}).toolRun(ctx, pargs("name", wf.Name))
	if err != nil {
		t.Fatal(err)
	}
	if head := out.(map[string]any)["run"].(*Run); head.Stage != "" || len(rec.EventsByTopic("manual.out")) != 1 {
		t.Fatalf("head run should take the branch else (no stage vars): %+v", head)
	}

	// Move the stage and change its vars, then replay the first run:
	// it must keep stage=staging, version 1 and the original vars.
	wf = updateWF(t, ctx, wf, strings.Replace(stagedManual, "{{ input.x }}", "changed", 1))
	if _, _, err := stageSet(ctx, "workflow", wf.Name, "stage", "staging", "version", float64(2),
		"vars", map[string]any{"url": "https://other", "enabled": true}); err != nil {
		t.Fatal(err)
	}
	replayed, err := (&App{}).toolReplay(ctx, pargs("run_id", float64(run.ID)))
	if err != nil {
		t.Fatal(err)
	}
	rr := replayed.(map[string]any)["run"].(*Run)
	if rr.Stage != "staging" || rr.WorkflowVersion != 1 || rr.Status != "completed" {
		t.Fatalf("replay: %+v", rr)
	}
	evs = rec.EventsByTopic("manual.out")
	last := evs[len(evs)-1].Data.(map[string]any)
	if last["who"] != "staging via https://s" || last["in"] != "hi" {
		t.Fatalf("replay used current stage state: %+v", last)
	}
	// Runs filter by stage.
	rs, err := (&App{}).toolRuns(ctx, pargs("name", wf.Name, "stage", "staging"))
	if err != nil || len(rs.(map[string]any)["runs"].([]*Run)) != 2 {
		t.Fatalf("stage-filtered runs: %v %v", rs, err)
	}
}

func TestStages_TemplateStageMissWhenUnstaged(t *testing.T) {
	v, ok := resolvePath(TemplateContext{}, "stage.name")
	if ok || v != nil {
		t.Fatalf("unstaged stage.name = %v %v", v, ok)
	}
	ctx := TemplateContext{Stage: map[string]any{"name": "prod", "vars": map[string]any{"k": "v"}}}
	got, _ := Render("{{ stage.name }}/{{ stage.vars.k }}", ctx)
	if got != "prod/v" {
		t.Fatalf("render = %v", got)
	}
}

// ─── listing, deletion, migration, manifest ────────────────────────

func TestStages_ListGetAndCascadeDelete(t *testing.T) {
	ctx, _ := newStageCtx(t)
	a := createWF(t, ctx, "ls-a", stagedEventV1)
	b := createWF(t, ctx, "ls-b", stagedEventV1)
	for _, wf := range []*Workflow{a, b} {
		if _, err := promoteMany(ctx, "production", map[string]any{"workflow": wf.Name, "version": float64(1)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := stageSet(ctx, "workflow", "ls-a", "stage", "staging", "version", float64(1)); err != nil {
		t.Fatal(err)
	}
	out, _ := (&App{}).toolStageList(ctx, pargs("stage", "production"))
	if out.(map[string]any)["count"] != 2 {
		t.Fatalf("by name: %+v", out)
	}
	out, _ = (&App{}).toolStageList(ctx, pargs("workflow", "ls-a"))
	if out.(map[string]any)["count"] != 2 {
		t.Fatalf("by workflow: %+v", out)
	}
	got, err := (&App{}).toolGet(ctx, pargs("name", "ls-a", "version", float64(1)))
	if err != nil {
		t.Fatal(err)
	}
	gm := got.(map[string]any)
	if len(gm["stages"].([]*Stage)) != 2 || len(gm["versions"].([]*WorkflowVersion)) != 1 || gm["version"].(*WorkflowVersion).Source == "" {
		t.Fatalf("get: %+v", gm)
	}
	if _, err := (&App{}).toolDelete(ctx, pargs("name", "ls-a")); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM workflow_stages WHERE workflow_id = ?`, a.ID).Scan(&n)
	var nv int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM workflow_versions WHERE workflow_id = ?`, a.ID).Scan(&nv)
	if n != 0 || nv != 0 {
		t.Fatalf("delete left stages=%d versions=%d", n, nv)
	}
}

func TestStages_MigrationOnExistingDB(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	m1, _ := os.ReadFile("migrations/001_init.sql")
	m2, _ := os.ReadFile("migrations/002_stages.sql")
	if _, err := db.Exec(string(m1)); err != nil {
		t.Fatal(err)
	}
	// A pre-0.5 install: one inline workflow at version 3, one repo
	// workflow, one historical run.
	if _, err := db.Exec(`INSERT INTO workflows (id, project_id, name, version, source_kind, source, source_hash, trigger_kind, trigger_json, status)
		VALUES (1, 'p', 'old', 3, 'inline', ?, 'h3', 'event', '{"kind":"event","source":"tables","topic":"row.inserted"}', 'active'),
		       (2, 'p', 'repo', 1, 'repo', NULL, 'hr', 'manual', NULL, 'active')`, stagedEventV1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_runs (project_id, workflow_id, workflow_name, workflow_version, trigger_kind, status)
		VALUES ('p', 1, 'old', 3, 'manual', 'completed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(m2)); err != nil {
		t.Fatalf("002 on existing db: %v", err)
	}
	v, err := dbGetVersion(db, 1, 3)
	if err != nil || v == nil || v.Source != stagedEventV1 || v.SourceHash != "h3" || !strings.Contains(v.TriggerJSON, "tables") {
		t.Fatalf("backfilled version: %+v %v", v, err)
	}
	if v, _ := dbGetVersion(db, 2, 1); v != nil {
		t.Fatalf("repo workflow should not be backfilled without bytes: %+v", v)
	}
	runs, err := dbListRuns(db, "p", 1, 10)
	if err != nil || len(runs) != 1 || runs[0].Stage != "" {
		t.Fatalf("old run after migration: %+v %v", runs, err)
	}
	// Workflows without stages still list for event dispatch exactly as before.
	all, err := dbListEventTriggeredWorkflowsAll(db)
	if err != nil || len(all) != 1 || all[0].Name != "old" {
		t.Fatalf("event workflows: %+v %v", all, err)
	}
	// A stage can be created on the backfilled version.
	wf, _ := dbGetWorkflow(db, "p", 1, "")
	tx, _ := db.Begin()
	if _, _, err := txUpsertStage(tx, "p", wf, "production", stagePatch{Version: intp(3)}); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
}

func TestStages_ManifestListsEveryTool(t *testing.T) {
	tools := map[string]bool{}
	for _, tool := range (&App{}).MCPTools() {
		tools[tool.Name] = true
	}
	for _, want := range []string{
		"workflows_stage_set", "workflows_stage_list", "workflows_stage_delete",
		"workflows_stage_promote_many", "workflows_stage_rollback",
	} {
		if !tools[want] {
			t.Errorf("MCPTools missing %s", want)
		}
	}
	diskBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(diskBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []sdk.Manifest{(&App{}).Manifest(), *disk} {
		declared := map[string]bool{}
		for _, tool := range m.Provides.MCPTools {
			declared[tool.Name] = true
		}
		for name := range tools {
			if !declared[name] {
				t.Errorf("manifest does not declare tool %s", name)
			}
		}
		if len(declared) != len(tools) {
			t.Errorf("manifest declares %d tools, code registers %d", len(declared), len(tools))
		}
	}
	if disk.Version != "0.5.1" {
		t.Errorf("apteva.yaml version = %s, want 0.5.1", disk.Version)
	}
}
