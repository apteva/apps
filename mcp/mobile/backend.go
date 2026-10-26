package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxImage = 12 << 20
const maxSource = 4 << 20

type runnerConfig struct {
	AppiumURL     string
	DevicesJSON   string
	AndroidDriver string
	ArtifactDir   string
	LockDir       string
}

// Device commands never run in a host shell. ADB's *remote* shell still
// interprets its command, so every argument is separately POSIX-quoted.
type commandFunc func(context.Context, string, ...string) ([]byte, error)
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("command output limit exceeded")
	}
	return b.Buffer.Write(p)
}
func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out limitedBuffer
	out.limit = 18 << 20
	var stderr limitedBuffer
	stderr.limit = 8192
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s failed: %s", name, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

type drivers struct {
	cfg     runnerConfig
	command commandFunc
	http    *http.Client
}

func newDrivers(cfg runnerConfig) *drivers {
	return &drivers{cfg: cfg, command: runCommand, http: &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d *drivers) adb(ctx context.Context, serial string, args ...string) ([]byte, error) {
	return d.command(ctx, "adb", append([]string{"-s", serial}, args...)...)
}
func (d *drivers) shell(ctx context.Context, serial string, args ...string) ([]byte, error) {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return d.adb(ctx, serial, "shell", strings.Join(quoted, " "))
}

func (d *drivers) devices(ctx context.Context) ([]Device, []string, error) {
	devices := []Device{}
	diagnostics := []string{}
	seen := map[string]bool{}
	serials := map[string]bool{}
	if raw := strings.TrimSpace(d.cfg.DevicesJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &devices); err != nil {
			return nil, nil, problem("invalid_configuration", "devices_json must be an array of device objects")
		}
		for i := range devices {
			v := &devices[i]
			if v.ID == "" || v.UDID == "" || (v.Platform != "android" && v.Platform != "ios") || seen[v.ID] || serials[v.Platform+":"+v.UDID] {
				return nil, nil, problem("invalid_configuration", "devices_json requires unique id and platform/udid, and platform android or ios")
			}
			seen[v.ID] = true
			serials[v.Platform+":"+v.UDID] = true
			if v.Name == "" {
				v.Name = v.ID
			}
			if v.Driver == "" {
				v.Driver = "appium"
				if v.Platform == "android" && d.cfg.AndroidDriver != "appium" {
					v.Driver = "adb"
				}
			}
			if v.Driver != "adb" && v.Driver != "appium" || v.Platform == "ios" && v.Driver != "appium" {
				return nil, nil, problem("invalid_configuration", "unsupported device driver")
			}
			v.State = "configured"
			v.Capabilities = capabilities(v.Driver)
			if v.Driver == "appium" && d.cfg.AppiumURL == "" {
				v.State = "setup_required"
				v.Hint = "Configure appium_url and prepare the device on that Appium host."
			}
		}
	}
	out, err := d.command(ctx, "adb", "devices", "-l")
	if err != nil {
		diagnostics = append(diagnostics, "ADB discovery unavailable. Install Android Platform Tools; authorize the phone. Configured Appium devices remain usable.")
	} else {
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 || f[0] == "List" || strings.HasPrefix(f[0], "*") {
				continue
			}
			if serials["android:"+f[0]] {
				for i := range devices {
					if devices[i].Platform == "android" && devices[i].UDID == f[0] && devices[i].Driver == "adb" {
						devices[i].State = adbState(f[1])
						if f[1] != "device" {
							devices[i].Hint = "Unlock the phone and authorize debugging, or reconnect it."
						}
					}
				}
				continue
			}
			name := f[0]
			for _, x := range f[2:] {
				if strings.HasPrefix(x, "model:") {
					name = strings.ReplaceAll(strings.TrimPrefix(x, "model:"), "_", " ")
				}
			}
			driver := "adb"
			if d.cfg.AndroidDriver == "appium" {
				driver = "appium"
			}
			v := Device{ID: "android:" + f[0], UDID: f[0], Name: name, Platform: "android", Physical: !strings.HasPrefix(f[0], "emulator-"), Driver: driver, State: adbState(f[1]), Capabilities: capabilities(driver)}
			if f[1] != "device" {
				v.Hint = "Unlock the phone and authorize debugging, or reconnect it."
			}
			if driver == "appium" && d.cfg.AppiumURL == "" {
				v.State = "setup_required"
				v.Hint = "Set appium_url."
			}
			if seen[v.ID] {
				return nil, nil, problem("invalid_configuration", "configured device id conflicts with discovered Android id")
			}
			devices = append(devices, v)
		}
	}
	if d.cfg.AppiumURL != "" {
		diagnostics = append(diagnostics, "iOS and remote Appium devices must be registered in devices_json. iOS requires a prepared device and signed WebDriverAgent; use a Mac/Xcode runner for the supported setup.")
	} else {
		diagnostics = append(diagnostics, "Set appium_url and devices_json to enable iOS or structured Android automation.")
	}
	return devices, diagnostics, nil
}
func adbState(s string) string {
	if s == "device" {
		return "ready"
	}
	return s
}
func capabilities(driver string) []string {
	out := []string{"screen", "tap", "swipe", "text", "navigate", "install", "launch", "stop", "uninstall", "source"}
	if driver == "appium" {
		out = append(out, "structured", "elements", "contexts")
	}
	return out
}

