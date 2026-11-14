package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Native iOS control without Appium: devicectl/simctl manage devices and
// apps, and a small XCUITest bundle (runner/) serves taps, typing, UI source
// and screenshots over HTTP. Devices are reached through usbmuxd, simulators
// over loopback.

//go:embed runner
var runnerFS embed.FS

const (
	driverXCTest     = "xctest"
	methodSimctl     = "simctl"
	runnerDevicePort = 22087
	runnerBuildLimit = 15 * time.Minute
	runnerStartLimit = 4 * time.Minute
)

var teamIDPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

func (d *drivers) nativeIOS() bool {
	return runtime.GOOS == "darwin" && d.cfg.IOSDriver != "appium"
}

// --- discovery ---------------------------------------------------------------

type devicectlDevice struct {
	Identifier           string `json:"identifier"`
	ConnectionProperties struct {
		PairingState  string `json:"pairingState"`
		TransportType string `json:"transportType"`
		TunnelState   string `json:"tunnelState"`
	} `json:"connectionProperties"`
	DeviceProperties struct {
		Name                string `json:"name"`
		OSVersionNumber     string `json:"osVersionNumber"`
		DeveloperModeStatus string `json:"developerModeStatus"`
	} `json:"deviceProperties"`
	HardwareProperties struct {
		UDID     string `json:"udid"`
		Platform string `json:"platform"`
		Reality  string `json:"reality"`
	} `json:"hardwareProperties"`
}

