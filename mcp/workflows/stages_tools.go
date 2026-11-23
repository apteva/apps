package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// resolveRunTarget picks what a manual / HTTP run executes. Without a
// stage it is the head workflow, unchanged from pre-0.5 behaviour.
// With a stage it is that stage's frozen version plus its vars.
// Paused stages can still be run by hand: paused only stops event
// dispatch.
func resolveRunTarget(db *sql.DB, wf *Workflow, stage, trigger string) (*Workflow, runOptions, error) {
	opts := runOptions{triggerKind: trigger}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return wf, opts, nil
	}
	st, err := dbGetStage(db, wf.ID, stage)
	if err != nil {
		return nil, opts, err
	}
	if st == nil {
		return nil, opts, fmt.Errorf("workflow %q has no stage %q", wf.Name, stage)
	}
	v, err := dbGetVersion(db, wf.ID, st.Version)
	if err != nil {
		return nil, opts, err
	}
	if v == nil {
		return nil, opts, fmt.Errorf("stage %q points at missing version %d", stage, st.Version)
	}
	sw, err := stagedWorkflow(wf, v)
	if err != nil {
		return nil, opts, err
	}
	opts.stage = &runStage{Name: st.Name, Vars: st.Vars}
	return sw, opts, nil
}

// ensureVersionSnapshot covers repo-backed workflows created before
// 0.5.0: their head version has no frozen row yet. Promoting that
// exact head version resolves the repo source once, checks it still
// hashes to the recorded source_hash, and freezes it. Anything else
// (older versions, hash drift) is left for validation to reject.
func ensureVersionSnapshot(ctx *sdk.AppCtx, pid string, wf *Workflow, version int) error {
	if wf.SourceKind != "repo" || version != wf.Version {
		return nil
	}
	db := dbFor(ctx)
	if v, err := dbGetVersion(db, wf.ID, version); err != nil || v != nil {
		return err
	}
	bytes, err := resolveWorkflowSource(ctx, wf)
	if err != nil {
		return fmt.Errorf("workflow %q: resolve repo source for version %d: %w", wf.Name, version, err)
	}
	if hashSource(bytes) != wf.SourceHash {
		return fmt.Errorf("workflow %q: repo source changed since version %d was recorded; update the workflow to freeze a new version", wf.Name, version)
	}
	return txSnapshotVersion(db, pid, wf.ID, bytes)
}

// ─── Argument parsing ──────────────────────────────────────────────

func optIntArg(args map[string]any, key string) (*int, error) {
	raw, has := args[key]
	if !has || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case float64:
		if v != float64(int(v)) {
			return nil, fmt.Errorf("%s must be an integer", key)
		}
		n := int(v)
		return &n, nil
	case int:
		return &v, nil
	case int64:
		n := int(v)
		return &n, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		n := intArg(map[string]any{key: v}, key, -1)
		if n < 0 {
			return nil, fmt.Errorf("%s must be an integer", key)
		}
		return &n, nil
	}
	return nil, fmt.Errorf("%s must be an integer", key)
}

func parseStageFilterArg(raw any) (*StageFilter, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("filter must be an object {when?, source_install_id?}")
	}
	f := &StageFilter{}
	for k, v := range m {
		switch k {
		case "when":
			if v == nil {
				continue
			}
			s, ok := v.(string)
			if !ok {
				return nil, errors.New("filter.when must be a string")
			}
			f.When = strings.TrimSpace(s)
		case "source_install_id":
			if v == nil {
				continue
			}
			id, ok := parseInstallID(v)
			if !ok {
				return nil, errors.New("filter.source_install_id must be an integer")
			}
			f.SourceInstallID = id
		default:
			return nil, fmt.Errorf("filter: unknown key %q (allowed: when, source_install_id)", k)
		}
	}
	return f, nil
}