func (d *drivers) Open(ctx context.Context, v Device, mode string) (string, error) {
	if v.State != "ready" && v.State != "configured" {
		return "", problem("device_unavailable", "device is "+v.State+": "+v.Hint)
	}
	if v.Driver == "adb" {
		if mode == "structured" {
			return "", problem("unsupported", "structured mode requires the Appium driver")
		}
		_, err := d.adb(ctx, v.UDID, "get-state")
		return "", err
	}
	if d.cfg.AppiumURL == "" {
		return "", problem("setup_required", "appium_url is required")
	}
	driver := "UiAutomator2"
	platform := "Android"
	if v.Platform == "ios" {
		driver = "XCUITest"
		platform = "iOS"
	}
	caps := map[string]any{"platformName": platform, "appium:automationName": driver, "appium:udid": v.UDID, "appium:noReset": true, "appium:newCommandTimeout": 900}
	if mode == "structured" {
		browser := v.Browser
		if browser == "" {
			browser = "Chrome"
			if v.Platform == "ios" {
				browser = "Safari"
			}
		}
		caps["browserName"] = browser
	} else {
		caps["appium:autoLaunch"] = false
	}
	var response struct {
		Value struct {
			SessionID string `json:"sessionId"`
		} `json:"value"`
		SessionID string `json:"sessionId"`
	}
	err := d.wire(ctx, "POST", "/session", map[string]any{"capabilities": map[string]any{"alwaysMatch": caps, "firstMatch": []any{map[string]any{}}}}, &response)
	if err != nil {
		return "", err
	}
	sid := response.Value.SessionID
	if sid == "" {
		sid = response.SessionID
	}
	if sid == "" {
		return "", errors.New("Appium returned no session id")
	}
	return sid, nil
}
func (d *drivers) Close(ctx context.Context, s Session) error {
	if s.ProviderID == "" || s.Device.Driver == "adb" {
		return nil
	}
	err := d.wire(ctx, "DELETE", "/session/"+url.PathEscape(s.ProviderID), nil, nil)
	if errorCode(err) == "invalid session id" {
		return nil
	}
	return err
}
func (d *drivers) wire(ctx context.Context, method, path string, body any, target any) error {
	u, err := url.Parse(d.cfg.AppiumURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return problem("invalid_configuration", "appium_url must be an HTTP(S) base URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	var data io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), data)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := d.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return problem("infrastructure_error", "cannot reach Appium; verify the runner's appium_url and server health")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 18<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 18<<20 {
		return errors.New("Appium response exceeds limit")
	}
	var envelope struct {
		Value struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		} `json:"value"`
	}
	_ = json.Unmarshal(b, &envelope)
	if res.StatusCode >= 300 || envelope.Value.Error != "" {
		code := envelope.Value.Error
		if code == "" {
			code = "infrastructure_error"
		}
		return problem(code, "Appium operation failed ("+code+"); inspect the Appium server log for details")
	}
	if target != nil {
		return json.Unmarshal(b, target)
	}
	return nil
}
func (d *drivers) sessionWire(ctx context.Context, s Session, method, path string, body any) (any, error) {
	var res struct {
		Value any `json:"value"`
	}
	err := d.wire(ctx, method, "/session/"+url.PathEscape(s.ProviderID)+path, body, &res)
	return res.Value, err
}
func (d *drivers) script(ctx context.Context, s Session, name string, args map[string]any) (any, error) {
	return d.sessionWire(ctx, s, "POST", "/execute/sync", map[string]any{"script": "mobile: " + name, "args": []any{args}})
}

