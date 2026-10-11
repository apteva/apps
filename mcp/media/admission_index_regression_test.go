package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteGenerationClearsRetryPreservesAudioAndCooldown(t *testing.T) {
	app := newTestCtx(t)
	db := app.AppDB()
	probe := sampleVideoProbe()
	if err := upsertMedia(db, testProj, "45", probe, "sha", "/", "source.mov"); err != nil {
		t.Fatal(err)
	}
	_ = upsertDerivation(db, testProj, "45", "thumbnail", 100, 320, 180, 0)
	_ = upsertDerivation(db, testProj, "45", "keyframe", 101, 320, 180, 1000)
	_ = upsertDerivation(db, testProj, "45", "transcript_audio", 102, 0, 0, 0)
	_, _ = db.Exec(`UPDATE media SET derivation_retry_at=datetime('now','-1 day') WHERE file_id='45'`)
	policy := remoteGenerationPolicy{sourceSHA: "sha", expected: []int64{1000, 6000}}
	if err := replaceRemoteDerivations(db, testProj, "45", probe, 103, 0, []remoteKeyframe{{1000, 104}}, 320, 800, 120, policy); err != nil {
		t.Fatal(err)
	}
	var future bool
	var reason string
	_ = db.QueryRow(`SELECT julianday(derivation_retry_at)>julianday('now'),derivation_retry_reason FROM media WHERE file_id='45'`).Scan(&future, &reason)
	if !future || reason == "" {
		t.Fatal("partial retry missing cooldown/reason")
	}
	ds, _ := listDerivations(db, testProj, "45")
	has := map[string]string{}
	for _, d := range ds {
		has[fmt.Sprintf("%s:%d", d.Kind, d.PositionMs)] = d.StorageFileID
	}
	if has["transcript_audio:0"] != "102" || has["thumbnail:0"] != "103" {
		t.Fatalf("lost output: %v", has)
	}
	if got := indexerCandidates(db, testProj, []StorageFile{{ID: 45, SHA256: "sha"}}, 1, true); len(got) != 0 {
		t.Fatal("partial generation ignores cooldown")
	}
	if err := replaceRemoteDerivations(db, testProj, "45", probe, 0, 0, nil, 320, 800, 120, policy); err != nil {
		t.Fatal(err)
	}
	ds, _ = listDerivations(db, testProj, "45")
	for _, d := range ds {
		if d.Kind == "thumbnail" && d.StorageFileID != "103" {
			t.Fatal("partial lost usable thumbnail")
		}
	}
	if err := replaceRemoteDerivations(db, testProj, "45", probe, 103, 0, []remoteKeyframe{{1000, 104}, {6000, 105}}, 320, 800, 120, policy); err != nil {
		t.Fatal(err)
	}
	var cleared bool
	var sha string
	_ = db.QueryRow(`SELECT derivation_retry_at IS NULL AND derivation_retry_reason IS NULL,derivation_source_sha256 FROM media WHERE file_id='45'`).Scan(&cleared, &sha)
	if !cleared || sha != "sha" {
		t.Fatal("successful generation did not atomically clear retry")
	}
	for i := 0; i < 3; i++ {
		if len(indexerCandidates(db, testProj, []StorageFile{{ID: 45, SHA256: "sha"}}, 1, true)) != 0 {
			t.Fatal("successful generation selected again")
		}
	}
	policy.sourceSHA = "different"
	if err := replaceRemoteDerivations(db, testProj, "45", probe, 999, 0, nil, 320, 800, 120, policy); err == nil {
		t.Fatal("source mismatch committed")
	}
}

func TestFourUnitFIFOAndIndependentAdmissionDeadline(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"render_host_id": "35001", "media_work_capacity": "4"}))
	_, release3, e := acquireMediaWork(context.Background(), app, 3)
	if e != nil {
		t.Fatal(e)
	}
	budgetValue, _ := mediaBudgets.Load("35001")
	budget := budgetValue.(*mediaBudget)
	awaitQueue := func(n int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			budget.mu.Lock()
			size := len(budget.waiters)
			budget.mu.Unlock()
			if size == n {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("waiter never queued")
	}
	heavy := make(chan func(), 1)
	light := make(chan func(), 1)
	go func() {
		_, r, err := acquireMediaWork(context.Background(), app, 2)
		if err == nil {
			heavy <- r
		}
	}()
	awaitQueue(1)
	go func() {
		_, r, err := acquireMediaWork(context.Background(), app, 1)
		if err == nil {
			light <- r
		}
	}()
	awaitQueue(2)
	select {
	case r := <-light:
		r()
		t.Fatal("new light job overtook heavy waiter")
	default:
	}
	release3()
	var rh, rl func()
	select {
	case rh = <-heavy:
	case <-time.After(time.Second):
		t.Fatal("heavy starved")
	}
	select {
	case rl = <-light:
	case <-time.After(time.Second):
		t.Fatal("light starved")
	}
	budget.mu.Lock()
	used := budget.used
	budget.mu.Unlock()
	if used != 3 {
		t.Fatalf("used=%d", used)
	}
	rh()
	rl()
	wait, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	leased, release, err := acquireMediaWorkWithLeaseContext(wait, context.Background(), app, 1)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	defer release()
	if leased.Err() != nil {
		t.Fatal("admission timer canceled granted execution lease")
	}
}

