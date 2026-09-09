package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type SavedOutput struct {
	ID                     int64           `json:"id"`
	CompositionID          int64           `json:"composition_id"`
	Kind                   string          `json:"kind"`
	Revision               int64           `json:"revision"`
	Settings               OutputSettings  `json:"settings"`
	Plan                   json.RawMessage `json:"plan"`
	InputRevision          string          `json:"input_revision"`
	OutOfDate              bool            `json:"out_of_date"`
	LatestAttempt          map[string]any  `json:"latest_attempt"`
	LatestSuccessfulRender map[string]any  `json:"latest_successful_render"`
	PlanError              string          `json:"plan_error,omitempty"`
}

func outputComposition(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	pid := strArg(args, "project_id", strArg(args, "_project_id", projectScope(ctx)))
	if pid == "" {
		return nil, errors.New("project_id required")
	}
	if current := ctx.CurrentProject(); current != "" && current != pid {
		return nil, errors.New("project scope mismatch")
	}
	row, err := loadComposition(ctx.WithProject(pid), int64Arg(args, "id", 0))
	if err != nil {
		return nil, err
	}
	if row["project_id"] != pid {
		return nil, errors.New("composition not found in project")
	}
	return row, nil
}

func ensureOutputs(ctx *sdk.AppCtx, row map[string]any) error {
	id := row["id"]
	var base Output
	_ = json.Unmarshal([]byte(row["output_json"].(string)), &base)
	validateOutput(&base)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, kind := range []string{"song", "image_video", "full_clip"} {
		s := OutputSettings{Output: base}
		if kind == "song" {
			s.Format = "mp3"
		} else {
			s.Format = "mp4"
		}
		if _, err = tx.Exec(`INSERT INTO composition_outputs(composition_id,kind,settings_json) VALUES(?,?,?) ON CONFLICT(composition_id,kind) DO NOTHING`, id, kind, outputJSON(s)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO composition_shared_inputs(composition_id) VALUES(?) ON CONFLICT DO NOTHING`, id); err != nil {
		return err
	}
	return tx.Commit()
}
func loadSharedMaster(ctx *sdk.AppCtx, id int64) (*Clip, int64, error) {
	var raw string
	var rev int64
	err := ctx.AppDB().QueryRow(`SELECT master_json,revision FROM composition_shared_inputs WHERE composition_id=?`, id).Scan(&raw, &rev)
	var master *Clip
	if err == nil {
		err = json.Unmarshal([]byte(raw), &master)
	}
	return master, rev, err
}
func loadOutput(ctx *sdk.AppCtx, id int64, kind string) (SavedOutput, error) {
	var o SavedOutput
	var settings, plan string
	err := ctx.AppDB().QueryRow(`SELECT id,composition_id,kind,revision,settings_json,plan_json FROM composition_outputs WHERE composition_id=? AND kind=?`, id, kind).Scan(&o.ID, &o.CompositionID, &o.Kind, &o.Revision, &settings, &plan)
	if err == nil {
		err = json.Unmarshal([]byte(settings), &o.Settings)
	}
	o.Plan = json.RawMessage(plan)
	return o, err
}
func (a *App) toolOutputList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	row, err := outputComposition(ctx, args)
	if err != nil {
		return nil, err
	}
	if err = ensureOutputs(ctx, row); err != nil {
		return nil, err
	}
	id := row["id"].(int64)
	master, rev, err := loadSharedMaster(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []SavedOutput{}
	for _, kind := range []string{"song", "image_video", "full_clip"} {
		o, err := loadOutput(ctx, id, kind)
		if err != nil {
			return nil, err
		}
		snap, planErr := buildOutputSnapshot(row, kind, o.Settings, string(o.Plan), master)
		o.InputRevision = outputHash(snap)
		if planErr != nil {
			o.PlanError = planErr.Error()
			o.InputRevision = outputHash([]any{row["edit_json"], master, o.Plan, o.Settings})
		}
		o.LatestAttempt, err = outputRenderRow(ctx, o.ID, false)
		if err != nil {
			return nil, err
		}
		o.LatestSuccessfulRender, err = outputRenderRow(ctx, o.ID, true)
		if err != nil {
			return nil, err
		}
		if r := o.LatestSuccessfulRender; r != nil {
			o.OutOfDate = r["input_revision"] != o.InputRevision || r["output_revision"] != o.Revision
		}
		out = append(out, o)
	}
	costs, err := outputCosts(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"composition_id": id, "outputs": out, "master": master, "shared_revision": rev, "costs": costs}, nil
}
func outputRenderRow(ctx *sdk.AppCtx, id int64, success bool) (map[string]any, error) {
	q := `SELECT id FROM renders WHERE output_id=?`
	if success {
		q += ` AND status='complete'`
	}
	q += ` ORDER BY id DESC LIMIT 1`
	var rid int64
	err := ctx.AppDB().QueryRow(q, id).Scan(&rid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return readOutputRender(ctx, rid)
}
func readOutputRender(ctx *sdk.AppCtx, id int64) (map[string]any, error) {
	var oid, cid, revision, sid, dur int64
	var status, msg, input, snapshot, pending, created, key string
	var cost, generation sql.NullFloat64
	err := ctx.AppDB().QueryRow(`SELECT output_id,composition_id,output_revision,storage_id,duration_ms,status,error,input_revision,output_snapshot,pending_json,created_at,cost_usd,generation_cost_usd,idempotency_key FROM renders WHERE id=?`, id).Scan(&oid, &cid, &revision, &sid, &dur, &status, &msg, &input, &snapshot, &pending, &created, &cost, &generation, &key)
	if err != nil {
		return nil, err
	}
	var snap outputSnapshot
	_ = json.Unmarshal([]byte(snapshot), &snap)
	r := map[string]any{"id": id, "render_id": id, "output_id": oid, "composition_id": cid, "output_revision": revision, "status": status, "error": msg, "storage_id": sid, "duration_ms": dur, "input_revision": input, "settings": snap.Settings, "pending": json.RawMessage(pending), "created_at": created, "generation_cost_usd": nil, "render_cost_usd": nil, "idempotency_key": key}
	var jobs, known int
	var generationTotal sql.NullFloat64
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*),COUNT(cost_usd),SUM(cost_usd) FROM output_asset_jobs WHERE first_render_id=?`, id).Scan(&jobs, &known, &generationTotal); err != nil {
		return nil, err
	}
	if jobs == known {
		r["generation_cost_usd"] = generationTotal.Float64
	}

	if cost.Valid && cost.Float64 > 0 {
		r["render_cost_usd"] = cost.Float64
	}
	if sid > 0 {
		r["storage_url"] = fmt.Sprintf("/api/apps/storage/files/%d/content", sid)
	} else if status == "complete" {
		r["local_cache_url"] = localCacheURL(id)
	}
	return r, nil
}
func (a *App) toolOutputUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	row, err := outputComposition(ctx, args)
	if err != nil {
		return nil, err
	}
	if err = ensureOutputs(ctx, row); err != nil {
		return nil, err
	}
	id := row["id"].(int64)
	kind := strArg(args, "kind", "")
	o, err := loadOutput(ctx, id, kind)
	if err != nil {
		return nil, err
	}
	expected := int64Arg(args, "expected_revision", 0)
	if expected != o.Revision {
		return nil, errors.New("output revision conflict; refresh before saving")
	}
	if s, ok := args["settings"]; ok {
		if err = json.Unmarshal([]byte(outputJSON(s)), &o.Settings); err != nil {
			return nil, err
		}
	}
	if err = validateOutputSettings(kind, &o.Settings); err != nil {
		return nil, err
	}
	if p, ok := args["plan"]; ok {
		o.Plan = json.RawMessage(outputJSON(p))
		var e Edit
		if err = json.Unmarshal(o.Plan, &e); err != nil {
			return nil, err
		}
		for _, t := range e.Timeline.Tracks {
			if trackKind(t) == "audio" {
				return nil, errors.New("plans cannot override shared audio")
			}
		}
	}
	// Saving incomplete presets is allowed; render validates the effective plan.
	res, err := ctx.AppDB().Exec(`UPDATE composition_outputs SET settings_json=?,plan_json=?,revision=revision+1 WHERE id=? AND revision=?`, outputJSON(o.Settings), string(o.Plan), o.ID, expected)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, errors.New("output revision conflict")
	}
	return a.toolOutputList(ctx, args)
}
func (a *App) toolOutputMaster(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	row, err := outputComposition(ctx, args)
	if err != nil {
		return nil, err
	}
	if err = ensureOutputs(ctx, row); err != nil {
		return nil, err
	}
	var master *Clip
	if err = json.Unmarshal([]byte(outputJSON(args["master"])), &master); err != nil {
		return nil, err
	}
	if master != nil {
		master.Start = 0
		master.UID = "shared-master"
		master.Asset.Type = "audio"
		if master.AI != nil && assetTypeForAI(master.AI.MediaKind) != "audio" {
			return nil, errors.New("master must be audio")
		}
		e := &Edit{Timeline: Timeline{Tracks: []Track{{Type: "audio", Clips: []Clip{*master}}}}}
		if err = validateEdit(e); err != nil {
			return nil, err
		}
		if strings.HasPrefix(master.Asset.Src, "storage:") {
			sid, _ := strconv.ParseInt(strings.TrimPrefix(master.Asset.Src, "storage:"), 10, 64)
			mime, e := outputStorageMIME(ctx.WithProject(row["project_id"].(string)), sid)
			if e != nil {
				return nil, e
			}
			if !strings.HasPrefix(mime, "audio/") {
				return nil, errors.New("shared master must be an audio file")
			}
		}
		if err = validateOutputAssets(ctx.WithProject(row["project_id"].(string)), e); err != nil {
			return nil, err
		}
	}
	res, err := ctx.AppDB().Exec(`UPDATE composition_shared_inputs SET master_json=?,revision=revision+1 WHERE composition_id=? AND revision=?`, outputJSON(master), row["id"], int64Arg(args, "expected_revision", 0))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, errors.New("shared input revision conflict")
	}
	return a.toolOutputList(ctx, args)
}
func (a *App) toolOutputHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	row, err := outputComposition(ctx, args)
	if err != nil {
		return nil, err
	}
	o, err := loadOutput(ctx, row["id"].(int64), strArg(args, "kind", ""))
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id FROM renders WHERE output_id=? ORDER BY id DESC LIMIT 100`, o.ID)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, id := range ids {
		r, err := readOutputRender(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return map[string]any{"renders": out}, nil
}

func (a *App) toolOutputRender(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	row, err := outputComposition(ctx, args)
	if err != nil {
		return nil, err
	}
	if err = ensureOutputs(ctx, row); err != nil {
		return nil, err
	}
	id := row["id"].(int64)
	pid := row["project_id"].(string)
	ctx = ctx.WithProject(pid)
	o, err := loadOutput(ctx, id, strArg(args, "kind", ""))
	if err != nil {
		return nil, err
	}
	key := strArg(args, "idempotency_key", "")
	if key == "" || len(key) > 200 {
		return nil, errors.New("idempotency_key required (max 200 characters)")
	}
	expected := int64Arg(args, "expected_revision", 0)
	var rid, previousRevision int64
	err = ctx.AppDB().QueryRow(`SELECT id,output_revision FROM renders WHERE output_id=? AND idempotency_key=?`, o.ID, key).Scan(&rid, &previousRevision)
	if err == nil {
		if expected != previousRevision {
			return nil, errors.New("idempotency key belongs to another revision")
		}
		// Only a waiting attempt may resume. Terminal attempts remain immutable.
		return a.runOutputAttempt(ctx, rid)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if expected != o.Revision {
		return nil, errors.New("output revision conflict; refresh before rendering")
	}
	master, _, err := loadSharedMaster(ctx, id)
	if err != nil {
		return nil, err
	}
	snap, planErr := buildOutputSnapshot(row, o.Kind, o.Settings, string(o.Plan), master)
	snap.Executor = strArg(args, "executor", "")
	inputSnap := snap
	inputSnap.Executor = ""
	hash := outputHash(inputSnap)
	// INSERT SELECT makes revision validation and attempt creation atomic.
	res, err := ctx.AppDB().Exec(`INSERT INTO renders(composition_id,project_id,executor,status,edit_snapshot,output_id,output_revision,input_revision,output_snapshot,idempotency_key)
 SELECT ?,?,'output','queued',?,?,?,?,?,? FROM composition_outputs WHERE id=? AND revision=?
 ON CONFLICT(output_id,idempotency_key) WHERE output_id IS NOT NULL DO NOTHING`, id, pid, outputJSON(snap), o.ID, o.Revision, hash, outputJSON(snap), key, o.ID, expected)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if err = ctx.AppDB().QueryRow(`SELECT id FROM renders WHERE output_id=? AND idempotency_key=?`, o.ID, key).Scan(&rid); err != nil {
			return nil, errors.New("output revision conflict")
		}
	} else {
		rid, _ = res.LastInsertId()
	}
	if planErr != nil {
		return failOutputAttempt(ctx, rid, planErr)
	}
	return a.runOutputAttempt(ctx, rid)
}
func failOutputAttempt(ctx *sdk.AppCtx, id int64, cause error) (any, error) {
	_, err := ctx.AppDB().Exec(`UPDATE renders SET status='failed',error=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status!='cancelled'`, redactSecrets(cause.Error()), id)
	if err != nil {
		return nil, err
	}
	return readOutputRender(ctx, id)
}
func (a *App) runOutputAttempt(ctx *sdk.AppCtx, id int64) (any, error) {
	// Durable compare-and-swap prevents concurrent HTTP/MCP callers rendering twice.
	claim, err := ctx.AppDB().Exec(`UPDATE renders SET status='preparing',updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('queued','waiting_ai')`, id)
	if err != nil {
		return nil, err
	}
	n, _ := claim.RowsAffected()
	if n == 0 {
		return readOutputRender(ctx, id)
	}
	var raw, pid string
	var cid int64
	if err = ctx.AppDB().QueryRow(`SELECT output_snapshot,project_id,composition_id FROM renders WHERE id=?`, id).Scan(&raw, &pid, &cid); err != nil {
		return nil, err
	}
	var snap outputSnapshot
	if err = json.Unmarshal([]byte(raw), &snap); err != nil {
		return failOutputAttempt(ctx, id, err)
	}
	if snap.Edit == nil && snap.Spec == nil {
		return failOutputAttempt(ctx, id, errors.New("output has no effective timeline"))
	}
	pending := []string{}
	if snap.Edit != nil {
		if err = validateOutputAssets(ctx, snap.Edit); err != nil {
			return failOutputAttempt(ctx, id, err)
		}
		pending, err = prepareOutputAssets(ctx, snap.Edit, cid, id)
		// Persist partial successes even if a later shot fails.
		if _, saveErr := ctx.AppDB().Exec(`UPDATE renders SET edit_snapshot=?,pending_json=? WHERE id=?`, outputJSON(snap), outputJSON(pending), id); saveErr != nil {
			return nil, saveErr
		}
		if err != nil {
			return failOutputAttempt(ctx, id, err)
		}
		if len(pending) > 0 {
			_, err = ctx.AppDB().Exec(`UPDATE renders SET status='waiting_ai' WHERE id=? AND status='preparing'`, id)
			if err != nil {
				return nil, err
			}
			return readOutputRender(ctx, id)
		}
	}
	rctx, cancel := context.WithTimeout(composerLifetimeContext(), 30*time.Minute)
	defer cancel()
	unregister := registerRenderCancel(id, cancel)
	defer unregister()
	claim, err = ctx.AppDB().Exec(`UPDATE renders SET status='rendering',updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='preparing'`, id)
	if err != nil {
		return nil, err
	}
	if n, _ := claim.RowsAffected(); n == 0 {
		return readOutputRender(ctx, id)
	}
	rctx = withJobRenderProgress(rctx, ctx, id, cid, pid, "output")
	var result Result
	if a.outputRenderer != nil {
		result, err = a.outputRenderer(rctx, ctx, snap, pid)
	} else if snap.Spec != nil {
		if snap.Edit != nil {
			for _, t := range snap.Edit.Timeline.Tracks {
				for _, c := range t.Clips {
					assetID := fmt.Sprintf("output-audio-%d", len(snap.Spec.Audio))
					snap.Spec.Assets = append(snap.Spec.Assets, V2Asset{ID: assetID, Type: "audio", Src: c.Asset.Src})
					snap.Spec.Audio = append(snap.Spec.Audio, V2Audio{ID: c.UID, Asset: assetID, Start: c.Start, Duration: clipDuration(c), Volume: c.Volume, SourceStart: c.SourceStart, SourceEnd: c.SourceEnd, PlaybackRate: c.PlaybackRate})
				}
			}
		}
		if strings.EqualFold(snap.Spec.Output.Renderer, "browser") {
			result, _, err = renderV2BrowserWindow(rctx, ctx, snap.Spec, pid, snap.Settings.ExcerptStart, snap.Settings.ExcerptEnd)
		} else {
			result, _, err = renderV2NativeWindow(rctx, ctx, snap.Spec, pid, snap.Settings.ExcerptStart, snap.Settings.ExcerptEnd)
		}
	} else {
		var ex Executor
		ex, err = chooseExecutor(ctx, snap.Executor)
		if err == nil {
			result, err = ex.Render(rctx, ctx, snap.Edit, snap.Settings.Output, pid)
		}
	}
	if result.Cleanup != nil {
		defer func() {
			if result.Cleanup != nil {
				result.Cleanup()
			}
		}()
	}
	if err != nil {
		return failOutputAttempt(ctx, id, err)
	}
	if !result.Sync && snap.Spec == nil {
		return failOutputAttempt(ctx, id, errors.New("asynchronous render executor is not supported for saved outputs"))
	}
	if err = rctx.Err(); err != nil {
		return failOutputAttempt(ctx, id, err)
	}
	var sid int64
	if strings.HasPrefix(result.LocalPath, "storage://files/") {
		sid, _ = strconv.ParseInt(strings.TrimPrefix(result.LocalPath, "storage://files/"), 10, 64)
	} else if result.LocalPath != "" {
		sid = saveRenderOutputContext(rctx, ctx, result.LocalPath, snap.Settings.Format, pid, cid)
		if sid == 0 {
			if err = writeLocalCacheFromPath(id, result.LocalPath, snap.Settings.Format); err != nil {
				result.Cleanup = nil
				return failOutputAttempt(ctx, id, err)
			}
		}
	} else {
		return failOutputAttempt(ctx, id, errors.New("renderer returned no artifact"))
	}
	duration := 0.0
	if snap.Spec != nil {
		duration = v2DurationSeconds(snap.Spec)
		if snap.Settings.ExcerptEnd > 0 {
			duration = snap.Settings.ExcerptEnd
		}
		duration -= snap.Settings.ExcerptStart
	} else {
		duration = editDurationSeconds(snap.Edit)
	}
	claim, err = ctx.AppDB().Exec(`UPDATE renders SET status='complete',storage_id=?,duration_ms=?,cost_usd=?,ffmpeg_command=?,error='',pending_json='[]',phase='complete',progress_pct=100,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='rendering'`, sid, int64(duration*1000), result.CostUSD, redactSecrets(result.FFmpegCommand), id)
	if err != nil {
		return nil, err
	}
	if n, _ := claim.RowsAffected(); n == 0 {
		return readOutputRender(ctx, id)
	}
	ctx.EmitWithProject("composition.output.rendered", pid, map[string]any{"composition_id": cid, "render_id": id})
	return readOutputRender(ctx, id)
}

func (a *App) handleOutputs(w http.ResponseWriter, r *http.Request, id int64, tail []string) {
	args := map[string]any{"id": id, "project_id": r.URL.Query().Get("project_id")}
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&args); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		args["id"] = id
		args["project_id"] = r.URL.Query().Get("project_id")
	}
	var fn func(*sdk.AppCtx, map[string]any) (any, error)
	if len(tail) == 0 && r.Method == http.MethodGet {
		fn = a.toolOutputList
	}
	if len(tail) == 1 && tail[0] == "master" && r.Method == http.MethodPatch {
		fn = a.toolOutputMaster
	}
	if len(tail) > 0 && validOutputKind(tail[0]) {
		args["kind"] = tail[0]
		if len(tail) == 1 && r.Method == http.MethodPatch {
			fn = a.toolOutputUpdate
		}
		if len(tail) == 2 {
			if tail[1] == "estimate" && r.Method == http.MethodPost {
				fn = a.toolOutputEstimate
			}
			if tail[1] == "adopt" && r.Method == http.MethodPost {
				fn = a.toolOutputAdopt
			}
			if tail[1] == "render" && r.Method == http.MethodPost {
				fn = a.toolOutputRender
			}
			if tail[1] == "renders" && r.Method == http.MethodGet {
				fn = a.toolOutputHistory
			}
		}
	}
	if fn == nil {
		http.Error(w, "unsupported output route/method", 405)
		return
	}
	out, err := fn(requestAppCtx(r), args)
	if err != nil {
		code := 400
		if strings.Contains(err.Error(), "conflict") {
			code = 409
		}
		http.Error(w, err.Error(), code)
		return
	}
	jsonResp(w, out)
}

// The sidecar owns one database. On restart, retain completed artifacts and
// surface interrupted work; a provider submission with no durable job id is
// deliberately not replayed because its billing outcome is unknown.
func recoverOutputAttempts(ctx *sdk.AppCtx) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE renders SET status='failed',error='Export interrupted by sidecar restart. Start a new attempt to reuse completed assets.',updated_at=CURRENT_TIMESTAMP WHERE output_id IS NOT NULL AND status IN ('preparing','rendering')`); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE output_asset_jobs SET state='blocked',error='Submission interrupted before its result was saved; reconcile with the provider before explicitly regenerating.' WHERE state='submitting'`); err != nil {
		return err
	}
	return tx.Commit()
}
