package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestEmbeddedManifestMatchesYAML guards the dual-source-of-truth
// hazard: apteva.yaml is what the platform reads at install time,
// manifestYAML is what a built sidecar binary self-reports. They MUST
// agree on the load-bearing fields (name, version, scope, the tool
// list, declared permissions) or installs will succeed against a yaml
// that promises tools the binary doesn't expose.
func TestEmbeddedManifestMatchesYAML(t *testing.T) {
	yamlBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	fromFile, err := sdk.ParseManifest(yamlBytes)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	fromEmbed, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}

	if fromFile.Name != fromEmbed.Name {
		t.Errorf("name drift: yaml=%q embed=%q", fromFile.Name, fromEmbed.Name)
	}
	if fromFile.Version != fromEmbed.Version {
		t.Errorf("version drift: yaml=%q embed=%q", fromFile.Version, fromEmbed.Version)
	}
	if !sameScopes(fromFile.Scopes, fromEmbed.Scopes) {
		t.Errorf("scopes drift: yaml=%v embed=%v", fromFile.Scopes, fromEmbed.Scopes)
	}
	if !samePermissions(fromFile.Requires.Permissions, fromEmbed.Requires.Permissions) {
		t.Errorf("permissions drift: yaml=%v embed=%v",
			fromFile.Requires.Permissions, fromEmbed.Requires.Permissions)
	}

	yamlTools := toolNames(fromFile.Provides.MCPTools)
	embedTools := toolNames(fromEmbed.Provides.MCPTools)
	if len(yamlTools) != len(embedTools) {
		t.Errorf("tool-count drift: yaml=%d embed=%d (yaml=%v embed=%v)",
			len(yamlTools), len(embedTools), yamlTools, embedTools)
	}
	for i, name := range yamlTools {
		if i >= len(embedTools) || embedTools[i] != name {
			t.Errorf("tool names differ: yaml=%v embed=%v", yamlTools, embedTools)
			break
		}
	}
	yamlEvents := eventNames(fromFile.Provides.Publishes)
	embedEvents := eventNames(fromEmbed.Provides.Publishes)
	if !sameStringSlices(yamlEvents, embedEvents) {
		t.Errorf("publish-event drift: yaml=%v embed=%v", yamlEvents, embedEvents)
	}

	var browserView, browserCard *sdk.UIComponent
	for i := range fromFile.Provides.UIComponents {
		switch fromFile.Provides.UIComponents[i].Name {
		case "browser-view":
			browserView = &fromFile.Provides.UIComponents[i]
		case "browser-card":
			browserCard = &fromFile.Provides.UIComponents[i]
		}
	}
	if browserView == nil {
		t.Fatal("browser-view component missing")
	}
	required, ok := browserView.PropsSchema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "session_id" {
		t.Fatalf("browser-view must require only session_id, got %#v", browserView.PropsSchema["required"])
	}
	if browserCard == nil {
		t.Fatal("browser-card compatibility alias missing")
	}
	if browserCard.Entry != browserView.Entry {
		t.Fatalf("browser-card must resolve to browser-view renderer: alias=%q canonical=%q", browserCard.Entry, browserView.Entry)
	}
	required, ok = browserCard.PropsSchema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "instance_id" {
		t.Fatalf("browser-card compatibility alias must require only instance_id, got %#v", browserCard.PropsSchema["required"])
	}

	var embeddedBrowserCard *sdk.UIComponent
	for i := range fromEmbed.Provides.UIComponents {
		if fromEmbed.Provides.UIComponents[i].Name == "browser-card" {
			embeddedBrowserCard = &fromEmbed.Provides.UIComponents[i]
			break
		}
	}
	if embeddedBrowserCard == nil || embeddedBrowserCard.Entry != browserView.Entry {
		t.Fatalf("embedded browser-card must resolve to browser-view renderer, got %#v", embeddedBrowserCard)
	}
}

// TestRegistry covers the registry's contract: put adds, get refreshes
// lastUsed, remove is idempotent on unknown ids, reapIdle returns only
// stale ids and closes them.
func TestRegistry(t *testing.T) {
	r := &registry{m: map[string]*session{}}

	// put + get
	now := time.Now()
	fake1 := &fakeComp{}
	r.put("a", &session{comp: fake1, backend: "local", openedAt: now, lastUsed: now})
	got, ok := r.get("a")
	if !ok || got.comp != fake1 {
		t.Fatalf("get(a): want fake1, got=%v ok=%v", got, ok)
	}

	// get refreshes lastUsed
	r.put("b", &session{comp: &fakeComp{}, backend: "local", openedAt: now, lastUsed: now.Add(-2 * time.Hour)})
	beforeGet := time.Now()
	_, _ = r.get("b")
	got2, _ := r.get("b")
	if !got2.lastUsed.After(beforeGet.Add(-time.Second)) {
		t.Errorf("get did not refresh lastUsed; got %v want >= %v", got2.lastUsed, beforeGet)
	}

	// remove on unknown returns ok=false, doesn't panic
	if _, ok := r.remove("does-not-exist"); ok {
		t.Errorf("remove of unknown returned ok=true")
	}

	// reapIdle closes only stale entries
	r.put("stale", &session{comp: &fakeComp{}, backend: "local", openedAt: now, lastUsed: now.Add(-2 * time.Hour)})
	r.put("provider-expired", &session{comp: &fakeComp{}, backend: "browserbase", timeout: 60, openedAt: now.Add(-2 * time.Minute), lastUsed: now})
	r.put("fresh", &session{comp: &fakeComp{}, backend: "local", openedAt: now, lastUsed: time.Now()})
	reaped := r.reapIdle(30 * time.Minute)
	sort.Strings(reaped)
	if len(reaped) != 2 || reaped[0] != "provider-expired" || reaped[1] != "stale" {
		t.Errorf("reapIdle: want [provider-expired stale], got %v", reaped)
	}
	if _, ok := r.get("fresh"); !ok {
		t.Errorf("fresh session was reaped")
	}
}

// TestBrowserSessionComputerUseClose drives the generic MCP surface
// end-to-end against a fake backend. The fake records OpenSession +
// Screenshot + Close calls so we can verify the registry actually
// closes the underlying Computer on browser_session(close).
func TestBrowserSessionComputerUseClose(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		if cfg.Type != "local" {
			t.Errorf("backend type: want local, got %q", cfg.Type)
		}
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	// open
	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":  "open",
		"backend": "local",
		"url":     "https://example.com",
	})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	openMap, ok := openOut.(map[string]any)
	if !ok {
		t.Fatalf("open returned %T, want map", openOut)
	}
	sessionID, _ := openMap["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned empty session_id; out=%v", openMap)
	}
	if got := openMap["backend"]; got != "local" {
		t.Errorf("open backend: want local, got %v", got)
	}
	if got := openMap["width"]; got != 1024 {
		t.Errorf("open width: want 1024, got %v", got)
	}
	assertBrowserViewReference(t, openMap["view"], sessionID)
	if fake.openSessionURL != "https://example.com" {
		t.Errorf("OpenSession URL: want example.com, got %q", fake.openSessionURL)
	}

	// screenshot
	shotOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "screenshot",
	})
	if err != nil {
		t.Fatalf("computer_use screenshot: %v", err)
	}
	shotMap := shotOut.(map[string]any)
	envelope := shotMap["screenshot"].(map[string]any)
	gotPNG, _ := base64.StdEncoding.DecodeString(envelope["base64"].(string))
	if len(gotPNG) != len(fake.png) || gotPNG[0] != 0x89 {
		t.Errorf("screenshot bytes round-trip failed: got %v", gotPNG)
	}
	if shotMap["mime_type"] != "image/png" {
		t.Errorf("screenshot mime: want image/png, got %v", shotMap["mime_type"])
	}
	if shotMap["current_url"] != "https://example.com" {
		t.Errorf("current_url: want example.com, got %v", shotMap["current_url"])
	}
	if fake.screenshotCalls != 1 {
		t.Errorf("screenshot calls: want 1, got %d", fake.screenshotCalls)
	}
	if got := fake.lastScreenshotAnnotate(); got == nil || *got != true {
		t.Errorf("computer_use screenshot annotate default: want true, got %v", got)
	}
	if _, ok := shotMap["som"]; ok {
		t.Fatalf("computer_use screenshot should not return structured som unless include_som=true: %#v", shotMap["som"])
	}

	fake.somTargets = []backends.SetOfMarkTarget{{
		Label:             3,
		X:                 100,
		Y:                 200,
		W:                 80,
		H:                 32,
		Tag:               "button",
		Text:              "I accept",
		AccessibleName:    "Publish",
		Disabled:          true,
		Loading:           true,
		Dangerous:         true,
		DestructiveEffect: "immediate_publish",
	}}
	safetyOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "screenshot",
	})
	if err != nil {
		t.Fatalf("computer_use default safety targets: %v", err)
	}
	if _, ok := safetyOut.(map[string]any)["som"]; ok {
		t.Fatalf("default screenshot unexpectedly returned full SoM: %#v", safetyOut)
	}
	safetyItems, ok := safetyOut.(map[string]any)["safety_targets"].([]backends.SetOfMarkTarget)
	if !ok || len(safetyItems) != 1 || safetyItems[0].AccessibleName != "Publish" || !safetyItems[0].Loading {
		t.Fatalf("default safety targets=%#v", safetyOut.(map[string]any)["safety_targets"])
	}
	somOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id":  sessionID,
		"action":      "screenshot",
		"include_som": true,
	})
	if err != nil {
		t.Fatalf("computer_use screenshot include_som: %v", err)
	}
	somItems, ok := somOut.(map[string]any)["som"].([]backends.SetOfMarkTarget)
	if !ok || len(somItems) != 1 || somItems[0].Label != 3 || somItems[0].Text != "I accept" ||
		somItems[0].AccessibleName != "Publish" || !somItems[0].Disabled || !somItems[0].Loading ||
		!somItems[0].Dangerous || somItems[0].DestructiveEffect != "immediate_publish" {
		t.Fatalf("structured som=%#v", somOut.(map[string]any)["som"])
	}
	safetyItems, ok = somOut.(map[string]any)["safety_targets"].([]backends.SetOfMarkTarget)
	if !ok || len(safetyItems) != 1 || safetyItems[0].AccessibleName != "Publish" || !safetyItems[0].Loading {
		t.Fatalf("default safety targets=%#v", somOut.(map[string]any)["safety_targets"])
	}

	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
	}); err == nil || !strings.Contains(err.Error(), "requires label") {
		t.Fatalf("computer_use click without label/coordinate: want target error, got %v", err)
	}

	clickOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id":  sessionID,
		"action":      "click",
		"label":       "3",
		"include_som": true,
	})
	if err != nil {
		t.Fatalf("computer_use click with string label: %v", err)
	}
	if clickOut.(map[string]any)["current_url"] != "https://example.com" {
		t.Errorf("click current_url: want example.com, got %v", clickOut.(map[string]any)["current_url"])
	}
	for _, omitted := range []string{"som", "tabs", "width", "height"} {
		if _, ok := clickOut.(map[string]any)[omitted]; ok {
			t.Errorf("click response should omit repeated %s metadata: %v", omitted, clickOut)
		}
	}
	if fake.lastAction.Type != "click" || fake.lastAction.Label != 3 {
		t.Errorf("click action: want label 3, got %+v", fake.lastAction)
	}
	_, err = app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"selector":   `a[href="https://go.marcoschwartz.com/digilo"]`,
		"label":      1, // compatibility selectors beat serializer defaults
	})
	if err != nil {
		t.Fatalf("computer_use selector click: %v", err)
	}
	if fake.lastAction.Type != "click" || fake.lastAction.Selector != `a[href="https://go.marcoschwartz.com/digilo"]` || fake.lastAction.Label != 0 {
		t.Fatalf("selector click action: got %+v", fake.lastAction)
	}
	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id":    sessionID,
		"action":        "click",
		"label":         1,
		"selector":      `a[href="https://go.marcoschwartz.com/digilo"]`,
		"coordinate":    "113,746",
		"expected_text": "Schedule",
	}); err != nil {
		t.Fatalf("computer_use coordinate with populated label: %v", err)
	}
	if fake.lastAction.Label != 0 || fake.lastAction.Selector != "" || fake.lastAction.X != 113 || fake.lastAction.Y != 746 ||
		fake.lastAction.ExpectedText != "Schedule" || !fake.lastAction.GuardDangerousCoordinate {
		t.Errorf("explicit coordinate should override populated label: got %+v", fake.lastAction)
	}
	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"label":      999,
	}); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("stale/unknown label should fail before dispatch: %v", err)
	}

	// close
	closeOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "close", "session_id": sessionID})
	if err != nil {
		t.Fatalf("browser_session close: %v", err)
	}
	if closeOut.(map[string]any)["closed"] != true {
		t.Errorf("close closed=true expected; got %v", closeOut)
	}
	assertBrowserViewReference(t, closeOut.(map[string]any)["view"], sessionID)
	if fake.closeCalls != 1 {
		t.Errorf("Close calls: want 1, got %d", fake.closeCalls)
	}
	listOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("legacy browser_session list after close: %v", err)
	}
	if sessions := listOut.(map[string]any)["sessions"].([]sessionInfo); len(sessions) != 0 {
		t.Fatalf("legacy browser_session list exposed closed sessions: %+v", sessions)
	}

	// close again — idempotent
	closeOut2, err := app.toolBrowserSession(ctx, map[string]any{"action": "close", "session_id": sessionID})
	if err != nil {
		t.Fatalf("browser_session close (2nd): %v", err)
	}
	if closeOut2.(map[string]any)["closed"] != false {
		t.Errorf("2nd close: closed=false expected; got %v", closeOut2)
	}
	assertBrowserViewReference(t, closeOut2.(map[string]any)["view"], sessionID)

	// screenshot after close — error, not panic
	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": sessionID, "action": "screenshot"}); err == nil {
		t.Errorf("screenshot after close: want error, got nil")
	}
}

