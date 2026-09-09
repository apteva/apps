package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type outputPlatform struct {
	tk.BasePlatformClient
	mu          sync.Mutex
	calls       map[string]int
	queued      bool
	jobComplete bool
	failPrompt  string
	denied      bool
	onGenerate  func()
}

func (p *outputPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result any
	switch tool {
	case "files_get":
		if p.denied {
			return errors.New("asset not accessible in project")
		}
		result = map[string]any{"found": true, "file": map[string]any{"content_type": "audio/mpeg"}}
	case "files_get_url":
		if p.denied {
			return errors.New("asset not accessible in project")
		}
		result = map[string]any{"url": "https://assets.test/file"}
	case "media_generate":
		if p.onGenerate != nil {
			p.onGenerate()
		}
		kind := args["kind"].(string)
		p.calls[kind]++
		if args["prompt"] == p.failPrompt {
			return errors.New("provider balance exhausted")
		}
		if p.queued && kind == "music" {
			result = map[string]any{"_meta": map[string]any{"status": "queued", "job_id": 12}}
		} else {
			result = map[string]any{"_meta": map[string]any{"status": "complete", "storage_ids": []int64{100 + int64(len(p.calls))}, "generation_id": 55}}
		}
	case "media_job_get":
		status := "queued"
		if p.jobComplete {
			status = "complete"
		}
		result = map[string]any{"status": status, "result_storage_id": 101, "generation_id": 55, "cost_usd": 0.2}
	default:
		return fmt.Errorf("unexpected %s/%s", app, tool)
	}
	return json.Unmarshal([]byte(outputJSON(result)), out)
}
func outputFixture(t *testing.T) (*App, *sdk.AppCtx, *outputPlatform, int64) {
	t.Helper()
	p := &outputPlatform{calls: map[string]int{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p)).WithProject("project-a")
	a := &App{outputRenderer: func(_ context.Context, _ *sdk.AppCtx, s outputSnapshot, _ string) (Result, error) {
		return Result{Sync: true, LocalPath: "storage://files/900"}, nil
	}}
	edit := Edit{Timeline: Timeline{Tracks: []Track{
		{Type: "visual", Clips: []Clip{{UID: "scene-1", Asset: Asset{Type: "video"}, Length: 30, AI: &AIAsset{MediaKind: "video", Prompt: "city", Duration: 30}}}},
		{Type: "audio", Clips: []Clip{{UID: "song", Asset: Asset{Type: "audio"}, Length: 30, AI: &AIAsset{MediaKind: "music", Prompt: "song", Duration: 30}}}},
	}}}
	res, err := ctx.AppDB().Exec(`INSERT INTO compositions(project_id,name,edit_json,output_json,duration_seconds) VALUES('project-a','Song',?,'{"format":"mp4","aspect":"9:16","resolution":"sd","fps":24}',30)`, outputJSON(edit))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err = a.toolOutputList(ctx, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	return a, ctx, p, id
}
func renderOutput(t *testing.T, a *App, ctx *sdk.AppCtx, id int64, kind, key string) map[string]any {
	t.Helper()
	o, err := loadOutput(ctx, id, kind)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.toolOutputRender(ctx, map[string]any{"id": id, "kind": kind, "expected_revision": o.Revision, "idempotency_key": key})
	if err != nil {
		t.Fatal(err)
	}
	return got.(map[string]any)
}
func setImagePlan(t *testing.T, a *App, ctx *sdk.AppCtx, id int64) {
	t.Helper()
	o, _ := loadOutput(ctx, id, "image_video")
	plan := Edit{Timeline: Timeline{Tracks: []Track{{Type: "visual", Clips: []Clip{{UID: "still-1", Asset: Asset{Type: "image"}, Length: 30, AI: &AIAsset{MediaKind: "image", Prompt: "city still"}}}}}}}
	if _, err := a.toolOutputUpdate(ctx, map[string]any{"id": id, "kind": "image_video", "expected_revision": o.Revision, "plan": plan}); err != nil {
		t.Fatal(err)
	}
}
func TestOutputsSelectiveGenerationAndSharedReuse(t *testing.T) {
	a, ctx, p, id := outputFixture(t)
	setImagePlan(t, a, ctx, id)
	for _, kind := range []string{"song", "image_video", "full_clip"} {
		r := renderOutput(t, a, ctx, id, kind, kind)
		if r["status"] != "complete" {
			t.Fatalf("%s: %+v", kind, r)
		}
		if kind == "song" && (p.calls["image"] != 0 || p.calls["video"] != 0) {
			t.Fatal("audio export generated visuals")
		}
		if kind == "image_video" && p.calls["video"] != 0 {
			t.Fatal("image export generated videos")
		}
	}
	if p.calls["music"] != 1 || p.calls["image"] != 1 || p.calls["video"] != 1 {
		t.Fatalf("unexpected provider calls: %v", p.calls)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM compositions`).Scan(&count)
	if count != 1 {
		t.Fatal("created duplicate parent")
	}
	row, _ := loadComposition(ctx, id)
	if strings.Contains(row["edit_json"].(string), "storage:101") {
		t.Fatal("mutated canonical source")
	}
}
func TestOutputsConcurrentSharedSongAndIdempotentResume(t *testing.T) {
	a, ctx, p, id := outputFixture(t)
	p.queued = true
	setImagePlan(t, a, ctx, id)
	// Separate outputs race on the same ungenerated master.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for _, kind := range []string{"song", "image_video"} {
		wg.Add(1)
		go func(kind string) {
			defer wg.Done()
			o, _ := loadOutput(ctx, id, kind)
			_, err := a.toolOutputRender(ctx, map[string]any{"id": id, "kind": kind, "expected_revision": o.Revision, "idempotency_key": kind})
			errs <- err
		}(kind)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if p.calls["music"] != 1 {
		t.Fatalf("duplicate song request: %v", p.calls)
	}
	first := renderOutput(t, a, ctx, id, "song", "song")
	if first["status"] != "waiting_ai" {
		t.Fatalf("%+v", first)
	}
	p.mu.Lock()
	p.jobComplete = true
	p.mu.Unlock()
	last := renderOutput(t, a, ctx, id, "song", "song")
	again := renderOutput(t, a, ctx, id, "song", "song")
	if last["status"] != "complete" || again["id"] != first["id"] || last["id"] != first["id"] {
		t.Fatalf("attempt not resumed: %+v %+v", first, last)
	}
	if p.calls["music"] != 1 {
		t.Fatal("polling resubmitted generation")
	}
}
func TestOutputsFailureIsolationAndSuccessfulPreview(t *testing.T) {
	a, ctx, p, id := outputFixture(t)
	setImagePlan(t, a, ctx, id)
	song := renderOutput(t, a, ctx, id, "song", "song")
	image := renderOutput(t, a, ctx, id, "image_video", "image")
	p.failPrompt = "city"
	failed := renderOutput(t, a, ctx, id, "full_clip", "bad")
	if failed["status"] != "failed" {
		t.Fatal(failed)
	}
	got, err := a.toolOutputList(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	outputs := got.(map[string]any)["outputs"].([]SavedOutput)
	if outputs[0].LatestSuccessfulRender["id"] != song["id"] || outputs[1].LatestSuccessfulRender["id"] != image["id"] {
		t.Fatal("failed clip invalidated sibling previews")
	}
	// A later rendering failure preserves the same output's successful artifact.
	a.outputRenderer = func(context.Context, *sdk.AppCtx, outputSnapshot, string) (Result, error) {
		return Result{}, errors.New("encoder failed")
	}
	renderOutput(t, a, ctx, id, "song", "bad-song")
	o, _ := loadOutput(ctx, id, "song")
	success, _ := outputRenderRow(ctx, o.ID, true)
	if success["id"] != song["id"] {
		t.Fatal("regeneration hid successful preview")
	}
}
func TestOutputsPartialRetryReusesCompletedShot(t *testing.T) {
	a, ctx, p, id := outputFixture(t)
	p.failPrompt = "second"
	row, _ := loadComposition(ctx, id)
	var edit Edit
	json.Unmarshal([]byte(row["edit_json"].(string)), &edit)
	edit.Timeline.Tracks[0].Clips[0].Length = 15
	edit.Timeline.Tracks[0].Clips = append(edit.Timeline.Tracks[0].Clips, Clip{UID: "second", Start: 15, Length: 15, Asset: Asset{Type: "video"}, AI: &AIAsset{MediaKind: "video", Prompt: "second", Duration: 15}})
	ctx.AppDB().Exec(`UPDATE compositions SET edit_json=? WHERE id=?`, outputJSON(edit), id)
	if r := renderOutput(t, a, ctx, id, "full_clip", "first"); r["status"] != "failed" {
		t.Fatal(r)
	}
	if p.calls["video"] != 2 {
		t.Fatal(p.calls)
	}
	// Ordinary retry never repeats a paid failure or successful shot.
	renderOutput(t, a, ctx, id, "full_clip", "retry")
	if p.calls["video"] != 2 {
		t.Fatal("paid generation automatically retried")
	}
	// Explicitly regenerate only the failed shot by selecting its new version.
	p.failPrompt = ""
	edit.Timeline.Tracks[0].Clips[1].AI.CacheKey = "retry-second-v2"
	ctx.AppDB().Exec(`UPDATE compositions SET edit_json=? WHERE id=?`, outputJSON(edit), id)
	if r := renderOutput(t, a, ctx, id, "full_clip", "retry-explicit"); r["status"] != "complete" {
		t.Fatal(r)
	}
	if p.calls["video"] != 3 {
		t.Fatalf("successful shot regenerated: %v", p.calls)
	}
}
func TestOutputsExcerptPreservesMasterAndSourceOffset(t *testing.T) {
	a, ctx, _, id := outputFixture(t)
	row, _ := loadComposition(ctx, id)
	master := &Clip{Asset: Asset{Type: "audio", Src: "storage:30"}, Length: 30}
	s := OutputSettings{Output: Output{Format: "mp4"}, ExcerptStart: 5, ExcerptEnd: 15}
	snap, err := buildOutputSnapshot(row, "full_clip", s, "{}", master)
	if err != nil {
		t.Fatal(err)
	}
	audio := snap.Edit.Timeline.Tracks[0].Clips[0]
	if audio.Length != 10 || audio.SourceStart != 5 || master.Length != 30 || master.SourceStart != 0 {
		t.Fatalf("bad excerpt %+v master %+v", audio, master)
	}
	_ = a
	var filter strings.Builder
	writeTimedAudioFilter(&filter, 0, audio, 0, "out")
	if !strings.Contains(filter.String(), "atrim=start=5") {
		t.Fatal(filter.String())
	}
}
func TestOutputsStalenessTracksRelevantInputs(t *testing.T) {
	a, ctx, _, id := outputFixture(t)
	renderOutput(t, a, ctx, id, "song", "song")
	row, _ := loadComposition(ctx, id)
	var edit Edit
	json.Unmarshal([]byte(row["edit_json"].(string)), &edit)
	edit.Timeline.Tracks[0].Clips[0].AI.Prompt = "new visuals"
	ctx.AppDB().Exec(`UPDATE compositions SET edit_json=? WHERE id=?`, outputJSON(edit), id)
	got, _ := a.toolOutputList(ctx, map[string]any{"id": id})
	if got.(map[string]any)["outputs"].([]SavedOutput)[0].OutOfDate {
		t.Fatal("visual change invalidated audio")
	}
	master := Clip{Asset: Asset{Type: "audio", Src: "storage:99"}, Length: 30}
	got, err := a.toolOutputMaster(ctx, map[string]any{"id": id, "expected_revision": int64(1), "master": master})
	if err != nil {
		t.Fatal(err)
	}
	song := got.(map[string]any)["outputs"].([]SavedOutput)[0]
	if !song.OutOfDate || song.LatestSuccessfulRender == nil {
		t.Fatal("changed master must preserve stale preview")
	}
}
func TestOutputsOwnershipRevisionAndAssetAccess(t *testing.T) {
	a, ctx, p, id := outputFixture(t)
	if _, err := a.toolOutputList(ctx.WithProject("other"), map[string]any{"id": id}); err == nil {
		t.Fatal("cross-project read allowed")
	}
	if _, err := a.toolOutputList(ctx, map[string]any{"id": id, "project_id": "other"}); err == nil {
		t.Fatal("project override allowed")
	}
	if _, err := a.toolOutputUpdate(ctx, map[string]any{"id": id, "kind": "song", "expected_revision": 0}); err == nil {
		t.Fatal("stale write allowed")
	}
	if _, err := a.toolOutputRender(ctx, map[string]any{"id": id, "kind": "song", "expected_revision": 0, "idempotency_key": "stale"}); err == nil {
		t.Fatal("stale render allowed")
	}
	p.denied = true
	if _, err := a.toolOutputMaster(ctx, map[string]any{"id": id, "expected_revision": 1, "master": Clip{Asset: Asset{Type: "audio", Src: "storage:99"}, Length: 30}}); err == nil {
		t.Fatal("unauthorized master accepted")
	}
	if len(p.calls) > 0 {
		t.Fatal("validation invoked generation")
	}
}
func TestOutputsImageVideoRejectsMovingPlanBeforeGeneration(t *testing.T) {
	a, ctx, p, id := outputFixture(t)
	r := renderOutput(t, a, ctx, id, "image_video", "invalid")
	if r["status"] != "failed" {
		t.Fatal(r)
	}
	if len(p.calls) > 0 {
		t.Fatal("invalid plan generated assets")
	}
	o, _ := loadOutput(ctx, id, "image_video")
	latest, _ := outputRenderRow(ctx, o.ID, false)
	if latest == nil {
		t.Fatal("validation failure missing attempt")
	}
}
func TestOutputsConcurrentSameRequestExecutesOnce(t *testing.T) {
	a, ctx, _, id := outputFixture(t)
	start, finish := make(chan struct{}), make(chan struct{})
	a.outputRenderer = func(context.Context, *sdk.AppCtx, outputSnapshot, string) (Result, error) {
		close(start)
		<-finish
		return Result{Sync: true, LocalPath: "storage://files/99"}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.toolOutputRender(ctx, map[string]any{"id": id, "kind": "song", "expected_revision": 1, "idempotency_key": "same"})
		done <- err
	}()
	<-start
	got := renderOutput(t, a, ctx, id, "song", "same")
	if got["status"] != "rendering" {
		t.Fatal(got)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM renders`).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate attempts")
	}
}
func TestOutputsHTTPRoutesAndHistory(t *testing.T) {
	a, ctx, _, id := outputFixture(t)
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	renderOutput(t, a, ctx, id, "song", "song")
	for _, path := range []string{fmt.Sprintf("/composition/%d/outputs?project_id=project-a", id), fmt.Sprintf("/composition/%d/outputs/song/renders?project_id=project-a", id)} {
		w := httptest.NewRecorder()
		a.handleCompositionByID(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
	}
}
func TestOutputsV2NativeLayoutAndAudioOnlySelection(t *testing.T) {
	spec := V2Composition{Version: composerV2Version, Output: V2Output{Format: "mp4", Duration: 30}, Scenes: []V2Scene{{ID: "scene", Duration: 30, Elements: []V2Element{{ID: "title", Type: "text", Text: "Hello", X: "20%", Enter: map[string]any{"preset": "typewriter"}}}}}, Assets: []V2Asset{{ID: "audio-master", Type: "audio", Src: "storage:1"}}, Audio: []V2Audio{{ID: "song", Asset: "audio-master", Duration: 30}}}
	row := map[string]any{"edit_json": outputJSON(spec), "output_json": "{}"}
	native, err := buildOutputSnapshot(row, "image_video", OutputSettings{Output: Output{Format: "mp4"}, ExcerptStart: 5, ExcerptEnd: 15}, "{}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if native.Spec == nil || native.Spec.Scenes[0].Elements[0].Enter["preset"] != "typewriter" || native.Edit.Timeline.Tracks[0].Clips[0].SourceStart != 5 {
		t.Fatalf("lost native layout/offset: %+v", native)
	}
	audio, err := buildOutputSnapshot(row, "song", OutputSettings{Output: Output{Format: "mp3"}}, "{}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if audio.Spec != nil || len(audio.Edit.Timeline.Tracks) != 1 || trackKind(audio.Edit.Timeline.Tracks[0]) != "audio" {
		t.Fatal("audio retained visual scene")
	}
}
func TestOutputsInterruptedSubmissionIsNotRepeated(t *testing.T) {
	_, ctx, p, id := outputFixture(t)
	ai := &AIAsset{MediaKind: "music", Prompt: "song", Duration: 30}
	key := outputHash([]string{aiCacheKey(ai), ""})
	_, err := ctx.AppDB().Exec(`INSERT INTO output_asset_jobs(project_id,composition_id,cache_key,state,asset_json) VALUES('project-a',?,?,'submitting',?)`, id, key, outputJSON(ai))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := prepareOutputAsset(ctx, ai, id, ttsContinuityPlan{})
	if err != nil || pending == "" {
		t.Fatalf("%q %v", pending, err)
	}
	if p.calls["music"] != 0 {
		t.Fatal("ambiguous submission repeated")
	}
}

func TestOutputsMigrationPreservesLegacyData(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, name := range []string{"001_init.sql", "002_render_qa.sql", "003_render_progress.sql", "004_composition_revision.sql"} {
		b, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(b)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO compositions(id,project_id,name,edit_json) VALUES(72,'a','Cobalt Pulse','{"original":true}'); INSERT INTO renders(id,composition_id,project_id,executor,status,storage_id,edit_snapshot) VALUES(9,72,'a','local','complete',83764,'{"snapshot":true}')`)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("migrations/005_outputs.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
	var edit, snapshot string
	var sid int64
	var outputID sql.NullInt64
	if err = db.QueryRow(`SELECT c.edit_json,r.edit_snapshot,r.storage_id,r.output_id FROM compositions c JOIN renders r ON r.composition_id=c.id WHERE c.id=72`).Scan(&edit, &snapshot, &sid, &outputID); err != nil {
		t.Fatal(err)
	}
	if edit != `{"original":true}` || snapshot != `{"snapshot":true}` || sid != 83764 || outputID.Valid {
		t.Fatal("migration rewrote historical data")
	}
}
func TestOutputsAdoptionPreservesLegacyPreviewAndHistory(t *testing.T) {
	a, ctx, _, id := outputFixture(t)
	res, err := ctx.AppDB().Exec(`INSERT INTO renders(composition_id,project_id,executor,status,storage_id,duration_ms,edit_snapshot) VALUES(?,'project-a','local','complete',77,30000,'{"original":true}')`, id)
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := res.LastInsertId()
	args := map[string]any{"id": id, "kind": "song", "render_id": rid, "expected_revision": 1, "idempotency_key": "adopt"}
	got, err := a.toolOutputAdopt(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	r := got.(map[string]any)
	if r["storage_id"] != int64(77) || r["id"] == rid {
		t.Fatal(r)
	}
	again, err := a.toolOutputAdopt(ctx, args)
	if err != nil || again.(map[string]any)["id"] != r["id"] {
		t.Fatalf("adoption not idempotent: %v %v", again, err)
	}
	renderOutput(t, a, ctx, id, "song", "new")
	legacy := loadLatestRender(ctx, id)
	if legacy["id"] != rid {
		t.Fatalf("named outputs changed legacy preview: %+v", legacy)
	}
	var original string
	ctx.AppDB().QueryRow(`SELECT edit_snapshot FROM renders WHERE id=?`, rid).Scan(&original)
	if original != `{"original":true}` {
		t.Fatal("original history changed")
	}
}
func TestOutputsRestartRecoveryAndCostAccounting(t *testing.T) {
	a, ctx, p, id := outputFixture(t)
	p.queued = true
	renderOutput(t, a, ctx, id, "song", "song")
	p.jobComplete = true
	renderOutput(t, a, ctx, id, "song", "song")
	costs, err := outputCosts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if costs["shared_audio_usd"] != 0.2 {
		t.Fatal(costs)
	}
	renderOutput(t, a, ctx, id, "full_clip", "video")
	costs, _ = outputCosts(ctx, id)
	if costs["shared_audio_usd"] != 0.2 || costs["visual_generation_usd"] != nil {
		t.Fatalf("shared charge repeated or unknown reported as zero: %+v", costs)
	}
	ctx.AppDB().Exec(`UPDATE renders SET status='preparing' WHERE id=(SELECT MAX(id) FROM renders)`)
	if err = recoverOutputAttempts(ctx); err != nil {
		t.Fatal(err)
	}
	o, _ := loadOutput(ctx, id, "full_clip")
	last, _ := outputRenderRow(ctx, o.ID, false)
	if last["status"] != "failed" {
		t.Fatal("interrupted attempt stuck busy")
	}
	song, _ := loadOutput(ctx, id, "song")
	last, _ = outputRenderRow(ctx, song.ID, true)
	if last == nil {
		t.Fatal("recovery lost completed sibling")
	}
}
func TestOutputsEstimateDoesNotGenerate(t *testing.T) {
	a, ctx, p, id := outputFixture(t)
	got, err := a.toolOutputEstimate(ctx, map[string]any{"id": id, "kind": "song"})
	if err != nil {
		t.Fatal(err)
	}
	estimate := got.(map[string]any)
	if estimate["additional_generation_cost_usd"] != nil || estimate["missing_dependencies"].(map[string]int)["music"] != 1 {
		t.Fatal(estimate)
	}
	if len(p.calls) != 0 {
		t.Fatal("estimate generated assets")
	}
	renderOutput(t, a, ctx, id, "song", "song")
	got, err = a.toolOutputEstimate(ctx, map[string]any{"id": id, "kind": "song"})
	if err != nil || got.(map[string]any)["additional_generation_cost_usd"] != 0.0 {
		t.Fatalf("shared dependency not reused: %v %v", got, err)
	}
}
func TestOutputsRealAudioExcerpt(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	a, ctx, _, id := outputFixture(t)
	a.outputRenderer = nil
	dir := t.TempDir()
	t.Setenv("DB_PATH", filepath.Join(dir, "composer.db"))
	source := filepath.Join(dir, "source.wav")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "3", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %s %v", b, err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer server.Close()
	edit := Edit{Timeline: Timeline{Tracks: []Track{{Type: "audio", Clips: []Clip{{Asset: Asset{Type: "audio", Src: server.URL + "/source.wav"}, Length: 3}}}}}}
	ctx.AppDB().Exec(`UPDATE compositions SET edit_json=? WHERE id=?`, outputJSON(edit), id)
	o, _ := loadOutput(ctx, id, "song")
	_, err := a.toolOutputUpdate(ctx, map[string]any{"id": id, "kind": "song", "expected_revision": o.Revision, "settings": map[string]any{"format": "wav", "excerpt_start": 1, "excerpt_end": 2}})
	if err != nil {
		t.Fatal(err)
	}
	got := renderOutput(t, a, ctx, id, "song", "real")
	if got["status"] != "complete" {
		t.Fatal(got)
	}
	path, ok := localCachePath(got["id"].(int64))
	if !ok {
		t.Fatal("missing rendered audio")
	}
	duration := probeRenderDuration(path)
	if math.Abs(duration-1) > 0.05 {
		t.Fatalf("excerpt duration %f, want 1", duration)
	}
	if d := probeRenderDuration(source); math.Abs(d-3) > 0.05 {
		t.Fatalf("source master shortened: %f", d)
	}
	// Run a native V2 video excerpt through the same output API and encoder.
	spec := V2Composition{Version: composerV2Version, Output: V2Output{Format: "mp4", Duration: 3}, Assets: []V2Asset{{ID: "master", Type: "audio", Src: server.URL + "/source.wav"}}, Audio: []V2Audio{{Asset: "master", Duration: 3}}, Scenes: []V2Scene{{Duration: 3, Elements: []V2Element{{Type: "shape", X: 0, Y: 0, Width: "100%", Height: "100%", Style: map[string]any{"fill": "#123456"}}}}}}
	if _, err = ctx.AppDB().Exec(`UPDATE compositions SET edit_json=? WHERE id=?`, outputJSON(spec), id); err != nil {
		t.Fatal(err)
	}
	o, _ = loadOutput(ctx, id, "image_video")
	if _, err = a.toolOutputUpdate(ctx, map[string]any{"id": id, "kind": "image_video", "expected_revision": o.Revision, "settings": map[string]any{"format": "mp4", "resolution": "sd", "fps": 24, "excerpt_start": 1, "excerpt_end": 2}}); err != nil {
		t.Fatal(err)
	}
	video := renderOutput(t, a, ctx, id, "image_video", "real-native")
	if video["status"] != "complete" {
		t.Fatal(video)
	}
	videoPath, ok := localCachePath(video["id"].(int64))
	if !ok {
		t.Fatal("missing native output")
	}
	if d := probeRenderDuration(videoPath); math.Abs(d-1) > 0.1 {
		t.Fatalf("native excerpt duration %f", d)
	}
}

func TestOutputsV2CanSelectMasterWithoutOriginalAudio(t *testing.T) {
	spec := V2Composition{Version: composerV2Version, Scenes: []V2Scene{{Duration: 3, Elements: []V2Element{{Type: "shape"}}}}, Output: V2Output{Format: "mp4", Duration: 3}}
	row := map[string]any{"edit_json": outputJSON(spec)}
	master := &Clip{Asset: Asset{Type: "audio"}, Length: 30, AI: &AIAsset{MediaKind: "music", Prompt: "song", Duration: 30}}
	snap, err := buildOutputSnapshot(row, "song", OutputSettings{Output: Output{Format: "mp3"}}, "{}", master)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Edit.Timeline.Tracks[0].Clips[0].Length != 30 {
		t.Fatal("new master shortened to old visuals")
	}
}
func TestOutputsExistingPendingAndSelectedAssetsAreReused(t *testing.T) {
	_, ctx, p, id := outputFixture(t)
	p.queued = true
	ai := &AIAsset{MediaKind: "music", Prompt: "song", JobID: 12, Status: "generating"}
	if pending, err := prepareOutputAsset(ctx, ai, id, ttsContinuityPlan{}); err != nil || pending == "" {
		t.Fatalf("%q %v", pending, err)
	}
	if p.calls["music"] != 0 {
		t.Fatal("existing pending job was resubmitted")
	}
	edit := &Edit{Timeline: Timeline{Tracks: []Track{{Type: "audio", Clips: []Clip{{Asset: Asset{Type: "audio", Src: "storage:77"}, Length: 30, AI: &AIAsset{MediaKind: "music", Prompt: "old draft"}}}}}}}
	if _, err := prepareOutputAssets(ctx, edit, id, 0); err != nil {
		t.Fatal(err)
	}
	if p.calls["music"] != 0 {
		t.Fatal("selected audio regenerated")
	}
}

func TestOutputsCancellationDuringPreparation(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint(queued), func(t *testing.T) {
			a, ctx, p, id := outputFixture(t)
			p.queued = queued
			p.onGenerate = func() {
				var rid int64
				if err := ctx.AppDB().QueryRow(`SELECT MAX(id) FROM renders`).Scan(&rid); err != nil {
					t.Fatal(err)
				}
				if _, err := cancelQueuedRender(ctx, rid, "project-a"); err != nil {
					t.Fatal(err)
				}
			}
			a.outputRenderer = func(context.Context, *sdk.AppCtx, outputSnapshot, string) (Result, error) {
				t.Error("cancelled preparation reached renderer")
				return Result{}, nil
			}
			got := renderOutput(t, a, ctx, id, "song", "cancel-preparing")
			if got["status"] != "cancelled" {
				t.Fatal(got)
			}
			if again := renderOutput(t, a, ctx, id, "song", "cancel-preparing"); again["status"] != "cancelled" {
				t.Fatal(again)
			}
		})
	}
}

func TestOutputsCancellationDuringRenderPreservesTerminalState(t *testing.T) {
	a, ctx, _, id := outputFixture(t)
	a.outputRenderer = func(rctx context.Context, _ *sdk.AppCtx, _ outputSnapshot, _ string) (Result, error) {
		var rid int64
		if err := ctx.AppDB().QueryRow(`SELECT MAX(id) FROM renders`).Scan(&rid); err != nil {
			t.Fatal(err)
		}
		if _, err := cancelQueuedRender(ctx, rid, "project-a"); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(rctx.Err(), context.Canceled) {
			t.Fatal("renderer context was not cancelled")
		}
		return Result{Sync: true, LocalPath: "storage://files/900"}, nil
	}
	got := renderOutput(t, a, ctx, id, "song", "cancel-render")
	if got["status"] != "cancelled" {
		t.Fatal(got)
	}
	o, _ := loadOutput(ctx, id, "song")
	if success, err := outputRenderRow(ctx, o.ID, true); err != nil || success != nil {
		t.Fatalf("cancelled render became successful: %v %v", success, err)
	}
}

func TestLegacyWorkerDoesNotClaimNamedOutput(t *testing.T) {
	_, ctx, _, id := outputFixture(t)
	o, _ := loadOutput(ctx, id, "song")
	res, err := ctx.AppDB().Exec(`INSERT INTO renders(composition_id,project_id,executor,status,output_id,output_snapshot,edit_snapshot) VALUES(?,'project-a','output','queued',?,'{}','{}')`, id, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := res.LastInsertId()
	processNextQueuedRender(ctx)
	var status string
	if err = ctx.AppDB().QueryRow(`SELECT status FROM renders WHERE id=?`, rid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("legacy worker claimed named output: %s", status)
	}
}

func TestOutputsTTSReuseSurvivesProviderContinuityIDs(t *testing.T) {
	_, ctx, p, id := outputFixture(t)
	makeEdit := func(requestID string) *Edit {
		return &Edit{Timeline: Timeline{Tracks: []Track{{Type: "audio", Clips: []Clip{
			{UID: "first", Asset: Asset{Type: "audio", Src: "storage:88"}, Length: 5, AI: &AIAsset{MediaKind: "audio.tts", Prompt: "First sentence.", ProviderRequestID: requestID}},
			{UID: "second", Asset: Asset{Type: "audio"}, Start: 5, Length: 5, AI: &AIAsset{MediaKind: "audio.tts", Prompt: "Second sentence."}},
		}}}}}
	}
	for _, requestID := range []string{"", "provider-request-1"} {
		if _, err := prepareOutputAssets(ctx, makeEdit(requestID), id, 0); err != nil {
			t.Fatal(err)
		}
	}
	if p.calls["audio.tts"] != 1 {
		t.Fatalf("continuity IDs caused duplicate generation: %v", p.calls)
	}
}
