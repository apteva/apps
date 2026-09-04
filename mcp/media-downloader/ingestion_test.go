package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func richMetadataFixture() map[string]any {
	return map[string]any{
		"id":          "abc123",
		"title":       "A useful video",
		"description": "Full description",
		"channel":     "Example Channel",
		"channel_id":  "UC123",
		"uploader":    "Example",
		"upload_date": "20260904",
		"duration":    123.5,
		"thumbnail":   "https://img.example/thumb.jpg",
		"tags":        []any{"one", "two"},
		"categories":  []any{"Education"},
		"formats":     []any{map[string]any{"format_id": "18"}},
		"subtitles": map[string]any{
			"en": []any{map[string]any{"ext": "vtt", "name": "English"}},
			"es": []any{map[string]any{"ext": "vtt", "name": "Spanish"}},
		},
		"automatic_captions": map[string]any{
			"en": []any{map[string]any{"ext": "vtt", "name": "English (auto)"}},
			"fr": []any{map[string]any{"ext": "vtt", "name": "French (auto)"}},
		},
	}
}

func TestNormalizeMetadataReturnsRichSafeFields(t *testing.T) {
	metadata := normalizeMetadata(richMetadataFixture())
	if metadata.Description != "Full description" || metadata.ThumbnailURL == "" || metadata.Channel != "Example Channel" {
		t.Fatalf("normalized metadata missing rich fields: %#v", metadata)
	}
	if metadata.PublishDate != "2026-09-04" || metadata.FormatCount != 1 {
		t.Fatalf("normalized date/count = %#v", metadata)
	}
	if strings.Join(metadata.Tags, ",") != "one,two" || len(metadata.CaptionTracks) != 4 {
		t.Fatalf("normalized arrays = %#v", metadata)
	}
	encoded := string(mustJSON(t, metadata))
	if strings.Contains(encoded, "format_id") || strings.Contains(encoded, "automatic_captions") {
		t.Fatalf("normalized metadata leaked raw extractor data: %s", encoded)
	}
}

func TestSelectCaptionTracksPrefersManualAndHonorsLanguages(t *testing.T) {
	raw := richMetadataFixture()
	defaults := selectCaptionTracks(raw, nil)
	if len(defaults) != 2 || defaults[0].Source != "manual" || defaults[1].Source != "manual" {
		t.Fatalf("default captions = %#v, want all manual tracks", defaults)
	}
	requested := selectCaptionTracks(raw, []string{"fr", "en"})
	if len(requested) != 2 || requested[0].Language != "en" || requested[0].Source != "manual" || requested[1].Language != "fr" || requested[1].Source != "automatic" {
		t.Fatalf("requested captions = %#v", requested)
	}
}

func TestBuildDownloadArgsAddsSelectedCaptionAndThumbnailFlags(t *testing.T) {
	req := downloadRequest{
		URL:    "https://www.youtube.com/watch?v=abc",
		Mode:   "video",
		Ingest: true,
		CaptionTracks: []captionTrack{
			{Language: "en", Source: "manual"},
			{Language: "fr", Source: "automatic"},
		},
	}
	args := strings.Join(buildDownloadArgs(req, "/tmp/job", ""), " ")
	for _, expected := range []string{"--write-thumbnail", "--write-subs", "--write-auto-subs", "--sub-langs en,fr", "--sub-format vtt/best"} {
		if !strings.Contains(args, expected) {
			t.Fatalf("ingestion args missing %q: %s", expected, args)
		}
	}
}

