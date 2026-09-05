package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSourceCacheVerifiesAndReuses(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"render_scratch_dir": t.TempDir()}))
	payload := strings.Repeat("verified source", 1000)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); fmt.Fprint(w, payload) }))
	defer srv.Close()
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	file := &StorageFile{ID: 99881, SHA256: digest, SizeBytes: int64(len(payload))}
	// Per-test cache root; testkit config points render_scratch_dir at a temp dir.
	root := t.TempDir()
	original := app.Config().Get("render_scratch_dir")
	_ = original
	first := filepath.Join(root, "first")
	hit, err := materializeLocalSource(context.Background(), app, sc, testProj, file, first)
	if err != nil || hit {
		t.Fatalf("cold hit=%v err=%v", hit, err)
	}
	second := filepath.Join(root, "second")
	hit, err = materializeLocalSource(context.Background(), app, sc, testProj, file, second)
	if err != nil || !hit {
		t.Fatalf("warm hit=%v err=%v", hit, err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("downloads=%d", downloads.Load())
	}
	if raw, _ := os.ReadFile(second); string(raw) != payload {
		t.Fatal("cached content changed")
	}
	// Changing authoritative identity forces a verified fetch; wrong bytes fail.
	file.SHA256 = strings.Repeat("a", 64)
	if _, err := materializeLocalSource(context.Background(), app, sc, testProj, file, filepath.Join(root, "bad")); err == nil {
		t.Fatal("wrong checksum accepted")
	}
}
func TestAutoRatingCannotOverwriteManualRating(t *testing.T) {
	app := newTestCtx(t)
	_ = upsertMedia(app.AppDB(), testProj, "1", sampleImageProbe(), "sha", "/", "a.png")
	row, _ := getMedia(app.AppDB(), testProj, "1")
	var prose, rating int64
	_ = app.AppDB().QueryRow(`SELECT prose_revision,audience_revision FROM media WHERE file_id='1'`).Scan(&prose, &rating)
	_ = setAudienceRating(app.AppDB(), testProj, "1", "mature", "human")
	_, err := commitAutoDescription(app.AppDB(), row, prose, rating, parsedDescribe{Description: "AI", AudienceRating: "general"}, true)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := getMedia(app.AppDB(), testProj, "1")
	if got.AudienceRating != "mature" {
		t.Fatal("manual classification overwritten")
	}
}
func TestTranscriptAttemptTokenRejectsStaleResult(t *testing.T) {
	app := newTestCtx(t)
	_ = insertPendingTranscript(app.AppDB(), testProj, "1", "manual")
	old, err := claimNextPendingTranscript(app.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = app.AppDB().Exec(`UPDATE transcripts SET status='pending',started_at=NULL WHERE file_id='1'`)
	current, err := claimNextPendingTranscript(app.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	old.Text = "stale"
	if err := transcriptMarkOk(app.AppDB(), old); err == nil {
		t.Fatal("stale attempt committed")
	}
	current.Text = "current"
	if err := transcriptMarkOk(app.AppDB(), current); err != nil {
		t.Fatal(err)
	}
}
func TestFailedDerivativeGenerationPreservesWorkingPointer(t *testing.T) {
	app := newTestCtx(t)
	_ = upsertMedia(app.AppDB(), testProj, "1", sampleImageProbe(), "sha", "/", "a.png")
	_ = upsertDerivation(app.AppDB(), testProj, "1", "thumbnail", 5, 100, 100, 0)
	previous, _ := listDerivations(app.AppDB(), testProj, "1")
	stage := &derivationStage{failed: true, rows: []DerivationRow{{FileID: "1", Kind: "thumbnail", Status: "failed", Error: "upload unavailable"}}}
	if err := commitDerivationStage(context.Background(), app, nil, testProj, "1", stage, previous); err != nil {
		t.Fatal(err)
	}
	ds, _ := listDerivations(app.AppDB(), testProj, "1")
	if len(ds) != 1 || ds[0].StorageFileID != "5" || ds[0].Status != "ok" {
		t.Fatalf("lost working thumbnail: %+v", ds)
	}
}
func TestMediaBudgetCancellation(t *testing.T) {
	app := newTestCtx(t)
	ctx, release, err := acquireMediaWork(context.Background(), app, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, nested, err := acquireMediaWork(ctx, app, 1)
	if err != nil {
		t.Fatal(err)
	}
	nested()
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := acquireMediaWork(deadline, app, 1); err == nil {
		t.Fatal("blocked admission ignored deadline")
	}
}
func TestEncoderProfilesKeepCopyPaths(t *testing.T) {
	for _, op := range []string{"trim", "concat"} {
		sources := []string{"1"}
		if op == "concat" {
			sources = append(sources, "2")
		}
		plan, err := buildPlan(op, sources, json.RawMessage(`{"encoder_profile":"preview","end_ms":1000}`), "out.mp4", ".mp4")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(plan.Args, " "), "libx264") {
			t.Fatalf("%s lost stream copy", op)
		}
	}
	if _, err := buildPlan("transcode", []string{"1"}, json.RawMessage(`{"format":"mp4","video_codec":"libx265","encoder_profile":"preview"}`), "", ".mp4"); err == nil {
		t.Fatal("conflicting codec silently changed")
	}
}

func TestCompletionOutboxRetriesFailedDelivery(t *testing.T) {
	app := newTestCtx(t)
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test")
	_, err := app.AppDB().Exec(`INSERT INTO media_event_outbox(event_id,project_id,topic,payload) VALUES('event-1',?,'media.completed','{"file_id":"1"}')`, testProj)
	if err != nil {
		t.Fatal(err)
	}
	flushCompletionOutbox(app)
	var count int
	_ = app.AppDB().QueryRow(`SELECT COUNT(*) FROM media_event_outbox`).Scan(&count)
	if count != 1 {
		t.Fatal("failed delivery lost durable event")
	}
	flushCompletionOutbox(app)
	_ = app.AppDB().QueryRow(`SELECT COUNT(*) FROM media_event_outbox`).Scan(&count)
	if count != 0 || attempts.Load() != 2 {
		t.Fatal("successful retry not acknowledged")
	}
}

func TestLocalRenderResultCacheSkipsEncoder(t *testing.T) {
	root := t.TempDir()
	source := "source bytes"
	output := "rendered bytes"
	sourceSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	outputSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(output)))
	var downloads atomic.Int32
	outputFile := StorageFile{ID: 99, Name: "out.mp4", Folder: "/renders/", ContentType: "video/mp4", SHA256: outputSHA, SizeBytes: int64(len(output))}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files/1/content"):
			downloads.Add(1)
			fmt.Fprint(w, source)
		case strings.HasSuffix(r.URL.Path, "/files/1"):
			json.NewEncoder(w).Encode(map[string]any{"file": StorageFile{ID: 1, Name: "source.mp4", SHA256: sourceSHA, SizeBytes: int64(len(source))}})
		case strings.HasSuffix(r.URL.Path, "/files/99"):
			json.NewEncoder(w).Encode(map[string]any{"file": outputFile})
		case strings.HasSuffix(r.URL.Path, "/uploads"):
			json.NewEncoder(w).Encode(map[string]any{"was_existing": true, "file": outputFile})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"render_scratch_dir": root}))
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "test")
	marker := filepath.Join(root, "encodes")
	binary := filepath.Join(root, "fake-ffmpeg")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nfor out do :; done\nprintf x >> "+shellQuote(marker)+"\nprintf 'rendered bytes' > \"$out\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	executor := &localExecutor{ffmpegPath: binary, scratchRoot: root, outputFolder: "/renders/"}
	for i := 0; i < 2; i++ {
		id, err := insertRender(app.AppDB(), testProj, "trim", []string{"1"}, map[string]any{"start_ms": 0, "end_ms": 1000}, "out.mp4", "/renders/", "")
		if err != nil {
			t.Fatal(err)
		}
		row, err := getRender(app.AppDB(), testProj, id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := executor.Execute(context.Background(), app, row)
		if err != nil || got != 99 {
			t.Fatalf("render %d: id=%d err=%v", i, got, err)
		}
		_, _ = app.AppDB().Exec(`UPDATE renders SET status='ok' WHERE id=?`, id)
	}
	for i := 0; i < 2; i++ {
		_, err := insertRender(app.AppDB(), testProj, "trim", []string{"1"}, map[string]any{"start_ms": 0, "end_ms": 1000}, "out.mp4", "/renders/", "")
		if err != nil {
			t.Fatal(err)
		}
		row, err := claimNextPending(app.AppDB())
		if err != nil {
			t.Fatal(err)
		}
		runOneRender(app, row, executor, nil, 30)
		got, err := getRender(app.AppDB(), testProj, row.ID)
		if err != nil || got.Status != "ok" || got.OutputFileID != "99" {
			t.Fatalf("queued render: %+v err=%v", got, err)
		}
		if i == 1 {
			var metrics map[string]any
			if json.Unmarshal(got.Metrics, &metrics) != nil || metrics["result_cache_hit"] != true {
				t.Fatalf("request cache missed: %s", got.Metrics)
			}
		}
	}
	encoded, _ := os.ReadFile(marker)
	if string(encoded) != "x" || downloads.Load() != 1 {
		t.Fatalf("repeated work: encodes=%q downloads=%d", encoded, downloads.Load())
	}
}

