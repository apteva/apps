package main

// Audit-only tests: expectations describe the proposed corrected behavior.
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestAuditIndexerHonorsBatch(t *testing.T) {
	app := newTestCtx(t)
	files := make([]StorageFile, 100)
	for i := range files {
		files[i] = StorageFile{ID: int64(i + 1)}
	}
	got := indexerCandidates(app.AppDB(), testProj, files, 25)
	if len(got) > 25 {
		t.Fatalf("batch limit=25, selected=%d", len(got))
	}
}

func TestAuditRenderCleanupKeepsExistingOutput(t *testing.T) {
	var app *sdk.AppCtx
	var row *RenderRow
	deleted := false
	const payload = "already existing render bytes"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files/1/content"):
			fmt.Fprint(w, "source")
		case strings.HasSuffix(r.URL.Path, "/files/1"):
			fmt.Fprint(w, `{"file":{"id":1,"name":"source.mp4","folder":"/","content_type":"video/mp4"}}`)
		case strings.HasSuffix(r.URL.Path, "/uploads"):
			// Simulate the render becoming cancelled before its completion commit.
			if err := renderMarkCancelled(app.AppDB(), row.ID); err != nil {
				panic(err)
			}
			json.NewEncoder(w).Encode(map[string]any{"was_existing": true, "file": map[string]any{"id": 99, "name": "repeat.mp4", "folder": "/renders/", "sha256": digest, "size_bytes": len(payload)}})
		case strings.HasSuffix(r.URL.Path, "/mcp"):
			deleted = true
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	app = tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEnv("APTEVA_GATEWAY_URL", srv.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "test"))
	globalCtx = app
	id, err := insertRender(app.AppDB(), testProj, "trim", []string{"1"}, map[string]any{"start_ms": 0, "end_ms": 1000}, "repeat.mp4", "/renders/", "")
	if err != nil {
		t.Fatal(err)
	}
	row, err = claimNextPending(app.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != id {
		t.Fatal("unexpected claimed row")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "fake-ffmpeg")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nfor out do :; done\nprintf 'already existing render bytes' > \"$out\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runOneRender(app, row, &localExecutor{ffmpegPath: binary, scratchRoot: root, outputFolder: "/renders/"}, nil, 30)
	if deleted {
		t.Fatal("completion conflict deleted pre-existing storage file 99 returned by dedup")
	}
}

func TestAuditVerticalSmartCropPreservesY(t *testing.T) {
	app := newTestCtx(t)
	img := image.NewRGBA(image.Rect(0, 0, 120, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 120; x++ {
			c := color.RGBA{70, 70, 70, 255}
			if y > 160 {
				if (x/4+y/4)%2 == 0 {
					c = color.RGBA{250, 230, 220, 255}
				} else {
					c = color.RGBA{150, 60, 30, 255}
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	frame, err := analyzeSmartCropV2Frame(120, 240, 1, 1, img)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Y == 0 {
		t.Fatal("fixture did not exercise a vertical crop")
	}
	p := sampleImageProbe()
	p.Width = 120
	p.Height = 240
	if err := upsertMedia(app.AppDB(), testProj, "1", p, "sha", "/", "portrait.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := upsertDerivation(app.AppDB(), testProj, "1", "thumbnail", 2, 120, 240, 0); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			jpeg.Encode(w, img, &jpeg.Options{Quality: 95})
			return
		}
		fmt.Fprint(w, `{"files":[{"id":2,"name":"1.jpg","folder":"/.media/thumbnail/","content_type":"image/jpeg","source":"media-derivation"}]}`)
	}))
	defer srv.Close()
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	got, err := computeSmartCropStillV2(context.Background(), app, sc, testProj, "1", 1, 1, smartCropTarget{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Y == 0 {
		t.Fatalf("frame analyzer selected Y=%d but complete V2 returned Y=0", frame.Y)
	}
}

func TestAuditSingleKeyframeDoesNotPanic(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"keyframe_max_count": "1"}))
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("keyframe_max_count=1 crashes: %v", p)
		}
	}()
	got := keyframePositions(60000, app)
	if len(got) != 1 {
		t.Errorf("got %v", got)
	}
}

