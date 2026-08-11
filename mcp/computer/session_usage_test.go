package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

type usageFakeComp struct {
	*fakeComp
	usage      backends.SessionUsage
	usageErr   error
	usageCalls int
}

func (f *usageFakeComp) SessionUsage(context.Context) (backends.SessionUsage, error) {
	f.usageCalls++
	return f.usage, f.usageErr
}

type blockingUsageComp struct{ *fakeComp }

func (f *blockingUsageComp) SessionUsage(ctx context.Context) (backends.SessionUsage, error) {
	<-ctx.Done()
	return backends.SessionUsage{}, ctx.Err()
}

func readyUsage(bytes int64) backends.SessionUsage {
	return backends.SessionUsage{
		Status:     backends.SessionUsageReady,
		ProxyBytes: &bytes,
		MeasuredAt: time.Date(2026, 8, 11, 9, 30, 0, 0, time.UTC),
	}
}

func usageSession(comp backends.Computer, backend string, openedAt, lastUsed time.Time) *session {
	return &session{
		comp: comp, backend: backend, openedAt: openedAt, lastUsed: lastUsed,
		proxy: SessionProxyState{Mode: "managed", Provider: backend, Country: "SK"},
	}
}

func assertStoredUsage(t *testing.T, ctxDB interface{ AppDB() *sql.DB }, id, status string, bytes *int64) {
	t.Helper()
	row, err := dbGetSession(ctxDB.AppDB(), id)
	if err != nil {
		t.Fatalf("dbGetSession(%s): %v", id, err)
	}
	if row.UsageStatus != status {
		t.Fatalf("usage status for %s = %q, want %q", id, row.UsageStatus, status)
	}
	if bytes == nil {
		if row.ProxyBytes != nil {
			t.Fatalf("proxy bytes for %s = %d, want nil", id, *row.ProxyBytes)
		}
	} else if row.ProxyBytes == nil || *row.ProxyBytes != *bytes {
		t.Fatalf("proxy bytes for %s = %v, want %d", id, row.ProxyBytes, *bytes)
	}
}

func TestBrowserCloseReturnsAndReplaysPersistedUsage(t *testing.T) {
	previousBackend := newBackend
	t.Cleanup(func() { newBackend = previousBackend })
	fake := &usageFakeComp{
		fakeComp: &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}, url: "https://example.test"},
		usage:    readyUsage(4213920),
	}
	newBackend = func(backends.Config) (backends.Computer, error) { return fake, nil }
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{reg: &registry{m: map[string]*session{}}}

	opened, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := opened.(map[string]any)["session_id"].(string)
	closed, err := app.toolBrowserClose(ctx, map[string]any{"session_id": id})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	closeMap := closed.(map[string]any)
	usage, ok := closeMap["usage"].(*sessionUsageInfo)
	if !ok || usage.Status != "ready" || usage.ProxyBytes == nil || *usage.ProxyBytes != 4213920 {
		t.Fatalf("close usage = %#v", closeMap["usage"])
	}
	if fake.usageCalls != 1 || fake.closeCalls != 1 {
		t.Fatalf("close/reporter calls = %d/%d", fake.closeCalls, fake.usageCalls)
	}

	replayed, err := app.toolBrowserClose(ctx, map[string]any{"session_id": id})
	if err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	replayedMap := replayed.(map[string]any)
	replayedUsage, ok := replayedMap["usage"].(*sessionUsageInfo)
	if replayedMap["closed"] != false || replayedMap["already_closed"] != true || !ok || replayedUsage.ProxyBytes == nil || *replayedUsage.ProxyBytes != 4213920 {
		t.Fatalf("idempotent close result = %#v", replayedMap)
	}
	if fake.usageCalls != 1 || fake.closeCalls != 1 {
		t.Fatalf("idempotent close repeated provider work: close/reporter=%d/%d", fake.closeCalls, fake.usageCalls)
	}

	// A later active-style upsert must not erase already-final usage.
	row, err := dbGetSession(ctx.AppDB(), id)
	if err != nil {
		t.Fatal(err)
	}
	row.UsageStatus, row.ProxyBytes, row.UsageMeasuredAt = "", nil, nil
	if err := dbPutSession(ctx.AppDB(), row); err != nil {
		t.Fatal(err)
	}
	wantBytes := int64(4213920)
	assertStoredUsage(t, ctx, id, backends.SessionUsageReady, &wantBytes)
}