func TestIndexLeaseEndsBetweenStagesAndImageWeight(t *testing.T) {
	app := newTestCtx(t)
	_ = upsertMedia(app.AppDB(), testProj, "20", sampleImageProbe(), "sha", "/", "image.png")
	if renderWorkWeight(app, &RenderRow{Operation: "crop", ProjectID: testProj, SourceFileIDs: []string{"20"}}) != 1 {
		t.Fatal("image crop classified heavy")
	}
	if smartCropWorkWeight("extract_reel") != 2 {
		t.Fatal("reel pose classified light")
	}
	for i := 0; i < 3; i++ {
		if e := runIndexStage(context.Background(), app, func(c context.Context) error {
			if c.Value(mediaLeaseKey{}) == nil {
				t.Fatal("unadmitted indexing")
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		v, _ := mediaBudgets.Load("0")
		b := v.(*mediaBudget)
		b.mu.Lock()
		used := b.used
		b.mu.Unlock()
		if used != 0 {
			t.Fatal("indexing retains lease between stages")
		}
	}
}

func TestRemoteIndexStagesReuseProbeAndBoundKeyframes(t *testing.T) {
	s := buildRemoteIndexScript(remoteIndexScriptInputs{Stage: "keyframe", ProbeBase64: "e30=", PositionMs: 6000, KeyframesEnabled: true, ThumbWidth: 320})
	if strings.Contains(s, "-show_streams") || strings.Contains(s, "file=@thumb.jpg") {
		t.Fatal("keyframe stage repeats full probe/thumbnail")
	}
	if !strings.Contains(s, "KEYFRAME_MAX_COUNT=1") || !strings.Contains(s, "POS_SEC=6.000") {
		t.Fatal("keyframe stage unbounded")
	}
	if !strings.Contains(s, "media_resource_exhausted") || !strings.Contains(s, "-threads 1") {
		t.Fatal("memory protections removed")
	}
}

func TestAdmissionTerminalNotifications(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			rec := tk.NewEmitRecorder()
			host := fmt.Sprint(35002)
			if cancelled {
				host = "35003"
			}
			app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEmitter(rec), tk.WithConfig(map[string]string{"render_host_id": host, "media_work_capacity": "2", "render_admission_timeout_seconds": "1"}))
			_, release, err := acquireMediaWork(context.Background(), app, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			id, err := insertRender(app.AppDB(), testProj, "crop", []string{"999"}, map[string]any{}, "image.png", "/", "tester")
			if err != nil {
				t.Fatal(err)
			}
			row, err := claimNextPending(app.AppDB())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { runOneRender(app, row, nil, nil, 30); close(done) }()
			if cancelled {
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					v, _ := mediaBudgets.Load(host)
					b := v.(*mediaBudget)
					b.mu.Lock()
					n := len(b.waiters)
					b.mu.Unlock()
					if n > 0 {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if !triggerCancel(id) {
					t.Fatal("cancel not registered")
				}
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("admission did not terminate")
			}
			terminal, _ := getRender(app.AppDB(), testProj, id)
			topic := topicRenderFailed
			status := "failed"
			if cancelled {
				topic = topicRenderCancelled
				status = "cancelled"
			}
			if terminal.Status != status || len(rec.EventsByTopic(topic)) != 1 {
				t.Fatalf("terminal=%+v events=%+v", terminal, rec.Events())
			}
			if !cancelled && renderFailureCode(terminal.Error) != "render_admission_timeout" {
				t.Fatal("unstructured admission error")
			}
		})
	}
}

func TestRemoteIndexStagesExecuteAndLegacyRepairIsSourceVerified(t *testing.T) {
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, e := exec.LookPath("ffprobe")
	if e != nil {
		t.Skip("ffprobe unavailable")
	}
	source := filepath.Join(t.TempDir(), "sample.mp4")
	if b, e := exec.Command(ffmpeg, "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=10:d=2", "-c:v", "libx264", "-threads", "1", source).CombinedOutput(); e != nil {
		t.Fatalf("fixture: %v %s", e, b)
	}
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			fmt.Fprint(w, `{"id":501}`)
			return
		}
		fmt.Fprint(w, `{"files":[{"id":501,"name":"45.jpg","folder":"/.media/thumbnail/","content_type":"image/jpeg","source":"media-derivation"}]}`)
	}))
	defer storage.Close()
	in := remoteIndexScriptInputs{FFmpeg: ffmpeg, FFprobe: ffprobe, SignedURL: source, FileID: "45", ThumbWidth: 320, ThumbSeek: 1, PublicURL: storage.URL, StorageToken: "test", ProjectID: testProj, Stage: "probe"}
	run := func() *remoteIndexResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b, e := exec.CommandContext(ctx, "bash", "-c", buildRemoteIndexScript(in)).CombinedOutput()
		if e != nil {
			t.Fatalf("stage %s: %v %s", in.Stage, e, b)
		}
		r, e := parseAptevaIndex(string(b))
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := run()
	in.ProbeBase64 = r.ProbeBase64
	in.Stage = "derive"
	r = run()
	if r.ThumbnailFileID != 501 {
		t.Fatal("thumbnail not uploaded")
	}
	raw, _ := base64.StdEncoding.DecodeString(in.ProbeBase64)
	probe, e := parseProbeBytes(raw)
	if e != nil {
		t.Fatal(e)
	}
	in.Summary = probe
	in.Stage = "keyframe"
	in.KeyframesEnabled = true
	in.PositionMs = 1000
	r = run()
	if len(r.Keyframes) != 1 || r.Keyframes[0].PositionMs != 1000 {
		t.Fatal("keyframe stage wrong", r)
	}
	app := newTestCtx(t)
	_ = upsertMedia(app.AppDB(), testProj, "45", sampleImageProbe(), "sha", "/", "source.png")
	_ = upsertDerivation(app.AppDB(), testProj, "45", "thumbnail", 501, 320, 180, 0)
	_, _ = app.AppDB().Exec(`UPDATE media SET derivation_retry_at=datetime('now','-1 day'),probe_at=datetime('now','-1 hour') WHERE file_id='45'`)
	row, _ := getMedia(app.AppDB(), testProj, "45")
	sc := &storageClient{base: storage.URL, httpClient: storage.Client()}
	if repairLegacyDerivationRetry(context.Background(), app, sc, row, StorageFile{ID: 45, SHA256: "changed"}) {
		t.Fatal("changed source repaired")
	}
	if !repairLegacyDerivationRetry(context.Background(), app, sc, row, StorageFile{ID: 45, SHA256: "sha"}) {
		b, _ := json.Marshal(row)
		t.Fatalf("valid stale generation not repaired %s", b)
	}
	_, _ = app.AppDB().Exec(`UPDATE media SET derivation_retry_at=datetime('now','-1 day'),derivation_retry_reason='partial_remote_index_generation' WHERE file_id='45'`)
	if repairLegacyDerivationRetry(context.Background(), app, sc, row, StorageFile{ID: 45, SHA256: "sha"}) {
		t.Fatal("genuine partial failure falsely repaired")
	}
}