func TestAuditMetadataScalarTypes(t *testing.T) {
	app := newTestCtx(t)
	if err := upsertMedia(app.AppDB(), testProj, "1", sampleImageProbe(), "sha", "/", "a.png"); err != nil {
		t.Fatal(err)
	}
	_, err := patchMediaMetadata(app.AppDB(), testProj, "1", []byte(`{"approved":1}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	conditions, err := parseMetadataConditions(map[string]any{"metadata.approved": true}, "conditions")
	if err != nil {
		t.Fatal(err)
	}
	got, err := patchMediaMetadata(app.AppDB(), testProj, "1", []byte(`{"published":true}`), conditions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Updated {
		t.Fatal("boolean true precondition matched numeric 1")
	}
}

func TestAuditFolderWildcardIsLiteral(t *testing.T) {
	app := newTestCtx(t)
	if err := upsertMedia(app.AppDB(), testProj, "1", sampleImageProbe(), "sha", "/clipsA1/", "a.png"); err != nil {
		t.Fatal(err)
	}
	rows, err := searchMedia(app.AppDB(), testProj, SearchFilters{Folder: "/clips_1/", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("/clips_1/ also returned /clipsA1/: %d rows", len(rows))
	}
}

func TestAuditShortThumbnailWithinSource(t *testing.T) {
	got := candidateThumbnailSeeks(200, 1)
	for _, s := range got {
		if s >= 0.2 {
			t.Errorf("200 ms video gets out-of-range seek %.3fs", s)
		}
	}
}

func TestAuditRefusalNotSavedAsDescription(t *testing.T) {
	got := parseDescribeJSON(`{"description":"","audience_rating":"adult","audience_reasoning":"I cannot assist with this content"}`)
	if got.Description != "" {
		t.Fatalf("empty/refused description became %q", got.Description)
	}
}

func TestAuditUnratedResetQueuesClassification(t *testing.T) {
	app := newTestCtx(t)
	if err := upsertMedia(app.AppDB(), testProj, "1", sampleImageProbe(), "sha", "/", "a.png"); err != nil {
		t.Fatal(err)
	}
	desc := "Existing AI description"
	if _, err := setDescription(app.AppDB(), testProj, "1", DescriptionFields{Description: &desc, Source: "ai-generated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolSetAudienceRating(app, map[string]any{"_project_id": testProj, "file_id": "1", "rating": "unrated"}); err != nil {
		t.Fatal(err)
	}
	rows, err := describeCandidates(app.AppDB(), testProj, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("rating reset does not queue an already described file")
	}
}

func TestAuditHumanClearAllowsRegeneration(t *testing.T) {
	app := newTestCtx(t)
	if err := upsertMedia(app.AppDB(), testProj, "1", sampleImageProbe(), "sha", "/", "a.png"); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if _, err := setDescription(app.AppDB(), testProj, "1", DescriptionFields{Description: &empty}); err != nil {
		t.Fatal(err)
	}
	got, err := (&App{}).toolDescribe(app, map[string]any{"_project_id": testProj, "file_id": "1", "force": true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.(map[string]any)["queued"].(bool) {
		t.Fatalf("clear still prevents generation: %v", got)
	}
}

func TestAuditDeleteChecksMCPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"permission denied"}]}}`)
	}))
	defer srv.Close()
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	if err := sc.DeleteFile(context.Background(), testProj, 1); err == nil {
		t.Fatal("HTTP 200 MCP failure reported as successful deletion")
	}
}

func TestAuditAudioCoverIsNotVideo(t *testing.T) {
	raw := []byte(`{"format":{"format_name":"mp3","duration":"60"},"streams":[{"codec_type":"audio","codec_name":"mp3"},{"codec_type":"video","codec_name":"mjpeg","width":600,"height":600,"disposition":{"attached_pic":1}}]}`)
	got, err := parseProbeBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasVideo {
		t.Fatal("MP3 cover art is classified as a video stream")
	}
}