func assertBrowserViewReference(t *testing.T, value any, sessionID string) {
	t.Helper()
	view, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("view = %T, want map", value)
	}
	if view["app"] != "computer" || view["name"] != "browser-view" {
		t.Fatalf("view identity = %#v", view)
	}
	props, ok := view["props"].(map[string]any)
	if !ok || len(props) != 1 || props["session_id"] != sessionID {
		t.Fatalf("view props = %#v, want only session_id=%q", view["props"], sessionID)
	}
}

func TestBrowserSessionDemoPresentationPropagatesToActions(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":            "open",
		"backend":           "browserbase",
		"url":               "https://example.com",
		"presentation_mode": "demo",
	})
	if err != nil {
		t.Fatalf("browser_session demo open: %v", err)
	}
	openMap := openOut.(map[string]any)
	if openMap["presentation_mode"] != "demo" || openMap["presentation_cursor_supported"] != true {
		t.Fatalf("demo presentation metadata: %#v", openMap)
	}
	sessionID := openMap["session_id"].(string)

	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "type",
		"text":       "demo",
	}); err != nil {
		t.Fatalf("computer_use demo type: %v", err)
	}
	got := fake.lastAction.Presentation
	if !got.Enabled() || !got.ShowCursor || got.TypingDelayMS <= 0 ||
		got.PointerDurationMS <= 0 || got.ClickEffectMS <= 0 || got.PostActionDelayMS <= 0 {
		t.Fatalf("demo presentation not propagated: %+v", got)
	}

	if _, err := app.toolBrowserClose(ctx, map[string]any{"session_id": sessionID}); err != nil {
		t.Fatalf("close demo session: %v", err)
	}
}

func TestBrowserSessionPresentationDefaultsToFastAndRejectsUnknown(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 800, Height: 600},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":  "open",
		"backend": "local",
	})
	if err != nil {
		t.Fatalf("browser_session fast open: %v", err)
	}
	openMap := openOut.(map[string]any)
	if openMap["presentation_mode"] != "fast" {
		t.Fatalf("default presentation mode: %#v", openMap)
	}
	sessionID := openMap["session_id"].(string)
	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "type",
		"text":       "fast",
	}); err != nil {
		t.Fatalf("computer_use fast type: %v", err)
	}
	if fake.lastAction.Presentation.Enabled() || fake.lastAction.Presentation.TypingDelayMS != 0 {
		t.Fatalf("default action behavior changed: %+v", fake.lastAction.Presentation)
	}
	_, _ = app.toolBrowserClose(ctx, map[string]any{"session_id": sessionID})

	if _, err := app.toolBrowserSession(ctx, map[string]any{
		"action":            "open",
		"backend":           "local",
		"presentation_mode": "cinematic",
	}); err == nil || !strings.Contains(err.Error(), "presentation_mode") {
		t.Fatalf("unknown presentation mode should fail validation: %v", err)
	}
}

func TestBrowserSessionEnvironmentIsOptInAndPropagates(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{display: backends.DisplaySize{Width: 390, Height: 844}}
	var created backends.Config
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		created = cfg
		return fake, nil
	}
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":   "open",
		"backend":  "steel",
		"viewport": map[string]any{"width": 390, "height": 844},
		"environment": map[string]any{
			"user_agent":          "Computer-Test/1.0",
			"locale":              "fr-FR",
			"timezone":            "Europe/Paris",
			"geolocation":         map[string]any{"latitude": 48.8566, "longitude": 2.3522},
			"device_scale_factor": 3.0,
			"mobile":              true,
			"touch":               true,
			"max_touch_points":    5,
		},
	})
	if err != nil {
		t.Fatalf("open with environment: %v", err)
	}
	if created.UserAgent != "Computer-Test/1.0" {
		t.Fatalf("native provider user agent = %q", created.UserAgent)
	}
	got := fake.openEnvironment
	if got.UserAgent != "Computer-Test/1.0" || got.Locale != "fr-FR" || got.Timezone != "Europe/Paris" {
		t.Fatalf("environment did not propagate: %+v", got)
	}
	if !reflect.DeepEqual(got.Languages, []string{"fr-FR"}) {
		t.Fatalf("effective languages = %#v", got.Languages)
	}
	if got.Geolocation == nil || got.Geolocation.Accuracy == nil || *got.Geolocation.Accuracy != 100 || got.Geolocation.Permission != "grant" {
		t.Fatalf("effective geolocation defaults = %+v", got.Geolocation)
	}
	openMap := out.(map[string]any)
	if openMap["environment"] == nil {
		t.Fatalf("effective environment missing from output: %#v", openMap)
	}
	firstSessionID := openMap["session_id"].(string)
	_, _ = app.toolBrowserClose(ctx, map[string]any{"session_id": firstSessionID})
	stored, err := dbGetSession(ctx.AppDB(), firstSessionID)
	if err != nil {
		t.Fatalf("read stored environment: %v", err)
	}
	if stored.Environment.UserAgent != "Computer-Test/1.0" || stored.Environment.Timezone != "Europe/Paris" {
		t.Fatalf("stored environment mismatch: %+v", stored.Environment)
	}

	// Omission is the compatibility contract: the zero environment reaches
	// the backend and is absent from tool output.
	fake = &fakeComp{display: backends.DisplaySize{Width: 1600, Height: 800}}
	out, err = app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open without environment: %v", err)
	}
	if !fake.openEnvironment.IsEmpty() {
		t.Fatalf("omitted environment changed defaults: %+v", fake.openEnvironment)
	}
	openMap = out.(map[string]any)
	if _, exists := openMap["environment"]; exists {
		t.Fatalf("omitted environment unexpectedly returned: %#v", openMap["environment"])
	}
	_, _ = app.toolBrowserClose(ctx, map[string]any{"session_id": openMap["session_id"]})
}

func TestBrowserSessionEnvironmentRejectsUnsupportedAndInvalidSettings(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	for name, args := range map[string]map[string]any{
		"invalid timezone": {"action": "open", "backend": "local", "environment": map[string]any{"timezone": "Paris"}},
		"service":          {"action": "open", "backend": "service", "environment": map[string]any{"locale": "de-DE"}},
		"attach":           {"action": "open", "backend": "browserbase", "backend_session_id": "provider-1", "environment": map[string]any{"locale": "de-DE"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := app.toolBrowserSession(ctx, args); err == nil {
				t.Fatal("expected environment validation error")
			}
		})
	}
}

func TestComputerUseReliableNavigationActions(t *testing.T) {
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com/start",
	}
	setURL := func(value string) {
		fake.mu.Lock()
		fake.url = value
		fake.mu.Unlock()
	}
	backEffective := true
	fake.executeHook = func(action backends.Action) error {
		switch action.Type {
		case "navigate":
			setURL(action.URL)
		case "back":
			if backEffective {
				setURL("https://example.com/start")
			}
		case "reload":
			// A reload intentionally retains the current URL.
		default:
			return fmt.Errorf("unexpected action %q", action.Type)
		}
		return nil
	}
	app := appWithSession("br_navigation", fake, "browserbase")
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	navigateOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_navigation",
		"action":     "navigate",
		"url":        "https://example.com/next",
	})
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	navigateMap := navigateOut.(map[string]any)
	if navigateMap["current_url"] != "https://example.com/next" || navigateMap["previous_url"] != "https://example.com/start" {
		t.Fatalf("navigate URL delta: %#v", navigateMap)
	}
	if navigateMap["url_changed"] != true || navigateMap["navigation"] != "navigate" {
		t.Fatalf("navigate metadata: %#v", navigateMap)
	}
	if fake.lastAction.URL != "https://example.com/next" {
		t.Fatalf("navigate URL not sent to backend: %+v", fake.lastAction)
	}

	backOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_navigation",
		"action":     "back",
	})
	if err != nil {
		t.Fatalf("back: %v", err)
	}
	backMap := backOut.(map[string]any)
	if backMap["current_url"] != "https://example.com/start" || backMap["url_changed"] != true {
		t.Fatalf("back URL delta: %#v", backMap)
	}

	reloadOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_navigation",
		"action":     "reload",
	})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloadMap := reloadOut.(map[string]any)
	if reloadMap["reloaded"] != true || reloadMap["url_changed"] != false {
		t.Fatalf("reload metadata: %#v", reloadMap)
	}

	backEffective = false
	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_navigation",
		"action":     "back",
	}); err == nil || !strings.Contains(err.Error(), "navigation_ineffective") {
		t.Fatalf("ineffective back should fail explicitly: %v", err)
	}
	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_navigation",
		"action":     "navigate",
		"url":        "not a URL",
	}); err == nil || !strings.Contains(err.Error(), "invalid_navigation") {
		t.Fatalf("invalid navigate should fail explicitly: %v", err)
	}
}

func TestComputerUseNavigationAndSafetySchema(t *testing.T) {
	tool := findTool(t, (&App{}).MCPTools(), "computer_use")
	properties := tool.InputSchema["properties"].(map[string]any)
	actions := properties["action"].(map[string]any)["enum"].([]string)
	for _, required := range []string{"navigate", "back", "reload", "wait_for_stable"} {
		if !slices.Contains(actions, required) {
			t.Fatalf("computer_use action schema missing %q: %v", required, actions)
		}
	}
	for _, required := range []string{"url", "expected_text", "quiet_ms", "timeout_ms"} {
		if _, ok := properties[required]; !ok {
			t.Fatalf("computer_use schema missing %s", required)
		}
	}

	app := appWithSession("br_service", &fakeComp{url: "https://example.com"}, "service")
	if _, err := app.toolComputerUse(tk.NewAppCtx(t, "apteva.yaml"), map[string]any{
		"session_id": "br_service",
		"action":     "reload",
	}); err == nil || !strings.Contains(err.Error(), "backend_not_supported") {
		t.Fatalf("service navigation should be rejected as unverifiable: %v", err)
	}
}

func TestComputerUseWaitForStableValidationAndDispatch(t *testing.T) {
	fake := &fakeComp{png: []byte{0x89, 0x50, 0x4e, 0x47}, url: "https://example.com"}
	app := appWithSession("br_stable", fake, "local")
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_stable",
		"action":     "wait_for_stable",
		"quiet_ms":   700,
		"timeout_ms": 3000,
	})
	if err != nil {
		t.Fatalf("wait_for_stable: %v", err)
	}
	if out.(map[string]any)["stable"] != true || out.(map[string]any)["quiet_ms"] != 700 {
		t.Fatalf("wait_for_stable result: %#v", out)
	}
	if fake.lastAction.Type != "wait_for_stable" || fake.lastAction.QuietMS != 700 || fake.lastAction.TimeoutMS != 3000 {
		t.Fatalf("wait_for_stable dispatch: %+v", fake.lastAction)
	}
	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_stable",
		"action":     "wait_for_stable",
		"quiet_ms":   2000,
		"timeout_ms": 1000,
	}); err == nil || !strings.Contains(err.Error(), "invalid_wait") {
		t.Fatalf("invalid stability bounds should fail: %v", err)
	}
	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_stable",
		"action":     "scroll",
		"quiet_ms":   500,
	}); err == nil || !strings.Contains(err.Error(), "invalid_wait") {
		t.Fatalf("quiet_ms on another action should fail: %v", err)
	}
}

func TestComputerUseRejectsExplicitLabelZero(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	_, err = app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"label":      0,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid_target") || !strings.Contains(err.Error(), "label=0") {
		t.Fatalf("click label=0: want invalid_target, got %v", err)
	}
	if fake.lastAction.Type != "" {
		t.Fatalf("invalid click executed action: %+v", fake.lastAction)
	}
}

