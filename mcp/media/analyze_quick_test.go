package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestQuickAnalysisDefaultWindowsAndHonestCoverage(t *testing.T) {
	app := newTestCtx(t)
	row := &MediaRow{FileID: "99806", HasVideo: true, HasAudio: true, DurationMs: 600000}
	opts, e := parseAnalysisOptions(app, row, map[string]any{})
	if e != nil || opts.Depth != "quick" || opts.Timeout > 60*time.Second {
		t.Fatal(opts, e)
	}
	windows := quickWindows(opts, row)
	want := []analysisWindow{{0, 2000}, {299000, 301000}, {598000, 600000}}
	if fmt.Sprint(windows) != fmt.Sprint(want) {
		t.Fatal(windows)
	}
	r := baseAnalysisResult(row, opts)
	if r.Analysis.Complete || r.Analysis.AnalyzedDurationMs != 6000 || r.Analysis.Ratio != .01 || len(r.Analysis.Limitations) != 3 {
		t.Fatal(r.Analysis)
	}
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "media_analyze" {
			if tool.HandlerCtx == nil || tool.InputSchema["x-apteva-timeout-ms"] != 340000 {
				t.Fatal("missing cancellation/timeout contract")
			}
			return
		}
	}
	t.Fatal("missing analysis tool")
}

func TestQuickAnalysisSinglePassNativeCoverageAndAudio(t *testing.T) {
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg unavailable")
	}
	source := guardedFixture(t, true)
	row := &MediaRow{FileID: "1", HasVideo: true, HasAudio: true, DurationMs: 5000}
	opts := analysisOptions{Depth: "quick", StartMs: 1017, EndMs: 3017, SilenceThresholdDB: -50, SilenceMinMs: 1000, Timeout: 30 * time.Second, DecoderThreads: 2}
	args := analysisFFmpegArgs(source, row, opts)
	if strings.Count(strings.Join(args, " "), " -i ") != 1 {
		t.Fatal("more than one read")
	}
	app := newTestCtx(t)
	q, e := analyzeExistingSourceLocal(app, source, row, opts)
	if e != nil || !q.DecodeOK || len(q.Samples) != 1 || q.Visual == nil || q.Audio == nil {
		t.Fatal(q, e)
	}
	v := q.Samples[0].Visual.FrameValidation
	if v == nil || v.FramesChecked != 60 || !v.WindowStartCovered {
		t.Fatal("native frame/preroll geometry lost", v)
	}
	if q.Audio.IntegratedLUFS != nil || q.Samples[0].Audio.IntegratedLUFS == nil {
		t.Fatal("sample loudness confused with programme loudness")
	}
	if q.Diagnostics.InputPasses != 1 || q.Diagnostics.DecodeThreads != 2 {
		t.Fatal(q.Diagnostics)
	}
	// Native and reduced-resolution black checks agree on an actual opening black
	// segment, including a black segment shorter than half a second.
	black := filepath.Join(t.TempDir(), "black.mp4")
	command := exec.Command("ffmpeg", "-y", "-v", "error", "-i", source, "-vf", "drawbox=x=0:y=0:w=iw:h=ih:color=black:t=fill:enable=lt(t\\,0.2)", "-c:v", "libx264", "-threads", "1", "-an", black)
	if b, e := command.CombinedOutput(); e != nil {
		t.Fatal(e, string(b))
	}
	vrow := &MediaRow{HasVideo: true, DurationMs: 5000}
	bo := opts
	bo.StartMs = 0
	bo.EndMs = 1000
	q, e = analyzeExistingSourceLocal(app, black, vrow, bo)
	if e != nil || len(q.Visual.BlackSegments) == 0 || !q.Samples[0].Visual.FrameValidation.OpeningBlack {
		t.Fatal("short black segment missed", q, e)
	}
}

func TestQuickAnalysisRemoteScriptAndCacheNoFullDownload(t *testing.T) {
	source := guardedFixture(t, true)
	info, _ := os.Stat(source)
	sha, _ := sha256File(source)
	meta := &StorageFile{ID: 777, Name: "sample.mov", SizeBytes: info.Size(), SHA256: sha}
	row := &MediaRow{HasVideo: true, HasAudio: true, DurationMs: 5000}
	opts := analysisOptions{Depth: "quick", EndMs: 5000, SilenceThresholdDB: -50, SilenceMinMs: 1000, DecoderThreads: 2, SourceMeta: meta}
	root := t.TempDir()
	name := remoteSourceCacheName("777", meta.Name, sha, meta.SizeBytes)
	data, _ := os.ReadFile(source)
	_ = os.WriteFile(filepath.Join(root, name), data, 0600)
	script := buildRemoteAnalysisScript("ffmpeg", source, row, opts)
	// Even when a cache is cold, quick checks never call materialize_source or
	// curl -o a complete multi-GB input. Existing verified blobs are reusable.
	if strings.Contains(script, "materialize_source '777'") {
		t.Fatal("quick path downloads entire source")
	}
	script = strings.ReplaceAll(script, remoteSourceCacheRoot, root)
	raw, e := exec.Command("bash", "-c", script).CombinedOutput()
	if e != nil {
		t.Fatal(e, string(raw))
	}
	wire, e := parseRemoteAnalysisResult(string(raw))
	if e != nil || !wire.SourceCacheHit || len(wire.Samples) != 1 || wire.Samples[0].Exit != 0 {
		t.Fatal(wire, e, string(raw))
	}
	// Cache corruption is rejected; the existing source is read by bounded seek.
	_ = os.WriteFile(filepath.Join(root, name), []byte("bad"), 0600)
	raw, e = exec.Command("bash", "-c", script).CombinedOutput()
	if e != nil {
		t.Fatal(e, string(raw))
	}
	wire, e = parseRemoteAnalysisResult(string(raw))
	if e != nil || wire.SourceCacheHit || wire.Samples[0].Exit != 0 {
		t.Fatal(wire, e)
	}
}

