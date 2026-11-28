package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestLockStateParse(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"@lock\n    CredentialType: PIN\ndisabled=false\n    isKeyguardShowing=true\n  mWakefulness=Asleep\n", "secure"},
		{"@lock\n    CredentialType: PATTERN\ndisabled=false\n", "secure"},
		{"@lock\n    CredentialType: NONE\ndisabled=false\n    isKeyguardShowing=true\n", "swipe"},
		{"@lock\n    CredentialType: NONE\ndisabled=true\n", "none"},
		{"@lock\ndisabled=false\n", ""},
	} {
		if got := parseLockOutput(c.in).Keyguard; got != c.want {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
	st := parseLockOutput("@lock\n    CredentialType: PIN\n    isKeyguardShowing=true\n  mWakefulness=Asleep\n")
	if st.Showing == nil || !*st.Showing || st.ScreenOn == nil || *st.ScreenOn {
		t.Fatalf("showing/screen: %+v", st)
	}
	h := parseHealth("@screen\n  mWakefulness=Awake\n@lock\n    CredentialType: PIN\n    isKeyguardShowing=false\n")
	if h.Keyguard != "secure" || h.Locked == nil || *h.Locked {
		t.Fatalf("health: %+v", h)
	}
}

const v2Phone = `@time
1795784000
@apps
package:com.example.shop
@settings
global wifi_on=1
@wifi
Network Id      SSID                         Security type
0               Home Net                     wpa2-psk
0               Home Net                     wpa3-sae
@a11y
null
@listeners
com.sec.android.app.launcher/com.android.launcher3.notification.NotificationListener
@admins
  Enabled Device Admins (User 0, provisioningState: 0):
    com.samsung.android.kgclient/.agent.KGDeviceAdminReceiver:
      uid=10083
      testOnlyAdmin=false

@bt
  Bonded devices: 0
@vpn
always_on=null
active=0
@users
1
@perms
#com.example.shop
android.permission.POST_NOTIFICATIONS
@lock
    CredentialType: NONE
disabled=false
`

func TestBaselineCoverage2ParseAndCompare(t *testing.T) {
	b := parseBaseline(v2Phone)
	if b.Coverage != 2 || b.Lock != "swipe" || strings.Join(b.WifiNetworks, ",") != "Home Net" || len(b.Accessibility) != 0 ||
		len(b.NotificationListeners) != 1 || strings.Join(b.DeviceAdmins, ",") != "com.samsung.android.kgclient/.agent.KGDeviceAdminReceiver" ||
		b.Users != 1 || strings.Join(b.Permissions["com.example.shop"], ",") != "android.permission.POST_NOTIFICATIONS" {
		t.Fatalf("baseline: %s", jsonText(b))
	}
	if len(compareBaseline(b, b)) != 0 {
		t.Fatal("identical baselines differ")
	}
	renter := strings.NewReplacer(
		"0               Home Net                     wpa3-sae", "0               Home Net                     wpa3-sae\n3               Cafe Free WiFi               open",
		"@a11y\nnull", "@a11y\ncom.spy.app/.Watcher",
		"NotificationListener\n", "NotificationListener:com.spy.app/.Listener\n",
		"testOnlyAdmin=false\n", "testOnlyAdmin=false\n    com.spy.app/.Admin:\n",
		"Bonded devices: 0", "Bonded devices: 2",
		"always_on=null", "always_on=com.spy.vpn",
		"active=0", "active=1",
		"@users\n1", "@users\n2",
		"android.permission.POST_NOTIFICATIONS\n", "android.permission.POST_NOTIFICATIONS\nandroid.permission.CAMERA\n",
		"CredentialType: NONE", "CredentialType: PIN",
	).Replace(v2Phone) + "@newfiles\n3\n"
	kinds := []string{}
	for _, d := range compareBaseline(b, parseBaseline(renter)) {
		kinds = append(kinds, d["kind"].(string))
	}
	want := "accessibility_added,always_on_vpn_changed,bluetooth_bonds_added,device_admin_added,lock_changed,notification_listener_added,permission_granted,shared_files_added,users_added,vpn_active,wifi_added"
	sort.Strings(kinds)
	if strings.Join(kinds, ",") != want {
		t.Fatalf("kinds: %v", kinds)
	}
	// A baseline captured before 0.12.0 is compared only on what it recorded.
	old := b
	old.Coverage = 0
	for _, d := range compareBaseline(old, parseBaseline(renter)) {
		if d["kind"] != "shared_files_added" {
			t.Fatalf("old baseline flagged %v", d)
		}
	}
}

// fakePhone answers the baseline script from mutable state and records the
// commands clean sends.
type fakePhone struct {
	state    string
	commands []string
}

func (f *fakePhone) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	cmd := args[len(args)-1]
	switch {
	case strings.Contains(cmd, "echo @time"):
		return []byte(f.state), nil
	case strings.HasPrefix(cmd, "'cmd' 'wifi' 'list-networks'"):
		_, rest, _ := strings.Cut(f.state, "@wifi\n")
		block, _, _ := strings.Cut(rest, "@")
		return []byte(block), nil
	}
	f.commands = append(f.commands, cmd)
	undo := map[string][2]string{
		"'dpm' 'remove-active-admin' 'com.spy.app/.Admin'":                 {"    com.spy.app/.Admin:\n", ""},
		"'settings' 'delete' 'secure' 'enabled_accessibility_services'":    {"@a11y\ncom.spy.app/.Watcher", "@a11y\nnull"},
		"'cmd' 'notification' 'disallow_listener' 'com.spy.app/.Listener'": {":com.spy.app/.Listener", ""},
		"'pm' 'revoke' 'com.example.shop' 'android.permission.CAMERA'":     {"android.permission.CAMERA\n", ""},
		"'cmd' 'wifi' 'forget-network' '3'":                                {"3               Cafe Free WiFi               open\n", ""},
		"'settings' 'delete' 'secure' 'always_on_vpn_app'":                 {"always_on=com.spy.vpn", "always_on=null"},
		"'pm' 'uninstall' 'com.spy.app'":                                   {"package:com.spy.app\n", ""},
	}
	if u, ok := undo[cmd]; ok {
		f.state = strings.Replace(f.state, u[0], u[1], 1)
		if strings.Contains(cmd, "uninstall") {
			return []byte("Success"), nil
		}
	}
	if strings.Contains(cmd, "find /sdcard/") {
		f.state = strings.Replace(f.state, "@newfiles\n3\n", "@newfiles\n0\n", 1)
		return []byte("3\n"), nil
	}
	return []byte(""), nil
}