func observation(b []byte, source string) (Observation, error) {
	if len(b) > maxImage {
		return Observation{}, errors.New("screenshot exceeds 12 MiB")
	}
	c, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != "png" || c.Width <= 0 || c.Height <= 0 || c.Width > 16384 || c.Height > 16384 {
		return Observation{}, errors.New("invalid device PNG screenshot")
	}
	return Observation{Image: base64.StdEncoding.EncodeToString(b), MIME: "image/png", Source: source, Width: c.Width, Height: c.Height}, nil
}
func (d *drivers) Observe(ctx context.Context, s Session, source bool) (Observation, error) {
	var png []byte
	var tree string
	var err error
	if s.Device.Driver == "adb" {
		png, err = d.adb(ctx, s.Device.UDID, "exec-out", "screencap", "-p")
		if err != nil {
			return Observation{}, err
		}
		if source {
			remote := "/data/local/tmp/apteva-" + s.ID + ".xml"
			_, err = d.shell(ctx, s.Device.UDID, "uiautomator", "dump", remote)
			if err != nil {
				return Observation{}, err
			}
			b, e := d.shell(ctx, s.Device.UDID, "cat", remote)
			_, _ = d.shell(ctx, s.Device.UDID, "rm", "-f", remote)
			if e != nil {
				return Observation{}, e
			}
			tree = string(b)
		}
	} else {
		v, e := d.sessionWire(ctx, s, "GET", "/screenshot", nil)
		if e != nil {
			return Observation{}, e
		}
		b64, ok := v.(string)
		if !ok {
			return Observation{}, errors.New("Appium returned invalid screenshot")
		}
		png, err = base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return Observation{}, err
		}
		if source {
			v, e := d.sessionWire(ctx, s, "GET", "/source", nil)
			if e != nil {
				return Observation{}, e
			}
			tree, _ = v.(string)
		}
	}
	if len(tree) > maxSource {
		return Observation{}, errors.New("UI source exceeds 4 MiB")
	}
	return observation(png, tree)
}

func validateURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return problem("invalid_argument", "url must be HTTP(S), with no embedded credentials")
	}
	if len(raw) > 8192 {
		return problem("invalid_argument", "url is too long")
	}
	return nil
}

var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)

func appID(args map[string]any) (string, error) {
	v, err := requireString(args, "app_id")
	if err != nil {
		return "", err
	}
	if !appIDPattern.MatchString(v) {
		return "", problem("invalid_argument", "invalid app_id")
	}
	return v, nil
}

