package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

type downloadTestComp struct {
	*fakeComp
	downloads []backends.Download
	payloads  map[string][]byte
	cursor    uint64
	wait      bool
	err       error
}

func (f *downloadTestComp) ListDownloads(context.Context) ([]backends.Download, error) {
	return append([]backends.Download(nil), f.downloads...), f.err
}

func (f *downloadTestComp) WaitForDownload(ctx context.Context, id string) (backends.Download, error) {
	for _, download := range f.downloads {
		if download.ID != id {
			continue
		}
		if f.wait {
			<-ctx.Done()
			return download, ctx.Err()
		}
		return download, f.err
	}
	return backends.Download{}, errors.New("download_not_found_or_not_owned")
}

func (f *downloadTestComp) OpenDownload(_ context.Context, id string) (io.ReadCloser, backends.Download, error) {
	for _, download := range f.downloads {
		if download.ID != id {
			continue
		}
		if download.Status != backends.DownloadCompleted {
			return nil, download, errors.New("download_not_ready")
		}
		return io.NopCloser(bytes.NewReader(f.payloads[id])), download, f.err
	}
	return nil, backends.Download{}, errors.New("download_not_found_or_not_owned")
}

func (f *downloadTestComp) DownloadEventCursor() uint64 { return f.cursor }

func (f *downloadTestComp) DownloadsStartedSince(cursor uint64) []backends.Download {
	if f.cursor <= cursor {
		return nil
	}
	return append([]backends.Download(nil), f.downloads...)
}

func downloadCaller(project, thread string, agent int64) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: project, ThreadID: thread, AgentID: agent})
}

func downloadApp(id string, comp backends.Computer, owner sessionOwner) *App {
	app := appWithSession(id, comp, "local")
	sess, _ := app.reg.get(id)
	sess.owner = owner
	return app
}