func TestCachedRenderSkipsSaturatedHostAdmission(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files/1") {
			fmt.Fprint(w, `{"file":{"id":1,"name":"source.png","folder":"/","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size_bytes":100}}`)
			return
		}
		fmt.Fprint(w, `{"file":{"id":501,"name":"out.png","folder":"/renders/","sha256":"output-sha","size_bytes":500}}`)
	}))
	defer storage.Close()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEnv("APTEVA_GATEWAY_URL", storage.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "test"))
	_ = upsertMedia(app.AppDB(), testProj, "1", sampleImageProbe(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "/", "source.png")
	_, e := insertRender(app.AppDB(), testProj, "crop", []string{"1"}, map[string]any{"target_ratio": "1:1", "crop_w": 100, "crop_h": 100, "crop_x": 0, "crop_y": 0}, "out.png", "/renders/", "tester")
	if e != nil {
		t.Fatal(e)
	}
	row, e := claimNextPending(app.AppDB())
	if e != nil {
		t.Fatal(e)
	}
	_, e = prepareRenderBudget(app, row, 30)
	if e != nil {
		t.Fatal(e)
	}
	executor := &localExecutor{ffmpegPath: "ffmpeg"}
	key, _, _ := requestRenderCacheKey(context.Background(), app, newStorageClient(), row, executor)
	if key == "" {
		f, err := newStorageClient().GetFile(context.Background(), testProj, 1)
		_, planErr := buildPlan(row.Operation, row.SourceFileIDs, row.Params, row.OutputName, ".png")
		t.Fatalf("cache key empty: file=%+v err=%v plan=%v", f, err, planErr)
	}
	_, e = app.AppDB().Exec(`INSERT INTO render_result_cache(cache_key,project_id,storage_file_id,sha256,size_bytes,resolved_params) VALUES(?,?,?,?,?,?)`, key, testProj, 501, "output-sha", 500, string(row.Params))
	if e != nil {
		t.Fatal(e)
	}
	_, release, e := acquireMediaWork(context.Background(), app, 2)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { runOneRender(app, row, executor, nil, 30); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		release()
		t.Fatal("cached retrieval waited for heavy admission")
	}
	release()
	saved, _ := getRender(app.AppDB(), testProj, row.ID)
	if saved.Status != "ok" || saved.OutputFileID != "501" {
		t.Fatalf("cache reuse failed: %+v", saved)
	}
}