func parseStagePatch(args map[string]any) (stagePatch, error) {
	var p stagePatch
	var err error
	if p.Version, err = optIntArg(args, "version"); err != nil {
		return p, err
	}
	if p.ExpectedRev, err = optIntArg(args, "expected_rev"); err != nil {
		return p, err
	}
	if raw, has := args["vars"]; has {
		p.VarsSet = true
		if raw != nil {
			m, ok := raw.(map[string]any)
			if !ok {
				return p, errors.New("vars must be a JSON object")
			}
			p.Vars = m
		}
	}
	if raw, has := args["filter"]; has {
		p.FilterSet = true
		if p.Filter, err = parseStageFilterArg(raw); err != nil {
			return p, err
		}
	}
	p.Status = strings.TrimSpace(strArg(args, "status"))
	return p, nil
}

// withTx runs fn in one transaction; any error rolls everything back.
func withTx(db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func emitStageChanged(ctx *sdk.AppCtx, action string, stages []*Stage) {
	if ctx == nil || len(stages) == 0 {
		return
	}
	items := make([]map[string]any, 0, len(stages))
	for _, s := range stages {
		items = append(items, map[string]any{
			"workflow_id": s.WorkflowID, "workflow": s.WorkflowName,
			"stage": s.Name, "version": s.Version, "status": s.Status, "rev": s.Rev,
		})
	}
	ctx.Emit("workflow.stage.changed", map[string]any{"action": action, "stages": items})
}

// ─── Tools ─────────────────────────────────────────────────────────

func (a *App) toolStageSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "stage"))
	if err := validateStageName(name); err != nil {
		return nil, err
	}
	patch, err := parseStagePatch(args)
	if err != nil {
		return nil, err
	}
	db := dbFor(ctx)
	wf, err := getWorkflowRef(db, pid, args["workflow"])
	if err != nil {
		return nil, err
	}
	if patch.Version != nil {
		if err := ensureVersionSnapshot(ctx, pid, wf, *patch.Version); err != nil {
			return nil, err
		}
	}
	var out *Stage
	var changed bool
	if err := withTx(db, func(tx *sql.Tx) error {
		out, changed, err = txUpsertStage(tx, pid, wf, name, patch)
		return err
	}); err != nil {
		return nil, err
	}
	if changed {
		emitStageChanged(ctx, "set", []*Stage{out})
		kickEventTrigger()
	}
	return map[string]any{"stage": out, "changed": changed}, nil
}

func (a *App) toolStageList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	db := dbFor(ctx)
	var wfID int64
	if ref, has := args["workflow"]; has && ref != nil && ref != "" {
		wf, err := getWorkflowRef(db, pid, ref)
		if err != nil {
			return nil, err
		}
		wfID = wf.ID
	}
	stages, err := dbListStages(db, pid, wfID, strings.TrimSpace(strArg(args, "stage")))
	if err != nil {
		return nil, err
	}
	return map[string]any{"stages": stages, "count": len(stages)}, nil
}

func (a *App) toolStageDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "stage"))
	if err := validateStageName(name); err != nil {
		return nil, err
	}
	expected, err := optIntArg(args, "expected_rev")
	if err != nil {
		return nil, err
	}
	db := dbFor(ctx)
	wf, err := getWorkflowRef(db, pid, args["workflow"])
	if err != nil {
		return nil, err
	}
	deleted := false
	if err := withTx(db, func(tx *sql.Tx) error {
		cur, err := dbGetStage(tx, wf.ID, name)
		if err != nil {
			return err
		}
		if cur == nil {
			// Idempotent: already gone.
			return nil
		}
		if expected != nil && cur.Rev != *expected {
			return revConflict(wf, name, cur.Rev, *expected)
		}
		if _, err := tx.Exec(`DELETE FROM workflow_stages WHERE id = ?`, cur.ID); err != nil {
			return err
		}
		deleted = true
		return nil
	}); err != nil {
		return nil, err
	}
	if deleted {
		if ctx != nil {
			ctx.Emit("workflow.stage.changed", map[string]any{"action": "delete", "stages": []map[string]any{{
				"workflow_id": wf.ID, "workflow": wf.Name, "stage": name,
			}}})
		}
		kickEventTrigger()
	}
	return map[string]any{"deleted": deleted, "workflow": wf.Name, "stage": name}, nil
}