func TestInternalSessionExtractSuccessfulRenderedExtraction(t *testing.T) {
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1280, Height: 720},
		url:     "https://example.com/rendered",
		extractResult: backends.ExtractResult{
			URL:         "https://example.com/rendered",
			Title:       "Rendered Page",
			Description: "Live DOM content",
			Text:        "Hello from the hydrated page",
			Markdown:    "Hello from the hydrated page",
			Links:       []backends.ExtractLink{{URL: "https://example.com/next", Text: "Next"}},
			Regions: []backends.ExtractRegion{{
				ID:              "r1",
				Tag:             "section",
				Heading:         "Contact",
				Text:            "Contact partnerships@example.com",
				CoordinateFrame: "document_css_px",
				Rect:            backends.ExtractRect{X: 10, Y: 20, Width: 300, Height: 120},
				ViewportRect:    backends.ExtractRect{X: 10, Y: 20, Width: 300, Height: 120},
				Visible:         true,
			}},
			Metadata:          map[string]any{"description": "Live DOM content"},
			StructuredData:    map[string]any{"json_ld": []any{map[string]any{"@type": "Article"}}},
			Rendered:          true,
			ExtractionBackend: "browser_dom",
		},
	}
	app := &App{reg: &registry{m: map[string]*session{}}}
	app.reg.put("br_test", &session{
		comp:     fake,
		backend:  "local",
		openedAt: time.Now(),
		lastUsed: time.Now(),
	})

	w := internalExtractResponse(t, app, "br_test", `{
		"formats":["text","markdown","metadata","links","regions"],
		"max_chars":2000
	}`, "web")
	if w.Code != http.StatusOK {
		t.Fatalf("extract status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode extraction response: %v", err)
	}
	if out["session_id"] != "br_test" {
		t.Errorf("session_id: got %v", out["session_id"])
	}
	if out["extraction_backend"] != "browser_dom" {
		t.Errorf("extraction_backend: got %v", out["extraction_backend"])
	}
	if out["title"] != "Rendered Page" {
		t.Errorf("title: got %v", out["title"])
	}
	if out["text"] != "Hello from the hydrated page" {
		t.Errorf("text: got %v", out["text"])
	}
	if _, ok := out["structured_data"]; ok {
		t.Errorf("unrequested structured_data should be omitted: %#v", out["structured_data"])
	}
	regions, ok := out["regions"].([]any)
	if !ok || len(regions) != 1 || regions[0].(map[string]any)["heading"] != "Contact" {
		t.Fatalf("regions missing: %#v", out["regions"])
	}
	if got := fake.extractOptions.MaxChars; got != 2000 {
		t.Errorf("max_chars forwarded: want 2000, got %d", got)
	}
	if len(fake.extractOptions.Formats) != 5 || fake.extractOptions.Formats[0] != "text" {
		t.Errorf("formats forwarded: got %v", fake.extractOptions.Formats)
	}
	if !fake.extractOptions.Readability {
		t.Error("readability should default to true")
	}
}

func TestInternalSessionExtractEnforcesAggregateResponseLimit(t *testing.T) {
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1280, Height: 720},
		url:     "https://example.com/large",
		extractResult: backends.ExtractResult{
			URL:               "https://example.com/large",
			Title:             "Large page",
			Text:              strings.Repeat("text ", 1000),
			Markdown:          strings.Repeat("markdown ", 1000),
			HTML:              strings.Repeat("<p>html</p>", 1000),
			Links:             []backends.ExtractLink{{URL: "https://example.com/next", Text: strings.Repeat("link ", 200)}},
			Metadata:          map[string]any{"description": strings.Repeat("metadata ", 500)},
			Rendered:          true,
			ExtractionBackend: "browser_dom",
		},
	}
	app := appWithSession("br_large", fake, "local")

	out, err := app.extractSessionDOM("br_large", extractOptions{
		Formats:  []string{"text", "markdown", "html", "links", "metadata"},
		MaxChars: 700,
	})
	if err != nil {
		t.Fatalf("extract large response: %v", err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal limited response: %v", err)
	}
	if len(encoded) > 700 {
		t.Fatalf("aggregate response exceeds max_chars: got %d bytes\n%s", len(encoded), encoded)
	}
	if out["truncated"] != true {
		t.Fatalf("limited response should report truncated=true: %#v", out)
	}
	if got := fake.extractOptions.MaxChars; got != 700 {
		t.Fatalf("backend max chars: want 700, got %d", got)
	}
}

func TestInternalSessionExtractDefaultsToTextOnly(t *testing.T) {
	fake := &fakeComp{
		url: "https://example.com/default",
		extractResult: backends.ExtractResult{
			URL:      "https://example.com/default",
			Text:     "default text",
			Markdown: "default markdown",
			HTML:     "<p>default html</p>",
		},
	}
	app := appWithSession("br_default", fake, "local")
	out, err := app.extractSessionDOM("br_default", extractOptions{})
	if err != nil {
		t.Fatalf("extract defaults: %v", err)
	}
	if out["text"] != "default text" {
		t.Fatalf("default text missing: %#v", out)
	}
	for _, omitted := range []string{"markdown", "html", "links", "metadata", "structured_data"} {
		if _, ok := out[omitted]; ok {
			t.Fatalf("default response should omit %s: %#v", omitted, out)
		}
	}
	if !reflect.DeepEqual(fake.extractOptions.Formats, []string{"text"}) {
		t.Fatalf("default backend formats: got %v", fake.extractOptions.Formats)
	}
	if fake.extractOptions.MaxChars != defaultExtractChars {
		t.Fatalf("default backend max chars: want %d, got %d", defaultExtractChars, fake.extractOptions.MaxChars)
	}
}

func TestInternalSessionExtractErrorsAndBounds(t *testing.T) {
	t.Run("missing internal caller header", func(t *testing.T) {
		fake := &fakeComp{}
		app := appWithSession("br_test", fake, "local")
		w := internalExtractResponse(t, app, "br_test", `{}`, "")
		if w.Code != http.StatusForbidden {
			t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
		}
		if !reflect.DeepEqual(fake.extractOptions, backends.ExtractOptions{}) {
			t.Fatalf("extractor called without internal header: %+v", fake.extractOptions)
		}
	})

	t.Run("unknown session", func(t *testing.T) {
		app := &App{reg: &registry{m: map[string]*session{}}}
		w := internalExtractResponse(t, app, "br_missing", `{}`, "web")
		if w.Code != http.StatusNotFound {
			t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("backend without DOM extractor", func(t *testing.T) {
		app := appWithSession("br_test", &nonExtractComp{}, "service")
		w := internalExtractResponse(t, app, "br_test", `{}`, "web")
		if w.Code != http.StatusNotImplemented {
			t.Fatalf("status: want 501, got %d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("invalid options", func(t *testing.T) {
		fake := &fakeComp{}
		app := appWithSession("br_test", fake, "local")
		w := internalExtractResponse(t, app, "br_test", `{"formats":["pdf"]}`, "web")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status: want 400, got %d body=%s", w.Code, w.Body.String())
		}
		w = internalExtractResponse(t, app, "br_test", `{"unknown":true}`, "web")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unknown field status: want 400, got %d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("oversized options are clamped", func(t *testing.T) {
		fake := &fakeComp{}
		app := appWithSession("br_test", fake, "local")
		w := internalExtractResponse(t, app, "br_test", `{
			"formats":["TEXT","text","json"],
			"max_chars":999999,
			"readability":false,
			"wait_ms":999999
		}`, "web")
		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
		}
		want := backends.ExtractOptions{
			Formats:     []string{"text", "json"},
			MaxChars:    maxExtractChars,
			Readability: false,
			WaitMS:      maxExtractWaitMS,
		}
		if !reflect.DeepEqual(fake.extractOptions, want) {
			t.Fatalf("bounded options: want %+v, got %+v", want, fake.extractOptions)
		}
	})
}

func TestBrowserExtractIsAppOnly(t *testing.T) {
	app := &App{}
	foundRuntime := false
	for _, tool := range app.MCPTools() {
		if tool.Name == "browser_extract" {
			foundRuntime = true
			if tool.Exposure != sdk.ToolExposureAppOnly {
				t.Fatalf("runtime exposure=%q, want app_only", tool.Exposure)
			}
		}
	}
	if !foundRuntime {
		t.Fatal("browser_extract runtime handler is not registered")
	}
	foundSpec := false
	for _, tool := range app.Manifest().Provides.MCPTools {
		if tool.Name == "browser_extract" {
			foundSpec = true
			if tool.Exposure != sdk.ToolExposureAppOnly {
				t.Fatalf("manifest exposure=%q, want app_only", tool.Exposure)
			}
		}
	}
	if !foundSpec {
		t.Fatal("browser_extract manifest spec is not registered")
	}
	foundRoute := false
	for _, route := range app.HTTPRoutes() {
		if route.Method == http.MethodPost && route.Pattern == "/internal/sessions/{id}/extract" {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Fatal("internal session extraction route is not registered")
	}
}

func TestSessionReuseIsNotAdvertisedToAgents(t *testing.T) {
	app := &App{}
	for _, tool := range app.MCPTools() {
		if tool.Name == "browser_list" {
			t.Fatal("browser_list must not be exposed through MCPTools")
		}
	}

	tool := findTool(t, app.MCPTools(), "browser_session")
	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("browser_session schema has no properties map: %#v", tool.InputSchema)
	}
	if _, ok := props["backend_session_id"]; ok {
		t.Fatal("browser_session must not advertise provider-session attachment")
	}
	if _, ok := props["proxy"]; ok {
		t.Fatal("browser_session must not advertise the deprecated proxy boolean")
	}
	environmentSchema, ok := props["environment"].(map[string]any)
	if !ok {
		t.Fatalf("browser_session environment schema missing: %#v", props["environment"])
	}
	environmentProps, ok := environmentSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("browser_session environment properties missing: %#v", environmentSchema)
	}
	for _, field := range []string{"user_agent", "locale", "languages", "timezone", "geolocation", "device_scale_factor", "mobile", "touch", "max_touch_points"} {
		if _, ok := environmentProps[field]; !ok {
			t.Fatalf("browser_session environment missing %q", field)
		}
	}
	presentationMode, ok := props["presentation_mode"].(map[string]any)
	if !ok {
		t.Fatalf("browser_session presentation_mode schema missing: %#v", props["presentation_mode"])
	}
	modes, ok := presentationMode["enum"].([]string)
	if !ok || len(modes) != 2 || modes[0] != "fast" || modes[1] != "demo" {
		t.Fatalf("browser_session presentation_mode enum: %#v", presentationMode["enum"])
	}
	action, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatalf("browser_session action schema missing: %#v", props["action"])
	}
	actions, ok := action["enum"].([]string)
	if !ok {
		t.Fatalf("browser_session action enum has unexpected type: %#v", action["enum"])
	}
	for _, forbidden := range []string{"list", "resume"} {
		for _, action := range actions {
			if action == forbidden {
				t.Fatalf("browser_session advertises %q action: %v", forbidden, actions)
			}
		}
	}
	if !strings.Contains(tool.Description, "Always use action=open for new browsing work") {
		t.Fatalf("browser_session does not direct agents to fresh sessions:\n%s", tool.Description)
	}
	legacyOpen := findTool(t, app.MCPTools(), "browser_open")
	legacyProps := legacyOpen.InputSchema["properties"].(map[string]any)
	if _, ok := legacyProps["proxy"]; ok {
		t.Fatal("browser_open must not advertise the deprecated proxy boolean")
	}

	for _, manifestTool := range app.Manifest().Provides.MCPTools {
		if manifestTool.Name == "browser_list" {
			t.Fatal("browser_list must not be declared in the manifest")
		}
	}
}

func TestComputerUseSelectOptionArgs(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "select_option",
		"text":       "Gold",
	}); err == nil || !strings.Contains(err.Error(), "requires label") {
		t.Fatalf("select_option without target: want target error, got %v", err)
	}

	_, err = app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "select_option",
		"selector":   "button[role=combobox]",
		"texts":      []any{"Gold", "VIP"},
		"values":     `["gold","vip"]`,
		"mode":       "add",
	})
	if err != nil {
		t.Fatalf("select_option: %v", err)
	}
	if fake.lastAction.Type != "select_option" {
		t.Fatalf("action type: got %+v", fake.lastAction)
	}
	if fake.lastAction.Selector != "button[role=combobox]" || fake.lastAction.Mode != "add" {
		t.Fatalf("select action target/mode: got %+v", fake.lastAction)
	}
	if got := strings.Join(fake.lastAction.Texts, ","); got != "Gold,VIP" {
		t.Fatalf("texts: got %q", got)
	}
	if got := strings.Join(fake.lastAction.Values, ","); got != "gold,vip" {
		t.Fatalf("values: got %q", got)
	}
}