func (d *drivers) Action(ctx context.Context, s Session, action string, args map[string]any) (any, error) {
	if action == "navigate" {
		if err := validateURL(str(args, "url")); err != nil {
			return nil, err
		}
	}
	if s.Device.Driver == "adb" {
		return d.adbAction(ctx, s, action, args)
	}
	switch action {
	case "navigate":
		if s.Mode == "structured" {
			return d.sessionWire(ctx, s, "POST", "/url", map[string]any{"url": str(args, "url")})
		}
		p := map[string]any{"url": str(args, "url")}
		if s.Device.Platform == "ios" {
			p["bundleId"] = "com.apple.mobilesafari"
		} else {
			p["package"] = "com.android.chrome"
		}
		return d.script(ctx, s, "deepLink", p)
	case "tap", "swipe":
		x, e := coord(args, "x")
		if e != nil {
			return nil, e
		}
		y, e := coord(args, "y")
		if e != nil {
			return nil, e
		}
		v, e := d.sessionWire(ctx, s, "GET", "/window/rect", nil)
		if e != nil {
			return nil, e
		}
		r, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("invalid Appium window rectangle")
		}
		w, wok := number(r["width"])
		h, hok := number(r["height"])
		if !wok || !hok || w <= 0 || h <= 0 {
			return nil, errors.New("invalid Appium window size")
		}
		steps := []any{map[string]any{"type": "pointerMove", "duration": 0, "origin": "viewport", "x": int(x * (w - 1)), "y": int(y * (h - 1))}, map[string]any{"type": "pointerDown", "button": 0}}
		if action == "swipe" {
			tx, e := coord(args, "to_x")
			if e != nil {
				return nil, e
			}
			ty, e := coord(args, "to_y")
			if e != nil {
				return nil, e
			}
			steps = append(steps, map[string]any{"type": "pointerMove", "duration": 400, "origin": "viewport", "x": int(tx * (w - 1)), "y": int(ty * (h - 1))})
		}
		steps = append(steps, map[string]any{"type": "pointerUp", "button": 0})
		return d.sessionWire(ctx, s, "POST", "/actions", map[string]any{"actions": []any{map[string]any{"type": "pointer", "id": "touch", "parameters": map[string]any{"pointerType": "touch"}, "actions": steps}}})
	case "text":
		text, e := requireString(args, "text")
		if e != nil {
			return nil, e
		}
		v, e := d.sessionWire(ctx, s, "GET", "/element/active", nil)
		if e != nil {
			return nil, e
		}
		element, e := elementID(v)
		if e != nil {
			return nil, e
		}
		return d.sessionWire(ctx, s, "POST", "/element/"+url.PathEscape(element)+"/value", map[string]any{"text": text})
	case "key":
		key := str(args, "key")
		if key == "home" {
			if s.Device.Platform == "ios" {
				return d.script(ctx, s, "pressButton", map[string]any{"name": "home"})
			}
			return d.script(ctx, s, "pressKey", map[string]any{"keycode": 3})
		}
		if key == "back" {
			if s.Device.Platform == "android" && s.Mode == "screen" {
				return d.script(ctx, s, "pressKey", map[string]any{"keycode": 4})
			}
			return d.sessionWire(ctx, s, "POST", "/back", map[string]any{})
		}
		if key == "enter" {
			return d.sessionWire(ctx, s, "POST", "/actions", map[string]any{"actions": []any{map[string]any{"type": "key", "id": "keyboard", "actions": []any{map[string]any{"type": "keyDown", "value": "\ue007"}, map[string]any{"type": "keyUp", "value": "\ue007"}}}}})
		}
		return nil, problem("invalid_argument", "key must be home, back or enter")
	case "tap_element":
		using := str(args, "using")
		switch using {
		case "accessibility id", "id", "xpath", "css selector":
		default:
			return nil, problem("invalid_argument", "using must be accessibility id, id, xpath or css selector")
		}
		value, e := requireString(args, "value")
		if e != nil {
			return nil, e
		}
		v, e := d.sessionWire(ctx, s, "POST", "/element", map[string]any{"using": using, "value": value})
		if e != nil {
			return nil, e
		}
		element, e := elementID(v)
		if e != nil {
			return nil, e
		}
		return d.sessionWire(ctx, s, "POST", "/element/"+url.PathEscape(element)+"/click", map[string]any{})
	case "context":
		if str(args, "context") == "" {
			return d.sessionWire(ctx, s, "GET", "/contexts", nil)
		}
		return d.sessionWire(ctx, s, "POST", "/context", map[string]any{"name": str(args, "context")})
	default:
		return nil, problem("unsupported", "unsupported mobile action")
	}
}
func elementID(v any) (string, error) {
	m, ok := v.(map[string]any)
	if ok {
		for _, k := range []string{"element-6066-11e4-a52e-4f735466cecf", "ELEMENT"} {
			if s, ok := m[k].(string); ok && s != "" {
				return s, nil
			}
		}
	}
	return "", errors.New("Appium returned no element id")
}

func (d *drivers) adbAction(ctx context.Context, s Session, action string, args map[string]any) (any, error) {
	serial := s.Device.UDID
	var command []string
	switch action {
	case "navigate":
		command = []string{"am", "start", "-W", "-a", "android.intent.action.VIEW", "-d", str(args, "url"), "-p", "com.android.chrome"}
	case "tap", "swipe":
		x, e := coord(args, "x")
		if e != nil {
			return nil, e
		}
		y, e := coord(args, "y")
		if e != nil {
			return nil, e
		}
		obs, e := d.Observe(ctx, s, false)
		if e != nil {
			return nil, e
		}
		command = []string{"input", action, strconv.Itoa(int(x * float64(obs.Width-1))), strconv.Itoa(int(y * float64(obs.Height-1)))}
		if action == "swipe" {
			tx, e := coord(args, "to_x")
			if e != nil {
				return nil, e
			}
			ty, e := coord(args, "to_y")
			if e != nil {
				return nil, e
			}
			command = append(command, strconv.Itoa(int(tx*float64(obs.Width-1))), strconv.Itoa(int(ty*float64(obs.Height-1))), "400")
		}
	case "text":
		text, e := requireString(args, "text")
		if e != nil {
			return nil, e
		}
		for _, r := range text {
			if r < 32 || r > 126 || r == '%' {
				return nil, problem("unsupported", "ADB text supports printable ASCII excluding %. Use Appium for Unicode or percent signs.")
			}
		}
		command = []string{"input", "text", strings.ReplaceAll(text, " ", "%s")}
	case "key":
		codes := map[string]string{"home": "3", "back": "4", "enter": "66"}
		code, ok := codes[str(args, "key")]
		if !ok {
			return nil, problem("invalid_argument", "key must be home, back or enter")
		}
		command = []string{"input", "keyevent", code}
	default:
		return nil, problem("unsupported", "this action requires Appium; ADB supports navigate, tap, swipe, text and key")
	}
	out, e := d.shell(ctx, serial, command...)
	if e != nil {
		return nil, e
	}
	if strings.Contains(string(out), "Error:") || strings.Contains(string(out), "Exception") {
		return nil, problem("infrastructure_error", "Android rejected the action; verify the target app is installed and the phone is unlocked")
	}
	return map[string]any{"ok": true}, nil
}