// toolStagePromoteMany moves one stage across many workflows in a
// single transaction. Missing stages are created (active, empty vars).
// Any invalid version, unknown workflow or rev conflict aborts the
// whole batch.
func (a *App) toolStagePromoteMany(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "stage"))
	if err := validateStageName(name); err != nil {
		return nil, err
	}
	rawItems, ok := args["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return nil, errors.New("items must be a non-empty array of {workflow, version, expected_rev?}")
	}
	db := dbFor(ctx)
	type item struct {
		wf    *Workflow
		patch stagePatch
	}
	items := make([]item, 0, len(rawItems))
	seen := map[int64]bool{}
	for i, raw := range rawItems {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("items[%d]: must be an object", i)
		}
		wf, err := getWorkflowRef(db, pid, m["workflow"])
		if err != nil {
			return nil, fmt.Errorf("items[%d]: %w", i, err)
		}
		if seen[wf.ID] {
			return nil, fmt.Errorf("items[%d]: workflow %q listed twice", i, wf.Name)
		}
		seen[wf.ID] = true
		version, err := optIntArg(m, "version")
		if err != nil {
			return nil, fmt.Errorf("items[%d]: %w", i, err)
		}
		if version == nil {
			return nil, fmt.Errorf("items[%d]: version required", i)
		}
		expected, err := optIntArg(m, "expected_rev")
		if err != nil {
			return nil, fmt.Errorf("items[%d]: %w", i, err)
		}
		if err := ensureVersionSnapshot(ctx, pid, wf, *version); err != nil {
			return nil, fmt.Errorf("items[%d]: %w", i, err)
		}
		items = append(items, item{wf: wf, patch: stagePatch{Version: version, ExpectedRev: expected}})
	}
	out := make([]*Stage, 0, len(items))
	changedStages := []*Stage{}
	if err := withTx(db, func(tx *sql.Tx) error {
		for i, it := range items {
			st, changed, err := txUpsertStage(tx, pid, it.wf, name, it.patch)
			if err != nil {
				return fmt.Errorf("items[%d]: %w", i, err)
			}
			out = append(out, st)
			if changed {
				changedStages = append(changedStages, st)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(changedStages) > 0 {
		emitStageChanged(ctx, "promote", changedStages)
		kickEventTrigger()
	}
	return map[string]any{"stage": name, "stages": out, "changed": len(changedStages)}, nil
}

// toolStageRollback points each listed workflow's stage back at its
// previous_version (and the current version becomes the new
// previous_version) in one transaction.
func (a *App) toolStageRollback(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "stage"))
	if err := validateStageName(name); err != nil {
		return nil, err
	}
	rawItems, ok := args["workflows"].([]any)
	if !ok || len(rawItems) == 0 {
		return nil, errors.New("workflows must be a non-empty array of workflow ids/names or {workflow, expected_rev?}")
	}
	db := dbFor(ctx)
	out := []*Stage{}
	if err := withTx(db, func(tx *sql.Tx) error {
		seen := map[int64]bool{}
		for i, raw := range rawItems {
			ref := raw
			var expected *int
			if m, ok := raw.(map[string]any); ok {
				if r, has := m["workflow"]; has {
					ref = r
				}
				var err error
				if expected, err = optIntArg(m, "expected_rev"); err != nil {
					return fmt.Errorf("workflows[%d]: %w", i, err)
				}
			}
			wf, err := getWorkflowRef(tx, pid, ref)
			if err != nil {
				return fmt.Errorf("workflows[%d]: %w", i, err)
			}
			if seen[wf.ID] {
				return fmt.Errorf("workflows[%d]: workflow %q listed twice", i, wf.Name)
			}
			seen[wf.ID] = true
			st, err := txRollbackStage(tx, wf, name, expected)
			if err != nil {
				return fmt.Errorf("workflows[%d]: %w", i, err)
			}
			out = append(out, st)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	emitStageChanged(ctx, "rollback", out)
	kickEventTrigger()
	return map[string]any{"stage": name, "stages": out}, nil
}