func TestBrowserDownloadListWaitGetAndCallerIsolation(t *testing.T) {
	payload := []byte("PK\x03\x04fixture")
	now := time.Now().UTC()
	download := backends.Download{ID: "dl_fixture", Filename: "bid-pack.zip", MIMEType: "application/zip", Size: int64(len(payload)), Status: backends.DownloadCompleted, CreatedAt: now, CompletedAt: &now}
	comp := &downloadTestComp{fakeComp: &fakeComp{}, downloads: []backends.Download{download}, payloads: map[string][]byte{download.ID: payload}}
	owner := sessionOwner{ProjectID: "project-a", AgentID: 42, ThreadID: "thread-a"}
	app := downloadApp("br_fixture", comp, owner)
	ctx := downloadCaller(owner.ProjectID, owner.ThreadID, owner.AgentID)

	listed, err := app.toolBrowserDownload(ctx, nil, map[string]any{"action": "list", "session_id": "br_fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.(map[string]any)["downloads"].([]backends.Download); len(got) != 1 || got[0].ID != download.ID {
		t.Fatalf("list result: %#v", listed)
	}
	waited, err := app.toolBrowserDownload(ctx, nil, map[string]any{"action": "wait", "session_id": "br_fixture", "download_id": download.ID})
	if err != nil || waited.(map[string]any)["terminal"] != true {
		t.Fatalf("wait result=%#v err=%v", waited, err)
	}
	exported, err := app.toolBrowserDownload(ctx, nil, map[string]any{"action": "get", "session_id": "br_fixture", "download_id": download.ID})
	if err != nil {
		t.Fatal(err)
	}
	envelope := exported.(map[string]any)
	if envelope["_binary"] != true || envelope["mimeType"] != "application/zip" || envelope["size"] != len(payload) {
		t.Fatalf("binary envelope: %#v", envelope)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope["base64"].(string))
	if err != nil || !bytes.Equal(decoded, payload) {
		t.Fatalf("export mismatch: %q err=%v", decoded, err)
	}

	other := downloadCaller("project-a", "thread-b", 43)
	if _, err := app.toolBrowserDownload(other, nil, map[string]any{"action": "get", "session_id": "br_fixture", "download_id": download.ID}); err == nil || !strings.Contains(err.Error(), "download_not_found_or_not_owned") {
		t.Fatalf("cross-caller download access was not rejected: %v", err)
	}
}

func TestBrowserSessionCallerBindsDownloadOwnership(t *testing.T) {
	previous := newBackend
	t.Cleanup(func() { newBackend = previous })
	comp := &downloadTestComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}}, payloads: map[string][]byte{}}
	newBackend = func(backends.Config) (backends.Computer, error) { return comp, nil }
	app := &App{reg: &registry{m: map[string]*session{}}}
	appCtx := tk.NewAppCtx(t, "apteva.yaml")
	callerCtx := downloadCaller("project-a", "thread-a", 42)
	opened, err := app.toolBrowserSessionCaller(callerCtx, appCtx, map[string]any{"action": "open", "backend": "local", "url": "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := opened.(map[string]any)["session_id"].(string)
	sess, ok := app.reg.get(sessionID)
	if !ok || sess.owner != (sessionOwner{ProjectID: "project-a", AgentID: 42, ThreadID: "thread-a"}) {
		t.Fatalf("authenticated caller was not bound to session: %+v", sess)
	}
	if _, err := app.toolBrowserDownload(callerCtx, appCtx, map[string]any{"action": "list", "session_id": sessionID}); err != nil {
		t.Fatalf("bound caller cannot list its downloads: %v", err)
	}
}

func TestBrowserDownloadTimeoutUnsupportedAndIntegrityErrors(t *testing.T) {
	owner := sessionOwner{ProjectID: "project-a", AgentID: 42, ThreadID: "thread-a"}
	ctx := downloadCaller(owner.ProjectID, owner.ThreadID, owner.AgentID)
	inProgress := backends.Download{ID: "dl_slow", Filename: "slow.zip", Status: backends.DownloadInProgress, CreatedAt: time.Now()}
	slow := &downloadTestComp{fakeComp: &fakeComp{}, downloads: []backends.Download{inProgress}, wait: true}
	app := downloadApp("br_slow", slow, owner)
	out, err := app.toolBrowserDownload(ctx, nil, map[string]any{"action": "wait", "session_id": "br_slow", "download_id": inProgress.ID, "timeout_ms": 1})
	if err != nil || out.(map[string]any)["timed_out"] != true || out.(map[string]any)["terminal"] != false {
		t.Fatalf("timeout result=%#v err=%v", out, err)
	}

	unsupported := downloadApp("br_unsupported", &fakeComp{}, owner)
	if _, err := unsupported.toolBrowserDownload(ctx, nil, map[string]any{"action": "list", "session_id": "br_unsupported"}); err == nil || !strings.Contains(err.Error(), "downloads_not_supported") {
		t.Fatalf("unsupported backend error: %v", err)
	}

	badMeta := backends.Download{ID: "dl_bad", Filename: "bad.bin", Size: 99, Status: backends.DownloadCompleted, CreatedAt: time.Now()}
	bad := &downloadTestComp{fakeComp: &fakeComp{}, downloads: []backends.Download{badMeta}, payloads: map[string][]byte{badMeta.ID: []byte("short")}}
	badApp := downloadApp("br_bad", bad, owner)
	if _, err := badApp.toolBrowserDownload(ctx, nil, map[string]any{"action": "get", "session_id": "br_bad", "download_id": badMeta.ID}); err == nil || !strings.Contains(err.Error(), "download_integrity_mismatch") {
		t.Fatalf("integrity mismatch not rejected: %v", err)
	}
}

func TestComputerUseReportsOnlyCompactDownloadStartMetadata(t *testing.T) {
	payload := []byte("secret downloaded file bytes")
	download := backends.Download{ID: "dl_click", Filename: "report.zip", MIMEType: "application/zip", Status: backends.DownloadInProgress, CreatedAt: time.Now()}
	comp := &downloadTestComp{fakeComp: &fakeComp{url: "https://example.test"}, downloads: []backends.Download{download}, payloads: map[string][]byte{download.ID: payload}}
	comp.executeActionHook = func(backends.Action) error {
		comp.cursor++
		return nil
	}
	app := appWithSession("br_click", comp, "local")
	out, err := app.toolComputerUse(nil, map[string]any{"action": "click", "session_id": "br_click", "selector": "#download"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	started := result["downloads_started"].([]map[string]any)
	if len(started) != 1 || started[0]["id"] != download.ID || started[0]["status"] != download.Status {
		t.Fatalf("compact download event: %#v", result)
	}
	if _, found := started[0]["base64"]; found {
		t.Fatalf("normal action leaked download bytes: %#v", result)
	}
	if strings.Contains(mustJSON(result), base64.StdEncoding.EncodeToString(payload)) {
		t.Fatalf("normal action history contains download bytes: %#v", result)
	}
	batchOut, err := app.toolComputerUse(nil, map[string]any{
		"action": "batch", "session_id": "br_click", "observation": "none",
		"steps": []any{map[string]any{"action": "click", "selector": "#download"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if started, ok := batchOut.(map[string]any)["downloads_started"].([]map[string]any); !ok || len(started) != 1 || started[0]["id"] != download.ID {
		t.Fatalf("batch omitted compact download start metadata: %#v", batchOut)
	}
}

func TestBrowserDownloadToolContractIsAgentVisible(t *testing.T) {
	tool := findTool(t, (&App{}).MCPTools(), "browser_download")
	if tool.Exposure == sdk.ToolExposureAppOnly {
		t.Fatal("browser_download must be agent-visible")
	}
	props := tool.InputSchema["properties"].(map[string]any)
	actions := props["action"].(map[string]any)["enum"].([]string)
	for _, action := range []string{"list", "wait", "get"} {
		if !containsString(actions, action) {
			t.Fatalf("schema missing %q: %v", action, actions)
		}
	}
	if !strings.Contains(tool.Description, "Click success never proves download completion") || !strings.Contains(tool.Description, "never refetches") || !strings.Contains(tool.Description, "does not unzip") {
		t.Fatalf("download lifecycle guidance is incomplete: %s", tool.Description)
	}
}
