package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestADBDiscoveryAndAuthorization(t *testing.T) {
	d := newDrivers(runnerConfig{})
	d.command = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("List of devices attached\nphone device product:p model:Pixel_8\nlocked unauthorized usb:1\nemulator-5554 device model:Emulator\n"), nil
	}
	devices, hints, err := d.devices(context.Background())
	if err != nil || len(devices) != 3 || len(hints) == 0 {
		t.Fatalf("inventory: %+v %v", devices, err)
	}
	if devices[0].Name != "Pixel 8" || !devices[0].Physical || devices[1].State != "unauthorized" || devices[2].Physical {
		t.Fatalf("wrong device facts: %+v", devices)
	}
}
func TestConfiguredDevicesAndDuplicateIDs(t *testing.T) {
	d := newDrivers(runnerConfig{DevicesJSON: `[{"id":"iphone","platform":"ios","udid":"ios-1","physical":true}]`})
	d.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no adb") }
	devices, _, err := d.devices(context.Background())
	if err != nil || len(devices) != 1 || devices[0].State != "setup_required" {
		t.Fatalf("ios diagnostics: %+v %v", devices, err)
	}
	d.cfg.DevicesJSON = `[{"id":"x","platform":"ios","udid":"1"},{"id":"x","platform":"ios","udid":"2"}]`
	if _, _, err = d.devices(context.Background()); err == nil {
		t.Fatal("duplicate id accepted")
	}
}
func TestADBRemoteShellQuotesUntrustedValues(t *testing.T) {
	d := newDrivers(runnerConfig{})
	var got []string
	d.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "adb" {
			t.Fatal(name)
		}
		got = args
		return nil, nil
	}
	s := Session{Device: Device{Driver: "adb", UDID: "serial"}}
	text := `hello ' ; $(touch /tmp/never) & " world`
	if _, err := d.Action(context.Background(), s, "text", map[string]any{"text": text}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0] != "-s" || got[1] != "serial" || got[2] != "shell" || got[3] != "'input' 'text' "+shellQuote(strings.ReplaceAll(text, " ", "%s")) {
		t.Fatalf("unsafe argv: %#v", got)
	}
	for _, text := range []string{"hello\nworld", "你好", "100%"} {
		if _, err := d.Action(context.Background(), s, "text", map[string]any{"text": text}); errorCode(err) != "unsupported" {
			t.Fatalf("unsupported input accepted: %q", text)
		}
	}
}
func TestCoordinatesBoundaries(t *testing.T) {
	d := newDrivers(runnerConfig{})
	s := Session{Device: Device{Driver: "adb", UDID: "p"}}
	var command string
	d.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[2] == "exec-out" {
			return testPNG(), nil
		}
		command = args[len(args)-1]
		return nil, nil
	}
	if _, err := d.Action(context.Background(), s, "tap", map[string]any{"x": 1.0, "y": 1.0}); err != nil {
		t.Fatal(err)
	}
	if command != "'input' 'tap' '99' '199'" {
		t.Fatal(command)
	}
	for _, args := range []map[string]any{{"x": -0.1, "y": 0.2}, {"x": 0.1, "y": 1.1}, {"x": 0.1}, {"x": "0", "y": 0.1}} {
		if _, err := d.Action(context.Background(), s, "tap", args); errorCode(err) != "invalid_argument" {
			t.Fatalf("invalid coordinates accepted: %v %v", args, err)
		}
	}
}
func TestURLAndAppValidation(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://user:password@example.com", ""} {
		if validateURL(raw) == nil {
			t.Fatalf("bad URL %s", raw)
		}
	}
	for _, v := range []string{"-option", "com.app;rm", "../app", ""} {
		if _, err := appID(map[string]any{"app_id": v}); err == nil {
			t.Fatalf("bad app id %s", v)
		}
	}
}

func TestADBLaunchResolvesActivityWithoutMonkey(t *testing.T) {
	d := newDrivers(runnerConfig{})
	var commands []string
	d.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		command := args[len(args)-1]
		commands = append(commands, command)
		if strings.Contains(command, "resolve-activity") {
			return []byte("priority=0\ncom.example.app/.MainActivity\n"), nil
		}
		return []byte("Status: ok"), nil
	}
	_, err := d.App(context.Background(), Session{Device: Device{Driver: "adb", UDID: "phone"}}, "launch", map[string]any{"app_id": "com.example.app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[1] != "'am' 'start' '-W' '-n' 'com.example.app/.MainActivity'" {
		t.Fatalf("launcher commands: %v", commands)
	}
}

func TestAppiumIOSWebDriverLifecycle(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /session":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			caps := req["capabilities"].(map[string]any)["alwaysMatch"].(map[string]any)
			if caps["appium:udid"] != "iphone" || caps["platformName"] != "iOS" || caps["appium:automationName"] != "XCUITest" || caps["browserName"] != "Safari" || caps["appium:noReset"] != true {
				t.Errorf("wrong iOS capabilities: %v", caps)
			}
			writeJSON(w, map[string]any{"value": map[string]any{"sessionId": "provider"}})
		case "GET /session/provider/screenshot":
			writeJSON(w, map[string]any{"value": base64.StdEncoding.EncodeToString(testPNG())})
		case "GET /session/provider/source":
			writeJSON(w, map[string]any{"value": "<html>Welcome</html>"})
		case "POST /session/provider/element":
			writeJSON(w, map[string]any{"value": map[string]any{"element-6066-11e4-a52e-4f735466cecf": "button"}})
		default:
			writeJSON(w, map[string]any{"value": nil})
		}
	}))
	defer server.Close()
	d := newDrivers(runnerConfig{AppiumURL: server.URL})
	device := Device{Platform: "ios", Driver: "appium", UDID: "iphone", State: "configured"}
	provider, err := d.Open(context.Background(), device, "structured")
	if err != nil {
		t.Fatal(err)
	}
	s := Session{ProviderID: provider, Device: device, Mode: "structured"}
	if _, err = d.Action(context.Background(), s, "navigate", map[string]any{"url": "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	obs, err := d.Observe(context.Background(), s, true)
	if err != nil || !strings.Contains(obs.Source, "Welcome") || obs.Width != 100 {
		t.Fatalf("observation %+v %v", obs, err)
	}
	if _, err = d.Action(context.Background(), s, "tap_element", map[string]any{"using": "accessibility id", "value": "Continue"}); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	joined := strings.Join(paths, "\n")
	mu.Unlock()
	for _, want := range []string{"POST /session/provider/url", "POST /session/provider/element/button/click", "DELETE /session/provider"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}
}
func TestAppiumErrorDoesNotExposeProviderSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		writeJSON(w, map[string]any{"value": map[string]any{"error": "session not created", "message": "secret-api-key password=private"}})
	}))
	defer server.Close()
	d := newDrivers(runnerConfig{AppiumURL: server.URL})
	_, err := d.Open(context.Background(), Device{Platform: "ios", Driver: "appium", State: "configured"}, "screen")
	if err == nil || strings.Contains(err.Error(), "secret") || errorCode(err) != "session not created" {
		t.Fatalf("unsafe error: %v", err)
	}
}
func TestAppiumRedirectNotFollowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed Appium redirect") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	d := newDrivers(runnerConfig{AppiumURL: server.URL})
	if err := d.wire(context.Background(), "POST", "/session", nil, nil); err == nil {
		t.Fatal("redirect accepted")
	}
}