func TestComputerUseSetCheckedArgs(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "set_checked",
		"selector":   "#sell",
	}); err == nil || !strings.Contains(err.Error(), "requires checked") {
		t.Fatalf("set_checked without checked: want checked error, got %v", err)
	}

	_, err = app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "set_checked",
		"selector":   "#sell",
		"checked":    false,
	})
	if err != nil {
		t.Fatalf("set_checked false: %v", err)
	}
	if fake.lastAction.Type != "set_checked" || fake.lastAction.Selector != "#sell" || fake.lastAction.Checked != false {
		t.Fatalf("set_checked action: got %+v", fake.lastAction)
	}
}

func TestComputerUseSetTemporalArgs(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "set_temporal",
		"selector":   "#time",
	}); err == nil || !strings.Contains(err.Error(), "requires value") {
		t.Fatalf("set_temporal without value: want value error, got %v", err)
	}

	_, err = app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "set_temporal",
		"selector":   "#time",
		"value":      "11:00 AM",
	})
	if err != nil {
		t.Fatalf("set_temporal: %v", err)
	}
	if fake.lastAction.Type != "set_temporal" || fake.lastAction.Selector != "#time" || fake.lastAction.Value != "11:00 AM" {
		t.Fatalf("set_temporal action: got %+v", fake.lastAction)
	}
}

func TestComputerUseContextCanceledEvictsSession(t *testing.T) {
	fake := &fakeComp{
		display:    backends.DisplaySize{Width: 1024, Height: 768},
		executeErr: context.Canceled,
		somTargets: []backends.SetOfMarkTarget{{Label: 5}},
	}
	app := &App{reg: &registry{m: map[string]*session{}}}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))
	openedAt := time.Now().Add(-time.Minute)
	app.reg.put("br_dead", &session{
		comp:         fake,
		backend:      "browserbase",
		appContextID: "ctx_login",
		contextName:  "Ashley Login",
		persist:      true,
		openedAt:     openedAt,
		lastUsed:     openedAt,
	})

	_, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_dead",
		"action":     "click",
		"label":      5,
	})
	if err == nil || !strings.Contains(err.Error(), "session_unhealthy") || !strings.Contains(err.Error(), "context_id=ctx_login") {
		t.Fatalf("context canceled: want session_unhealthy with context, got %v", err)
	}
	if _, ok := app.reg.get("br_dead"); ok {
		t.Fatalf("unhealthy session was not removed from registry")
	}
	if fake.closeCalls != 1 {
		t.Fatalf("unhealthy session Close calls: want 1, got %d", fake.closeCalls)
	}
	payload := lastEventData(t, rec, "session.closed")
	if payload["session_id"] != "br_dead" || payload["close_reason"] != "session_unhealthy" {
		t.Fatalf("session.closed payload = %#v", payload)
	}
}

func TestComputerUseDeadlineExceededIsActionTimeout(t *testing.T) {
	fake := &fakeComp{
		display:    backends.DisplaySize{Width: 1024, Height: 768},
		executeErr: context.DeadlineExceeded,
		somTargets: []backends.SetOfMarkTarget{{Label: 5}},
	}
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	now := time.Now()
	app.reg.put("br_slow", &session{comp: fake, backend: "browserbase", openedAt: now, lastUsed: now})

	_, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_slow",
		"action":     "click",
		"label":      5,
	})
	if err == nil || !strings.Contains(err.Error(), "action_timeout") {
		t.Fatalf("deadline exceeded: want action_timeout, got %v", err)
	}
	if _, ok := app.reg.get("br_slow"); !ok {
		t.Fatalf("action timeout should not evict session automatically")
	}
}

func TestComputerUseReportsUnsafeClickRejection(t *testing.T) {
	fake := &fakeComp{
		display:    backends.DisplaySize{Width: 1024, Height: 768},
		executeErr: fmt.Errorf(`click: click rejected: expected target "Schedule" but live target is "Publish"`),
	}
	app := appWithSession("br_guard", fake, "local")
	_, err := app.toolComputerUse(tk.NewAppCtx(t, "apteva.yaml"), map[string]any{
		"session_id":    "br_guard",
		"action":        "click",
		"coordinate":    "900,36",
		"expected_text": "Schedule",
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe_click_rejected") || !strings.Contains(err.Error(), `live target is \"Publish\"`) {
		t.Fatalf("guard rejection should be actionable: %v", err)
	}
}

func TestComputerUseUploadFileFromBase64(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	var seenPath string
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
		executeHook: func(action backends.Action) error {
			if action.Type != "upload_file" {
				return nil
			}
			if action.Selector != "input#mainMedia" {
				t.Fatalf("upload selector: got %q", action.Selector)
			}
			if len(action.Files) != 1 {
				t.Fatalf("upload files: got %v", action.Files)
			}
			seenPath = action.Files[0]
			raw, err := os.ReadFile(seenPath)
			if err != nil {
				t.Fatalf("read prepared upload file: %v", err)
			}
			if string(raw) != "hello image" {
				t.Fatalf("prepared upload bytes: got %q", string(raw))
			}
			if filepath.Base(seenPath) != "photo.jpg" {
				t.Fatalf("prepared upload filename: got %q", filepath.Base(seenPath))
			}
			return nil
		},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "upload_file",
		"selector":   "input#mainMedia",
		"base64":     base64.StdEncoding.EncodeToString([]byte("hello image")),
		"filename":   "photo.jpg",
		"mime_type":  "image/jpeg",
	})
	if err != nil {
		t.Fatalf("upload_file: %v", err)
	}
	if seenPath == "" {
		t.Fatal("fake backend did not receive upload file path")
	}
	if _, err := os.Stat(seenPath); !os.IsNotExist(err) {
		t.Fatalf("temporary upload file should be removed after action, stat err=%v", err)
	}
	m := out.(map[string]any)
	if m["uploaded"] != true || m["filename"] != "photo.jpg" || m["mime_type"] != "image/jpeg" {
		t.Fatalf("upload output metadata = %#v", m)
	}
}

func TestComputerUseUploadFileFromSourceURL(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Disposition", `attachment; filename="remote.png"`)
		_, _ = w.Write([]byte("png bytes"))
	}))
	t.Cleanup(srv.Close)

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		executeHook: func(action backends.Action) error {
			if len(action.Files) != 1 {
				t.Fatalf("upload files: got %v", action.Files)
			}
			if filepath.Base(action.Files[0]) != "remote.png" {
				t.Fatalf("source filename: got %q", filepath.Base(action.Files[0]))
			}
			raw, err := os.ReadFile(action.Files[0])
			if err != nil {
				t.Fatalf("read source file: %v", err)
			}
			if string(raw) != "png bytes" {
				t.Fatalf("source bytes: got %q", string(raw))
			}
			return nil
		},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)
	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "upload_file",
		"selector":   "input[type=file]",
		"source_url": srv.URL + "/remote.png",
	})
	if err != nil {
		t.Fatalf("upload_file source_url: %v", err)
	}
	if out.(map[string]any)["filename"] != "remote.png" {
		t.Fatalf("upload output filename: %v", out)
	}
}

func TestComputerUseUploadFileFromSourceURLRetriesIPv4(t *testing.T) {
	prevBackend := newBackend
	prevClient := sourceURLHTTPClient
	prevIPv4Client := sourceURLIPv4HTTPClient
	prevPublicDNSClient := sourceURLPublicDNSHTTPClient
	t.Cleanup(func() {
		newBackend = prevBackend
		sourceURLHTTPClient = prevClient
		sourceURLIPv4HTTPClient = prevIPv4Client
		sourceURLPublicDNSHTTPClient = prevPublicDNSClient
	})

	var primaryCalls, ipv4Calls int
	sourceURLHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		primaryCalls++
		return nil, fmt.Errorf("dial tcp [2606:4700::6810:e684]:443: connect: no route to host")
	})}
	sourceURLIPv4HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		ipv4Calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"image/png"},
			},
			Body: io.NopCloser(strings.NewReader("ipv4 png bytes")),
		}, nil
	})}

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		executeHook: func(action backends.Action) error {
			if action.Type != "upload_file" {
				return nil
			}
			if len(action.Files) != 1 {
				t.Fatalf("upload files: got %v", action.Files)
			}
			raw, err := os.ReadFile(action.Files[0])
			if err != nil {
				t.Fatalf("read source file: %v", err)
			}
			if string(raw) != "ipv4 png bytes" {
				t.Fatalf("source bytes: got %q", string(raw))
			}
			return nil
		},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)
	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "upload_file",
		"selector":   "input[type=file]",
		"source_url": "https://example.test/image.png",
		"filename":   "fallback.png",
	})
	if err != nil {
		t.Fatalf("upload_file source_url with IPv4 retry: %v", err)
	}
	if primaryCalls != 1 || ipv4Calls != 1 {
		t.Fatalf("source URL calls: primary=%d ipv4=%d", primaryCalls, ipv4Calls)
	}
	if out.(map[string]any)["filename"] != "fallback.png" {
		t.Fatalf("upload output filename: %v", out)
	}
}

func TestComputerUseUploadFileFromSourceURLRetriesPublicDNS(t *testing.T) {
	prevBackend := newBackend
	prevClient := sourceURLHTTPClient
	prevIPv4Client := sourceURLIPv4HTTPClient
	prevPublicDNSClient := sourceURLPublicDNSHTTPClient
	t.Cleanup(func() {
		newBackend = prevBackend
		sourceURLHTTPClient = prevClient
		sourceURLIPv4HTTPClient = prevIPv4Client
		sourceURLPublicDNSHTTPClient = prevPublicDNSClient
	})

	var primaryCalls, ipv4Calls, publicDNSCalls int
	dnsErr := fmt.Errorf("dial tcp: lookup fresh.trycloudflare.com: no such host")
	sourceURLHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		primaryCalls++
		return nil, dnsErr
	})}
	sourceURLIPv4HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		ipv4Calls++
		return nil, dnsErr
	})}
	sourceURLPublicDNSHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		publicDNSCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"image/png"},
			},
			Body: io.NopCloser(strings.NewReader("public dns png bytes")),
		}, nil
	})}

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		executeHook: func(action backends.Action) error {
			if action.Type != "upload_file" {
				return nil
			}
			if len(action.Files) != 1 {
				t.Fatalf("upload files: got %v", action.Files)
			}
			raw, err := os.ReadFile(action.Files[0])
			if err != nil {
				t.Fatalf("read source file: %v", err)
			}
			if string(raw) != "public dns png bytes" {
				t.Fatalf("source bytes: got %q", string(raw))
			}
			return nil
		},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)
	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "upload_file",
		"selector":   "input[type=file]",
		"source_url": "https://fresh.trycloudflare.com/image.png",
		"filename":   "public-dns.png",
	})
	if err != nil {
		t.Fatalf("upload_file source_url with public DNS retry: %v", err)
	}
	if primaryCalls != 1 || ipv4Calls != 1 || publicDNSCalls != 1 {
		t.Fatalf("source URL calls: primary=%d ipv4=%d publicDNS=%d", primaryCalls, ipv4Calls, publicDNSCalls)
	}
	if out.(map[string]any)["filename"] != "public-dns.png" {
		t.Fatalf("upload output filename: %v", out)
	}
}

func TestComputerUseUploadFileUnsupportedBackend(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	now := time.Now()
	app.reg.put("br_steel", &session{comp: &fakeComp{}, backend: "steel", openedAt: now, lastUsed: now})
	_, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": "br_steel",
		"action":     "upload_file",
		"base64":     base64.StdEncoding.EncodeToString([]byte("x")),
		"filename":   "x.txt",
	})
	if err == nil || !strings.Contains(err.Error(), "backend_not_supported") {
		t.Fatalf("unsupported upload backend: want backend_not_supported, got %v", err)
	}
}

func TestBrowserSessionResumeWithLiveAppSessionIDReturnsStatus(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	resumeOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":     "resume",
		"backend":    "browserbase",
		"session_id": sessionID,
	})
	if err != nil {
		t.Fatalf("resume live app session: %v", err)
	}
	if got := resumeOut.(map[string]any)["session_id"]; got != sessionID {
		t.Fatalf("resume returned session_id=%v, want %s", got, sessionID)
	}
	if fake.openSessionID != "" {
		t.Fatalf("resume with live app session called provider attach with %q", fake.openSessionID)
	}
}

func TestBrowserSessionResumeWithStaleAppSessionIDIsClearError(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	called := false
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		called = true
		return &fakeComp{}, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := app.toolBrowserSession(ctx, map[string]any{
		"action":     "resume",
		"backend":    "browserbase",
		"session_id": "br_d911d6d7aa06cc5a",
	})
	if err == nil || !strings.Contains(err.Error(), "session_not_active") {
		t.Fatalf("resume stale app session: want session_not_active, got %v", err)
	}
	if called {
		t.Fatalf("resume stale app session should not instantiate provider backend")
	}
}