func TestRemoteIndexReuseCarriesVerifiedLineageWithoutHostLease(t *testing.T) {
	app := newTestCtx(t)
	probe, err := parseProbeBytes([]byte(`{"format":{"format_name":"mov","duration":"2"},"streams":[{"codec_type":"video","codec_name":"h264","width":320,"height":180,"r_frame_rate":"10/1"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = upsertMedia(app.AppDB(), testProj, "45", probe, "sha", "/", "source.mp4")
	_ = upsertDerivation(app.AppDB(), testProj, "45", "thumbnail", 501, 320, 180, 0)
	_ = upsertDerivation(app.AppDB(), testProj, "45", "keyframe", 502, 320, 180, 1000)
	_, _ = app.AppDB().Exec(`UPDATE media SET probe_at=datetime('now','-1 hour') WHERE file_id='45'`)
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"files":[{"id":501,"name":"45.jpg","folder":"/.media/thumbnail/","content_type":"image/jpeg","source":"media-derivation"},{"id":502,"name":"45-1000.jpg","folder":"/.media/keyframe/","content_type":"image/jpeg","source":"media-derivation"}]}`)
	}))
	defer storage.Close()
	sc := &storageClient{base: storage.URL, httpClient: storage.Client()}
	params := remoteIndexParams{}
	reuseRemoteIndexGeneration(context.Background(), app, sc, testProj, StorageFile{ID: 45, SHA256: "sha"}, &params)
	if params.ReuseThumbnail != 501 || len(params.ReuseKeyframes) != 1 || params.ReuseProbe == nil {
		t.Fatal("verified artifacts not reused", params.ReuseThumbnail, len(params.ReuseKeyframes))
	}
	// Saturated host and no remote token still permit fully cached retrieval.
	_, release, err := acquireMediaWork(context.Background(), app, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, thumb, _, frames, err := runRemoteIndexing(ctx, app, testProj, params)
	if err != nil || thumb != 501 || len(frames) != 1 {
		t.Fatalf("cached indexing entered heavy work: %v", err)
	}
	if err = replaceRemoteDerivations(app.AppDB(), testProj, "45", probe, 501, 0, frames, 320, 800, 120, remoteGenerationPolicy{sourceSHA: "sha", expected: []int64{1000, 1500}}); err != nil {
		t.Fatal(err)
	}
	// A later probe timestamp must not invalidate explicitly tagged usable output.
	_, _ = app.AppDB().Exec(`UPDATE media SET probe_at=datetime('now','+1 minute') WHERE file_id='45'`)
	next := remoteIndexParams{}
	reuseRemoteIndexGeneration(context.Background(), app, sc, testProj, StorageFile{ID: 45, SHA256: "sha"}, &next)
	if next.ReuseThumbnail != 501 || len(next.ReuseKeyframes) != 1 {
		t.Fatal("partial retry lost verified lineage")
	}
	changed := remoteIndexParams{}
	reuseRemoteIndexGeneration(context.Background(), app, sc, testProj, StorageFile{ID: 45, SHA256: "different"}, &changed)
	if changed.ReuseProbe != nil || changed.ReuseThumbnail != 0 {
		t.Fatal("changed source reused generation")
	}
}

func TestStoryboardDoesNotRequireEmptyEndSeek(t *testing.T) {
	app := newTestCtx(t)
	for _, duration := range []int64{796568, 775258, 629011, 803406, 775556} {
		positions := keyframePositions(duration, app)
		if len(positions) == 0 || positions[len(positions)-1] >= duration-250 {
			t.Fatalf("unsafe end seek duration=%d positions=%v", duration, positions)
		}
	}
}
