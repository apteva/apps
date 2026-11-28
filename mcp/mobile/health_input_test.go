package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

const samsungHealth = `@battery
Current Battery Service state:
  AC powered: false
  USB powered: true
  Wireless powered: false
  level: 12
  temperature: 431
  health: 2
@thermal
Thermal Status: 1
@wifi
Wifi is enabled
Wifi is disconnected
Wifi is not connected to any network
@signal
  mSignalStrength=SignalStrength:{mLte=CellSignalStrengthLte: rssi=-61 rsrp=-89 level=3}
@screen
  mWakefulness=Awake
@storage
/dev/block/dm-46   110000000 109000000 1048576  99% /data
@uptime
5025.12 9934.71
`

func TestParseHealthAndAlerts(t *testing.T) {
	h := parseHealth(samsungHealth)
	if h.BatteryLevel == nil || *h.BatteryLevel != 12 || *h.BatteryTempC != 43.1 || h.BatteryHealth != "good" || !*h.Charging {
		t.Fatalf("battery: %+v", h)
	}
	if *h.ThermalStatus != 1 || !*h.WifiEnabled || *h.WifiConnected || *h.SignalLevel != 3 || !*h.ScreenOn || *h.FreeStorageBytes != 1<<30 || *h.UptimeSeconds != 5025 {
		t.Fatalf("health: %s", jsonText(h))
	}
	got := strings.Join(healthAlerts(h), ",")
	// Charging, so a low level is not an alert.
	if got != "hot,wifi_down,low_storage" {
		t.Fatalf("alerts: %s", got)
	}
	if a := healthAlerts(parseHealth("garbage")); len(a) != 1 || a[0] != "health_check_failed" {
		t.Fatalf("failed read: %v", a)
	}
}

// presenceFixture lets a test change what adb devices reports.
func presenceFixture(t *testing.T, grace time.Duration) (*engine, *fakeBackend, func(string)) {
	e, b := fixture(t)
	var mu sync.Mutex
	state := "device"
	e.drivers.cfg.OfflineGrace = grace
	e.drivers.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if name == "adb" && len(args) > 0 && args[0] == "devices" {
			if state == "" {
				return []byte("List of devices attached\n"), nil
			}
			return []byte("List of devices attached\nphone " + state + " model:Pixel_8\n"), nil
		}
		return []byte("List of devices attached\nphone device model:Pixel_8\n"), nil
	}
	set := func(s string) { mu.Lock(); state = s; mu.Unlock() }
	return e, b, set
}

func TestPhoneDropPausesAndResumesSession(t *testing.T) {
	e, _, set := presenceFixture(t, time.Hour)
	e.health.scan()
	s := openFixture(t, e, "p")
	set("")
	e.health.scan()
	got, _ := e.session("p", s.ID)
	if got.Status != "paused" || got.PausedAt == nil {
		t.Fatalf("after drop: %+v", got)
	}
	if _, err := e.Execute(context.Background(), "p", "mobile_action", map[string]any{"session_id": s.ID, "action": "tap", "x": 0.5, "y": 0.5}); errorCode(err) != "device_unavailable" {
		t.Fatalf("action on paused: %v", err)
	}
	// The paused session still holds the phone.
	if _, err := e.open(context.Background(), "other", map[string]any{"device_id": "android:phone"}); err == nil {
		t.Fatal("paused phone leased to someone else")
	}
	set("device")
	e.health.scan()
	got, _ = e.session("p", s.ID)
	if got.Status != "active" || got.PausedAt != nil {
		t.Fatalf("after return: %+v", got)
	}
	if len(eventsOf(e, "p", "mobile.device.offline")) != 1 || len(eventsOf(e, "p", "mobile.device.online")) != 1 {
		t.Fatal("device events missing")
	}
}

func TestPhoneAwayPastGraceEndsAsDeviceLost(t *testing.T) {
	e, _, set := presenceFixture(t, time.Second)
	e.health.scan()
	s := openFixture(t, e, "p")
	set("")
	e.health.scan()
	_, _ = e.db.Exec(`UPDATE mobile_session_meta SET paused_at=? WHERE session_id=?`, time.Now().Add(-time.Minute).Unix(), s.ID)
	e.health.expirePaused()
	got, _ := e.session("p", s.ID)
	if got.Status != "device_lost" || got.EndReason != "device_lost" {
		t.Fatalf("after grace: %+v", got)
	}
}