func (d *drivers) installPath(raw, platform string) (string, error) {
	if raw == "" {
		return "", problem("invalid_argument", "artifact is required")
	}
	root, e := filepath.EvalSymlinks(d.cfg.ArtifactDir)
	if e != nil {
		return "", problem("setup_required", "artifact directory is unavailable")
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	p, e := filepath.EvalSymlinks(candidate)
	if e != nil {
		return "", problem("invalid_argument", "artifact not found")
	}
	rel, e := filepath.Rel(root, p)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", problem("invalid_argument", "artifact must be inside the configured artifact directory")
	}
	ext := strings.ToLower(filepath.Ext(p))
	if platform == "android" && ext != ".apk" || platform == "ios" && ext != ".ipa" {
		return "", problem("invalid_argument", "physical Android needs an APK; physical iOS needs a device-signed IPA")
	}
	info, e := os.Stat(p)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 512<<20 {
		return "", problem("invalid_argument", "artifact must be a regular file no larger than 512 MiB")
	}
	return p, nil
}
func (d *drivers) App(ctx context.Context, s Session, action string, args map[string]any) (any, error) {
	if action == "uninstall" && !boolean(args, "confirm") {
		return nil, problem("invalid_argument", "uninstall requires confirm=true")
	}
	if action == "install" {
		p, e := d.installPath(str(args, "artifact"), s.Device.Platform)
		if e != nil {
			return nil, e
		}
		if s.Device.Driver == "adb" {
			_, e = d.adb(ctx, s.Device.UDID, "install", "-r", p)
			return map[string]any{"ok": e == nil}, e
		}
		u, _ := url.Parse(d.cfg.AppiumURL)
		if u == nil || !isLoopback(u.Hostname()) {
			return nil, problem("unsupported", "install through Appium requires Appium on this runner's loopback interface, sharing its artifact filesystem")
		}
		return d.sessionWire(ctx, s, "POST", "/appium/device/install_app", map[string]any{"appPath": p})
	}
	if action != "launch" && action != "stop" && action != "uninstall" {
		return nil, problem("invalid_argument", "app action must be install, launch, stop or uninstall")
	}
	app, e := appID(args)
	if e != nil {
		return nil, e
	}
	if s.Device.Driver == "adb" {
		var out []byte
		switch action {
		case "launch":
			resolved, resolveErr := d.shell(ctx, s.Device.UDID, "cmd", "package", "resolve-activity", "--brief", "-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER", app)
			if resolveErr != nil {
				return nil, resolveErr
			}
			component := ""
			for _, line := range strings.Split(string(resolved), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, app+"/") {
					component = line
				}
			}
			if component == "" {
				return nil, problem("infrastructure_error", "no launcher activity found for this app")
			}
			out, e = d.shell(ctx, s.Device.UDID, "am", "start", "-W", "-n", component)
		case "stop":
			out, e = d.shell(ctx, s.Device.UDID, "am", "force-stop", app)
		case "uninstall":
			out, e = d.adb(ctx, s.Device.UDID, "uninstall", app)
		}
		if e == nil && (strings.Contains(string(out), "Error") || strings.Contains(string(out), "Failure") || strings.Contains(string(out), "No activities found")) {
			e = problem("infrastructure_error", "Android could not perform the app operation")
		}
		return map[string]any{"ok": e == nil}, e
	}
	path := map[string]string{"launch": "activate_app", "stop": "terminate_app", "uninstall": "remove_app"}[action]
	return d.sessionWire(ctx, s, "POST", "/appium/device/"+path, map[string]any{"appId": app})
}
