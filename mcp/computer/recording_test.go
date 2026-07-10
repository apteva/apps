package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"github.com/apteva/apps/mcp/computer/internal/browser/replay"
)

func TestSessionHistoryPersistsProviderIDBeforeClose(t *testing.T) {
	previous := newBackend
	t.Cleanup(func() { newBackend = previous })

	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{reg: &registry{m: map[string]*session{}}}
	fake := &historyFakeComp{sessionID: "provider-session-1", url: "https://example.test/final"}
	newBackend = func(backends.Config) (backends.Computer, error) { return fake, nil }

	opened, err := app.toolBrowserSession(ctx, map[string]any{
		"action": "open", "backend": "browserbase", "url": "https://example.test/initial",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := opened.(map[string]any)["session_id"].(string)
	fake.closeHook = func() error {
		row, err := dbGetSession(ctx.AppDB(), id)
		if err != nil {
			return err
		}
		if row.BackendSessionID != "provider-session-1" || row.Status != "active" {
			return fmt.Errorf("history before Close = %+v", row)
		}
		return nil
	}

	if _, err := app.toolBrowserClose(ctx, map[string]any{"session_id": id}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if fake.sessionID != "" {
		t.Fatalf("fake provider id was not cleared by Close: %q", fake.sessionID)
	}
	row, err := dbGetSession(ctx.AppDB(), id)
	if err != nil {
		t.Fatalf("history after close: %v", err)
	}
	if row.BackendSessionID != "provider-session-1" || row.Status != "closed" || row.CloseReason != "explicit_close" || row.RecordingStatus != "processing" || row.ClosedAt == nil {
		t.Fatalf("history after close = %+v", row)
	}
}

func TestSessionHistoryCoversReapUnhealthyUnmountAndRestart(t *testing.T) {
	previous := newBackend
	t.Cleanup(func() { newBackend = previous })
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	open := func(t *testing.T, app *App, fake *historyFakeComp) string {
		newBackend = func(backends.Config) (backends.Computer, error) { return fake, nil }
		out, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return out.(map[string]any)["session_id"].(string)
	}

	reapApp := &App{reg: &registry{m: map[string]*session{}}}
	reapID := open(t, reapApp, &historyFakeComp{sessionID: "provider-reap", url: "https://reap.test"})
	reapApp.reg.mu.Lock()
	reapApp.reg.m[reapID].lastUsed = time.Now().Add(-time.Hour)
	reapApp.reg.mu.Unlock()
	if rows := reapApp.reapIdleSessions(ctx, 30*time.Minute); len(rows) != 1 {
		t.Fatalf("reaped rows = %+v", rows)
	}
	assertHistoryStatus(t, ctx, reapID, "reaped", "idle_timeout")

	unhealthyApp := &App{reg: &registry{m: map[string]*session{}}}
	unhealthyID := open(t, unhealthyApp, &historyFakeComp{
		sessionID:  "provider-unhealthy",
		url:        "https://unhealthy.test",
		executeErr: errors.New("target closed while capturing screenshot"),
	})
	if _, err := unhealthyApp.toolComputerUse(ctx, map[string]any{"session_id": unhealthyID, "action": "screenshot"}); err == nil {
		t.Fatal("unhealthy computer_use unexpectedly succeeded")
	}
	assertHistoryStatus(t, ctx, unhealthyID, "failed", "session_unhealthy")

	unmountApp := &App{reg: &registry{m: map[string]*session{}}}
	unmountID := open(t, unmountApp, &historyFakeComp{sessionID: "provider-unmount", url: "https://unmount.test"})
	if err := unmountApp.OnUnmount(ctx); err != nil {
		t.Fatalf("OnUnmount: %v", err)
	}
	assertHistoryStatus(t, ctx, unmountID, "interrupted", "app_unmount")

	stamp := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	orphan := &ComputerSession{
		ID: "br_orphan", Backend: "browserbase", BackendSessionID: "provider-orphan",
		Status: "active", RecordingStatus: "recording", OpenedAt: stamp, UpdatedAt: stamp,
	}
	if err := dbPutSession(ctx.AppDB(), orphan); err != nil {
		t.Fatalf("put orphan: %v", err)
	}
	mounted := &App{}
	if err := mounted.OnMount(ctx); err != nil {
		t.Fatalf("OnMount: %v", err)
	}
	t.Cleanup(func() {
		_ = mounted.OnUnmount(ctx)
		globalCtx = nil
	})
	assertHistoryStatus(t, ctx, orphan.ID, "interrupted", "app_restart")
}

func TestBrowserRecordingStatusesAndReadyEvent(t *testing.T) {
	previous := newReplayResolver
	t.Cleanup(func() { newReplayResolver = previous })
	recorder := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(recorder)).WithProject("project-1")
	app := &App{reg: &registry{m: map[string]*session{}}}

	recent := putRecordingHistory(t, ctx, "br_recent", "browserbase", time.Now().Add(-time.Minute))
	resolver := &fakeReplayResolver{metadataErr: replay.ErrNotFound}
	newReplayResolver = func(*sdk.AppCtx, string) (replay.Resolver, error) { return resolver, nil }
	out, err := app.toolBrowserRecording(ctx, map[string]any{"session_id": recent.ID})
	if err != nil || out.(map[string]any)["status"] != "processing" {
		t.Fatalf("recent recording = %#v, %v", out, err)
	}

	old := putRecordingHistory(t, ctx, "br_old", "browserbase", time.Now().Add(-recordingProcessingWindow-time.Minute))
	out, err = app.toolBrowserRecording(ctx, map[string]any{"session_id": old.ID})
	if err != nil || out.(map[string]any)["status"] != "unavailable" {
		t.Fatalf("old recording = %#v, %v", out, err)
	}

	ready := putRecordingHistory(t, ctx, "br_ready", "browserbase", time.Now().Add(-time.Minute))
	resolver.metadataErr = nil
	resolver.metadata = replay.Recording{
		Supported: true,
		Status:    "ready",
		Streams: []replay.RecordingStream{
			{ID: "0", StartMS: 0, EndMS: 1200, SourceURL: "https://api.browserbase.test/private"},
			{ID: "1", StartMS: 250, EndMS: 1200, SourceURL: "https://api.browserbase.test/private-2"},
		},
	}
	out, err = app.toolBrowserRecording(ctx, map[string]any{"session_id": ready.ID})
	if err != nil {
		t.Fatalf("ready recording: %v", err)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "api.browserbase.test") || !strings.Contains(string(encoded), "/api/apps/computer/sessions/br_ready/recording/1.m3u8") || !strings.Contains(string(encoded), "project_id=project-1") {
		t.Fatalf("ready output = %s", encoded)
	}
	event := lastEventData(t, recorder, "recording.ready")
	if event["session_id"] != ready.ID || event["backend_session_id"] != ready.BackendSessionID {
		t.Fatalf("recording.ready = %#v", event)
	}

	unsupported := putRecordingHistory(t, ctx, "br_local", "local", time.Now().Add(-time.Minute))
	out, err = app.toolBrowserRecording(ctx, map[string]any{"session_id": unsupported.ID})
	if err != nil || out.(map[string]any)["status"] != "unsupported" {
		t.Fatalf("unsupported recording = %#v, %v", out, err)
	}
}

func TestUISessionListIncludesHistoryWithoutChangingAgentList(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	now := time.Now().UTC()
	app := &App{reg: &registry{m: map[string]*session{
		"br_live": {
			comp:     &historyFakeComp{sessionID: "provider-live", url: "https://live.test"},
			backend:  "browserbase",
			openedAt: now,
			lastUsed: now,
		},
	}}}
	liveStamp := now.Format(time.RFC3339Nano)
	if err := dbPutSession(ctx.AppDB(), &ComputerSession{
		ID: "br_live", Backend: "browserbase", BackendSessionID: "provider-live",
		Status: "active", RecordingStatus: "recording", OpenedAt: liveStamp, UpdatedAt: liveStamp,
	}); err != nil {
		t.Fatalf("put active history: %v", err)
	}
	past := putRecordingHistory(t, ctx, "br_past", "browserbase", now.Add(-time.Minute))
	past.RecordingStatus = "ready"
	if err := dbPutSession(ctx.AppDB(), past); err != nil {
		t.Fatalf("update past history: %v", err)
	}

	uiRows, err := app.listUISessions(ctx, 100)
	if err != nil {
		t.Fatalf("list UI sessions: %v", err)
	}
	if len(uiRows) != 2 || uiRows[0].SessionID != "br_live" || uiRows[1].SessionID != "br_past" {
		t.Fatalf("UI sessions = %+v", uiRows)
	}
	if uiRows[1].Status != "closed" || uiRows[1].RecordingStatus != "ready" || uiRows[1].ClosedAt == "" {
		t.Fatalf("past UI session = %+v", uiRows[1])
	}
	agentRows := app.listSessions()
	if len(agentRows) != 1 || agentRows[0].SessionID != "br_live" {
		t.Fatalf("agent sessions = %+v", agentRows)
	}
}

func TestRecordingHTTPRoutesProxyPlaylistsWithoutCredentials(t *testing.T) {
	previous := newReplayResolver
	previousGlobal := globalCtx
	t.Cleanup(func() {
		newReplayResolver = previous
		globalCtx = previousGlobal
	})

	ctx := tk.NewAppCtx(t, "apteva.yaml")
	globalCtx = ctx
	app := &App{reg: &registry{m: map[string]*session{}}}
	row := putRecordingHistory(t, ctx, "br_http", "browserbase", time.Now().Add(-time.Minute))
	resolver := &fakeResourceReplayResolver{
		fakeReplayResolver: fakeReplayResolver{
			metadata: replay.Recording{Supported: true, Status: "ready", Streams: []replay.RecordingStream{{ID: "0"}}},
			playlist: []byte("#EXTM3U\nhttps://cdn.browserbase.test/segment.ts?signature=signed\n"),
		},
		proxyContains: "cdn.browserbase.test",
	}
	newReplayResolver = func(*sdk.AppCtx, string) (replay.Resolver, error) { return resolver, nil }

	metadataRequest := httptest.NewRequest(http.MethodGet, "/sessions/"+row.ID+"/recording?project_id=project-http", nil)
	metadataResponse := httptest.NewRecorder()
	app.handleRecordingMetadata(metadataResponse, metadataRequest)
	if metadataResponse.Code != http.StatusOK || strings.Contains(metadataResponse.Body.String(), "provider-http") || !strings.Contains(metadataResponse.Body.String(), "project_id=project-http") {
		t.Fatalf("metadata status=%d body=%s", metadataResponse.Code, metadataResponse.Body.String())
	}

	playlistRequest := httptest.NewRequest(http.MethodGet, "/sessions/"+row.ID+"/recording/0.m3u8?project_id=project-http", nil)
	playlistResponse := httptest.NewRecorder()
	app.handleRecordingPlaylist(playlistResponse, playlistRequest)
	if playlistResponse.Code != http.StatusOK || playlistResponse.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" || playlistResponse.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("playlist status=%d headers=%v body=%s", playlistResponse.Code, playlistResponse.Header(), playlistResponse.Body.String())
	}
	if strings.Contains(playlistResponse.Body.String(), "secret") || strings.Contains(playlistResponse.Body.String(), "cdn.browserbase.test") || !strings.Contains(playlistResponse.Body.String(), "/recording/0/asset?") {
		t.Fatalf("playlist body = %s", playlistResponse.Body.String())
	}
}

func TestHTTPRoutePatternsRegister(t *testing.T) {
	mux := http.NewServeMux()
	for _, route := range (&App{}).HTTPRoutes() {
		pattern := route.Pattern
		if route.Method != "" {
			pattern = route.Method + " " + pattern
		}
		mux.HandleFunc(pattern, route.Handler)
	}
}

func TestRewriteAuthenticatedPlaylistUsesAppOwnedTokens(t *testing.T) {
	resolver := &fakeResourceReplayResolver{fakeReplayResolver: fakeReplayResolver{}, proxyContains: "api.steel.test"}
	body, err := rewriteAuthenticatedPlaylist(
		[]byte("#EXTM3U\nhttps://api.steel.test/v1/media/child.m3u8\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://api.steel.test/v1/media/key\"\nhttps://cdn.steel.test/signed.ts\n"),
		resolver,
		"provider-steel",
		func(token string) string { return "/sessions/br_steel/recording/0/asset?token=" + token },
	)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got := string(body)
	if strings.Contains(got, "api.steel.test") || strings.Count(got, "token=opaque-") != 2 || !strings.Contains(got, "https://cdn.steel.test/signed.ts") {
		t.Fatalf("rewritten playlist = %q", got)
	}
}

func assertHistoryStatus(t *testing.T, ctx *sdk.AppCtx, id, status, reason string) {
	t.Helper()
	row, err := dbGetSession(ctx.AppDB(), id)
	if err != nil {
		t.Fatalf("get history %s: %v", id, err)
	}
	if row.Status != status || row.CloseReason != reason || row.BackendSessionID == "" || row.ClosedAt == nil {
		t.Fatalf("history %s = %+v", id, row)
	}
}

func putRecordingHistory(t *testing.T, ctx *sdk.AppCtx, id, backend string, closedAt time.Time) *ComputerSession {
	t.Helper()
	openedAt := closedAt.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	closed := closedAt.UTC().Format(time.RFC3339Nano)
	recordingStatus := "processing"
	providerID := "provider-" + strings.TrimPrefix(id, "br_")
	if !recordingSupported(backend) {
		recordingStatus = "unsupported"
		providerID = ""
	}
	row := &ComputerSession{
		ID: id, Backend: backend, BackendSessionID: providerID,
		Status: "closed", CloseReason: "explicit_close", RecordingStatus: recordingStatus,
		OpenedAt: openedAt, ClosedAt: &closed, UpdatedAt: closed,
	}
	if err := dbPutSession(ctx.AppDB(), row); err != nil {
		t.Fatalf("put recording history: %v", err)
	}
	return row
}

type historyFakeComp struct {
	sessionID  string
	url        string
	executeErr error
	closeHook  func() error
	display    backends.DisplaySize
}

func (f *historyFakeComp) Execute(backends.Action) ([]byte, error) {
	if f.executeErr != nil {
		return nil, f.executeErr
	}
	return []byte{0x89, 0x50, 0x4e, 0x47}, nil
}
func (f *historyFakeComp) Screenshot() ([]byte, error) {
	return f.Execute(backends.Action{Type: "screenshot"})
}
func (f *historyFakeComp) DisplaySize() backends.DisplaySize {
	if f.display.Width == 0 {
		return backends.DisplaySize{Width: 1600, Height: 800}
	}
	return f.display
}
func (f *historyFakeComp) Close() error {
	if f.closeHook != nil {
		if err := f.closeHook(); err != nil {
			return err
		}
	}
	f.sessionID = ""
	return nil
}
func (f *historyFakeComp) OpenSession(backends.OpenOptions) error { return nil }
func (f *historyFakeComp) SessionType() string                    { return "fake" }
func (f *historyFakeComp) SessionID() string                      { return f.sessionID }
func (f *historyFakeComp) CurrentURL() string                     { return f.url }

type fakeReplayResolver struct {
	metadata    replay.Recording
	metadataErr error
	playlist    []byte
	playlistErr error
}

func (f *fakeReplayResolver) Metadata(context.Context, string) (replay.Recording, error) {
	return f.metadata, f.metadataErr
}
func (f *fakeReplayResolver) Playlist(context.Context, string, string) ([]byte, string, error) {
	return f.playlist, "application/vnd.apple.mpegurl", f.playlistErr
}

type fakeResourceReplayResolver struct {
	fakeReplayResolver
	proxyContains string
}

func (f *fakeResourceReplayResolver) SignResource(_ string, resourceURL string) (string, error) {
	if strings.Contains(resourceURL, f.proxyContains) {
		return "opaque-" + fmt.Sprint(len(resourceURL)), nil
	}
	return "", replay.ErrExternalResource
}
func (f *fakeResourceReplayResolver) Resource(context.Context, string, string) ([]byte, string, error) {
	return nil, "", errors.New("not implemented")
}