func TestAuditStandardAnalysisCapsExplicitRange(t *testing.T) {
	app := newTestCtx(t)
	opts, err := parseAnalysisOptions(app, &MediaRow{DurationMs: 600000}, map[string]any{"depth": "standard", "end_ms": float64(600000)})
	if err != nil {
		t.Fatal(err)
	}
	if opts.EndMs-opts.StartMs > 60000 {
		t.Fatalf("standard depth analyzes %d ms despite 60000 ms contract", opts.EndMs-opts.StartMs)
	}
}

func TestAuditSQL24HourBoundary(t *testing.T) {
	app := newTestCtx(t)
	var included int
	if err := app.AppDB().QueryRow(`SELECT julianday('2026-09-04T01:00:00Z') >= julianday('2026-09-05 12:00:00','-24 hours')`).Scan(&included); err != nil {
		t.Fatal(err)
	}
	if included != 0 {
		t.Fatal("35-hour-old completion included in 24-hour window")
	}
}

type auditEditingPlatform struct {
	tk.BasePlatformClient
	during func()
}

func (p *auditEditingPlatform) ExecuteIntegrationTool(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
	p.during()
	content, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"description":"AI output","audience_rating":"general"}`}}}})
	return &sdk.ExecuteResult{Success: true, Data: content}, nil
}

func TestAuditInFlightAIDoesNotOverwriteHuman(t *testing.T) {
	platform := &auditEditingPlatform{}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(platform))
	globalCtx = app
	if err := upsertMedia(app.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "/", "a.wav"); err != nil {
		t.Fatal(err)
	}
	if err := upsertTranscript(app.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Text: "Hello", SourceKind: "imported", Provider: "imported"}); err != nil {
		t.Fatal(err)
	}
	platform.during = func() {
		s := "Human edit while AI is running"
		if _, err := setDescription(app.AppDB(), testProj, "1", DescriptionFields{Description: &s}); err != nil {
			panic(err)
		}
	}
	runOneDescription(app, &sdk.BoundIntegration{ConnectionID: 777, AppSlug: "openai-api", ToolFor: func(s string) string { return "chat_completion" }}, testProj, "1")
	row, err := getMedia(app.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Description != "Human edit while AI is running" {
		t.Fatalf("human edit overwritten: source=%s description=%q", row.DescriptionSource, row.Description)
	}
}

func TestAuditDescriptionSweepDoesNotStarveReadyFiles(t *testing.T) {
	app := newTestCtx(t)
	for i := 1; i <= 6; i++ {
		p := sampleAudioProbe()
		if i == 1 {
			p = sampleImageProbe()
		}
		if err := upsertMedia(app.AppDB(), testProj, fmt.Sprint(i), p, "sha", "/", "source"); err != nil {
			t.Fatal(err)
		}
		if _, err := app.AppDB().Exec(`UPDATE media SET created_at=? WHERE file_id=?`, fmt.Sprintf("2026-01-%02dT00:00:00Z", i), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := describeCandidates(app.AppDB(), testProj, 5, 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == "1" {
			return
		}
	}
	t.Fatalf("five audio rows without transcripts permanently hide ready image 1: candidates=%v", ids)
}

func TestAuditSkippedTranscriptTriggersCompletion(t *testing.T) {
	platform := boundDeepgram()
	app := newTestCtxWithPlatform(t, platform)
	p := sampleAudioProbe()
	p.DurationMs = 121 * 60000
	if err := upsertMedia(app.AppDB(), testProj, "1", p, "sha", "/", "long.wav"); err != nil {
		t.Fatal(err)
	}
	if err := upsertDerivation(app.AppDB(), testProj, "1", "waveform", 2, 100, 20, 0); err != nil {
		t.Fatal(err)
	}
	if err := insertPendingTranscript(app.AppDB(), testProj, "1", "auto"); err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPendingTranscript(app.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	runOneTranscription(app, app.IntegrationFor("transcripts"), row)
	var before, after string
	if err := app.AppDB().QueryRow(`SELECT COALESCE(completed_at,'') FROM media WHERE file_id='1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	maybeEmitMediaCompleted(app, testProj, "1")
	if err := app.AppDB().QueryRow(`SELECT COALESCE(completed_at,'') FROM media WHERE file_id='1'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before == "" && after != "" {
		t.Fatal("skipped transcript did not invoke completion; calling coordinator explicitly completes it")
	}
}