func TestAnalysisResultCacheAndSourceIdentity(t *testing.T) {
	app := newTestCtx(t)
	probe := sampleVideoProbe()
	_ = upsertMedia(app.AppDB(), testProj, "45", probe, strings.Repeat("a", 64), "/", "source.mov")
	row, _ := getMedia(app.AppDB(), testProj, "45")
	opts, _ := parseAnalysisOptions(app, row, map[string]any{})
	source := &StorageFile{ID: 45, SHA256: row.SourceSHA256, SizeBytes: 500}
	k := analysisCacheKey(testProj, row, opts, source)
	r := baseAnalysisResult(row, opts)
	r.Technical["decode_ok"] = true
	saveAnalysisCache(app.AppDB(), testProj, k, row, r)
	if _, ok := loadAnalysisCache(app.AppDB(), testProj, k); !ok {
		t.Fatal("cached result missing")
	}
	if _, ok := loadAnalysisCache(app.AppDB(), "other-project", k); ok {
		t.Fatal("cross-project cache leak")
	}
	source.SHA256 = strings.Repeat("b", 64)
	if analysisCacheKey(testProj, row, opts, source) == k {
		t.Fatal("changed source reused result")
	}
}

func TestAnalysisCancellationVerifiedAndFailureVisible(t *testing.T) {
	original := runRemoteCancellation
	defer func() { runRemoteCancellation = original }()
	app := newTestCtx(t)
	for _, fail := range []bool{false, true} {
		calls := 0
		runRemoteCancellation = func(_ context.Context, _ *sdk.AppCtx, _ int64, script string, _ int) (string, int, error) {
			calls++
			if !strings.Contains(script, "live_group") || !strings.Contains(script, "analysis-test") {
				t.Fatal("unsafe cancellation")
			}
			if fail {
				return "", 1, nil
			}
			return "REMOTE_CANCEL_CONFIRMED", 0, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		finish := watchAnalysisRemoteCancellation(ctx, app, 1, "/var/tmp/analysis-test")
		cancel()
		e := finish()
		if calls != 1 || (e != nil) != fail {
			t.Fatal(calls, e)
		}
	}
}

func TestAnalysisToolCacheHitSkipsSigningAndHeavyAdmission(t *testing.T) {
	app := newTestCtx(t)
	probe := sampleVideoProbe()
	sha := strings.Repeat("a", 64)
	_ = upsertMedia(app.AppDB(), testProj, "45", probe, sha, "/", "source.mov")
	meta := &StorageFile{ID: 45, SHA256: sha, SizeBytes: 500}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files/45") {
			json.NewEncoder(w).Encode(map[string]any{"file": meta})
			return
		}
		t.Error("cache hit signed/downloaded source")
		http.Error(w, "unneeded", 500)
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	row, _ := getMedia(app.AppDB(), testProj, "45")
	opts, _ := parseAnalysisOptions(app, row, map[string]any{})
	saveAnalysisCache(app.AppDB(), testProj, analysisCacheKey(testProj, row, opts, meta), row, baseAnalysisResult(row, opts))
	out, e := (&App{}).toolAnalyzeContext(context.Background(), app, map[string]any{"_project_id": testProj, "file_id": "45"})
	if e != nil || out.(map[string]any)["result_cache_hit"] != true {
		t.Fatal(out, e)
	}
}

// The production fixture is private and loaded only when explicitly supplied.
func TestExportProductionQuickAnalysisScript(t *testing.T) {
	path := os.Getenv("MEDIA_ANALYSIS_EXPORT_FIXTURE")
	if path == "" {
		t.Skip("private export fixture not supplied")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Row    MediaRow    `json:"row"`
		Source StorageFile `json:"source"`
		URL    string      `json:"url"`
		Output string      `json:"output"`
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	opts := analysisOptions{Depth: "quick", EndMs: fixture.Row.DurationMs, SilenceThresholdDB: -50, SilenceMinMs: 1000, DecoderThreads: 2, SourceMeta: &fixture.Source}
	script := buildRemoteAnalysisScript("ffmpeg", fixture.URL, &fixture.Row, opts)
	if e = os.WriteFile(fixture.Output, []byte(script), 0600); e != nil {
		t.Fatal(e)
	}
}