func TestSessionUsageLifecyclePersistence(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		wantStatus string
	}{
		{name: "idle reaper", id: "br_usage_reaper", wantStatus: "reaped"},
		{name: "provider timeout", id: "br_usage_provider_timeout", wantStatus: "reaped"},
		{name: "unhealthy", id: "br_usage_unhealthy", wantStatus: "failed"},
		{name: "unmount", id: "br_usage_unmount", wantStatus: "interrupted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tk.NewAppCtx(t, "apteva.yaml")
			fake := &usageFakeComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}}, usage: readyUsage(900)}
			now := time.Now()
			sess := usageSession(fake, "browserbase", now, now)
			app := &App{reg: &registry{m: map[string]*session{tt.id: sess}}}
			switch tt.name {
			case "idle reaper":
				sess.lastUsed = now.Add(-time.Hour)
				app.reapIdleSessions(ctx, 30*time.Minute)
			case "provider timeout":
				sess.openedAt = now.Add(-2 * time.Minute)
				sess.timeout = 60
				app.reapIdleSessions(ctx, 30*time.Minute)
			case "unhealthy":
				_ = app.closeUnhealthySession(ctx, tt.id, sess, "screenshot", errors.New("cdp closed"))
			case "unmount":
				if err := app.OnUnmount(ctx); err != nil {
					t.Fatal(err)
				}
			}
			row, err := dbGetSession(ctx.AppDB(), tt.id)
			if err != nil {
				t.Fatal(err)
			}
			if row.Status != tt.wantStatus || row.UsageStatus != "ready" || row.ProxyBytes == nil || *row.ProxyBytes != 900 {
				t.Fatalf("stored lifecycle row = %+v", row)
			}
			if fake.closeCalls != 1 || fake.usageCalls != 1 {
				t.Fatalf("close/reporter calls = %d/%d", fake.closeCalls, fake.usageCalls)
			}
		})
	}
}

func TestUnsupportedUnavailableAndNullableUsageRoundTrip(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	now := time.Now()
	app := &App{reg: &registry{m: map[string]*session{}}}

	local := &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}}
	row, err := app.finalizeSession(ctx, "br_usage_local", usageSession(local, "local", now, now), "closed", "test", "session.closed")
	if err != nil || row.UsageStatus != "unsupported" || row.ProxyBytes != nil {
		t.Fatalf("unsupported usage row=%+v err=%v", row, err)
	}

	failing := &usageFakeComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}}, usageErr: errors.New("provider unavailable")}
	row, err = app.finalizeSession(ctx, "br_usage_unavailable", usageSession(failing, "browserbase", now, now), "closed", "test", "session.closed")
	if err != nil || row.UsageStatus != "unavailable" || row.ProxyBytes != nil {
		t.Fatalf("unavailable usage row=%+v err=%v", row, err)
	}

	zero := int64(0)
	measured := "2026-08-11T09:30:00Z"
	direct := &ComputerSession{
		ID: "br_usage_zero", Backend: "browserbase", Status: "closed", RecordingStatus: "processing",
		OpenedAt: measured, UpdatedAt: measured, ProxyBytes: &zero, UsageStatus: "ready", UsageMeasuredAt: &measured,
	}
	if err := dbPutSession(ctx.AppDB(), direct); err != nil {
		t.Fatal(err)
	}
	assertStoredUsage(t, ctx, direct.ID, "ready", &zero)
}

func TestUsageLookupHasHardDeadline(t *testing.T) {
	previousTimeout := sessionUsageLookupTimeout
	sessionUsageLookupTimeout = 20 * time.Millisecond
	t.Cleanup(func() { sessionUsageLookupTimeout = previousTimeout })
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	comp := &blockingUsageComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}}}
	started := time.Now()
	row, err := (&App{}).finalizeSession(ctx, "br_usage_deadline", usageSession(comp, "browserbase", started, started), "closed", "test", "session.closed")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("usage deadline was not bounded: %s", elapsed)
	}
	if row.UsageStatus != "unavailable" {
		t.Fatalf("usage status = %q", row.UsageStatus)
	}
}