// devicectl writes JSON only to a file.
func (d *drivers) devicectlJSON(ctx context.Context, target any, args ...string) error {
	f, err := os.CreateTemp("", "apteva-devicectl-*.json")
	if err != nil {
		return err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if _, err := d.command(ctx, "xcrun", append(append([]string{"devicectl"}, args...), "--quiet", "--json-output", path)...); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

func (d *drivers) iosDevices(ctx context.Context, taken map[string]bool) ([]Device, []string) {
	var out []Device
	var diagnostics []string
	var list struct {
		Result struct {
			Devices []devicectlDevice `json:"devices"`
		} `json:"result"`
	}
	if err := d.devicectlJSON(ctx, &list, "list", "devices"); err != nil {
		diagnostics = append(diagnostics, "iOS device discovery unavailable: install Xcode 15 or newer on this Mac.")
	}
	for _, x := range list.Result.Devices {
		hw := x.HardwareProperties
		if hw.Platform != "iOS" || hw.Reality != "physical" || hw.UDID == "" || taken["ios:"+hw.UDID] {
			continue
		}
		v := Device{ID: "ios:" + hw.UDID, UDID: hw.UDID, Name: x.DeviceProperties.Name, Platform: "ios", Physical: true, Driver: driverXCTest, OS: x.DeviceProperties.OSVersionNumber, State: "ready"}
		if v.Name == "" {
			v.Name = "iPhone"
		}
		major, _ := strconv.Atoi(strings.SplitN(v.OS, ".", 2)[0])
		switch {
		case major > 0 && major < 17:
			v.State, v.Hint = "setup_required", "Mobile's native runner requires iOS 17 or newer. Update this iPhone before opening a session."
		case x.ConnectionProperties.TransportType == "":
			v.State, v.Hint = "offline", "Connect the device by USB (or keep it on the same Wi-Fi as this Mac) and unlock it."
		case x.ConnectionProperties.PairingState != "paired":
			v.State, v.Hint = "setup_required", "Unlock the device and tap Trust when asked to trust this Mac."
		case x.DeviceProperties.DeveloperModeStatus == "disabled":
			v.State, v.Hint = "setup_required", "Enable Developer Mode on the device: Settings > Privacy & Security > Developer Mode, then restart it."
		}
		v.Capabilities = append(capabilities(driverXCTest), d.recordCapabilities(v)...)
		out = append(out, v)
	}
	var sims struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if b, err := d.command(ctx, "xcrun", "simctl", "list", "devices", "booted", "-j"); err == nil && json.Unmarshal(b, &sims) == nil {
		runtimes := make([]string, 0, len(sims.Devices))
		for k := range sims.Devices {
			runtimes = append(runtimes, k)
		}
		sort.Strings(runtimes)
		for _, rt := range runtimes {
			if !strings.Contains(rt, "SimRuntime.iOS-") {
				continue
			}
			version := strings.ReplaceAll(rt[strings.Index(rt, "iOS-")+4:], "-", ".")
			for _, s := range sims.Devices[rt] {
				if s.State != "Booted" || s.UDID == "" || taken["ios-sim:"+s.UDID] {
					continue
				}
				v := Device{ID: "ios-sim:" + s.UDID, UDID: s.UDID, Name: s.Name + " (Simulator)", Platform: "ios", Driver: driverXCTest, OS: version, State: "ready"}
				v.Capabilities = append(capabilities(driverXCTest), d.recordCapabilities(v)...)
				out = append(out, v)
			}
		}
	}
	return out, diagnostics
}

// --- signing -------------------------------------------------------------------

// Team discovery must work before the first development certificate exists.
// The selected/configured team wins, followed by existing signing assets or
// a single team cached by Xcode. xcodebuild creates any missing signing assets.
func (d *drivers) teamID(ctx context.Context) (string, error) {
	if t := strings.TrimSpace(d.cfg.IOSTeamID); t != "" {
		if !teamIDPattern.MatchString(t) {
			return "", problem("invalid_configuration", "ios_team_id must be the 10-character Apple team ID")
		}
		return t, nil
	}
	if t := d.savedTeam(); t != "" {
		return t, nil
	}
	out, err := d.command(ctx, "security", "find-certificate", "-a", "-c", "Apple Development", "-p")
	if err == nil {
		if t := teamFromPEM(out, time.Now()); t != "" {
			return t, nil
		}
	}
	teams := d.signingTeams(ctx)
	if len(teams) == 1 {
		return teams[0].ID, nil
	}
	if len(teams) > 1 {
		return "", problem("setup_required", "Choose your Apple signing team in Mobile > Setup. Mobile will create the certificate and provisioning profile automatically.")
	}
	return "", problem("setup_required", "Open Xcode > Settings > Accounts and sign in with your Apple Account (a free account works), then refresh Mobile > Setup. No manual certificate creation is needed. Advanced runners can set ios_team_id.")
}

func teamFromPEM(b []byte, now time.Time) string {
	best, bestUntil := "", time.Time{}
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			return best
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || now.After(cert.NotAfter) || now.Before(cert.NotBefore) || len(cert.Subject.OrganizationalUnit) == 0 {
			continue
		}
		if t := cert.Subject.OrganizationalUnit[0]; teamIDPattern.MatchString(t) && cert.NotAfter.After(bestUntil) {
			best, bestUntil = t, cert.NotAfter
		}
	}
}

// --- runner lifecycle ------------------------------------------------------------

type ringLog struct {
	mu  sync.Mutex
	buf []byte
}

func (r *ringLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - 64<<10; over > 0 {
		r.buf = append([]byte(nil), r.buf[over:]...)
	}
	return len(p), nil
}
func (r *ringLog) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

type iosRunner struct {
	udid   string
	sim    bool
	port   int
	base   string
	client *http.Client
	ready  chan struct{} // closed once started or failed
	err    error
	proc   *exec.Cmd
	exited chan struct{}
	log    ringLog
	token  string
}

func (r *iosRunner) alive() bool {
	select {
	case <-r.ready:
	default:
		return true // still preparing
	}
	if r.err != nil {
		return false
	}
	select {
	case <-r.exited:
		return false
	default:
		return true
	}
}

type iosRunners struct {
	mu      sync.Mutex
	byUDID  map[string]*iosRunner
	buildMu sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	stopped bool
}

// runner returns a started runner for v, starting (and on first use
// building) it in the background. Callers that time out get a retryable
// setup_in_progress problem while preparation continues.
func (d *drivers) runner(ctx context.Context, v Device) (*iosRunner, error) {
	d.ios.mu.Lock()
	if d.ios.stopped {
		d.ios.mu.Unlock()
		return nil, problem("infrastructure_error", "Mobile's iOS runner is shutting down")
	}
	if d.ios.ctx == nil {
		d.ios.ctx, d.ios.cancel = context.WithCancel(context.Background())
	}
	if d.ios.byUDID == nil {
		d.ios.byUDID = map[string]*iosRunner{}
	}
	r := d.ios.byUDID[v.UDID]
	if r != nil && !r.alive() && r.err != nil {
		// A polling caller must see a completed preparation failure before
		// another build replaces it, especially Apple's approval/sign-in errors.
		delete(d.ios.byUDID, v.UDID)
		d.ios.mu.Unlock()
		return nil, r.err
	}
	if r == nil || !r.alive() {
		r = &iosRunner{udid: v.UDID, sim: !v.Physical, token: id(""), ready: make(chan struct{}), exited: make(chan struct{})}
		d.ios.byUDID[v.UDID] = r
		d.ios.wg.Add(1)
		go func() { defer d.ios.wg.Done(); d.prepareRunner(d.ios.ctx, r) }()
	}
	d.ios.mu.Unlock()
	select {
	case <-r.ready:
	case <-ctx.Done():
		return nil, problem("setup_in_progress", "the iOS runner is still starting (the first start builds and installs it, about 1-3 minutes); retry shortly")
	}
	if r.err != nil {
		d.ios.mu.Lock()
		if d.ios.byUDID[v.UDID] == r {
			delete(d.ios.byUDID, v.UDID)
		}
		d.ios.mu.Unlock()
		return nil, r.err
	}
	return r, nil
}

func (d *drivers) prepareRunner(parent context.Context, r *iosRunner) {
	defer close(r.ready)
	fail := func(err error) {
		r.err = err
		if r.proc != nil && r.proc.Process != nil {
			_ = r.proc.Process.Kill()
		}
	}
	ctx, cancel := context.WithTimeout(parent, runnerBuildLimit+runnerStartLimit)
	defer cancel()
	xctestrun, err := d.buildRunner(ctx, r)
	if err != nil {
		fail(err)
		return
	}
	if r.sim {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fail(err)
			return
		}
		r.port = l.Addr().(*net.TCPAddr).Port
		l.Close()
		r.base = "http://127.0.0.1:" + strconv.Itoa(r.port)
		r.client = &http.Client{Timeout: 90 * time.Second}
	} else {
		r.port = runnerDevicePort
		r.base = "http://runner"
		udid := r.udid
		r.client = &http.Client{Timeout: 90 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return usbmuxDial(ctx, udid, runnerDevicePort)
		}}}
	}
	cmd := exec.Command("xcrun", "xcodebuild", "test-without-building", "-xctestrun", xctestrun, "-destination", "id="+r.udid)
	// xcodebuild forwards TEST_RUNNER_-prefixed variables to the test process.
	cmd.Env = append(os.Environ(), "TEST_RUNNER_APTEVA_PORT="+strconv.Itoa(r.port), "TEST_RUNNER_APTEVA_TOKEN="+r.token)
	cmd.Stdout, cmd.Stderr = &r.log, &r.log
	if err := cmd.Start(); err != nil {
		fail(problem("setup_required", "cannot start xcodebuild: "+err.Error()))
		return
	}
	r.proc = cmd
	go func() { _ = cmd.Wait(); close(r.exited) }()
	deadline := time.Now().Add(runnerStartLimit)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			fail(ctx.Err())
			return
		case <-r.exited:
			fail(runnerProblem("the iOS runner stopped while starting", r.log.String()))
			return
		case <-time.After(time.Second):
		}
		probe, cancelProbe := context.WithTimeout(ctx, 3*time.Second)
		_, err := r.do(probe, "GET", "/status", nil)
		cancelProbe()
		if err == nil {
			return
		}
	}
	fail(runnerProblem("the iOS runner did not answer within 4 minutes", r.log.String()))
}

