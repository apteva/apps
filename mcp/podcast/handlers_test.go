package main

// Tier 1 — every MCP tool handler exercised against an in-memory
// SQLite via testkit's NewAppCtx. The cross-app dependency wiring
// (storage + media probe, routes/domains hostname claim, analytics)
// runs against a recording PlatformClient stub so the tier-degradation
// behaviour is pinned: hard deps must succeed, soft deps must fail
// quietly. Fast — whole suite well under a second, runs every commit.

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── recording PlatformClient stub ─────────────────────────────────
//
// Embeds testkit.BasePlatformClient so new PlatformClient methods
// don't break this file. Only CallApp/CallAppResult are overridden —
// that's the whole surface integration.go touches. Fixtures are fed
// pre-unwrapped (no JSON-RPC envelope), so CallAppResult is a single
// json.Unmarshal — see CLAUDE.md's note on stub envelope handling.

type callRecord struct {
	App, Tool string
	Input     map[string]any
}

type recordingPlatform struct {
	tk.BasePlatformClient
	mu        sync.Mutex
	calls     []callRecord
	responses map[string]json.RawMessage // "app/tool" -> inner JSON
	errs      map[string]error           // "app/tool" -> forced error
}

var _ sdk.PlatformClient = (*recordingPlatform)(nil)

func newRecordingPlatform() *recordingPlatform {
	return &recordingPlatform{
		responses: map[string]json.RawMessage{},
		errs:      map[string]error{},
	}
}

// on pre-loads the canned inner JSON returned for an (app, tool) call.
func (p *recordingPlatform) on(app, tool, innerJSON string) *recordingPlatform {
	p.responses[app+"/"+tool] = json.RawMessage(innerJSON)
	return p
}

// fail makes an (app, tool) call return err — used to simulate an
// uninstalled / unreachable dependency.
func (p *recordingPlatform) fail(app, tool string, err error) *recordingPlatform {
	p.errs[app+"/"+tool] = err
	return p
}

func (p *recordingPlatform) CallApp(app, tool string, in map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	p.calls = append(p.calls, callRecord{App: app, Tool: tool, Input: in})
	p.mu.Unlock()
	key := app + "/" + tool
	if err := p.errs[key]; err != nil {
		return nil, err
	}
	return p.responses[key], nil
}