func TestCleanUndoesAddedStateAndReportsTheRest(t *testing.T) {
	b := parseBaseline(v2Phone)
	phone := &fakePhone{state: strings.NewReplacer(
		"package:com.example.shop\n", "package:com.example.shop\npackage:com.spy.app\n",
		"0               Home Net                     wpa3-sae", "0               Home Net                     wpa3-sae\n3               Cafe Free WiFi               open",
		"@a11y\nnull", "@a11y\ncom.spy.app/.Watcher",
		"NotificationListener\n", "NotificationListener:com.spy.app/.Listener\n",
		"testOnlyAdmin=false\n", "testOnlyAdmin=false\n    com.spy.app/.Admin:\n",
		"Bonded devices: 0", "Bonded devices: 1",
		"always_on=null", "always_on=com.spy.vpn",
		"android.permission.POST_NOTIFICATIONS\n", "android.permission.POST_NOTIFICATIONS\nandroid.permission.CAMERA\n",
		"CredentialType: NONE", "CredentialType: PIN",
	).Replace(v2Phone) + "@newfiles\n3\n"}
	d := newDrivers(runnerConfig{})
	d.command = phone.run
	out, err := d.cleanPhone(context.Background(), "phone", b, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	report := out.(map[string]any)
	if report["clean"] != false {
		t.Fatal("clean reported success with a PIN and a pairing left")
	}
	left := []string{}
	for _, d := range report["not_undone"].([]map[string]any) {
		left = append(left, d["kind"].(string))
	}
	sort.Strings(left)
	if strings.Join(left, ",") != "bluetooth_bonds_added,lock_changed" {
		t.Fatalf("not undone: %v\ncommands: %v", left, phone.commands)
	}
	if report["files_removed"] != 3 || len(report["wifi_forgotten"].([]string)) != 1 {
		t.Fatalf("report: %v", report)
	}
	// The admin is removed before its app is uninstalled.
	admin, uninstall := -1, -1
	for i, c := range phone.commands {
		if strings.Contains(c, "remove-active-admin") {
			admin = i
		}
		if strings.Contains(c, "'pm' 'uninstall'") {
			uninstall = i
		}
	}
	if admin < 0 || uninstall < 0 || admin > uninstall {
		t.Fatalf("order: %v", phone.commands)
	}
	// Files outside Android/ only; the baseline's phone time bounds them.
	joined := strings.Join(phone.commands, "\n")
	if !strings.Contains(joined, "-path /sdcard/Android -prune") || !strings.Contains(joined, "-newermt @1795784000") {
		t.Fatalf("file clean: %s", joined)
	}
}

func TestCleanClearDataAllowList(t *testing.T) {
	b := parseBaseline(v2Phone)
	for _, c := range []struct {
		app    string
		system []string
		ok     bool
	}{
		{"com.example.shop", nil, true},
		{"com.android.chrome", nil, false},
		{"com.android.chrome", []string{"com.android.chrome"}, true},
		{"bad name;", nil, false},
	} {
		phone := &fakePhone{state: v2Phone}
		d := newDrivers(runnerConfig{CleanSystemApps: c.system})
		d.command = phone.run
		_, err := d.cleanPhone(context.Background(), "phone", b, map[string]any{"clear_data": []any{c.app}})
		if (err == nil) != c.ok {
			t.Fatalf("%s: %v", c.app, err)
		}
		if !c.ok && len(phone.commands) != 0 {
			t.Fatalf("%s: changed the phone before refusing: %v", c.app, phone.commands)
		}
	}
	rc := runnerConfig{}
	if err := operatorSettings(&rc, func(k string) string {
		if k == "clean_system_apps" {
			return "com.android.chrome, com.samsung.android.messaging"
		}
		return ""
	}); err != nil || len(rc.CleanSystemApps) != 2 {
		t.Fatalf("setting: %v %v", rc.CleanSystemApps, err)
	}
	if err := operatorSettings(&rc, func(k string) string {
		if k == "clean_system_apps" {
			return "com.ok, x; rm -rf /"
		}
		return ""
	}); err == nil {
		t.Fatal("bad clean_system_apps accepted")
	}
}

func TestWakeNeverPastsASecureLock(t *testing.T) {
	for _, c := range []struct {
		name, lock string
		ok         bool
	}{
		{"secure and showing", "    CredentialType: PIN\ndisabled=false\n    isKeyguardShowing=true\n  mWakefulness=Asleep\n", false},
		{"swipe", "    CredentialType: NONE\ndisabled=false\n    isKeyguardShowing=true\n  mWakefulness=Asleep\n", true},
		{"secure but already unlocked", "    CredentialType: PIN\ndisabled=false\n    isKeyguardShowing=false\n  mWakefulness=Asleep\n", true},
		{"unknown", "disabled=false\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			lock := c.lock
			sent := []string{}
			d := newDrivers(runnerConfig{})
			d.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				cmd := args[len(args)-1]
				if strings.Contains(cmd, "echo @lock") {
					return []byte("@lock\n" + lock), nil
				}
				sent = append(sent, cmd)
				if strings.Contains(cmd, "'224'") {
					lock = strings.Replace(lock, "Asleep", "Awake", 1)
				}
				if strings.Contains(cmd, "dismiss-keyguard") {
					lock = strings.Replace(lock, "isKeyguardShowing=true", "isKeyguardShowing=false", 1)
				}
				return nil, nil
			}
			_, err := d.wake(context.Background(), "phone")
			if (err == nil) != c.ok {
				t.Fatalf("err=%v sent=%v", err, sent)
			}
			for _, s := range sent {
				if strings.Contains(s, "'text'") || (strings.Contains(c.lock, "PIN") && strings.Contains(s, "dismiss-keyguard")) {
					t.Fatalf("wake typed or dismissed on a secure lock: %v", sent)
				}
			}
			if c.name == "secure and showing" && errorCode(err) != "device_locked" {
				t.Fatalf("code %v", err)
			}
		})
	}
}