func runnerSourceHash() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(runnerFS, "runner", func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := runnerFS.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(b))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12], err
}

// buildArgs returns the xcodebuild invocation for one runner build. Device
// builds use automatic signing and register the device with the team.
func runnerBuildArgs(project, derived, udid, team string) []string {
	args := []string{"xcodebuild", "build-for-testing", "-project", project, "-scheme", "AptevaRunner", "-derivedDataPath", derived}
	if team == "" {
		return append(args, "-destination", "generic/platform=iOS Simulator")
	}
	return append(args, "-destination", "id="+udid, "-allowProvisioningUpdates", "-allowProvisioningDeviceRegistration",
		"DEVELOPMENT_TEAM="+team, "CODE_SIGN_STYLE=Automatic", "PRODUCT_BUNDLE_IDENTIFIER=dev.apteva.mobile.runner."+strings.ToLower(team))
}

func (d *drivers) buildRunner(ctx context.Context, r *iosRunner) (string, error) {
	sum, err := runnerSourceHash()
	if err != nil {
		return "", err
	}
	team := ""
	key := "simulator"
	if !r.sim {
		if team, err = d.teamID(ctx); err != nil {
			return "", err
		}
		// Provisioning profiles list devices, so builds are per team and device.
		key = team + "-" + strings.ReplaceAll(r.udid, "-", "")
	}
	root := filepath.Join(d.cfg.ToolDir, "iosrunner-"+sum)
	derived := filepath.Join(root, key)
	if p := findXCTestRun(derived); p != "" && (r.sim || d.runnerProvisioningValid(ctx, derived)) {
		return p, nil
	}
	d.ios.buildMu.Lock()
	defer d.ios.buildMu.Unlock()
	if p := findXCTestRun(derived); p != "" && (r.sim || d.runnerProvisioningValid(ctx, derived)) {
		return p, nil
	}
	src := filepath.Join(root, "src")
	if err := extractRunner(src); err != nil {
		return "", err
	}
	args := runnerBuildArgs(filepath.Join(src, "AptevaRunner.xcodeproj"), derived, r.udid, team)
	var log ringLog
	bctx, cancel := context.WithTimeout(ctx, runnerBuildLimit)
	defer cancel()
	cmd := exec.CommandContext(bctx, "xcrun", args...)
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(derived)
		return "", runnerProblem("could not build the iOS runner", log.String())
	}
	if p := findXCTestRun(derived); p != "" {
		return p, nil
	}
	return "", errors.New("xcodebuild produced no .xctestrun file")
}

