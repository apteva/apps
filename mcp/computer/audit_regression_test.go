package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression contracts from the v0.7.87 audit.
func TestAuditScreenshotAliasReturnsImage(t *testing.T) {
	image := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	fake := &fakeComp{png: image, display: backends.DisplaySize{Width: 800, Height: 600}, url: "https://example.com/"}
	app := &App{reg: &registry{m: map[string]*session{}}}
	now := time.Now()
	app.reg.put("br_audit", &session{comp: fake, backend: "local", openedAt: now, lastUsed: now})
	out, err := app.toolBrowserScreenshot(tk.NewAppCtx(t, "apteva.yaml"), map[string]any{"session_id": "br_audit"})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["png_b64"]
	if got != base64.StdEncoding.EncodeToString(image) {
		t.Fatalf("advertised png_b64 contains %v; expected image bytes", got)
	}
}

func TestAuditSetTextCanClear(t *testing.T) {
	if err := validateSetTextArgs("set_text", map[string]any{"selector": "#search", "text": "", "mode": "replace"}); err != nil {
		t.Fatalf("explicit empty text must clear the field: %v", err)
	}
}

func TestAuditMissingContextIsRejected(t *testing.T) {
	app := &App{}
	got, err := app.resolveSessionContext(tk.NewAppCtx(t, "apteva.yaml"), "local", map[string]any{"context_name": "missing-saved-login"}, false)
	if err == nil {
		t.Fatalf("missing named context silently resolves to %+v", got)
	}
}

