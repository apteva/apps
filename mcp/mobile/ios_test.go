package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const devicectlFixture = `{"result":{"devices":[
 {"identifier":"A","connectionProperties":{"pairingState":"paired","transportType":"wired","tunnelState":"connected"},
  "deviceProperties":{"name":"Marco's iPhone","osVersionNumber":"26.1","developerModeStatus":"enabled"},
  "hardwareProperties":{"udid":"00008110-000A1B2C3D4E5F60","platform":"iOS","reality":"physical"}},
 {"identifier":"B","connectionProperties":{"pairingState":"paired","transportType":"wired"},
  "deviceProperties":{"name":"Test iPad","osVersionNumber":"18.2","developerModeStatus":"disabled"},
  "hardwareProperties":{"udid":"00008027-0011","platform":"iOS","reality":"physical"}},
 {"identifier":"C","connectionProperties":{"pairingState":"paired","transportType":""},
  "deviceProperties":{"name":"Old iPhone","developerModeStatus":"enabled"},
  "hardwareProperties":{"udid":"00008030-0022","platform":"iOS","reality":"physical"}},
 {"identifier":"D","connectionProperties":{"pairingState":"unpaired","transportType":"wired"},
  "deviceProperties":{"name":"New iPhone"},
  "hardwareProperties":{"udid":"00008140-0033","platform":"iOS","reality":"physical"}},
 {"identifier":"E","connectionProperties":{"pairingState":"paired","transportType":"wired"},
  "deviceProperties":{"name":"Watch"},
  "hardwareProperties":{"udid":"watch-1","platform":"watchOS","reality":"physical"}}]}}`

const simctlFixture = `{"devices":{
 "com.apple.CoreSimulator.SimRuntime.iOS-18-5":[{"udid":"SIM-1","name":"iPhone 16","state":"Booted"}],
 "com.apple.CoreSimulator.SimRuntime.watchOS-11-0":[{"udid":"SIM-W","name":"Watch","state":"Booted"}]}}`

// fakeXcrun answers devicectl/simctl invocations; other commands fail.
func fakeXcrun(t *testing.T, installed map[string]bool) commandFunc {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "xcrun" || len(args) == 0 {
			return nil, errors.New("not available")
		}
		joined := strings.Join(args, " ")
		writeJSON := func(v string) ([]byte, error) {
			for i, a := range args {
				if a == "--json-output" {
					return nil, os.WriteFile(args[i+1], []byte(v), 0600)
				}
			}
			t.Fatalf("devicectl call without --json-output: %v", args)
			return nil, nil
		}
		switch {
		case strings.HasPrefix(joined, "devicectl list devices"):
			return writeJSON(devicectlFixture)
		case strings.HasPrefix(joined, "devicectl device info apps"):
			app := args[len(args)-4]
			if installed[app] {
				return writeJSON(`{"result":{"apps":[{"bundleIdentifier":"` + app + `"}]}}`)
			}
			return writeJSON(`{"result":{"apps":[]}}`)
		case strings.HasPrefix(joined, "simctl list devices booted"):
			return []byte(simctlFixture), nil
		case strings.HasPrefix(joined, "simctl get_app_container"):
			if installed[args[len(args)-1]] {
				return []byte("/path"), nil
			}
			return nil, errors.New("No such app")
		}
		return nil, errors.New("unexpected xcrun " + joined)
	}
}

func TestNativeIOSDiscovery(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native iOS discovery runs on macOS")
	}
	d := newDrivers(runnerConfig{})
	d.command = fakeXcrun(t, nil)
	devices, _, err := d.devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Device{}
	for _, v := range devices {
		byID[v.ID] = v
	}
	if len(byID) != 5 {
		t.Fatalf("want 4 phones + 1 simulator, got %+v", devices)
	}
	ready := byID["ios:00008110-000A1B2C3D4E5F60"]
	if ready.State != "ready" || ready.Driver != driverXCTest || !ready.Physical || ready.OS != "26.1" || !contains(ready.Capabilities, "elements") || !contains(ready.Capabilities, "record_high") {
		t.Fatalf("ready phone: %+v", ready)
	}
	if v := byID["ios:00008027-0011"]; v.State != "setup_required" || !strings.Contains(v.Hint, "Developer Mode") {
		t.Fatalf("developer mode: %+v", v)
	}
	if v := byID["ios:00008030-0022"]; v.State != "offline" {
		t.Fatalf("offline: %+v", v)
	}
	if v := byID["ios:00008140-0033"]; v.State != "setup_required" || !strings.Contains(v.Hint, "Trust") {
		t.Fatalf("unpaired: %+v", v)
	}
	sim := byID["ios-sim:SIM-1"]
	if sim.Physical || sim.OS != "18.5" || sim.Name != "iPhone 16 (Simulator)" || !contains(sim.Capabilities, "record") || contains(sim.Capabilities, "record_audio") {
		t.Fatalf("simulator: %+v", sim)
	}

	// An inventory entry for the same phone wins over discovery.
	d.cfg.DevicesJSON = `[{"id":"lab","platform":"ios","udid":"00008110-000A1B2C3D4E5F60","driver":"appium"}]`
	devices, _, _ = d.devices(context.Background())
	for _, v := range devices {
		if v.ID == "ios:00008110-000A1B2C3D4E5F60" {
			t.Fatal("configured phone listed twice")
		}
	}

	d = newDrivers(runnerConfig{IOSDriver: "appium"})
	d.command = fakeXcrun(t, nil)
	devices, _, _ = d.devices(context.Background())
	if len(devices) != 0 {
		t.Fatalf("ios_driver=appium must not discover natively: %+v", devices)
	}
}