func TestBrowserSessionResumeRequiresExplicitBackendSessionID(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	called := false
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		called = true
		return &fakeComp{}, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := app.toolBrowserSession(ctx, map[string]any{
		"action":  "resume",
		"backend": "browserbase",
	})
	if err == nil || !strings.Contains(err.Error(), "backend_session_id required") {
		t.Fatalf("resume without ids: want backend_session_id required, got %v", err)
	}
	if called {
		t.Fatalf("resume without ids should not instantiate provider backend")
	}
}

func TestBrowserSessionResumeWithBackendSessionIDAttachesProvider(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{display: backends.DisplaySize{Width: 1024, Height: 768}, url: "https://example.com"}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":             "resume",
		"backend":            "browserbase",
		"backend_session_id": "1733196a-6c7b-480e-b8d2-e9fdaa47bc85",
	})
	if err != nil {
		t.Fatalf("resume provider session: %v", err)
	}
	if fake.openSessionID != "1733196a-6c7b-480e-b8d2-e9fdaa47bc85" {
		t.Fatalf("provider attach id: got %q", fake.openSessionID)
	}
	if out.(map[string]any)["session_id"] == "" {
		t.Fatalf("resume provider session returned no app session: %v", out)
	}
}

func TestBrowserSessionResumeProviderExpiredError(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{openErr: fmt.Errorf("browserbase: lookup session bad: HTTP 400: Invalid Session ID")}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := app.toolBrowserSession(ctx, map[string]any{
		"action":             "resume",
		"backend":            "browserbase",
		"backend_session_id": "bad",
	})
	if err == nil || !strings.Contains(err.Error(), "provider_session_expired") {
		t.Fatalf("resume invalid provider session: want provider_session_expired, got %v", err)
	}
}

func TestValidateBackendConfiguredReportsFastErrors(t *testing.T) {
	cases := []struct {
		name string
		cfg  backends.Config
		want string
	}{
		{name: "browserbase", cfg: backends.Config{Type: "browserbase"}, want: "backend_not_configured: browserbase api_key"},
		{name: "steel", cfg: backends.Config{Type: "steel"}, want: "backend_not_configured: steel api_key"},
		{name: "browser-engine", cfg: backends.Config{Type: "browser-engine"}, want: "backend_not_configured: browser-engine api_key"},
		{name: "service", cfg: backends.Config{Type: "service"}, want: "backend_not_configured: service backend_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBackendConfigured(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateBackendConfigured: want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestComputerUseAutoFollowsSingleNewTab(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	newTab := backends.TabInfo{ID: "tab_detail", URL: "https://example.com/models/42", Title: "Model detail"}
	fake := &fakeComp{
		display:       backends.DisplaySize{Width: 1024, Height: 768},
		png:           []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:           "https://example.com/results",
		activeTabID:   "tab_results",
		tabs:          []backends.TabInfo{{ID: "tab_results", URL: "https://example.com/results", Title: "Results"}},
		addTabOnClick: &newTab,
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": "10,10",
	})
	if err != nil {
		t.Fatalf("computer_use click: %v", err)
	}
	m := out.(map[string]any)
	if got := m["current_url"]; got != newTab.URL {
		t.Fatalf("current_url after new tab click: want %s, got %v", newTab.URL, got)
	}
	if got := m["active_tab_id"]; got != newTab.ID {
		t.Fatalf("active_tab_id: want %s, got %v", newTab.ID, got)
	}
	if got := m["switched_tab"]; got != true {
		t.Fatalf("switched_tab: want true, got %v", got)
	}
	if got := m["tab_count"]; got != 2 {
		t.Fatalf("tab_count delta: want 2, got %v", got)
	}
	if newTabs, ok := m["new_tabs"].([]backends.TabInfo); !ok || len(newTabs) != 1 || newTabs[0].ID != newTab.ID {
		t.Fatalf("new_tabs delta: got %#v", m["new_tabs"])
	}
	if _, ok := m["tabs"]; ok {
		t.Fatalf("action response should not repeat the full tab list: %#v", m["tabs"])
	}
	if len(fake.switchCalls) != 1 || fake.switchCalls[0] != newTab.ID {
		t.Fatalf("switch calls: want [%s], got %v", newTab.ID, fake.switchCalls)
	}
	if fake.screenshotCalls != 0 {
		t.Fatalf("summary-only click should not embed a switched-tab screenshot, got %d calls", fake.screenshotCalls)
	}
}

func TestComputerUseReturnsSummaryAndFrameReferenceByDefault(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display:     backends.DisplaySize{Width: 1024, Height: 768},
		png:         []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:         "https://example.com",
		activeTabID: "tab_1",
		tabs:        []backends.TabInfo{{ID: "tab_1", URL: "https://example.com"}},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": "10,10",
	})
	if err != nil {
		t.Fatalf("computer_use click: %v", err)
	}
	m := out.(map[string]any)
	if _, ok := m["screenshot"]; ok {
		t.Fatalf("ordinary action should not embed a screenshot: %v", m)
	}
	if got := m["screenshot_available"]; got != true {
		t.Fatalf("latest frame should remain available: %v", m)
	}
	if got, _ := m["screenshot_url"].(string); !strings.Contains(got, "/sessions/"+sessionID+"/screenshot") {
		t.Fatalf("missing live frame reference: %v", m)
	}
	if len(fake.actionOnlyCalls) != 1 {
		t.Fatalf("action-only should avoid an unnecessary capture: %+v", fake.actionOnlyCalls)
	}
	if fake.lastAction.Type != "click" {
		t.Fatalf("execute action: want click, got %+v", fake.lastAction)
	}
	for _, omitted := range []string{"tabs", "active_tab_id", "width", "height"} {
		if _, ok := m[omitted]; ok {
			t.Fatalf("ordinary action should omit repeated %s metadata: %#v", omitted, m)
		}
	}
}

func TestComputerUseActionOnlyAlsoAppliesToWait(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": "10,10",
	})
	if err != nil {
		t.Fatalf("computer_use click: %v", err)
	}
	m := out.(map[string]any)
	if got := m["screenshot_available"]; got != true {
		t.Fatalf("screenshot_available: want true, got %v", got)
	}
	if _, ok := m["screenshot_b64"]; ok {
		t.Fatalf("screenshot_b64 should be omitted when screenshot is skipped: %v", m)
	}
	if got := m["post_action_screenshot"]; got != "not_embedded" {
		t.Fatalf("post_action_screenshot: want not_embedded, got %v", got)
	}
	if len(fake.actionOnlyCalls) != 1 || fake.actionOnlyCalls[0].Type != "click" {
		t.Fatalf("action-only calls: want one click, got %+v", fake.actionOnlyCalls)
	}

	waitOut, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "wait",
		"duration":   25,
	})
	if err != nil {
		t.Fatalf("computer_use wait: %v", err)
	}
	if got := waitOut.(map[string]any)["screenshot_available"]; got != true {
		t.Fatalf("wait screenshot_available: want true, got %v", got)
	}
	if len(fake.actionOnlyCalls) != 2 || fake.actionOnlyCalls[1].Type != "wait" {
		t.Fatalf("action-only calls after wait: %+v", fake.actionOnlyCalls)
	}
}

func TestComputerUseBatchAbortsAndReturnsOneFinalFrame(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.com/editor",
		somTargets: []backends.SetOfMarkTarget{{
			ID: "som_publish", Label: 4, AccessibleName: "Publish", Dangerous: true, DestructiveEffect: "immediate_publish",
		}},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID, "action": "batch",
		"steps": []any{
			map[string]any{"action": "click", "label": 4, "expected_text": "Publish"},
			map[string]any{"action": "wait_for_stable", "quiet_ms": 200, "timeout_ms": 1000},
		},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	m := out.(map[string]any)
	if got := m["completed_steps"]; got != 2 {
		t.Fatalf("completed_steps=%v", got)
	}
	if fake.screenshotCalls != 1 {
		t.Fatalf("batch must capture exactly one final frame, got %d", fake.screenshotCalls)
	}
	if len(fake.actionOnlyCalls) != 2 {
		t.Fatalf("action calls=%+v", fake.actionOnlyCalls)
	}
	if _, ok := m["screenshot"].(map[string]any); !ok {
		t.Fatalf("missing final screenshot: %v", m)
	}
	if _, duplicated := m["screenshot_b64"]; duplicated {
		t.Fatalf("duplicate screenshot_b64 returned: %v", m)
	}
	delta := m["som_delta"].(map[string]any)
	if got := delta["revision"]; got != 1 {
		t.Fatalf("delta revision=%v", got)
	}

	fake.executeActionHook = func(action backends.Action) error {
		if action.Type == "wait_for_stable" {
			return errors.New("still saving")
		}
		return nil
	}
	fake.actionOnlyCalls = nil
	fake.screenshotCalls = 0
	_, err = app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID, "action": "batch",
		"steps": []any{
			map[string]any{"action": "click", "label": 4, "expected_text": "Publish"},
			map[string]any{"action": "wait_for_stable"},
			map[string]any{"action": "click", "label": 4, "expected_text": "Publish"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "step 2") {
		t.Fatalf("batch should abort at step 2: %v", err)
	}
	if len(fake.actionOnlyCalls) != 2 {
		t.Fatalf("later step executed after failure: %+v", fake.actionOnlyCalls)
	}
	if fake.screenshotCalls != 0 {
		t.Fatalf("failed batch must not claim a final screenshot, got %d", fake.screenshotCalls)
	}

	settings := backends.ScrollRegion{ID: "scroll_settings", Name: "Settings", Role: "region", CanScrollY: true, MaxScrollY: 800}
	fake.executeActionHook = nil
	fake.actionOnlyCalls = nil
	fake.screenshotCalls = 0
	fake.scrollRegions = []backends.ScrollRegion{settings}
	fake.scrollResult = &backends.ScrollResult{
		RequestedTargetID: settings.ID, ActualTargetID: settings.ID,
		RequestedTargetName: settings.Name, ActualTargetName: settings.Name,
		Moved: true, DeltaY: 350, AfterTop: 350, Regions: []backends.ScrollRegion{settings},
	}
	scrollBatch, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID, "action": "batch",
		"steps": []any{
			map[string]any{"action": "scroll", "target_id": settings.ID, "expected_name": "Settings", "expected_role": "region", "direction": "down", "amount": 350},
			map[string]any{"action": "wait_for_stable", "quiet_ms": 200, "timeout_ms": 1000},
		},
	})
	if err != nil {
		t.Fatalf("semantic scroll batch: %v", err)
	}
	if fake.screenshotCalls != 1 || len(fake.actionOnlyCalls) != 2 || fake.actionOnlyCalls[0].TargetID != settings.ID {
		t.Fatalf("scroll batch did not remain action-only until one final frame: screenshots=%d calls=%+v", fake.screenshotCalls, fake.actionOnlyCalls)
	}
	steps := scrollBatch.(map[string]any)["steps"].([]map[string]any)
	if steps[0]["scroll_moved"] != true || steps[0]["scroll_actual_target_name"] != "Settings" {
		t.Fatalf("scroll batch omitted movement feedback: %+v", steps[0])
	}
}

