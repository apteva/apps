package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const fakeProbeJSON = `{"format":{"format_name":"mov,mp4","duration":"20.000","bit_rate":"1000000"},
"streams":[{"codec_type":"video","codec_name":"h264","width":320,"height":180,"avg_frame_rate":"25/1"},
{"codec_type":"audio","codec_name":"aac","channels":2,"sample_rate":"48000","duration":"20.000"}]}`

// fakeIndexExecutor answers the executor component's protocol. The probe
// stage returns at once; later stages wait on hold (when set) so a test can
// look at the database while derivations are still running.
type fakeIndexExecutor struct {
	tk.BasePlatformClient
	mu       sync.Mutex
	receipts map[string]componentReceipt
	stages   []string
	hold     chan struct{}
	holding  chan struct{}
}

func newFakeIndexExecutor(hold bool) *fakeIndexExecutor {
	f := &fakeIndexExecutor{receipts: map[string]componentReceipt{}, holding: make(chan struct{}, 8)}
	if hold {
		f.hold = make(chan struct{})
	}
	return f
}

func (f *fakeIndexExecutor) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{}}, nil
}

func (f *fakeIndexExecutor) CallAppResultContext(ctx context.Context, name, tool string, input map[string]any, out any) error {
	if name != "component:executor" {
		return fmt.Errorf("unexpected app %q", name)
	}
	var result any
	switch tool {
	case "capabilities":
		result = map[string]any{"protocol": "apteva.media-executor/v1", "ffmpeg": "/bin/ffmpeg", "ffprobe": "/bin/ffprobe"}
	case "submit":
		id, _ := input["job_id"].(string)
		command, _ := input["command"].(string)
		stage := "derive"
		if strings.Contains(command, "-show_streams") {
			stage = "probe"
		}
		f.mu.Lock()
		f.stages = append(f.stages, stage)
		f.mu.Unlock()
		if stage != "probe" && f.hold != nil {
			f.holding <- struct{}{}
			select {
			case <-f.hold:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		marker := map[string]any{"probe_b64": base64.StdEncoding.EncodeToString([]byte(fakeProbeJSON))}
		if stage == "derive" {
			marker["thumbnail_file_id"] = 9001
		}
		raw, _ := json.Marshal(marker)
		r := componentReceipt{JobID: id, Status: "succeeded", Output: "APTEVA_INDEX:" + string(raw) + "\n", Stopped: true}
		f.mu.Lock()
		f.receipts[id] = r
		f.mu.Unlock()
		result = r
	case "get", "cancel":
		id, _ := input["job_id"].(string)
		f.mu.Lock()
		r, ok := f.receipts[id]
		f.mu.Unlock()
		if !ok {
			r = componentReceipt{JobID: id, Status: "cancelled", Stopped: true}
		}
		result = r
	default:
		return fmt.Errorf("unexpected executor tool %q", tool)
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}

func (f *fakeIndexExecutor) CallAppContext(ctx context.Context, name, tool string, input map[string]any) (json.RawMessage, error) {
	var out json.RawMessage
	err := f.CallAppResultContext(ctx, name, tool, input, &out)
	return out, err
}

func (f *fakeIndexExecutor) CallAppBatchContext(context.Context, string, []sdk.AppCall, sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	return nil, fmt.Errorf("batch not supported")
}

func remoteIndexApp(t *testing.T, exec *fakeIndexExecutor) *sdk.AppCtx {
	t.Helper()
	mediaBudgets.Delete("1")
	t.Cleanup(func() { mediaBudgets.Delete("1") })
	t.Setenv("APTEVA_INSTALL_ID", "23")
	return newTestCtxWithPlatform(t, exec, tk.WithConfig(map[string]string{
		"execution_mode":    "component",
		"keyframes_enabled": "false",
	}))
}

func runRemoteProcessOne(app *sdk.AppCtx, f StorageFile) {
	processOne(context.Background(), app, newStorageClient(), testProj, f, "", "", 0, 1.0, 320, 800, 120)
}

func TestRemoteIndexPersistsProbeBeforeDerivations(t *testing.T) {
	exec := newFakeIndexExecutor(true)
	app := remoteIndexApp(t, exec)
	file := StorageFile{ID: 7701, Name: "clip.mp4", Folder: "/", ContentType: "video/mp4", SHA256: "sha-7701"}
	if err := insertPendingTranscript(app.AppDB(), testProj, "7701", "auto", transcriptionDefaults(app)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { runRemoteProcessOne(app, file); close(done) }()
	select {
	case <-exec.holding:
	case <-time.After(5 * time.Second):
		t.Fatal("derivation stage never started")
	}

	// The probe is already usable while the thumbnail is still being made.
	m, err := getMedia(app.AppDB(), testProj, "7701")
	if err != nil {
		t.Fatal(err)
	}
	if m.ProbeStatus != "ok" || m.DurationMs != 20000 {
		t.Fatalf("probe not persisted before derivations: status=%s duration=%d", m.ProbeStatus, m.DurationMs)
	}
	row, err := claimNextPendingTranscript(app.AppDB(), testProj)
	if err != nil || row == nil || row.FileID != "7701" {
		t.Fatalf("transcript should be claimable once the probe lands: row=%+v err=%v", row, err)
	}

	// The whole file runs under one lease, so later stages never requeue.
	v, _ := mediaBudgets.Load("1")
	budget := v.(*mediaBudget)
	budget.mu.Lock()
	used, owners := budget.used, []string{}
	for w := range budget.leases {
		owners = append(owners, w.owner)
	}
	budget.mu.Unlock()
	if used != 1 || len(owners) != 1 || owners[0] != "index:"+testProj+":7701" {
		t.Fatalf("want one file-wide index lease, used=%d owners=%v", used, owners)
	}

	close(exec.hold)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("index did not finish")
	}
	m, _ = getMedia(app.AppDB(), testProj, "7701")
	hasThumb := false
	for _, d := range m.Derivations {
		hasThumb = hasThumb || (d.Kind == "thumbnail" && d.StorageFileID == "9001")
	}
	if m.ProbeStatus != "ok" || !hasThumb {
		t.Fatalf("final row: status=%s derivations=%+v", m.ProbeStatus, m.Derivations)
	}
	if got := strings.Join(exec.stages, ","); got != "probe,derive" {
		t.Fatalf("stages=%s; the probe must run once", got)
	}
}

// A worker that claimed a file and then died (sidecar killed) leaves only
// the durable claim. Once it is stale, another worker picks the file up and
// finishes the probe.
func TestInterruptedIndexClaimIsReclaimedAndFinished(t *testing.T) {
	exec := newFakeIndexExecutor(false)
	app := remoteIndexApp(t, exec)
	file := StorageFile{ID: 7702, Name: "render-out.mp4", Folder: "/renders/", ContentType: "video/mp4", SHA256: "sha-7702"}
	if _, err := claimIndexerAttempt(app.AppDB(), testProj, file); err != nil {
		t.Fatal(err)
	}
	if ids, _ := pendingIndexerFileIDs(app.AppDB(), testProj, 10); len(ids) != 0 {
		t.Fatalf("fresh claim must not be reclaimed yet: %v", ids)
	}
	_, _ = app.AppDB().Exec(`UPDATE media SET index_claimed_at=? WHERE file_id='7702'`,
		time.Now().Add(-20*time.Minute).UTC().Format(time.RFC3339))
	ids, err := pendingIndexerFileIDs(app.AppDB(), testProj, 10)
	if err != nil || len(ids) != 1 || ids[0] != "7702" {
		t.Fatalf("stale claim not reclaimed: ids=%v err=%v", ids, err)
	}
	runRemoteProcessOne(app, file)
	var status string
	var attempts int64
	var claimed bool
	var duration int64
	_ = app.AppDB().QueryRow(`SELECT probe_status, index_attempts, index_claimed_at IS NOT NULL, COALESCE(duration_ms,0) FROM media WHERE file_id='7702'`).Scan(&status, &attempts, &claimed, &duration)
	if status != "ok" || attempts != 2 || claimed || duration != 20000 {
		var probeErr string
		_ = app.AppDB().QueryRow(`SELECT probe_error FROM media WHERE file_id='7702'`).Scan(&probeErr)
		t.Fatalf("second worker did not finish: status=%s attempts=%d claimed=%v duration=%d error=%q stages=%v", status, attempts, claimed, duration, probeErr, exec.stages)
	}
}

func TestTranscriptWaitsForIndexingThenFailsSpecifically(t *testing.T) {
	app := newTestCtxWithPlatform(t, noBindings(), tk.WithConfig(map[string]string{"transcribe_engine": "parakeet"}))
	db := app.AppDB()
	if err := queueMediaReindex(db, testProj, "7703", false); err != nil {
		t.Fatal(err)
	}
	if err := insertPendingTranscript(db, testProj, "7703", "auto", transcriptionDefaults(app)); err != nil {
		t.Fatal(err)
	}
	claim := func() *TranscriptRow {
		row, _ := claimNextPendingTranscript(db, testProj)
		return row
	}
	if row := claim(); row != nil {
		t.Fatal("transcript claimed while its source is still indexing")
	}
	_, _ = db.Exec(`UPDATE media SET probe_status='failed', probe_error='executor unreachable', index_failures=1 WHERE file_id='7703'`)
	if row := claim(); row != nil {
		t.Fatal("transcript claimed between index retries")
	}
	_, _ = db.Exec(`UPDATE media SET index_failures=? WHERE file_id='7703'`, transcriptIndexWaitFailures)
	row := claim()
	if row == nil {
		t.Fatal("transcript never released after indexing gave up")
	}
	runOneTranscription(app, nil, row)
	got, err := getTranscript(db, testProj, "7703")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || !strings.HasPrefix(got.Error, "media_index_failed: probe failed: executor unreachable") {
		t.Fatalf("status=%s error=%q", got.Status, got.Error)
	}
}

func TestReindexReportsInFlightFile(t *testing.T) {
	app := newTestCtx(t)
	if !claimInFlight(testProj, "7704") {
		t.Fatal("claim")
	}
	defer releaseInFlight(testProj, "7704")
	out, err := (&App{}).toolReindex(app, map[string]any{"project_id": testProj, "file_id": "7704"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["in_flight"] != true || res["queued"] != 0 {
		t.Fatalf("reindex of an in-flight file: %+v", res)
	}
}