func testCert(t *testing.T, ou string, from, until time.Time) []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "Apple Development: Test", OrganizationalUnit: []string{ou}}, NotBefore: from, NotAfter: until}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestTeamDetection(t *testing.T) {
	now := time.Now()
	bundle := append(testCert(t, "AAAAAAAAAA", now.Add(-48*time.Hour), now.Add(-time.Hour)), testCert(t, "BBBBBBBBBB", now.Add(-time.Hour), now.Add(24*time.Hour))...)
	bundle = append(bundle, testCert(t, "CCCCCCCCCC", now.Add(-time.Hour), now.Add(300*24*time.Hour))...)
	bundle = append(bundle, testCert(t, "not a team", now.Add(-time.Hour), now.Add(900*24*time.Hour))...)
	if got := teamFromPEM(bundle, now); got != "CCCCCCCCCC" {
		t.Fatalf("team = %q", got)
	}
	d := newDrivers(runnerConfig{})
	d.command = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "security" {
			return bundle, nil
		}
		return nil, errors.New("no")
	}
	if team, err := d.teamID(context.Background()); err != nil || team != "CCCCCCCCCC" {
		t.Fatal(team, err)
	}
	d.cfg.IOSTeamID = "ZZ9ZZ9ZZ9Z"
	if team, _ := d.teamID(context.Background()); team != "ZZ9ZZ9ZZ9Z" {
		t.Fatal("configured team ignored")
	}
	d.cfg.IOSTeamID = "bad team"
	if _, err := d.teamID(context.Background()); errorCode(err) != "invalid_configuration" {
		t.Fatal(err)
	}
	d.cfg.IOSTeamID = ""
	d.command = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if _, err := d.teamID(context.Background()); errorCode(err) != "setup_required" || !strings.Contains(err.Error(), "Accounts") {
		t.Fatal(err)
	}
}

func TestRunnerBuildArgsAndProblems(t *testing.T) {
	sim := strings.Join(runnerBuildArgs("P", "D", "SIM", ""), " ")
	if !strings.Contains(sim, "generic/platform=iOS Simulator") || strings.Contains(sim, "DEVELOPMENT_TEAM") {
		t.Fatal(sim)
	}
	dev := strings.Join(runnerBuildArgs("P", "D", "UDID-1", "ABCDE12345"), " ")
	for _, want := range []string{"-destination id=UDID-1", "-allowProvisioningUpdates", "-allowProvisioningDeviceRegistration", "DEVELOPMENT_TEAM=ABCDE12345", "PRODUCT_BUNDLE_IDENTIFIER=dev.apteva.mobile.runner.abcde12345"} {
		if !strings.Contains(dev, want) {
			t.Fatalf("missing %q in %s", want, dev)
		}
	}
	cases := map[string]string{
		`error: No Account for Team "ABCDE12345".`:                   "Accounts",
		"Developer Mode disabled on the device":                      "Developer Mode",
		"The application could not be launched: Untrusted Developer": "VPN & Device Management",
		"Unable to launch: the device is locked":                     "unlock",
	}
	for log, want := range cases {
		if err := runnerProblem("x", log); !strings.Contains(err.Error(), want) {
			t.Fatalf("%q -> %v", log, err)
		}
	}
	err := runnerProblem("could not build", "noise\nfoo.swift:3: error: cannot find 'x'\nmore\n")
	if errorCode(err) != "infrastructure_error" || !strings.Contains(err.Error(), "cannot find 'x'") {
		t.Fatal(err)
	}
}