func TestComputerUseSemanticScrollFeedbackAndStableRevision(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	settings := backends.ScrollRegion{ID: "scroll_settings", Name: "Settings", Role: "region", X: 700, Y: 50, W: 280, H: 500, CanScrollY: true, MaxScrollY: 900}
	editor := backends.ScrollRegion{ID: "scroll_editor", Name: "Post body", Role: "textbox", X: 0, Y: 50, W: 680, H: 500, CanScrollY: true, MaxScrollY: 900}
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1000, Height: 600}, png: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, url: "https://example.com/editor",
		somTargets:    []backends.SetOfMarkTarget{{ID: "som_body", Label: 1, Role: "textbox", AccessibleName: "Post body"}},
		scrollRegions: []backends.ScrollRegion{editor, settings},
		scrollResult:  &backends.ScrollResult{RequestedTargetID: settings.ID, ActualTargetID: settings.ID, TargetName: "Settings", TargetRole: "region", BeforeTop: 0, AfterTop: 420, DeltaY: 420, Moved: true, Regions: []backends.ScrollRegion{editor, settings}},
	}
	fake.executeHook = func(action backends.Action) error {
		if action.Type == "scroll" {
			fake.somTargets = []backends.SetOfMarkTarget{{ID: "som_body", Label: 1, Role: "textbox", AccessibleName: "Post body"}, {ID: "som_schedule", Label: 2, Role: "button", AccessibleName: "Set publish date", Dangerous: true, DestructiveEffect: "schedule_publish"}}
		}
		return nil
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	opened, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatal(err)
	}
	id := opened.(map[string]any)["session_id"].(string)
	shot, err := app.toolComputerUse(ctx, map[string]any{"session_id": id, "action": "screenshot"})
	if err != nil {
		t.Fatal(err)
	}
	revision := shot.(map[string]any)["som_delta"].(map[string]any)["revision"].(int)
	if regions := shot.(map[string]any)["scroll_regions"].([]backends.ScrollRegion); len(regions) != 2 {
		t.Fatalf("scroll regions=%+v", regions)
	}
	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": id, "action": "scroll", "direction": "down", "amount": 300}); err == nil || !strings.Contains(err.Error(), "ambiguous_scroll_target") {
		t.Fatalf("ambiguous scroll accepted: %v", err)
	}
	out, err := app.toolComputerUse(ctx, map[string]any{"session_id": id, "action": "scroll", "direction": "down", "amount": 420, "target_id": settings.ID, "expected_name": "Settings", "expected_role": "region", "som_revision": revision})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["scroll_moved"] != true || m["scroll_wrong_target"] != false || m["navigation_progress"] != true {
		t.Fatalf("scroll feedback=%+v", m)
	}
	revealed := m["revealed_targets"].([]backends.SetOfMarkTarget)
	if len(revealed) != 1 || revealed[0].ID != "som_schedule" {
		t.Fatalf("revealed targets=%+v", revealed)
	}
	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": id, "action": "click", "target_id": "som_body", "som_revision": revision}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale revision accepted: %v", err)
	}
	currentRevision := m["som_delta"].(map[string]any)["revision"].(int)
	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": id, "action": "click", "target_id": "som_body", "som_revision": currentRevision, "expected_name": "Post body", "expected_role": "textbox"}); err != nil {
		t.Fatalf("stable target click: %v", err)
	}
	if fake.lastAction.Label != 1 {
		t.Fatalf("target_id did not resolve current label: %+v", fake.lastAction)
	}
}

func TestComputerUseReportsScreenshotRecoveryMetadata(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.com/contact",
		lastRecovery: &backends.ScreenshotRecoveryInfo{
			Recovered:     true,
			Strategy:      "fresh_target_same_url",
			PreviousTabID: "tab_old",
			ActiveTabID:   "tab_new",
			URL:           "https://example.com/contact",
			Cause:         "context deadline exceeded",
		},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "browserbase"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	out, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "screenshot",
	})
	if err != nil {
		t.Fatalf("computer_use screenshot: %v", err)
	}
	m := out.(map[string]any)
	if got := m["screenshot_recovered"]; got != true {
		t.Fatalf("screenshot_recovered: want true, got %v", got)
	}
	if got := m["screenshot_recovery"]; got != "fresh_target_same_url" {
		t.Fatalf("screenshot_recovery: want fresh_target_same_url, got %v", got)
	}
	if got := m["screenshot_recovery_previous_tab_id"]; got != "tab_old" {
		t.Fatalf("previous tab: want tab_old, got %v", got)
	}
	if got := m["screenshot_recovery_active_tab_id"]; got != "tab_new" {
		t.Fatalf("active tab: want tab_new, got %v", got)
	}
	if got := m["screenshot_recovery_cause"]; got != "context deadline exceeded" {
		t.Fatalf("cause: want timeout, got %v", got)
	}
}

func TestBrowserSessionTabActions(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display:     backends.DisplaySize{Width: 1024, Height: 768},
		png:         []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		activeTabID: "tab_a",
		tabs: []backends.TabInfo{
			{ID: "tab_a", URL: "https://example.com/a", Title: "A"},
			{ID: "tab_b", URL: "https://example.com/b", Title: "B"},
		},
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	openOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	tabsOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "tabs", "session_id": sessionID})
	if err != nil {
		t.Fatalf("browser_session tabs: %v", err)
	}
	if got := tabsOut.(map[string]any)["tab_count"]; got != 2 {
		t.Fatalf("tab_count: want 2, got %v", got)
	}

	switchOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "switch_tab", "session_id": sessionID, "tab_id": "tab_b"})
	if err != nil {
		t.Fatalf("browser_session switch_tab: %v", err)
	}
	if got := switchOut.(map[string]any)["active_tab_id"]; got != "tab_b" {
		t.Fatalf("active_tab_id after switch: want tab_b, got %v", got)
	}

	closeOut, err := app.toolBrowserSession(ctx, map[string]any{"action": "close_tab", "session_id": sessionID, "tab_id": "tab_a"})
	if err != nil {
		t.Fatalf("browser_session close_tab: %v", err)
	}
	if got := closeOut.(map[string]any)["tab_count"]; got != 1 {
		t.Fatalf("tab_count after close: want 1, got %v", got)
	}
	if len(fake.closeTabCalls) != 1 || fake.closeTabCalls[0] != "tab_a" {
		t.Fatalf("close tab calls: want [tab_a], got %v", fake.closeTabCalls)
	}
}

func TestHTTPRoutesUseCanonicalToolHandlers(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	proxy := true
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1200, Height: 700},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.test/app",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		if cfg.Type != "local" {
			t.Errorf("backend type: want local, got %q", cfg.Type)
		}
		if cfg.Width != 1200 || cfg.Height != 700 {
			t.Errorf("viewport cfg: want 1200x700, got %dx%d", cfg.Width, cfg.Height)
		}
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	globalCtx = tk.NewAppCtx(t, "apteva.yaml")
	t.Cleanup(func() { globalCtx = nil })

	openBody := map[string]any{
		"action":     "open",
		"backend":    "local",
		"url":        "https://example.test/app",
		"context_id": "ctx-login",
		"persist":    false,
		"proxy":      proxy,
		"viewport": map[string]any{
			"width":  1200,
			"height": 700,
		},
	}
	openOut := postJSON(t, app.handleOpenSession, "/sessions", openBody)
	sessionID, _ := openOut["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", openOut)
	}
	if fake.openContextID != "ctx-login" {
		t.Errorf("context_id: want ctx-login, got %q", fake.openContextID)
	}
	if fake.openPersist {
		t.Errorf("persist: want false")
	}
	if fake.openProxy == nil || *fake.openProxy != true {
		t.Errorf("proxy: want true, got %v", fake.openProxy)
	}

	useOut := postJSON(t, app.handleComputerUse, "/sessions/"+sessionID+"/use", map[string]any{
		"action":     "click",
		"coordinate": "42,64",
	})
	if got := useOut["current_url"]; got != "https://example.test/app" {
		t.Errorf("current_url: want example.test, got %v", got)
	}
	if fake.lastAction.Type != "click" || fake.lastAction.X != 42 || fake.lastAction.Y != 64 {
		t.Errorf("last action: want click 42,64 got %+v", fake.lastAction)
	}
}

func TestBrowserSessionPassesResolvedExternalProxyAndReturnsSafeState(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{display: backends.DisplaySize{Width: 1200, Height: 700}}
	newBackend = func(backends.Config) (backends.Computer, error) { return fake, nil }
	platform := &proxyPlatformStub{connectionID: 77}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	profile, err := dbCreateProxyProfile(appDB(ctx), ProxyProfile{
		Name: "US browser", ProviderSlug: "dataimpulse", ConnectionID: 77,
		ExternalRef: "321", Protocol: "http", DefaultCountry: "US", StickyScope: "session", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action": "open", "backend": "local", "proxy_mode": "profile", "proxy_profile": profile.ID,
	})
	if err != nil {
		t.Fatalf("open with external proxy: %v", err)
	}
	if fake.openExternalProxy == nil || fake.openExternalProxy.Server != "http://gw.dataimpulse.com:823" {
		t.Fatalf("external proxy = %#v", fake.openExternalProxy)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "base-user") || strings.Contains(string(encoded), "gw.dataimpulse.com") {
		t.Fatalf("session output leaked proxy secret or endpoint: %s", encoded)
	}
	proxyState, ok := out.(map[string]any)["proxy"].(SessionProxyState)
	if !ok || proxyState.ProfileID != profile.ID || proxyState.Country != "US" {
		t.Fatalf("session proxy state = %#v", out.(map[string]any)["proxy"])
	}
}

func TestHTTPHandlersEmitIntoRequestProject(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1200, Height: 700},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.test/app",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	rec := tk.NewEmitRecorder()
	globalCtx = tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))
	t.Cleanup(func() { globalCtx = nil })

	openOut := postJSON(t, app.handleOpenSession, "/sessions?project_id=proj-live", map[string]any{
		"action":  "open",
		"backend": "local",
		"url":     "https://example.test/app",
	})
	sessionID, _ := openOut["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("open returned no session_id: %v", openOut)
	}
	assertLastEventProject(t, rec, "session.opened", "proj-live")

	_ = postJSON(t, app.handleComputerUse, "/sessions/"+sessionID+"/use?project_id=proj-live", map[string]any{
		"action": "type",
		"text":   "hello",
	})
	assertLastEventProject(t, rec, "session.action", "proj-live")

	req := httptest.NewRequest(http.MethodDelete, "/sessions/"+sessionID+"?project_id=proj-live", nil)
	w := httptest.NewRecorder()
	app.handleCloseSession(w, req)
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("DELETE session: status=%d body=%s", w.Code, w.Body.String())
	}
	assertLastEventProject(t, rec, "session.closed", "proj-live")
}

func TestContextCatalogResolvesBrowserSessionContextName(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if ctx.AppDB() == nil {
		t.Fatal("test context has no AppDB")
	}

	created, err := app.toolContextCreate(ctx, map[string]any{
		"name":            "login",
		"backend":         "local",
		"persist_default": false,
	})
	if err != nil {
		t.Fatalf("context create: %v", err)
	}
	contextRow := created.(map[string]any)["context"].(*ComputerContext)
	if contextRow.ProviderContextID == "" {
		t.Fatalf("provider context id empty: %+v", contextRow)
	}

	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":       "open",
		"backend":      "local",
		"context_name": "login",
	})
	if err != nil {
		t.Fatalf("browser_session open with context_name: %v", err)
	}
	openMap := out.(map[string]any)
	if openMap["app_context_id"] != contextRow.ID {
		t.Errorf("app_context_id: want %q, got %v", contextRow.ID, openMap["app_context_id"])
	}
	if fake.openContextID != contextRow.ProviderContextID {
		t.Errorf("OpenSession context id: want %q, got %q", contextRow.ProviderContextID, fake.openContextID)
	}
	if fake.openPersist {
		t.Errorf("persist should use context default false")
	}
}

func TestContextListDefaultsAllAndReportsOtherBackends(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if ctx.AppDB() == nil {
		t.Fatal("test context has no AppDB")
	}

	created, err := app.toolContextCreate(ctx, map[string]any{
		"name":                 "patreon-login",
		"backend":              "browserbase",
		"provider_context_id":  "bb_ctx_123",
		"auto_create_provider": false,
	})
	if err != nil {
		t.Fatalf("context create: %v", err)
	}
	contextRow := created.(map[string]any)["context"].(*ComputerContext)

	allOut, err := app.toolContextList(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("context list all: %v", err)
	}
	allMap := allOut.(map[string]any)
	allRows := allMap["contexts"].([]*ComputerContext)
	if len(allRows) != 1 || allRows[0].Backend != "browserbase" {
		t.Fatalf("list all contexts = %+v", allRows)
	}
	if allMap["backend"] != "all" {
		t.Fatalf("list all backend marker = %v", allMap["backend"])
	}

	localOut, err := app.toolContextList(ctx, map[string]any{"backend": "local"})
	if err != nil {
		t.Fatalf("context list local: %v", err)
	}
	localMap := localOut.(map[string]any)
	if got := len(localMap["contexts"].([]*ComputerContext)); got != 0 {
		t.Fatalf("local contexts: want 0, got %d", got)
	}
	otherRows := localMap["other_contexts"].([]*ComputerContext)
	if len(otherRows) != 1 || otherRows[0].ID != contextRow.ID {
		t.Fatalf("other_contexts = %+v", otherRows)
	}
	if got := localMap["available_backends"].([]string); len(got) != 1 || got[0] != "browserbase" {
		t.Fatalf("available_backends = %#v", got)
	}

	if _, err := app.toolSettingsUpdate(ctx, map[string]any{"default_backend": "browserbase"}); err != nil {
		t.Fatalf("settings update: %v", err)
	}
	defaultOut, err := app.toolContextList(ctx, map[string]any{"backend": "default"})
	if err != nil {
		t.Fatalf("context list default: %v", err)
	}
	defaultRows := defaultOut.(map[string]any)["contexts"].([]*ComputerContext)
	if len(defaultRows) != 1 || defaultRows[0].ID != contextRow.ID {
		t.Fatalf("default contexts = %+v", defaultRows)
	}
}

