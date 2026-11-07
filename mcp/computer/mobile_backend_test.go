package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/mobile"
)

type mobilePlatform struct {
	tk.BasePlatformClient
	calls     []map[string]any
	tools     []string
	failClose bool
	image     []byte
}

func (p *mobilePlatform) CallAppContext(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, errors.New("raw calls are not supported")
}
func (p *mobilePlatform) CallAppBatchContext(context.Context, string, []sdk.AppCall, sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	return nil, errors.New("batch unavailable")
}
func (p *mobilePlatform) CallAppResultContext(ctx context.Context, app, tool string, args map[string]any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if app != "mobile" {
		return errors.New("wrong target app")
	}
	if args["_project_id"] != "p1" {
		return errors.New("project authority was lost")
	}
	p.calls = append(p.calls, args)
	p.tools = append(p.tools, tool)
	var result any
	switch tool {
	case "mobile_session":
		if p.failClose && args["action"] == "close" {
			return errors.New("runner offline")
		}
		result = map[string]any{"session": map[string]any{"id": "ses_phone", "status": "active", "device": map[string]any{"id": "iphone-test", "platform": "ios", "driver": "xctest"}, "expires_at": time.Now().Add(15 * time.Minute).Unix()}}
	case "mobile_observe":
		result = sdk.MCPResult{Content: []sdk.MCPContent{{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(p.image)}}}
	default:
		result = map[string]any{"ok": true}
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func mobileTestApp(t *testing.T) (*App, *sdk.AppCtx, *mobilePlatform) {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 400, 800))); err != nil {
		t.Fatal(err)
	}
	platform := &mobilePlatform{image: b.Bytes()}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	app := &App{reg: &registry{m: map[string]*session{}}}
	return app, ctx, platform
}
func openMobileTest(t *testing.T, app *App, ctx *sdk.AppCtx, args map[string]any) map[string]any {
	t.Helper()
	v, err := app.toolBrowserSession(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}
func TestMobileSavedContextLifecycle(t *testing.T) {
	app, ctx, p := mobileTestApp(t)
	v, err := app.toolContextCreate(ctx, map[string]any{"name": "test-phone", "backend": "mobile", "target": map[string]any{"kind": "mobile", "device_id": "iphone-test", "app_id": "com.example.app"}})
	if err != nil {
		t.Fatal(err)
	}
	saved := v.(map[string]any)["context"].(*ComputerContext)
	if saved.ProjectID != "p1" || saved.ProviderContextID != "" {
		t.Fatalf("wrong saved binding: %+v", saved)
	}
	opened := openMobileTest(t, app, ctx, map[string]any{"action": "open", "context_name": "test-phone"})
	id := opened["session_id"].(string)
	if opened["backend"] != "mobile" || opened["app_context_id"] != saved.ID || opened["target_kind"] != "mobile" {
		t.Fatalf("context resolution lost: %+v", opened)
	}
	if caps := opened["capabilities"].(api.Capabilities); caps.Context.Isolation {
		t.Fatal("physical phone claims isolation")
	}
	// The default app is launched once using the saved device binding.
	foundLaunch := false
	for i, tool := range p.tools {
		if tool == "mobile_app" && p.calls[i]["app_id"] == "com.example.app" {
			foundLaunch = true
		}
	}
	if !foundLaunch {
		t.Fatal("saved app was not launched")
	}
	for _, args := range []map[string]any{
		{"session_id": id, "action": "screenshot"},
		{"session_id": id, "action": "click", "coordinate": "100,600"},
		{"session_id": id, "action": "scroll", "direction": "down", "amount": 200},
		{"session_id": id, "action": "navigate", "url": "https://example.com"},
		{"session_id": id, "action": "home"},
		{"session_id": id, "action": "launch_app", "app_id": "com.example.other"},
	} {
		out, err := app.toolComputerUse(ctx, args)
		if err != nil {
			t.Fatalf("%s: %v", args["action"], err)
		}
		if args["action"] == "screenshot" {
			if _, ok := out.(map[string]any)["som_delta"]; ok {
				t.Fatal("phone screenshot falsely reports DOM semantics")
			}
		}
		if args["action"] == "navigate" {
			m := out.(map[string]any)
			if m["current_url"] != "" || m["navigation_verified"] != false {
				t.Fatal("unverified phone navigation was reported as observed")
			}
		}
	}
	if _, err := app.toolBrowserClose(ctx, map[string]any{"session_id": id}); err != nil {
		t.Fatal(err)
	}
	row, err := dbGetSession(ctx.AppDB(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "closed" || row.ProjectID != "p1" || row.Target.DeviceID != "iphone-test" || len(row.FinalScreenshot) == 0 || row.Capabilities.TargetKind != "mobile" {
		t.Fatalf("history lost target/authority: %+v", row)
	}
	next := openMobileTest(t, app, ctx, map[string]any{"action": "open", "context_id": saved.ID})
	if next["session_id"] == id {
		t.Fatal("reopening context reused stale handle")
	}
	if _, err := app.toolBrowserClose(ctx, map[string]any{"session_id": next["session_id"]}); err != nil {
		t.Fatal(err)
	}
}
func TestMobileCapabilityRejectionBeforeDispatch(t *testing.T) {
	app, ctx, p := mobileTestApp(t)
	opened := openMobileTest(t, app, ctx, map[string]any{"action": "open", "backend": "mobile", "device_id": "iphone-test"})
	for _, args := range []map[string]any{
		{"action": "prepare_upload", "source_url": "https://example.com/file"},
		{"action": "wait_for_stable"}, {"action": "batch", "steps": []any{map[string]any{"action": "click"}}},
		{"action": "click", "label": 1}, {"action": "click", "coordinate": "10,10", "expected_text": "Send"},
		{"action": "screenshot", "include_som": true}, {"action": "key", "key": "Backspace"},
	} {
		args["session_id"] = opened["session_id"]
		n := len(p.calls)
		if _, err := app.toolComputerUse(ctx, args); err == nil || !strings.Contains(err.Error(), "backend_not_supported") {
			t.Fatalf("expected capability rejection for %+v: %v", args, err)
		}
		if len(p.calls) != n {
			t.Fatal("unsupported operation called Mobile")
		}
	}
}
func TestMobileContextsAndSessionsStayInOriginalProject(t *testing.T) {
	app, ctx, p := mobileTestApp(t)
	opened := openMobileTest(t, app, ctx, map[string]any{"action": "open", "backend": "mobile", "device_id": "iphone-test", "context_name": "private-phone", "auto_create_context": true})
	other := ctx.WithProject("p2")
	n := len(p.calls)
	for _, args := range []map[string]any{
		{"action": "open", "context_name": "private-phone"},
		{"action": "open", "context_id": opened["app_context_id"]},
		{"action": "status", "session_id": opened["session_id"]},
		{"action": "close", "session_id": opened["session_id"]},
	} {
		if _, err := app.toolBrowserSession(other, args); err == nil {
			t.Fatalf("cross-project access: %+v", args)
		}
	}
	if _, err := app.toolComputerUse(other, map[string]any{"session_id": opened["session_id"], "action": "home"}); err == nil {
		t.Fatal("cross-project control")
	}
	list, err := app.toolContextList(other, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.(map[string]any)["contexts"].([]*ComputerContext)) != 0 {
		t.Fatal("context list leaked project binding")
	}
	got, err := app.toolContextGet(other, map[string]any{"id": opened["app_context_id"]})
	if err != nil || got.(map[string]any)["found"] != false {
		t.Fatal("context get leaked binding")
	}
	if _, err := app.toolContextUpdate(other, map[string]any{"id": opened["app_context_id"], "name": "stolen"}); err == nil {
		t.Fatal("cross-project context update")
	}
	if _, err := app.toolContextDelete(other, map[string]any{"id": opened["app_context_id"]}); err == nil {
		t.Fatal("cross-project context deletion")
	}
	if len(p.calls) != n {
		t.Fatal("cross-project access reached Mobile")
	}
	// HTTP frame access uses the same project guard, before screenshot capture.
	for _, route := range app.HTTPRoutes() {
		if route.Pattern == "/sessions/{id}/screenshot" {
			req := httptest.NewRequest(http.MethodGet, "/sessions/"+opened["session_id"].(string)+"/screenshot", nil)
			rr := httptest.NewRecorder()
			route.Handler(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("unscoped frame HTTP %d", rr.Code)
			}
		}
	}
}
func TestMobileOpenRejectsBrowserOverridesAndContextRetargeting(t *testing.T) {
	app, ctx, p := mobileTestApp(t)
	for _, extra := range []map[string]any{
		{"provider_context_id": "profile"}, {"persist": false}, {"target": map[string]any{"kind": "mobile", "typo": true}},
		{"target": map[string]any{"kind": "browser"}}, {"timeout": 4000}, {"proxy_mode": "managed"},
	} {
		args := map[string]any{"action": "open", "backend": "mobile", "device_id": "iphone-test"}
		for k, v := range extra {
			args[k] = v
		}
		if _, err := app.toolBrowserSession(ctx, args); err == nil {
			t.Fatalf("accepted %+v", extra)
		}
	}
	if len(p.calls) != 0 {
		t.Fatal("invalid open reached Mobile")
	}
	saved, err := app.createContextRecord(ctx, map[string]any{"name": "phone", "backend": "mobile", "device_id": "iphone-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.toolBrowserSession(ctx, map[string]any{"action": "open", "context_id": saved.ID, "device_id": "other-phone"}); err == nil {
		t.Fatal("context silently retargeted")
	}
	if _, err = app.createContextRecord(ctx, map[string]any{"name": "selector", "backend": "mobile", "target": map[string]any{"platform": "ios"}}); err == nil {
		t.Fatal("saved context accepted non-exact device")
	}
}
func TestMobileCleanupRetriesWithOriginalProject(t *testing.T) {
	app, ctx, p := mobileTestApp(t)
	opened := openMobileTest(t, app, ctx, map[string]any{"action": "open", "backend": "mobile", "device_id": "iphone-test"})
	id := opened["session_id"].(string)
	p.failClose = true
	if _, err := app.toolBrowserClose(ctx, map[string]any{"session_id": id}); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	row, err := dbGetSessionMetadata(ctx.AppDB(), id)
	if err != nil || row.Status != "cleanup_pending" {
		t.Fatalf("cleanup not durable: %+v %v", row, err)
	}
	p.failClose = false
	app.reconcileProviderCleanup(ctx.WithProject("p2"))
	row, err = dbGetSessionMetadata(ctx.AppDB(), id)
	if err != nil || row.Status != "closed" {
		t.Fatalf("cleanup did not recover: %+v %v", row, err)
	}
}

func TestMobileOpenCancellationAndBackendLock(t *testing.T) {
	app, ctx, p := mobileTestApp(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.toolBrowserSessionCaller(canceled, ctx, map[string]any{"action": "open", "backend": "mobile", "device_id": "iphone-test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("open cancellation lost: %v", err)
	}
	if len(p.calls) != 0 {
		t.Fatal("canceled open reached Mobile")
	}
	saved, err := app.createContextRecord(ctx, map[string]any{"name": "phone", "backend": "mobile", "device_id": "iphone-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dbUpdateSettings(ctx.AppDB(), map[string]any{"default_backend": "local", "lock_backend": true}); err != nil {
		t.Fatal(err)
	}
	if _, err = app.toolBrowserSession(ctx, map[string]any{"action": "open", "context_id": saved.ID}); err == nil || !strings.Contains(err.Error(), "context_backend_mismatch") {
		t.Fatalf("saved phone context bypassed backend lock: %v", err)
	}
}

func TestMobileSwipeNeedsExplicitEndpoints(t *testing.T) {
	app, ctx, p := mobileTestApp(t)
	opened := openMobileTest(t, app, ctx, map[string]any{"action": "open", "backend": "mobile", "device_id": "iphone-test"})
	for _, extra := range []map[string]any{{}, {"coordinate": "10,10"}, {"coordinate": "10,10", "to_x": 0.5, "to_y": 0}} {
		args := map[string]any{"session_id": opened["session_id"], "action": "swipe"}
		for k, v := range extra {
			args[k] = v
		}
		n := len(p.calls)
		if _, err := app.toolComputerUse(ctx, args); err == nil {
			t.Fatalf("accepted incomplete swipe: %+v", args)
		}
		if len(p.calls) != n {
			t.Fatal("invalid swipe dispatched")
		}
	}
}

type expiredMobileComp struct{ *fakeComp }

func (c *expiredMobileComp) ExecuteAction(api.Action) error { return api.ErrSessionExpired }
func TestMobileExpiredLeaseEvictsComputerSession(t *testing.T) {
	app, ctx, _ := mobileTestApp(t)
	comp := &expiredMobileComp{fakeComp: &fakeComp{}}
	app.reg.put("br_expired", &session{comp: comp, backend: "mobile", projectID: "p1", openedAt: time.Now(), lastUsed: time.Now()})
	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_expired", "action": "home"}); err == nil {
		t.Fatal("expired lease accepted input")
	}
	if _, ok := app.reg.get("br_expired"); ok {
		t.Fatal("expired handle remained active")
	}
	row, err := dbGetSessionMetadata(ctx.AppDB(), "br_expired")
	if err != nil || row.Status == "active" {
		t.Fatalf("expired session not finalized: %+v %v", row, err)
	}
}

func TestMobileStartupToolResultExplainsUserAuthorization(t *testing.T) {
	app, ctx, _ := mobileTestApp(t)
	prior := newBackend
	t.Cleanup(func() { newBackend = prior })
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return mobile.New(cfg.Target, func(context.Context, string, map[string]any, any) error {
			return errors.New("mobile.mobile_session: Timed out while enabling automation mode. (code=-32000)")
		})
	}
	out, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "mobile", "device_id": "iphone-test"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["status"] != "needs_user_action" || result["error_code"] != "mobile_authorization_required" || result["user_action_required"] != true || result["retryable"] != false || result["success"] != false {
		t.Fatalf("missing authorization state: %+v", result)
	}
	if result["session_id"] != nil || len(app.listSessions()) != 0 {
		t.Fatal("authorization gate returned a live session")
	}
	if !strings.Contains(result["next_step"].(string), "directly on the phone") {
		t.Fatal("missing human authorization instruction")
	}
}