func TestRunnerSourcesExtract(t *testing.T) {
	sum, err := runnerSourceHash()
	if err != nil || len(sum) != 12 {
		t.Fatal(sum, err)
	}
	dir := filepath.Join(t.TempDir(), "src")
	if err := extractRunner(dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"AptevaRunner.xcodeproj/project.pbxproj", "AptevaRunner.xcodeproj/xcshareddata/xcschemes/AptevaRunner.xcscheme", "AptevaRunner/RunnerTests.swift", "AptevaRunner/Server.swift", ".complete"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	if err := extractRunner(dir); err != nil {
		t.Fatal("re-extract:", err)
	}
}

// readyRunner registers an already-started runner backed by h.
func readyRunner(d *drivers, udid string, h http.Handler) *httptest.Server {
	srv := httptest.NewServer(h)
	r := &iosRunner{udid: udid, sim: true, base: srv.URL, client: srv.Client(), ready: make(chan struct{}), exited: make(chan struct{})}
	close(r.ready)
	d.ios.byUDID = map[string]*iosRunner{udid: r}
	return srv
}

func TestXCTestDriverOperations(t *testing.T) {
	d := newDrivers(runnerConfig{})
	d.command = fakeXcrun(t, map[string]bool{"com.example.game": true})
	var mu sync.Mutex
	calls := map[string]map[string]any{}
	srv := readyRunner(d, "SIM-1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls[r.Method+" "+r.URL.Path] = body
		mu.Unlock()
		switch r.URL.Path {
		case "/screenshot":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNG())
		case "/source":
			_, _ = w.Write([]byte(`<XCUIElementTypeApplication type="XCUIElementTypeApplication" name="Game" label="Game" visible="true" x="0" y="0" width="390" height="844"><XCUIElementTypeButton type="XCUIElementTypeButton" name="Play" label="Play" enabled="true" visible="true" x="100" y="400" width="190" height="44"/></XCUIElementTypeApplication>`))
		case "/type":
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"error":"action_failed","message":"Neither element nor any descendant has keyboard focus."}`))
		case "/app":
			_, _ = w.Write([]byte(`{"ok":true,"bundle_id":"com.example.game","state":"foreground"}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	s := Session{Device: Device{ID: "ios-sim:SIM-1", UDID: "SIM-1", Platform: "ios", Driver: driverXCTest, State: "ready"}, Mode: "screen"}
	if _, err := d.Open(ctx, s.Device, "screen"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Open(ctx, s.Device, "structured"); errorCode(err) != "unsupported" {
		t.Fatal("structured accepted", err)
	}
	obs, err := d.Observe(ctx, s, true)
	if err != nil || !strings.Contains(obs.Source, `name="Play"`) || obs.Width == 0 {
		t.Fatal(obs.Width, err)
	}
	if _, err := d.Action(ctx, s, "tap", map[string]any{"x": 0.5, "y": 0.25}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Action(ctx, s, "swipe", map[string]any{"x": 0.5, "y": 0.8, "to_x": 0.5, "to_y": 0.2}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Action(ctx, s, "navigate", map[string]any{"url": "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Action(ctx, s, "key", map[string]any{"key": "home"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Action(ctx, s, "key", map[string]any{"key": "back"}); errorCode(err) != "unsupported" {
		t.Fatal("back accepted", err)
	}
	if _, err := d.Action(ctx, s, "tap_element", map[string]any{"using": "xpath", "value": "//x"}); errorCode(err) != "unsupported" {
		t.Fatal("xpath accepted", err)
	}
	if _, err := d.Action(ctx, s, "tap_element", map[string]any{"using": "accessibility id", "value": "Play"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Action(ctx, s, "text", map[string]any{"text": "héllo"}); errorCode(err) != "action_failed" || !strings.Contains(err.Error(), "keyboard focus") {
		t.Fatal("runner error not surfaced", err)
	}
	if _, err := d.App(ctx, s, "launch", map[string]any{"app_id": "com.example.game"}); err != nil {
		t.Fatal(err)
	}
	st, err := d.App(ctx, s, "status", map[string]any{"app_id": "com.example.game"})
	if err != nil || st.(map[string]any)["installed"] != true {
		t.Fatal(st, err)
	}
	// Store flows find elements in points and tap in points.
	el, err := d.waitForElement(ctx, s, []matcher{{Text: "Play"}}, time.Second)
	if err != nil || el.cx != 195 || el.cy != 422 {
		t.Fatal(el, err)
	}
	if err := d.tapPoint(ctx, s, el.cx, el.cy); err != nil {
		t.Fatal(err)
	}
	if err := d.openLink(ctx, s, "itms-beta://beta.itunes.apple.com/v1/app/123", "com.apple.TestFlight"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if c := calls["POST /swipe"]; c["to_y"] != 0.2 || c["duration_ms"] != float64(400) {
		t.Fatalf("swipe: %v", c)
	}
	if c := calls["POST /tap"]; c["unit"] != "point" || c["x"] != float64(195) {
		t.Fatalf("point tap: %v", c)
	}
	if c := calls["POST /open"]; c["bundle_id"] != "com.apple.TestFlight" {
		t.Fatalf("open: %v", c)
	}
	if c := calls["POST /tap_element"]; c["by"] != "any" || c["value"] != "Play" {
		t.Fatalf("tap_element: %v", c)
	}
	if c := calls["POST /app"]; c["action"] != "launch" {
		t.Fatalf("app: %v", c)
	}
}

func TestSimulatorRecordingPlan(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("simulators exist on macOS only")
	}
	d := newDrivers(runnerConfig{})
	v := Device{Platform: "ios", Driver: driverXCTest}
	p, err := d.RecordPlan(v, recordOptions{Quality: "auto", Audio: true})
	if err != nil || p.Method != methodSimctl || p.Audio || p.Ext != ".mp4" {
		t.Fatal(p, err)
	}
}

func TestInstallPathAcceptsSimulatorApp(t *testing.T) {
	root := t.TempDir()
	d := newDrivers(runnerConfig{ArtifactDir: root})
	if err := os.MkdirAll(filepath.Join(root, "Game.app"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := d.installPath("Game.app", Device{Platform: "ios"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.installPath("Game.app", Device{Platform: "ios", Physical: true}); err == nil {
		t.Fatal("physical device accepted a .app bundle")
	}
}

// fakeUsbmuxd serves ListDevices and Connect, then answers one HTTP request
// on the "device port" like the runner would.
func fakeUsbmuxd(t *testing.T) string {
	dir, err := os.MkdirTemp("", "um")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "usbmuxd")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	reply := func(c net.Conn, tag uint32, body string) {
		head := make([]byte, 16)
		binary.LittleEndian.PutUint32(head[0:], uint32(16+len(body)))
		binary.LittleEndian.PutUint32(head[4:], 1)
		binary.LittleEndian.PutUint32(head[8:], 8)
		binary.LittleEndian.PutUint32(head[12:], tag)
		_, _ = c.Write(append(head, body...))
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				head := make([]byte, 16)
				if _, err := io.ReadFull(c, head); err != nil {
					return
				}
				payload := make([]byte, binary.LittleEndian.Uint32(head)-16)
				if _, err := io.ReadFull(c, payload); err != nil {
					return
				}
				v, _ := parsePlist(payload)
				m, _ := v.(map[string]any)
				tag := binary.LittleEndian.Uint32(head[12:])
				switch m["MessageType"] {
				case "ListDevices":
					reply(c, tag, `<?xml version="1.0"?><plist version="1.0"><dict><key>DeviceList</key><array>`+
						`<dict><key>DeviceID</key><integer>3</integer><key>Properties</key><dict><key>SerialNumber</key><string>00008110000A1B2C3D4E5F60</string><key>ConnectionType</key><string>Network</string></dict></dict>`+
						`<dict><key>DeviceID</key><integer>7</integer><key>Properties</key><dict><key>SerialNumber</key><string>00008110000A1B2C3D4E5F60</string><key>ConnectionType</key><string>USB</string></dict></dict>`+
						`</array></dict></plist>`)
				case "Connect":
					// 22087 = 0x5647; usbmuxd wants it byte-swapped (0x4756 = 18262).
					if m["DeviceID"] != 7 || m["PortNumber"] != 18262 {
						reply(c, tag, `<plist version="1.0"><dict><key>MessageType</key><string>Result</string><key>Number</key><integer>3</integer></dict></plist>`)
						return
					}
					reply(c, tag, `<plist version="1.0"><dict><key>MessageType</key><string>Result</string><key>Number</key><integer>0</integer></dict></plist>`)
					req, err := http.ReadRequest(bufio.NewReader(c))
					if err != nil || req.URL.Path != "/status" {
						return
					}
					_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 11\r\nConnection: close\r\n\r\n{\"ok\":true}")
				}
			}(c)
		}
	}()
	return path
}

func TestUsbmuxReachesRunnerOverUSB(t *testing.T) {
	old := usbmuxSocket
	usbmuxSocket = fakeUsbmuxd(t)
	defer func() { usbmuxSocket = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	devices, err := usbmuxDevices(ctx)
	if err != nil || len(devices) != 2 || devices[1].ID != 7 || devices[1].Connection != "USB" {
		t.Fatal(devices, err)
	}
	r := &iosRunner{udid: "00008110-000A1B2C3D4E5F60", base: "http://runner", client: &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return usbmuxDial(ctx, "00008110-000A1B2C3D4E5F60", runnerDevicePort)
	}}}}
	b, err := r.do(ctx, "GET", "/status", nil)
	if err != nil || string(b) != `{"ok":true}` {
		t.Fatal(string(b), err)
	}
	if _, err := usbmuxDial(ctx, "other-udid", runnerDevicePort); errorCode(err) != "device_unavailable" {
		t.Fatal(err)
	}
}