func TestKeycodesAndBlockList(t *testing.T) {
	d := newDrivers(runnerConfig{})
	if c, err := d.androidKeycode(map[string]any{"key": "volume_up"}); err != nil || c != 24 {
		t.Fatalf("named key: %d %v", c, err)
	}
	if c, err := d.androidKeycode(map[string]any{"keycode": float64(187)}); err != nil || c != 187 {
		t.Fatalf("keycode: %d %v", c, err)
	}
	for _, args := range []map[string]any{{"keycode": float64(26)}, {"keycode": float64(223)}} {
		if _, err := d.androidKeycode(args); errorCode(err) != "forbidden" {
			t.Fatalf("power not blocked: %v", err)
		}
	}
	for _, args := range []map[string]any{{"keycode": 1.5}, {"keycode": float64(-1)}, {"key": "reboot"}, {}} {
		if _, err := d.androidKeycode(args); errorCode(err) != "invalid_argument" {
			t.Fatalf("bad key accepted: %v %v", args, err)
		}
	}
	var cfg runnerConfig
	if err := operatorSettings(&cfg, func(k string) string {
		if k == "blocked_keycodes" {
			return "none"
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&drivers{cfg: cfg}).androidKeycode(map[string]any{"keycode": float64(26)}); err != nil {
		t.Fatalf("none should allow power: %v", err)
	}
	if err := operatorSettings(&cfg, func(k string) string {
		if k == "blocked_keycodes" {
			return "26,abc"
		}
		return ""
	}); err == nil {
		t.Fatal("bad block-list accepted")
	}
}

func TestGesturePoints(t *testing.T) {
	pts, err := gesturePoints(map[string]any{"path": []any{[]any{0.1, 0.2}, map[string]any{"x": 0.5, "y": 0.5}, []any{0.9, 0.8}}})
	if err != nil || len(pts) != 3 || pts[1] != [2]float64{0.5, 0.5} {
		t.Fatalf("path: %v %v", pts, err)
	}
	if pts, err = gesturePoints(map[string]any{"x": 0.1, "y": 0.1, "to_x": 0.2, "to_y": 0.3}); err != nil || len(pts) != 2 {
		t.Fatalf("swipe form: %v %v", pts, err)
	}
	long := make([]any, 51)
	for i := range long {
		long[i] = []any{0.5, 0.5}
	}
	for _, args := range []map[string]any{{"path": []any{[]any{0.1, 0.1}}}, {"path": long}, {"path": []any{[]any{0.1, 1.5}, []any{0.2, 0.2}}}, {}} {
		if _, err := gesturePoints(args); errorCode(err) != "invalid_argument" {
			t.Fatalf("bad path accepted: %v", args)
		}
	}
}

func TestBaselineParseAndCompare(t *testing.T) {
	out := `@time
1795784000
@apps
package:com.example.shop
package:io.appium.uiautomator2.server
@accounts
    Account {name=tester@example.com, type=com.google}
    Account {name=tester@example.com, type=com.google}
@settings
global wifi_on=1
system show_touches=0
@storage
/dev/block/dm-46 110000000 50000000 60000000 46% /data
@downloads
2
@screenshots
0
`
	b := parseBaseline(out)
	if len(b.Apps) != 2 || len(b.Accounts) != 1 || b.Settings["global wifi_on"] != "1" || b.FreeStorageBytes != 60000000*1024 || b.Downloads != 2 || b.PhoneTime != 1795784000 {
		t.Fatalf("baseline: %s", jsonText(b))
	}
	now := parseBaseline(strings.NewReplacer("package:com.example.shop", "package:com.other.app", "show_touches=0", "show_touches=1", "@downloads\n2", "@downloads\n5").Replace(out))
	diff := compareBaseline(b, now)
	kinds := []string{}
	for _, d := range diff {
		kinds = append(kinds, d["kind"].(string))
	}
	if strings.Join(kinds, ",") != "app_missing,app_added,setting_changed,downloads_added" {
		t.Fatalf("diff: %v", diff)
	}
	if len(compareBaseline(b, b)) != 0 {
		t.Fatal("identical baselines differ")
	}
}

func TestDeviceToolNeedsConfirmAndBaseline(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "p")
	if _, err := e.Execute(context.Background(), "p", "mobile_device", map[string]any{"session_id": s.ID, "action": "clean"}); errorCode(err) != "invalid_argument" {
		t.Fatalf("clean without confirm: %v", err)
	}
	if _, err := e.Execute(context.Background(), "p", "mobile_device", map[string]any{"session_id": s.ID, "action": "verify_clean"}); errorCode(err) != "not_found" {
		t.Fatalf("verify without baseline: %v", err)
	}
}

func TestStreamTokensExpireAndEndWithSession(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "p")
	h := e.streams
	h.mu.Lock()
	h.tokens["old"] = &streamToken{sid: s.ID, pid: "p", expires: time.Now().Add(-time.Second)}
	h.tokens["live"] = &streamToken{sid: s.ID, pid: "p", expires: time.Now().Add(time.Minute)}
	h.mu.Unlock()
	h.expireTokens()
	if _, _, err := h.stream("old", s.ID, "p"); errorCode(err) != "forbidden" {
		t.Fatal("expired token accepted")
	}
	if _, _, err := h.stream("live", s.ID, "other"); errorCode(err) != "forbidden" {
		t.Fatal("token accepted for another project")
	}
	h.mu.Lock()
	h.streams[s.ID] = newLiveStream(h, s, streamOptions{MaxSize: defaultMaxSize, BitRateKbps: defaultBitRate})
	h.mu.Unlock()
	if _, _, err := h.stream("live", s.ID, "p"); err != nil {
		t.Fatalf("live token refused: %v", err)
	}
	h.end(s.ID, "session closed")
	if _, _, err := h.stream("live", s.ID, "p"); err == nil {
		t.Fatal("token outlived its session")
	}
	ios := s
	ios.Device.Platform = "ios"
	if _, err := h.issue(ios, map[string]any{}); errorCode(err) != "unsupported" {
		t.Fatalf("ios video: %v", err)
	}
	if _, err := h.issue(s, map[string]any{"max_size": float64(100)}); errorCode(err) != "invalid_argument" {
		t.Fatalf("bad max_size: %v", err)
	}
}