func TestStreamLinkOwner(t *testing.T) {
	e, _ := fixture(t)
	s := openFixture(t, e, "p")
	h := e.streams
	h.mu.Lock()
	h.tokens["tok"] = &streamToken{sid: s.ID, pid: "p", owner: "user:42", expires: time.Now().Add(time.Minute)}
	h.streams[s.ID] = newLiveStream(h, s, streamOptions{MaxSize: 1280, BitRateKbps: 4000})
	h.mu.Unlock()
	if _, err := h.issue(s, map[string]any{"token": "tok", "owner": "user:43"}); errorCode(err) != "forbidden" {
		t.Fatalf("renewal changed owner: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.serveViewer(w, r, "p", s.ID, "tok", viewerIdentity(r))
	}))
	defer srv.Close()
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(ws, http.Header{"X-Apteva-Operator-ID": {"7"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != closeNotOwner {
		t.Fatalf("wrong viewer not refused with 4003: %v", err)
	}
	conn.Close()
	if _, owner, err := h.stream("tok", s.ID, "p"); err != nil || owner != "user:42" {
		t.Fatalf("owner %q %v", owner, err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Apteva-Operator-ID", "7")
	if viewerIdentity(r) != "operator:7" {
		t.Fatal("operator identity")
	}
	r.Header.Set("X-Apteva-Trusted-Principal", "forged")
	r.Header.Set("X-Apteva-Trusted-Principal-Signature", "00")
	if viewerIdentity(r) != "operator:7" {
		t.Fatal("unsigned principal trusted")
	}
}