func TestAuditSettingsAtomicConcurrentPatches(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	db := ctx.AppDB()
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, patch := range []map[string]any{{"default_backend": "steel"}, {"default_proxy_mode": "direct"}} {
		go func(p map[string]any) { <-start; _, err := dbUpdateSettings(db, p); errs <- err }(patch)
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, err := dbGetSettings(db)
	if err != nil || got.DefaultBackend != "steel" || got.DefaultProxyMode != "direct" {
		t.Fatalf("lost patch: %+v %v", got, err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_settings BEFORE UPDATE ON computer_settings WHEN NEW.key='lock_backend' BEGIN SELECT RAISE(ABORT,'fixture write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbUpdateSettings(db, map[string]any{"default_backend": "browserbase", "lock_backend": true}); err == nil {
		t.Fatal("expected transaction failure")
	}
	after, err := dbGetSettings(db)
	if err != nil || after != got {
		t.Fatalf("partial policy write: %+v %v", after, err)
	}
}

func TestAuditMetadataDoesNotReadScreenshot(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	row := &ComputerSession{ID: "br_blob", Backend: "local", Status: "closed", RecordingStatus: "unsupported", OpenedAt: nowUTC(), UpdatedAt: nowUTC(), FinalScreenshot: make([]byte, 2<<20)}
	if err := dbPutSession(ctx.AppDB(), row); err != nil {
		t.Fatal(err)
	}
	meta, err := dbGetSessionMetadata(ctx.AppDB(), row.ID)
	if err != nil || len(meta.FinalScreenshot) != 0 {
		t.Fatalf("metadata loads image: %v", err)
	}
	list, err := dbListSessions(ctx.AppDB(), 10)
	if err != nil || len(list) != 1 || len(list[0].FinalScreenshot) != 0 {
		t.Fatalf("list loads image: %v", err)
	}
	image, err := dbGetSession(ctx.AppDB(), row.ID)
	if err != nil || len(image.FinalScreenshot) != 2<<20 {
		t.Fatalf("screenshot lost: %v", err)
	}
}

type failingCloseComputer struct{ *fakeComp }

func (*failingCloseComputer) Close() error { return errors.New("provider unavailable") }
func TestAuditFailedReleaseRemainsPendingAndRetries(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{reg: &registry{m: map[string]*session{}}}
	comp := &failingCloseComputer{&fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}, url: "https://example.com"}}
	s := &session{comp: comp, backend: "steel", backendSessionID: "provider-1", openedAt: time.Now(), lastUsed: time.Now()}
	app.reg.put("br_retry", s)
	_, err := ctx.AppDB().Exec(`INSERT INTO computer_provider_leases(session_id,backend,provider_id) VALUES('br_retry','steel','provider-1')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolBrowserClose(ctx, map[string]any{"session_id": "br_retry"}); err == nil || !strings.Contains(err.Error(), "cleanup_pending") {
		t.Fatalf("release falsely succeeded: %v", err)
	}
	row, err := dbGetSessionMetadata(ctx.AppDB(), "br_retry")
	if err != nil || row.Status != "cleanup_pending" {
		t.Fatalf("lost pending state: %+v %v", row, err)
	}
	old := releaseProviderLease
	t.Cleanup(func() { releaseProviderLease = old })
	calls := 0
	releaseProviderLease = func(_ *sdk.AppCtx, l providerLease) error {
		calls++
		if l.ProviderID != "provider-1" {
			t.Fatalf("wrong identity: %+v", l)
		}
		return nil
	}
	app.reconcileProviderCleanup(ctx)
	app.reconcileProviderCleanup(ctx)
	row, err = dbGetSessionMetadata(ctx.AppDB(), "br_retry")
	if err != nil || row.Status != "closed" || calls != 1 {
		t.Fatalf("retry state: %+v calls=%d err=%v", row, calls, err)
	}
}

func TestAuditQueuedActionRejectedAfterRemoval(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	fake := &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}}
	s := &session{comp: fake, backend: "local", openedAt: time.Now(), lastUsed: time.Now()}
	app.reg.put("br_queued", s)
	s.actionMu.Lock()
	result := make(chan error, 1)
	go func() {
		_, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_queued", "action": "key", "key": "Escape"})
		result <- err
	}()
	app.reg.remove("br_queued")
	s.actionMu.Unlock()
	if err := <-result; err == nil {
		t.Fatal("action executed on removed session")
	}
	if len(fake.actionOnlyCalls) != 0 {
		t.Fatal("queued input was dispatched")
	}
}

func TestAuditConcurrentTabsListsAndActions(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	fake := &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}, tabs: []backends.TabInfo{{ID: "one", Active: true}, {ID: "two"}}}
	s := &session{comp: fake, backend: "local", openedAt: time.Now(), lastUsed: time.Now()}
	app.reg.put("br_concurrent", s)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				switch n {
				case 0:
					_, _ = app.toolComputerUse(ctx, map[string]any{"session_id": "br_concurrent", "action": "key", "key": "Escape", "response_mode": "none"})
				case 1:
					_, _ = app.toolBrowserSwitchTab(ctx, map[string]any{"session_id": "br_concurrent", "tab_id": "two"})
				case 2:
					_, _ = app.toolBrowserTabs(ctx, map[string]any{"session_id": "br_concurrent"})
				case 3:
					_ = app.listSessions()
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestAuditSavedContextCannotOverrideBackendLock(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{reg: &registry{m: map[string]*session{}}}
	rec, err := dbCreateContext(ctx.AppDB(), contextCreateInput{Name: "remote", Backend: "steel", ProviderContextID: "profile"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbUpdateSettings(ctx.AppDB(), map[string]any{"default_backend": "local", "lock_backend": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolBrowserOpen(ctx, map[string]any{"context_id": rec.ID}); err == nil {
		t.Fatal("saved context bypassed backend lock")
	}
}

func TestAuditScreenshotAliasConvertsJPEG(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	got := pngBase64(binaryEnvelope(encoded.Bytes(), "image/jpeg"))
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
}

type semanticAuditComputer struct {
	*fakeComp
	enumerations int
}

func (c *semanticAuditComputer) RefreshSemantics() error { c.enumerations++; return nil }
func TestAuditSemanticObservationDoesNotCaptureBitmap(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	comp := &semanticAuditComputer{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}}}
	app := &App{reg: &registry{m: map[string]*session{}}}
	app.reg.put("br_semantic", &session{comp: comp, backend: "local", openedAt: time.Now(), lastUsed: time.Now()})
	_, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_semantic", "action": "key", "key": "Escape", "observation": "som_delta"})
	if err != nil {
		t.Fatal(err)
	}
	if comp.screenshotCalls != 0 || comp.enumerations != 1 || len(comp.actionOnlyCalls) != 1 {
		t.Fatalf("redundant image work: screenshots=%d enumerations=%d actions=%d", comp.screenshotCalls, comp.enumerations, len(comp.actionOnlyCalls))
	}
}

func TestAuditHistoryPaginationAndOptInRetention(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	db := ctx.AppDB()
	old := time.Now().AddDate(0, 0, -90).UTC().Format(time.RFC3339Nano)
	for _, id := range []string{"a", "b", "pending"} {
		status := "closed"
		if id == "pending" {
			status = "cleanup_pending"
		}
		if err := dbPutSession(db, &ComputerSession{ID: id, Backend: "local", Status: status, OpenedAt: old, UpdatedAt: old, ClosedAt: &old, RecordingStatus: "unsupported"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := dbListSessionsPage(db, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := dbListSessionsPage(db, 1, 1)
	if err != nil || len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Fatalf("pagination %v", err)
	}
	if err := dbPruneSessionHistory(db, time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	rows, err := dbListSessions(db, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != "pending" {
		t.Fatalf("retention lost pending cleanup: %+v %v", rows, err)
	}
}

func TestAuditCancelledQueuedCallerReturnsWithoutWaitingForBrowser(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	s := &session{comp: &fakeComp{}, backend: "local"}
	app.reg.put("queued", s)
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := app.toolComputerUseCaller(ctx, nil, map[string]any{"session_id": "queued", "action": "key", "key": "Escape"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(began) > time.Second {
		t.Fatalf("queued cancellation blocked: %v", err)
	}
}

func TestAuditLegacyActiveProviderSessionIsReconciledAfterUpgrade(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if err := dbPutSession(ctx.AppDB(), &ComputerSession{ID: "legacy", Backend: "steel", BackendSessionID: "legacy-provider", Status: "active", RecordingStatus: "recording", OpenedAt: nowUTC(), UpdatedAt: nowUTC()}); err != nil {
		t.Fatal(err)
	}
	if err := recoverLegacyProviderLeases(ctx); err != nil {
		t.Fatal(err)
	}
	lease, err := leaseForSession(ctx.AppDB(), "legacy")
	if err != nil || lease.ProviderID != "legacy-provider" || lease.TerminalStatus != "interrupted" {
		t.Fatalf("legacy provider orphan forgotten: %+v %v", lease, err)
	}
}