func TestEditableClassAcceptsEditTextSubclasses(t *testing.T) {
	for _, c := range []string{"android.widget.EditText", "android.widget.AutoCompleteTextView", "android.widget.SearchView$SearchAutoComplete", "androidx.appcompat.widget.AppCompatEditText"} {
		if !editableClass(c) {
			t.Fatalf("%s rejected", c)
		}
	}
	for _, c := range []string{"android.widget.TextView", "android.widget.Button", ""} {
		if editableClass(c) {
			t.Fatalf("%s accepted", c)
		}
	}
}

func TestBaselineScriptKeepsDateAndSettings(t *testing.T) {
	d := newDrivers(runnerConfig{})
	var script string
	d.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		script = args[len(args)-1]
		return []byte("@time\n1795784000\n@apps\n"), nil
	}
	b, err := d.captureBaseline(context.Background(), "phone")
	if err != nil || b.PhoneTime != 1795784000 {
		t.Fatalf("capture: %+v %v", b, err)
	}
	if !strings.Contains(script, "date +%s;") || !strings.Contains(script, "settings get global wifi_on") || strings.Contains(script, "{{settings}}") {
		t.Fatalf("script: %s", script)
	}
}

func TestTransportChangeBetweenChecksReconnects(t *testing.T) {
	e, _, set := presenceFixture(t, time.Hour)
	set("device transport_id:7")
	e.health.scan()
	s := openFixture(t, e, "p")
	// The phone left and came back between two checks: still "device", new transport.
	set("device transport_id:8")
	e.health.scan()
	got, _ := e.session("p", s.ID)
	if got.Status != "active" {
		t.Fatalf("after re-attach: %+v", got)
	}
	if len(eventsOf(e, "p", "mobile.device.offline")) != 1 || len(eventsOf(e, "p", "mobile.device.online")) != 1 {
		t.Fatal("re-attach not reported as offline then online")
	}
	e.health.scan() // same transport: nothing new
	if len(eventsOf(e, "p", "mobile.device.online")) != 1 {
		t.Fatal("unchanged transport reported again")
	}
}

func TestStreamRecoversWhenPhoneLinkBreaks(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "p")
	old := restartBackoff
	restartBackoff = []time.Duration{time.Millisecond}
	t.Cleanup(func() { restartBackoff = old })
	scrcpyCache.Lock()
	scrcpyCache.at, scrcpyCache.path, scrcpyCache.ver, scrcpyCache.err = time.Now(), "/fake/scrcpy-server", "5.0.1", nil
	scrcpyCache.Unlock()
	t.Cleanup(func() { scrcpyCache.Lock(); scrcpyCache.at = time.Time{}; scrcpyCache.Unlock() })
	var mu sync.Mutex
	attempts := 0
	e.drivers.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "forward" {
				mu.Lock()
				attempts++
				mu.Unlock()
				return []byte("not-a-port"), nil // the phone is not back yet
			}
		}
		return []byte("List of devices attached\nphone device\n"), nil
	}
	st := newLiveStream(e.streams, s, streamOptions{MaxSize: defaultMaxSize, BitRateKbps: defaultBitRate})
	v := &viewer{out: make(chan wsMessage, viewerQueue), closed: make(chan struct{})}
	st.viewers[v] = true
	st.running, st.gen = true, 1
	st.lost(2) // a stale generation is not a loss
	if !st.running {
		t.Fatal("stale generation stopped the stream")
	}
	st.lost(1)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := attempts
		mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	if attempts < 3 {
		t.Fatalf("restart attempts: %d", attempts)
	}
	mu.Unlock()
	if m := <-v.out; !strings.Contains(string(m.data), "reconnecting") {
		t.Fatalf("viewer not told: %s", m.data)
	}
	// Once the session ends, recovery stops.
	execute(t, e, "p", "mobile_session", map[string]any{"action": "stop", "session_id": s.ID})
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	n := attempts
	mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if attempts > n+1 {
		t.Fatalf("still retrying after the session ended: %d -> %d", n, attempts)
	}
}