func TestContextGetAndOpenFindUniqueNameAcrossBackends(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	var gotBackend string
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		gotBackend = cfg.Type
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	created, err := app.toolContextCreate(ctx, map[string]any{
		"name":                 "patreon-login",
		"backend":              "browserbase",
		"provider_context_id":  "bb_ctx_456",
		"auto_create_provider": false,
		"persist_default":      true,
	})
	if err != nil {
		t.Fatalf("context create: %v", err)
	}
	contextRow := created.(map[string]any)["context"].(*ComputerContext)

	got, err := app.toolContextGet(ctx, map[string]any{"name": "patreon-login"})
	if err != nil {
		t.Fatalf("context get by unique name: %v", err)
	}
	gotMap := got.(map[string]any)
	if gotMap["found"] != true || gotMap["context"].(*ComputerContext).ID != contextRow.ID {
		t.Fatalf("context get by unique name = %#v", gotMap)
	}

	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":       "open",
		"context_name": "patreon-login",
	})
	if err != nil {
		t.Fatalf("browser_session open by cross-backend context_name: %v", err)
	}
	if gotBackend != "browserbase" {
		t.Fatalf("newBackend type: want browserbase, got %q", gotBackend)
	}
	if openOut.(map[string]any)["app_context_id"] != contextRow.ID {
		t.Fatalf("open output = %#v", openOut)
	}
	if fake.openContextID != "bb_ctx_456" {
		t.Fatalf("OpenSession context id: want bb_ctx_456, got %q", fake.openContextID)
	}
}

func TestBrowserScreenshotDefaultsClean(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")

	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":  "open",
		"backend": "local",
		"url":     "https://example.com",
	})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)

	clean, err := app.toolBrowserScreenshot(ctx, map[string]any{"session_id": sessionID})
	if err != nil {
		t.Fatalf("browser_screenshot: %v", err)
	}
	if got := fake.lastScreenshotAnnotate(); got == nil || *got != false {
		t.Errorf("browser_screenshot annotate default: want false, got %v", got)
	}
	if _, ok := clean.(map[string]any)["som"]; ok {
		t.Fatalf("browser_screenshot should not return structured som by default: %#v", clean.(map[string]any)["som"])
	}

	if _, err := app.toolBrowserScreenshot(ctx, map[string]any{"session_id": sessionID, "annotate": true}); err != nil {
		t.Fatalf("browser_screenshot annotate=true: %v", err)
	}
	if got := fake.lastScreenshotAnnotate(); got == nil || *got != true {
		t.Errorf("browser_screenshot annotate=true: want true, got %v", got)
	}

	fake.somTargets = []backends.SetOfMarkTarget{{Label: 4, Tag: "button", Text: "I accept"}}
	withSOM, err := app.toolBrowserScreenshot(ctx, map[string]any{"session_id": sessionID, "include_som": true})
	if err != nil {
		t.Fatalf("browser_screenshot include_som=true: %v", err)
	}
	if got := fake.lastScreenshotAnnotate(); got == nil || *got != true {
		t.Errorf("browser_screenshot include_som should force annotate: got %v", got)
	}
	somItems, ok := withSOM.(map[string]any)["som"].([]backends.SetOfMarkTarget)
	if !ok || len(somItems) != 1 || somItems[0].Label != 4 {
		t.Fatalf("browser_screenshot som=%#v", withSOM.(map[string]any)["som"])
	}
}

func TestBrowserSessionAutoCreateContextGeneratesName(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.com",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if ctx.AppDB() == nil {
		t.Fatal("test context has no AppDB")
	}

	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":              "open",
		"backend":             "local",
		"url":                 "https://example.com/login",
		"auto_create_context": true,
		"persist":             true,
	})
	if err != nil {
		t.Fatalf("browser_session open with generated context: %v", err)
	}
	openMap := out.(map[string]any)
	contextID, _ := openMap["app_context_id"].(string)
	if contextID == "" {
		t.Fatalf("app_context_id missing: %#v", openMap)
	}
	contextName, _ := openMap["context_name"].(string)
	if !strings.HasPrefix(contextName, "auto-local-example-com-") {
		t.Fatalf("generated context_name = %q", contextName)
	}
	rec, err := dbGetContext(ctx.AppDB(), contextID)
	if err != nil {
		t.Fatalf("get generated context: %v", err)
	}
	if !rec.AutoCreated || rec.Name != contextName {
		t.Fatalf("generated context row = %+v, name from output = %q", rec, contextName)
	}
	if fake.openContextID != rec.ProviderContextID {
		t.Errorf("OpenSession context id: want %q, got %q", rec.ProviderContextID, fake.openContextID)
	}
	if !fake.openPersist {
		t.Errorf("persist should be true")
	}
}

func TestComputerSettingsDriveDefaultBackend(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	var gotBackend string
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		gotBackend = cfg.Type
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if _, err := app.toolSettingsUpdate(ctx, map[string]any{"default_backend": "browserbase"}); err != nil {
		t.Fatalf("settings update: %v", err)
	}
	out, err := app.toolBrowserSession(ctx, map[string]any{"action": "open"})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	if gotBackend != "browserbase" {
		t.Errorf("newBackend type: want browserbase, got %q", gotBackend)
	}
	if out.(map[string]any)["backend"] != "browserbase" {
		t.Errorf("output backend: want browserbase, got %v", out.(map[string]any)["backend"])
	}
}

func TestComputerSettingsLockOverridesExplicitBackend(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	var gotBackend string
	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		gotBackend = cfg.Type
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if _, err := app.toolSettingsUpdate(ctx, map[string]any{
		"default_backend": "browserbase",
		"lock_backend":    true,
	}); err != nil {
		t.Fatalf("settings update: %v", err)
	}
	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":  "open",
		"backend": "local",
	})
	if err != nil {
		t.Fatalf("browser_session open with locked non-default backend: %v", err)
	}
	if gotBackend != "browserbase" {
		t.Errorf("newBackend type: want browserbase, got %q", gotBackend)
	}
	if out.(map[string]any)["backend"] != "browserbase" {
		t.Errorf("output backend: want browserbase, got %v", out.(map[string]any)["backend"])
	}
}

func TestBrowserbaseOpenHidesAndDisablesKeepAlive(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.test",
	}
	var gotCfg backends.Config
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		gotCfg = cfg
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolBrowserSession(ctx, map[string]any{
		"action":     "open",
		"backend":    "browserbase",
		"timeout":    3600,
		"keep_alive": true,
	})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	if gotCfg.Type != "browserbase" {
		t.Fatalf("backend type: want browserbase, got %q", gotCfg.Type)
	}
	if gotCfg.KeepAlive {
		t.Fatal("browserbase keep_alive arg should be ignored and forced off")
	}
	if gotCfg.Timeout != 3600 {
		t.Fatalf("browserbase timeout config: want 3600, got %d", gotCfg.Timeout)
	}
	if fake.openTimeout != 3600 {
		t.Fatalf("OpenSession timeout: want 3600, got %d", fake.openTimeout)
	}
	outMap := out.(map[string]any)
	if _, ok := outMap["keep_alive"]; ok {
		t.Fatalf("output leaked hidden keep_alive: %#v", outMap)
	}
	if outMap["timeout_seconds"] != 3600 {
		t.Fatalf("output timeout_seconds: want 3600, got %#v", outMap["timeout_seconds"])
	}
	if outMap["provider_expires_at"] == "" {
		t.Fatalf("output provider_expires_at missing: %#v", outMap)
	}
	if _, ok := outMap["app_idle_expires_at"]; ok {
		t.Fatalf("output leaked internal app_idle_expires_at: %#v", outMap)
	}

	for _, toolName := range []string{"browser_session", "browser_open"} {
		tool := findTool(t, app.MCPTools(), toolName)
		props, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema has no properties map: %#v", toolName, tool.InputSchema)
		}
		if _, ok := props["keep_alive"]; ok {
			t.Fatalf("%s schema exposes hidden keep_alive", toolName)
		}
	}
}

func TestComputerUseDescriptionTeachesLabelWorkflow(t *testing.T) {
	app := &App{}
	var desc string
	for _, tool := range app.MCPTools() {
		if tool.Name == "computer_use" {
			desc = tool.Description
			break
		}
	}
	if desc == "" {
		t.Fatal("computer_use tool missing")
	}
	for _, want := range []string{
		"action=screenshot first",
		"Set-of-Mark",
		"label=N",
		"Prefer label over coordinate",
		"browser_session(action=tabs)",
		"browser_session(action=switch_tab",
		"Do not use Ctrl+Tab",
		"use action=select_option first",
		"action=set_checked",
		"action=set_temporal",
		"accessible_name",
		"expected_text",
		"action=wait_for_stable",
		"safety_targets",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("computer_use description missing %q:\n%s", want, desc)
		}
	}

	yamlBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	manifest, err := sdk.ParseManifest(yamlBytes)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	for _, tool := range manifest.Provides.MCPTools {
		if tool.Name == "computer_use" {
			if !strings.Contains(tool.Description, "label=N") {
				t.Fatalf("manifest computer_use description does not teach label workflow:\n%s", tool.Description)
			}
			if !strings.Contains(tool.Description, "browser_session(action=tabs)") || !strings.Contains(tool.Description, "Do not use Ctrl+Tab") {
				t.Fatalf("manifest computer_use description does not teach explicit tab switching:\n%s", tool.Description)
			}
			if !strings.Contains(tool.Description, "action=set_checked") || !strings.Contains(tool.Description, "action=set_temporal") {
				t.Fatalf("manifest computer_use description does not teach safe checkbox/time actions:\n%s", tool.Description)
			}
			if !strings.Contains(tool.Description, "use action=select_option first") {
				t.Fatalf("manifest computer_use description does not teach select_option first:\n%s", tool.Description)
			}
			for _, want := range []string{"accessible_name", "expected_text", "action=wait_for_stable", "safety_targets"} {
				if !strings.Contains(tool.Description, want) {
					t.Fatalf("manifest computer_use description missing safety workflow %q:\n%s", want, tool.Description)
				}
			}
			return
		}
	}
	t.Fatal("manifest computer_use tool missing")
}

func TestAppBusEventsForComputerLifecycle(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 1024, Height: 768},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://example.test",
	}
	newBackend = func(cfg backends.Config) (backends.Computer, error) {
		return fake, nil
	}

	app := &App{reg: &registry{m: map[string]*session{}}}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))

	created, err := app.toolContextCreate(ctx, map[string]any{
		"name":            "login",
		"backend":         "local",
		"persist_default": true,
	})
	if err != nil {
		t.Fatalf("context create: %v", err)
	}
	contextRow := created.(map[string]any)["context"].(*ComputerContext)
	contextCreated := lastEventData(t, rec, "context.created")
	if contextCreated["id"] != contextRow.ID || contextCreated["backend"] != "local" {
		t.Fatalf("context.created payload = %#v", contextCreated)
	}

	if _, err := app.toolSettingsUpdate(ctx, map[string]any{
		"default_backend": "local",
		"lock_backend":    true,
	}); err != nil {
		t.Fatalf("settings update: %v", err)
	}
	settingsUpdated := lastEventData(t, rec, "settings.updated")
	if settingsUpdated["default_backend"] != "local" || settingsUpdated["lock_backend"] != true {
		t.Fatalf("settings.updated payload = %#v", settingsUpdated)
	}

	if _, err := app.toolContextUpdate(ctx, map[string]any{
		"id":              contextRow.ID,
		"persist_default": false,
	}); err != nil {
		t.Fatalf("context update: %v", err)
	}
	contextUpdated := lastEventData(t, rec, "context.updated")
	if contextUpdated["id"] != contextRow.ID || contextUpdated["persist_default"] != false {
		t.Fatalf("context.updated payload = %#v", contextUpdated)
	}

	openOut, err := app.toolBrowserSession(ctx, map[string]any{
		"action":     "open",
		"backend":    "local",
		"context_id": contextRow.ID,
		"url":        "https://example.test",
	})
	if err != nil {
		t.Fatalf("browser_session open: %v", err)
	}
	sessionID := openOut.(map[string]any)["session_id"].(string)
	sessionOpened := lastEventData(t, rec, "session.opened")
	if sessionOpened["session_id"] != sessionID || sessionOpened["app_context_id"] != contextRow.ID {
		t.Fatalf("session.opened payload = %#v", sessionOpened)
	}

	if _, err := app.toolComputerUse(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "type",
		"text":       "secret-value",
	}); err != nil {
		t.Fatalf("computer_use type: %v", err)
	}
	sessionAction := lastEventData(t, rec, "session.action")
	if sessionAction["action"] != "type" || sessionAction["text_length"] != len([]rune("secret-value")) {
		t.Fatalf("session.action payload = %#v", sessionAction)
	}
	if _, ok := sessionAction["text"]; ok {
		t.Fatalf("session.action leaked typed text: %#v", sessionAction)
	}

	if _, err := app.toolBrowserClose(ctx, map[string]any{"session_id": sessionID}); err != nil {
		t.Fatalf("browser close: %v", err)
	}
	sessionClosed := lastEventData(t, rec, "session.closed")
	if sessionClosed["session_id"] != sessionID || sessionClosed["backend"] != "local" {
		t.Fatalf("session.closed payload = %#v", sessionClosed)
	}

	if _, err := app.toolContextDelete(ctx, map[string]any{"id": contextRow.ID}); err != nil {
		t.Fatalf("context delete: %v", err)
	}
	contextDeleted := lastEventData(t, rec, "context.deleted")
	if contextDeleted["id"] != contextRow.ID || contextDeleted["provider_deleted"] != false {
		t.Fatalf("context.deleted payload = %#v", contextDeleted)
	}
}