func TestDiscoverSidecarArtifactsClassifiesCaptionAndThumbnail(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "video.mp4")
	metadata := filepath.Join(dir, "metadata.json")
	caption := filepath.Join(dir, "video.en.vtt")
	thumbnail := filepath.Join(dir, "video.webp")
	for _, path := range []string{primary, metadata, caption, thumbnail} {
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	artifacts, err := discoverSidecarArtifacts(dir, primary, metadata, []captionTrack{{Language: "en", Source: "manual"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 3 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	foundCaption, foundThumbnail := false, false
	for _, artifact := range artifacts {
		foundCaption = foundCaption || artifact.Kind == "captions" && artifact.Language == "en" && artifact.CaptionSource == "manual"
		foundThumbnail = foundThumbnail || artifact.Kind == "thumbnail"
	}
	if !foundCaption || !foundThumbnail {
		t.Fatalf("classified artifacts = %#v", artifacts)
	}
}

type ingestionRunner struct {
	mu    sync.Mutex
	calls int
}

type completedMediaPlatform struct {
	tk.BasePlatformClient
	mu    sync.Mutex
	calls []string
}

func (p *completedMediaPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	p.calls = append(p.calls, appName+":"+tool+":"+fmt.Sprint(input["file_id"]))
	p.mu.Unlock()
	var response map[string]any
	switch tool {
	case "media_get":
		response = map[string]any{"found": true}
	case "media_transcribe":
		response = map[string]any{"file_id": "42", "status": "pending"}
	case "media_get_transcript":
		response = map[string]any{"found": true, "transcript": map[string]any{"status": "ok", "language": "en", "text": "Hello"}}
	default:
		return fmt.Errorf("unexpected app call %s:%s", appName, tool)
	}
	body, _ := json.Marshal(response)
	return json.Unmarshal(body, out)
}

func (r *ingestionRunner) Run(_ context.Context, _ string, args []string, stdout func(string), _ func(string)) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if hasArg(args, "--dump-single-json") {
		stdout(string(mustJSONValue(richMetadataFixture())))
		return nil
	}
	jobDir := argAfter(args, "-P")
	if jobDir == "" {
		return fmt.Errorf("download args do not contain -P: %v", args)
	}
	files := map[string]string{
		"A-useful-video-abc123.mp4":    "video",
		"A-useful-video-abc123.en.vtt": "WEBVTT\n\n00:00.000 --> 00:01.000\nHello\n",
		"A-useful-video-abc123.webp":   "thumbnail",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(jobDir, name), []byte(body), 0600); err != nil {
			return err
		}
	}
	stdout("__APTEVA_META__A useful video|youtube")
	stdout("__APTEVA_FILE__" + filepath.Join(jobDir, "A-useful-video-abc123.mp4"))
	return nil
}

func TestIngestMediaCompletesWithAllSourceArtifacts(t *testing.T) {
	var uploadMu sync.Mutex
	uploadNames := map[string]string{}
	nextUploadID := 0
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if req.URL.Query().Get("project_id") != "project-1" {
			http.Error(w, "missing project", http.StatusBadRequest)
			return
		}
		path := strings.TrimPrefix(req.URL.Path, storageBoundProxyPath)
		switch {
		case req.Method == http.MethodPost && path == "/uploads":
			var body struct {
				Filename string `json:"filename"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			uploadMu.Lock()
			nextUploadID++
			uploadID := "upload-" + strconv.Itoa(nextUploadID)
			uploadNames[uploadID] = body.Filename
			uploadMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": uploadID, "part_size": 1024 * 1024})
		case req.Method == http.MethodPut && strings.Contains(path, "/parts/"):
			_, _ = io.Copy(io.Discard, req.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case req.Method == http.MethodPost && strings.HasSuffix(path, "/complete"):
			uploadID := strings.TrimSuffix(strings.TrimPrefix(path, "/uploads/"), "/complete")
			uploadMu.Lock()
			name, ok := uploadNames[uploadID]
			fileID := int64(100 + len(uploadNames))
			uploadMu.Unlock()
			if !ok {
				http.Error(w, "unknown upload", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file": map[string]any{"id": fileID, "url": "storage://" + name}})
		default:
			http.Error(w, "unexpected "+req.Method+" "+path, http.StatusNotFound)
		}
	}))
	defer storage.Close()

	dataDir := t.TempDir()
	emitter := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("project-1"),
		tk.WithEnv("APTEVA_DATA_DIR", dataDir),
		tk.WithEnv("APTEVA_GATEWAY_URL", storage.URL),
		tk.WithEnv("APTEVA_APP_TOKEN", "test-token"),
		tk.WithEmitter(emitter),
	)
	runner := &ingestionRunner{}
	app := &App{
		ctx:       ctx,
		ytdlpPath: "yt-dlp",
		dataDir:   dataDir,
		runner:    runner,
		cancels:   map[string]runningDownload{},
		slots:     make(chan struct{}, 1),
	}
	result, err := app.toolIngest(ctx, map[string]any{
		"url":               "https://www.youtube.com/watch?v=abc123",
		"caption_languages": []any{"en"},
	})
	if err != nil {
		t.Fatal(err)
	}
	queued := result.(map[string]any)["job"].(downloadJob)
	app.wg.Wait()

	job, err := getDownload(t.Context(), ctx.AppDB(), "project-1", queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != statusCompleted || !job.Ingest || job.Metadata == nil {
		t.Fatalf("completed ingestion = %#v", job)
	}
	if job.Metadata.Description != "Full description" || job.Metadata.PublishDate != "2026-09-04" {
		t.Fatalf("stored metadata = %#v", job.Metadata)
	}
	wantKinds := map[string]bool{"video": false, "metadata": false, "thumbnail": false, "captions": false}
	for _, artifact := range job.Artifacts {
		if _, ok := wantKinds[artifact.Kind]; ok {
			wantKinds[artifact.Kind] = true
		}
		if artifact.Kind == "captions" && (artifact.Language != "en" || artifact.CaptionSource != "manual") {
			t.Fatalf("caption artifact = %#v", artifact)
		}
	}
	if len(job.Artifacts) != 4 {
		t.Fatalf("artifacts = %#v, want four", job.Artifacts)
	}
	if len(job.StorageFileIDs) != 4 {
		t.Fatalf("storage_file_ids = %#v, want every artifact ID", job.StorageFileIDs)
	}
	for kind, found := range wantKinds {
		if !found {
			t.Fatalf("missing %s artifact: %#v", kind, job.Artifacts)
		}
	}
	if len(emitter.EventsByTopic("download.completed")) != 1 {
		t.Fatalf("completed events = %#v", emitter.Events())
	}
	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()
	if calls != 2 {
		t.Fatalf("yt-dlp calls = %d, want probe + download", calls)
	}
}

func TestTranscribeWithMediaReturnsCompletedTranscript(t *testing.T) {
	platform := &completedMediaPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-1"), tk.WithPlatform(platform))
	transcript, err := (&App{}).transcribeWithMedia(t.Context(), ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if mapString(transcript, "status") != "ok" || mapString(transcript, "language") != "en" || mapString(transcript, "text") != "Hello" {
		t.Fatalf("transcript = %#v", transcript)
	}
	platform.mu.Lock()
	calls := append([]string(nil), platform.calls...)
	platform.mu.Unlock()
	want := []string{"media:media_get:42", "media:media_transcribe:42", "media:media_get_transcript:42"}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Media calls = %#v, want %#v", calls, want)
	}
}

func hasArg(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

func argAfter(args []string, wanted string) string {
	for i, arg := range args {
		if arg == wanted && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func mustJSONValue(value any) []byte {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