func findXCTestRun(derived string) string {
	matches, _ := filepath.Glob(filepath.Join(derived, "Build", "Products", "*.xctestrun"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func extractRunner(dst string) error {
	if _, err := os.Stat(filepath.Join(dst, ".complete")); err == nil {
		return nil
	}
	_ = os.RemoveAll(dst)
	err := fs.WalkDir(runnerFS, "runner", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, strings.TrimPrefix(p, "runner"))
		if e.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		b, err := runnerFS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0600)
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, ".complete"), nil, 0600)
}

// runnerProblem turns xcodebuild output into an actionable message.
func runnerProblem(what, log string) error {
	l := strings.ToLower(log)
	hint := ""
	switch {
	case strings.Contains(l, "license") || strings.Contains(l, "firstlaunch"):
		return problem("setup_required", what+": open Xcode and complete Apple's license and first-launch component prompts, then retry.")
	case strings.Contains(l, "no account for team") || strings.Contains(l, "no accounts") || strings.Contains(l, "requires a development team") || strings.Contains(l, "no profiles for") || strings.Contains(l, "sign in with your apple id"):
		return problem("setup_required", what+": Xcode could not sign it. Open Xcode > Settings > Accounts, sign in with the Apple ID of the signing team (a free account works), then retry.")
	case strings.Contains(l, "developer mode"):
		return problem("setup_required", what+": enable Developer Mode on the device (Settings > Privacy & Security > Developer Mode) and restart it.")
	case strings.Contains(l, "untrusted developer") || strings.Contains(l, "not been explicitly trusted") || strings.Contains(l, "invalid code signature"):
		return problem("setup_required", what+": on the device open Settings > General > VPN & Device Management, trust the developer app for your Apple ID, then retry.")
	case strings.Contains(l, "is locked") || strings.Contains(l, "passcode") || strings.Contains(l, "unlock"):
		return problem("device_unavailable", what+": unlock the device and keep it unlocked while the runner starts, then retry.")
	case strings.Contains(l, "not paired") || strings.Contains(l, "pairing"):
		return problem("setup_required", what+": unlock the device and tap Trust to trust this Mac.")
	}
	var errs []string
	for _, line := range strings.Split(log, "\n") {
		t := strings.TrimSpace(line)
		if strings.Contains(t, "error:") || strings.HasPrefix(t, "Error") {
			errs = append(errs, t)
		}
	}
	if len(errs) > 3 {
		errs = errs[len(errs)-3:]
	}
	if len(errs) > 0 {
		hint = ": " + strings.Join(errs, " | ")
	}
	if len(hint) > 900 {
		hint = hint[:900]
	}
	return problem("infrastructure_error", what+hint)
}