func TestAppBusEventForReapedSession(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))

	fake := &fakeComp{
		display: backends.DisplaySize{Width: 800, Height: 600},
		png:     []byte{0x89, 0x50, 0x4e, 0x47},
		url:     "https://idle.example",
	}
	lastUsed := time.Now().Add(-2 * time.Hour)
	app.reg.put("br_stale", &session{
		comp:         fake,
		backend:      "local",
		appContextID: "ctx_login",
		contextName:  "login",
		persist:      true,
		openedAt:     lastUsed.Add(-time.Minute),
		lastUsed:     lastUsed,
	})

	rows := app.reapIdleSessions(ctx, 30*time.Minute)
	if len(rows) != 1 || rows[0].ID != "br_stale" {
		t.Fatalf("reaped rows = %+v", rows)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("stale session Close calls: want 1, got %d", fake.closeCalls)
	}
	payload := lastEventData(t, rec, "session.reaped")
	if payload["session_id"] != "br_stale" || payload["app_context_id"] != "ctx_login" {
		t.Fatalf("session.reaped payload = %#v", payload)
	}
	if idle, ok := payload["idle_seconds"].(int); !ok || idle < 3600 {
		t.Fatalf("session.reaped idle_seconds = %#v", payload["idle_seconds"])
	}
}

func TestAppBusEventForProviderExpiredSession(t *testing.T) {
	app := &App{reg: &registry{m: map[string]*session{}}}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))
	now := time.Now()
	fake := &fakeComp{display: backends.DisplaySize{Width: 800, Height: 600}, url: "https://expired.example"}
	app.reg.put("br_expired", &session{
		comp: fake, backend: "browserbase", timeout: 60,
		openedAt: now.Add(-2 * time.Minute), lastUsed: now,
	})

	rows := app.reapIdleSessions(ctx, 30*time.Minute)
	if len(rows) != 1 || !rows[0].ProviderExpired {
		t.Fatalf("provider-expired rows = %+v", rows)
	}
	payload := lastEventData(t, rec, "session.reaped")
	if payload["close_reason"] != "provider_timeout" || payload["reap_reason"] != "provider_timeout" {
		t.Fatalf("provider timeout payload = %#v", payload)
	}
}

// ─── fake Computer ─────────────────────────────────────────────────

// fakeComp implements backends.Computer + SessionOpener + SessionInfo +
// DOMExtractor
// for handler tests. Mutation is unguarded — tests are single-goroutine.
type fakeComp struct {
	display           backends.DisplaySize
	png               []byte
	url               string
	executeErr        error
	screenshotErr     error
	executeActionErr  error
	executeActionHook func(backends.Action) error
	executeHook       func(backends.Action) error
	openSessionURL    string
	openSessionID     string
	openContextID     string
	openPersist       bool
	openTimeout       int
	openProxy         *bool
	openExternalProxy *backends.ExternalProxy
	openEnvironment   backends.EnvironmentOptions
	openErr           error
	lastAction        backends.Action
	screenshotCalls   int
	annotateCalls     []bool
	somTargets        []backends.SetOfMarkTarget
	scrollRegions     []backends.ScrollRegion
	scrollResult      *backends.ScrollResult
	closeCalls        int
	tabs              []backends.TabInfo
	activeTabID       string
	switchCalls       []string
	closeTabCalls     []string
	addTabOnClick     *backends.TabInfo
	actionOnlyCalls   []backends.Action
	lastRecovery      *backends.ScreenshotRecoveryInfo
	extractResult     backends.ExtractResult
	extractErr        error
	extractOptions    backends.ExtractOptions
	mu                sync.Mutex // for the unlikely concurrent test
}

func (f *fakeComp) Execute(action backends.Action) ([]byte, error) {
	f.mu.Lock()
	f.lastAction = action
	if (action.Type == "click" || action.Type == "double_click") && f.addTabOnClick != nil {
		tab := *f.addTabOnClick
		if tab.ID == "" {
			tab.ID = "tab_new"
		}
		f.tabs = append(f.tabs, tab)
		f.addTabOnClick = nil
	}
	f.mu.Unlock()
	if f.executeHook != nil {
		if err := f.executeHook(action); err != nil {
			return nil, err
		}
	}
	if f.executeErr != nil {
		return nil, f.executeErr
	}
	return f.png, nil
}

func (f *fakeComp) ExecuteAction(action backends.Action) error {
	f.mu.Lock()
	f.lastAction = action
	f.actionOnlyCalls = append(f.actionOnlyCalls, action)
	if (action.Type == "click" || action.Type == "double_click") && f.addTabOnClick != nil {
		tab := *f.addTabOnClick
		if tab.ID == "" {
			tab.ID = "tab_new"
		}
		f.tabs = append(f.tabs, tab)
		f.addTabOnClick = nil
	}
	f.mu.Unlock()
	if f.executeActionHook != nil {
		if err := f.executeActionHook(action); err != nil {
			return err
		}
	}
	if f.executeActionErr == nil && f.executeErr != nil {
		return f.executeErr
	}
	return f.executeActionErr
}

func (f *fakeComp) Screenshot() ([]byte, error) {
	return f.ScreenshotWithOptions(backends.ScreenshotOptions{Annotate: true})
}

func (f *fakeComp) ScreenshotWithOptions(options backends.ScreenshotOptions) ([]byte, error) {
	f.mu.Lock()
	f.screenshotCalls++
	f.annotateCalls = append(f.annotateCalls, options.Annotate)
	f.mu.Unlock()
	if f.screenshotErr != nil {
		return nil, f.screenshotErr
	}
	return f.png, nil
}

func (f *fakeComp) LastScreenshotRecovery() *backends.ScreenshotRecoveryInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastRecovery == nil {
		return nil
	}
	cp := *f.lastRecovery
	return &cp
}

func (f *fakeComp) LastSetOfMark() []backends.SetOfMarkTarget {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]backends.SetOfMarkTarget, len(f.somTargets))
	copy(out, f.somTargets)
	return out
}

func (f *fakeComp) ScrollRegions() []backends.ScrollRegion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]backends.ScrollRegion(nil), f.scrollRegions...)
}

func (f *fakeComp) LastScrollResult() *backends.ScrollResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scrollResult == nil {
		return nil
	}
	clone := *f.scrollResult
	clone.Regions = append([]backends.ScrollRegion(nil), f.scrollResult.Regions...)
	return &clone
}

func (f *fakeComp) lastScreenshotAnnotate() *bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.annotateCalls) == 0 {
		return nil
	}
	v := f.annotateCalls[len(f.annotateCalls)-1]
	return &v
}

func (f *fakeComp) DisplaySize() backends.DisplaySize { return f.display }

func (f *fakeComp) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeComp) ListTabs() ([]backends.TabInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]backends.TabInfo, len(f.tabs))
	copy(out, f.tabs)
	for i := range out {
		out[i].Active = out[i].ID != "" && out[i].ID == f.activeTabID
	}
	return out, nil
}

func (f *fakeComp) ActiveTabID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeTabID
}

func (f *fakeComp) SwitchTab(tabID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.tabs {
		if f.tabs[i].ID == tabID {
			f.activeTabID = tabID
			f.switchCalls = append(f.switchCalls, tabID)
			return nil
		}
	}
	return fmt.Errorf("tab %s not found", tabID)
}

func (f *fakeComp) CloseTab(tabID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tabs) <= 1 {
		return fmt.Errorf("cannot close last tab")
	}
	for i := range f.tabs {
		if f.tabs[i].ID == tabID {
			f.tabs = append(f.tabs[:i], f.tabs[i+1:]...)
			f.closeTabCalls = append(f.closeTabCalls, tabID)
			if f.activeTabID == tabID && len(f.tabs) > 0 {
				f.activeTabID = f.tabs[0].ID
			}
			return nil
		}
	}
	return fmt.Errorf("tab %s not found", tabID)
}

func (f *fakeComp) OpenSession(opts backends.OpenOptions) error {
	f.openSessionURL = opts.URL
	f.openSessionID = opts.SessionID
	f.openContextID = opts.ContextID
	f.openPersist = opts.Persist
	f.openTimeout = opts.Timeout
	f.openProxy = opts.Proxy
	f.openExternalProxy = opts.ExternalProxy
	f.openEnvironment = opts.Environment
	return f.openErr
}

// SessionInfo interface
func (f *fakeComp) SessionType() string { return "fake" }
func (f *fakeComp) SessionID() string   { return f.openSessionID }
func (f *fakeComp) CurrentURL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tab := range f.tabs {
		if tab.ID == f.activeTabID && tab.URL != "" {
			return tab.URL
		}
	}
	return f.url
}

func (f *fakeComp) ExtractDOM(opts backends.ExtractOptions) (backends.ExtractResult, error) {
	f.mu.Lock()
	f.extractOptions = opts
	f.mu.Unlock()
	if f.extractErr != nil {
		return backends.ExtractResult{}, f.extractErr
	}
	return f.extractResult, nil
}

type nonExtractComp struct{}

func (*nonExtractComp) Execute(backends.Action) ([]byte, error) { return nil, nil }
func (*nonExtractComp) Screenshot() ([]byte, error)             { return nil, nil }
func (*nonExtractComp) DisplaySize() backends.DisplaySize {
	return backends.DisplaySize{Width: 800, Height: 600}
}
func (*nonExtractComp) Close() error { return nil }

// ─── helpers ───────────────────────────────────────────────────────

func appWithSession(id string, comp backends.Computer, backend string) *App {
	app := &App{reg: &registry{m: map[string]*session{}}}
	app.reg.put(id, &session{
		comp:     comp,
		backend:  backend,
		openedAt: time.Now(),
		lastUsed: time.Now(),
	})
	return app
}

func internalExtractResponse(t *testing.T, app *App, sessionID, body, callerID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/sessions/"+sessionID+"/extract", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if callerID != "" {
		req.Header.Set(internalAppCallerHeader, callerID)
	}
	w := httptest.NewRecorder()
	app.handleInternalSessionExtract(w, req)
	return w
}

func toolNames(tools []sdk.MCPToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

func findTool(t *testing.T, tools []sdk.Tool, name string) sdk.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return sdk.Tool{}
}

func eventNames(events []sdk.EventDecl) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

func sameStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameScopes(a, b []sdk.Scope) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[sdk.Scope]bool{}
	for _, s := range a {
		am[s] = true
	}
	for _, s := range b {
		if !am[s] {
			return false
		}
	}
	return true
}

func samePermissions(a, b []sdk.Permission) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[sdk.Permission]bool{}
	for _, p := range a {
		am[p] = true
	}
	for _, p := range b {
		if !am[p] {
			return false
		}
	}
	return true
}

func lastEventData(t *testing.T, rec *tk.EmitRecorder, topic string) map[string]any {
	t.Helper()
	events := rec.EventsByTopic(topic)
	if len(events) == 0 {
		t.Fatalf("expected event %q, got events %#v", topic, rec.Events())
	}
	data, ok := events[len(events)-1].Data.(map[string]any)
	if !ok {
		t.Fatalf("event %q data type = %T, want map[string]any", topic, events[len(events)-1].Data)
	}
	return data
}

func assertLastEventProject(t *testing.T, rec *tk.EmitRecorder, topic, projectID string) {
	t.Helper()
	events := rec.EventsByTopic(topic)
	if len(events) == 0 {
		t.Fatalf("expected event %q, got events %#v", topic, rec.Events())
	}
	if got := events[len(events)-1].ProjectID; got != projectID {
		t.Fatalf("%s project id: want %q, got %q", topic, projectID, got)
	}
}

func postJSON(t *testing.T, handler http.HandlerFunc, path string, body any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("POST %s: status=%d body=%s", path, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	return out
}