func (p *recordingPlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	raw, err := p.CallApp(app, tool, in)
	if err != nil {
		return err
	}
	if len(raw) == 0 || out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (p *recordingPlatform) callsTo(app, tool string) []callRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []callRecord
	for _, c := range p.calls {
		if c.App == app && c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// ─── fixtures ──────────────────────────────────────────────────────

func newTestCtx(t *testing.T, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	full := append([]tk.Option{tk.WithProjectID("test-proj")}, opts...)
	return tk.NewAppCtx(t, "apteva.yaml", full...)
}

func mustShow(t *testing.T, ctx *sdk.AppCtx, args map[string]any) *Show {
	t.Helper()
	out, err := (&App{}).toolShowCreate(ctx, args)
	if err != nil {
		t.Fatalf("show_create %v: %v", args, err)
	}
	return out.(map[string]any)["show"].(*Show)
}

func mustEpisode(t *testing.T, ctx *sdk.AppCtx, args map[string]any) *Episode {
	t.Helper()
	out, err := (&App{}).toolEpisodeCreate(ctx, args)
	if err != nil {
		t.Fatalf("episode_create %v: %v", args, err)
	}
	return out.(map[string]any)["episode"].(*Episode)
}

// ─── show + episode CRUD ───────────────────────────────────────────

func TestToolShowCRUD(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	// Create needs no platform: with an empty hostname, wireHostname
	// returns early before touching PlatformAPI().
	show := mustShow(t, ctx, map[string]any{"title": "My Show", "author": "Marco"})
	if show.Slug != "my-show" || show.Language != "en" {
		t.Errorf("defaults wrong: %+v", show)
	}

	out, err := app.toolShowUpdate(ctx, map[string]any{"id": float64(show.ID), "author": "Marco S."})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.(map[string]any)["show"].(*Show).Author != "Marco S." {
		t.Error("update not applied")
	}

	got, err := app.toolShowGet(ctx, map[string]any{"id": float64(show.ID)})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.(map[string]any)["feed_url"].(string) == "" {
		t.Error("show_get should return a feed_url")
	}

	listed, _ := app.toolShowList(ctx, map[string]any{})
	if listed.(map[string]any)["count"].(int) != 1 {
		t.Errorf("list count = %v, want 1", listed.(map[string]any)["count"])
	}

	if _, err := app.toolShowDelete(ctx, map[string]any{"id": float64(show.ID)}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := app.toolShowGet(ctx, map[string]any{"id": float64(show.ID)}); err == nil {
		t.Error("expected error fetching deleted show")
	}
}

func TestToolEpisodeCRUD(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	show := mustShow(t, ctx, map[string]any{"title": "Show"})

	ep := mustEpisode(t, ctx, map[string]any{
		"show_id": float64(show.ID), "title": "Ep 1", "episode_type": "trailer",
	})
	if ep.Status != "draft" || ep.EpisodeType != "trailer" || ep.GUID == "" {
		t.Errorf("new episode wrong: %+v", ep)
	}

	listed, _ := app.toolEpisodeList(ctx, map[string]any{"show_id": float64(show.ID)})
	if listed.(map[string]any)["count"].(int) != 1 {
		t.Errorf("episode list count = %v, want 1", listed.(map[string]any)["count"])
	}

	if _, err := app.toolEpisodeDelete(ctx, map[string]any{"id": float64(ep.ID)}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := app.toolEpisodeGet(ctx, map[string]any{"id": float64(ep.ID)}); err == nil {
		t.Error("expected error fetching deleted episode")
	}
}

// ─── hard dependencies: storage + media probe ──────────────────────

func TestEpisodeSetAudio_ProbesStorageAndMedia(t *testing.T) {
	pf := newRecordingPlatform().
		on("storage", "files_get", `{"found":true,"file":{"size_bytes":5500000,"content_type":"audio/mpeg","url":"https://cdn.example.com/ep.mp3","visibility":"public"}}`).
		on("media", "media_get", `{"found":true,"media":{"duration_ms":1230000,"has_audio":true}}`)
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}
	show := mustShow(t, ctx, map[string]any{"title": "Show"})
	ep := mustEpisode(t, ctx, map[string]any{"show_id": float64(show.ID), "title": "Ep"})

	out, err := app.toolEpisodeSetAudio(ctx, map[string]any{
		"id": float64(ep.ID), "audio_file_id": "42",
	})
	if err != nil {
		t.Fatalf("set_audio: %v", err)
	}
	res := out.(map[string]any)
	got := res["episode"].(*Episode)
	if got.AudioBytes != 5_500_000 || got.DurationSeconds != 1230 || got.MimeType != "audio/mpeg" {
		t.Errorf("probe not cached onto episode: %+v", got)
	}
	if got.AudioURL != "https://cdn.example.com/ep.mp3" {
		t.Errorf("audio_url = %q", got.AudioURL)
	}
	if res["warning"].(string) != "" {
		t.Errorf("clean probe should have no warning, got %q", res["warning"])
	}

	// Both hard deps must have been consulted, with the file id passed through.
	if c := pf.callsTo("storage", "files_get"); len(c) != 1 {
		t.Fatalf("storage.files_get called %d times, want 1", len(c))
	}
	if c := pf.callsTo("media", "media_get"); len(c) != 1 {
		t.Fatalf("media.media_get called %d times, want 1", len(c))
	} else if c[0].Input["file_id"] != "42" {
		t.Errorf("media.media_get file_id = %v, want 42", c[0].Input["file_id"])
	}
}

func TestEpisodeSetAudio_MediaNotProbedYet_Warns(t *testing.T) {
	// media's indexer probes asynchronously — a just-uploaded file may
	// not have a duration yet. That's a warning, not a failure.
	pf := newRecordingPlatform().
		on("storage", "files_get", `{"found":true,"file":{"size_bytes":900,"content_type":"audio/mpeg","url":"https://cdn/x.mp3","visibility":"public"}}`).
		on("media", "media_get", `{"found":false}`)
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	show := mustShow(t, ctx, map[string]any{"title": "Show"})
	ep := mustEpisode(t, ctx, map[string]any{"show_id": float64(show.ID), "title": "Ep"})

	out, err := (&App{}).toolEpisodeSetAudio(ctx, map[string]any{
		"id": float64(ep.ID), "audio_file_id": "1",
	})
	if err != nil {
		t.Fatalf("set_audio should not error when media hasn't probed: %v", err)
	}
	res := out.(map[string]any)
	if res["episode"].(*Episode).DurationSeconds != 0 {
		t.Error("duration should stay 0 when media hasn't probed")
	}
	if w := res["warning"].(string); !strings.Contains(w, "media") {
		t.Errorf("warning should mention media, got %q", w)
	}
}

func TestEpisodeSetAudio_NonPublicStorage_Warns(t *testing.T) {
	pf := newRecordingPlatform().
		on("storage", "files_get", `{"found":true,"file":{"size_bytes":900,"content_type":"audio/mpeg","url":"https://cdn/x.mp3","visibility":"private"}}`).
		on("media", "media_get", `{"found":true,"media":{"duration_ms":60000,"has_audio":true}}`)
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	show := mustShow(t, ctx, map[string]any{"title": "Show"})
	ep := mustEpisode(t, ctx, map[string]any{"show_id": float64(show.ID), "title": "Ep"})

	out, _ := (&App{}).toolEpisodeSetAudio(ctx, map[string]any{
		"id": float64(ep.ID), "audio_file_id": "1",
	})
	if w := out.(map[string]any)["warning"].(string); !strings.Contains(w, "public") {
		t.Errorf("non-public enclosure should warn about visibility, got %q", w)
	}
}

func TestEpisodeSetAudio_StorageMissing_IsHardError(t *testing.T) {
	// storage is a hard dependency — if it can't resolve the file there
	// is no enclosure, so this must fail rather than degrade.
	pf := newRecordingPlatform().fail("storage", "files_get", errors.New("storage unreachable"))
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	show := mustShow(t, ctx, map[string]any{"title": "Show"})
	ep := mustEpisode(t, ctx, map[string]any{"show_id": float64(show.ID), "title": "Ep"})

	if _, err := (&App{}).toolEpisodeSetAudio(ctx, map[string]any{
		"id": float64(ep.ID), "audio_file_id": "1",
	}); err == nil {
		t.Fatal("expected a hard error when storage is unreachable")
	}
}

// ─── soft dependencies: routes + domains hostname wiring ───────────

func TestShowCreate_WithHostname_WiresRoutes(t *testing.T) {
	pf := newRecordingPlatform() // domains/domain_get returns nil -> CNAME skipped silently
	ctx := newTestCtx(t, tk.WithPlatform(pf), tk.WithEnv("APTEVA_INSTALL_ID", "5"))

	out, err := (&App{}).toolShowCreate(ctx, map[string]any{
		"title": "Hosted Show", "hostname": "feeds.test.com",
	})
	if err != nil {
		t.Fatalf("show_create: %v", err)
	}
	if w := out.(map[string]any)["warning"].(string); w != "" {
		t.Errorf("clean wiring should have no warning, got %q", w)
	}
	calls := pf.callsTo("routes", "routes_register")
	if len(calls) != 1 {
		t.Fatalf("routes.routes_register called %d times, want 1", len(calls))
	}
	if calls[0].Input["hostname"] != "feeds.test.com" {
		t.Errorf("routes_register hostname = %v", calls[0].Input["hostname"])
	}
	if tgt, _ := calls[0].Input["target"].(string); !strings.HasPrefix(tgt, "http://127.0.0.1") {
		t.Errorf("routes_register target = %q, want loopback", tgt)
	}
}

func TestShowCreate_HostnameWiringFails_ShowStillCreated(t *testing.T) {
	// routes is a soft dependency: a wiring failure surfaces as a
	// warning but must NOT roll back the show write.
	pf := newRecordingPlatform().fail("routes", "routes_register", errors.New("routes unreachable"))
	ctx := newTestCtx(t, tk.WithPlatform(pf), tk.WithEnv("APTEVA_INSTALL_ID", "5"))
	app := &App{}

	out, err := app.toolShowCreate(ctx, map[string]any{
		"title": "Hosted Show", "hostname": "feeds.test.com",
	})
	if err != nil {
		t.Fatalf("show_create should not fail on wiring error: %v", err)
	}
	res := out.(map[string]any)
	if w := res["warning"].(string); !strings.Contains(w, "routes:") {
		t.Errorf("expected a routes wiring warning, got %q", w)
	}
	// The show is persisted regardless.
	show := res["show"].(*Show)
	if _, err := app.toolShowGet(ctx, map[string]any{"id": float64(show.ID)}); err != nil {
		t.Errorf("show should still be readable after wiring failure: %v", err)
	}
}

// ─── episode lifecycle ─────────────────────────────────────────────

func TestEpisodePublishLifecycle(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	show := mustShow(t, ctx, map[string]any{"title": "Show"})
	ep := mustEpisode(t, ctx, map[string]any{"show_id": float64(show.ID), "title": "Ep"})

	// Publish without a probed enclosure must be rejected.
	if _, err := app.toolEpisodePublish(ctx, map[string]any{"id": float64(ep.ID)}); err == nil {
		t.Fatal("expected publish to fail with no audio attached")
	}

	// Attach audio directly (probe path is covered above), then publish.
	if err := dbSetEpisodeAudio(ctx.AppDB(), ep.ID, "1", "https://cdn/x.mp3", 1000, 90, "audio/mpeg"); err != nil {
		t.Fatalf("attach audio: %v", err)
	}
	out, err := app.toolEpisodePublish(ctx, map[string]any{"id": float64(ep.ID)})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := out.(map[string]any)["episode"].(*Episode); got.Status != "published" || got.PublishedAt == nil {
		t.Errorf("episode not published: %+v", got)
	}

	// Unpublish drops it back to draft and out of the feed.
	out, err = app.toolEpisodeUnpublish(ctx, map[string]any{"id": float64(ep.ID)})
	if err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if out.(map[string]any)["episode"].(*Episode).Status != "draft" {
		t.Error("unpublish should return episode to draft")
	}
}

func TestEpisodeSchedule(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	show := mustShow(t, ctx, map[string]any{"title": "Show"})
	ep := mustEpisode(t, ctx, map[string]any{"show_id": float64(show.ID), "title": "Ep"})
	if err := dbSetEpisodeAudio(ctx.AppDB(), ep.ID, "1", "https://cdn/x.mp3", 1000, 90, "audio/mpeg"); err != nil {
		t.Fatalf("attach audio: %v", err)
	}

	// A past timestamp is rejected — episode_publish is the now path.
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	if _, err := app.toolEpisodeSchedule(ctx, map[string]any{"id": float64(ep.ID), "publish_at": past}); err == nil {
		t.Error("expected scheduling in the past to be rejected")
	}

	// A future timestamp moves the episode to scheduled.
	future := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	out, err := app.toolEpisodeSchedule(ctx, map[string]any{"id": float64(ep.ID), "publish_at": future})
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	got := out.(map[string]any)["episode"].(*Episode)
	if got.Status != "scheduled" || got.PublishAt == nil {
		t.Errorf("episode not scheduled: %+v", got)
	}
}

// ─── feed validation ───────────────────────────────────────────────

func TestFeedValidate_FlagsDirectoryRejectionRisks(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	// A bare show with one audio-less episode — lots wrong.
	show := mustShow(t, ctx, map[string]any{"title": "Bare Show"})
	mustEpisode(t, ctx, map[string]any{"show_id": float64(show.ID), "title": "No Audio Ep"})

	out, err := app.toolFeedValidate(ctx, map[string]any{"show_id": float64(show.ID)})
	if err != nil {
		t.Fatalf("feed_validate: %v", err)
	}
	res := out.(map[string]any)
	if res["ok"].(bool) {
		t.Fatal("expected ok=false for an incomplete feed")
	}
	joined := strings.Join(toStringSlice(res["issues"]), " | ")
	for _, want := range []string{"owner_email", "category", "no audio", "no published episodes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("issues should mention %q; got: %s", want, joined)
		}
	}
}

func toStringSlice(v any) []string {
	raw, _ := v.([]string)
	return raw
}