func (d *drivers) stopRunners() {
	d.ios.mu.Lock()
	d.ios.stopped = true
	if d.ios.cancel != nil {
		d.ios.cancel()
	}
	runners := make([]*iosRunner, 0, len(d.ios.byUDID))
	for _, r := range d.ios.byUDID {
		runners = append(runners, r)
	}
	d.ios.byUDID = nil
	d.ios.mu.Unlock()
	d.ios.wg.Wait()
	for _, r := range runners {
		select {
		case <-r.ready:
		default:
			continue // still building; the process dies with the sidecar
		}
		if r.err != nil || r.proc == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = r.do(ctx, "POST", "/stop", map[string]any{})
		cancel()
		select {
		case <-r.exited:
		case <-time.After(20 * time.Second):
			_ = r.proc.Process.Kill()
		}
	}
}

// --- runner protocol ------------------------------------------------------------

func (r *iosRunner) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var data io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, data)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	res, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, problem("infrastructure_error", "lost contact with the iOS runner; retry, and Mobile restarts it if needed")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxImage+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxImage {
		return nil, errors.New("iOS runner response exceeds limit")
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e)
		code := map[string]string{"invalid_argument": "invalid_argument", "no_such_element": "assertion_failed", "action_failed": "action_failed", "unsupported": "unsupported", "device_unavailable": "device_unavailable"}[e.Error]
		if code == "" {
			code = "infrastructure_error"
		}
		if e.Message == "" {
			e.Message = "iOS runner returned HTTP " + strconv.Itoa(res.StatusCode)
		}
		return nil, problem(code, e.Message)
	}
	return b, nil
}