func TestManualDescriptionCandidateDoesNotStarveInDisabledMode(t *testing.T) {
	app := newTestCtx(t)
	for i := 1; i <= 20; i++ {
		_ = upsertMedia(app.AppDB(), testProj, fmt.Sprint(i), sampleImageProbe(), "sha", "/", "image.jpg")
	}
	_, _ = app.AppDB().Exec(`UPDATE media SET describe_requested=1 WHERE file_id='20'`)
	ids, err := describeCandidatesMode(app.AppDB(), testProj, 5, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "20" {
		t.Fatalf("manual request starved by auto candidates: %v", ids)
	}
}

func TestIndexerRepairsMissingKeyframesOnlyWhenEnabled(t *testing.T) {
	app := newTestCtx(t)
	if err := upsertMedia(app.AppDB(), testProj, "1", sampleVideoProbe(), "sha", "/", "video.mp4"); err != nil {
		t.Fatal(err)
	}
	if err := upsertDerivation(app.AppDB(), testProj, "1", "thumbnail", 123, 320, 180, 0); err != nil {
		t.Fatal(err)
	}
	files := []StorageFile{{ID: 1, SHA256: "sha", Name: "video.mp4", Folder: "/"}}
	if got := indexerCandidates(app.AppDB(), testProj, files, 10, false); len(got) != 0 {
		t.Fatal("disabled keyframes scheduled repair")
	}
	if got := indexerCandidates(app.AppDB(), testProj, files, 10, true); len(got) != 1 {
		t.Fatal("missing keyframes not scheduled for repair")
	}
}