func (r *iosRunner) post(ctx context.Context, path string, body map[string]any) (any, error) {
	if _, err := r.do(ctx, "POST", path, body); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// --- driver operations ----------------------------------------------------------

func (d *drivers) xctestObserve(ctx context.Context, s Session, source bool) (Observation, error) {
	r, err := d.runner(ctx, s.Device)
	if err != nil {
		return Observation{}, err
	}
	png, err := r.do(ctx, "GET", "/screenshot", nil)
	if err != nil {
		return Observation{}, err
	}
	tree := ""
	if source {
		b, err := r.do(ctx, "GET", "/source", nil)
		if err != nil {
			return Observation{}, err
		}
		tree = string(b)
	}
	if len(tree) > maxSource {
		return Observation{}, errors.New("UI source exceeds 4 MiB")
	}
	return observation(png, tree)
}

func (d *drivers) xctestAction(ctx context.Context, s Session, action string, args map[string]any) (any, error) {
	r, err := d.runner(ctx, s.Device)
	if err != nil {
		return nil, err
	}
	switch action {
	case "navigate":
		return r.post(ctx, "/open", map[string]any{"url": str(args, "url"), "bundle_id": "com.apple.mobilesafari"})
	case "tap", "swipe":
		x, e := coord(args, "x")
		if e != nil {
			return nil, e
		}
		y, e := coord(args, "y")
		if e != nil {
			return nil, e
		}
		if action == "tap" {
			return r.post(ctx, "/tap", map[string]any{"x": x, "y": y})
		}
		tx, e := coord(args, "to_x")
		if e != nil {
			return nil, e
		}
		ty, e := coord(args, "to_y")
		if e != nil {
			return nil, e
		}
		return r.post(ctx, "/swipe", map[string]any{"x": x, "y": y, "to_x": tx, "to_y": ty, "duration_ms": 400})
	case "text":
		text, e := requireString(args, "text")
		if e != nil {
			return nil, e
		}
		return r.post(ctx, "/type", map[string]any{"text": text})
	case "key":
		switch key := str(args, "key"); key {
		case "home", "enter":
			return r.post(ctx, "/key", map[string]any{"key": key})
		case "back":
			return nil, problem("unsupported", "iOS has no system back key; tap the app's back button (tap_element or tap)")
		}
		return nil, problem("invalid_argument", "key must be home, back or enter")
	case "tap_element":
		by := map[string]string{"accessibility id": "any", "id": "identifier"}[str(args, "using")]
		if by == "" {
			return nil, problem("unsupported", "the native iOS driver matches elements with using=accessibility id or id")
		}
		value, e := requireString(args, "value")
		if e != nil {
			return nil, e
		}
		return r.post(ctx, "/tap_element", map[string]any{"value": value, "by": by})
	}
	return nil, problem("unsupported", "unsupported mobile action for the native iOS driver")
}

func (d *drivers) xctestTapPoint(ctx context.Context, s Session, x, y int) error {
	r, err := d.runner(ctx, s.Device)
	if err != nil {
		return err
	}
	_, err = r.post(ctx, "/tap", map[string]any{"x": x, "y": y, "unit": "point"})
	return err
}

func (d *drivers) xctestOpenLink(ctx context.Context, s Session, link, app string) error {
	r, err := d.runner(ctx, s.Device)
	if err != nil {
		return err
	}
	_, err = r.post(ctx, "/open", map[string]any{"url": link, "bundle_id": app})
	return err
}

func (d *drivers) xctestApp(ctx context.Context, s Session, action, app, artifact string) (any, error) {
	udid := s.Device.UDID
	switch action {
	case "install":
		if s.Device.Physical {
			_, err := d.command(ctx, "xcrun", "devicectl", "device", "install", "app", "--device", udid, artifact)
			return map[string]any{"ok": err == nil}, err
		}
		_, err := d.command(ctx, "xcrun", "simctl", "install", udid, artifact)
		return map[string]any{"ok": err == nil}, err
	case "uninstall":
		if s.Device.Physical {
			_, err := d.command(ctx, "xcrun", "devicectl", "device", "uninstall", "app", "--device", udid, app)
			return map[string]any{"ok": err == nil}, err
		}
		_, err := d.command(ctx, "xcrun", "simctl", "uninstall", udid, app)
		return map[string]any{"ok": err == nil}, err
	case "status":
		installed, err := d.iosInstalled(ctx, s.Device, app)
		if err != nil {
			return nil, err
		}
		return map[string]any{"app_id": app, "installed": installed}, nil
	}
	r, err := d.runner(ctx, s.Device)
	if err != nil {
		return nil, err
	}
	// No pre-flight install check: app queries take seconds, and the runner
	// already fails a launch of a missing app.
	b, err := r.do(ctx, "POST", "/app", map[string]any{"action": map[string]string{"launch": "launch", "stop": "terminate"}[action], "bundle_id": app})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *drivers) iosInstalled(ctx context.Context, v Device, app string) (bool, error) {
	if !v.Physical {
		_, err := d.command(ctx, "xcrun", "simctl", "get_app_container", v.UDID, app)
		return err == nil, nil
	}
	var apps struct {
		Result struct {
			Apps []struct {
				BundleIdentifier string `json:"bundleIdentifier"`
			} `json:"apps"`
		} `json:"result"`
	}
	if err := d.devicectlJSON(ctx, &apps, "device", "info", "apps", "--device", v.UDID, "--bundle-id", app); err != nil {
		return false, problem("device_unavailable", "could not list apps on the device; unlock it and reconnect: "+err.Error())
	}
	for _, a := range apps.Result.Apps {
		if a.BundleIdentifier == app {
			return true, nil
		}
	}
	return false, nil
}

// --- simulator recording --------------------------------------------------------

func (d *drivers) startSimRecording(ctx context.Context, s Session, path string) (recorder, error) {
	watch := &lineWatcher{lines: make(chan string, 16)}
	proc, err := startProcess("xcrun", []string{"simctl", "io", s.Device.UDID, "recordVideo", "--codec=h264", "--force", path}, watch)
	if err != nil {
		return nil, err
	}
	// simctl reports "Recording started" on stderr once frames flow; the
	// watcher sees stdout, so fall back to a short liveness wait.
	select {
	case <-proc.done:
		return nil, problem("infrastructure_error", "simulator recording stopped immediately; is the simulator still booted?")
	case <-time.After(1500 * time.Millisecond):
		return &procRecorder{proc}, nil
	case <-ctx.Done():
		_ = proc.interrupt(context.Background(), 5*time.Second)
		return nil, ctx.Err()
	}
}
