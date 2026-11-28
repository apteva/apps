package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A baseline records a shared phone's known-good state so it can be cleaned
// between users or test runs. No secrets: apps, account names and types, a
// fixed list of settings, and storage use. No factory reset.

type Baseline struct {
	CapturedAt       string            `json:"captured_at"`
	Apps             []string          `json:"apps"`     // third-party packages
	Accounts         []string          `json:"accounts"` // "type: name"
	Settings         map[string]string `json:"settings"`
	FreeStorageBytes int64             `json:"free_storage_bytes"`
	Downloads        int               `json:"downloads"`
	Screenshots      int               `json:"screenshots"`
	PhoneTime        int64             `json:"phone_time,omitempty"` // phone clock at capture; clean removes only newer files

	// Coverage 2 (Mobile 0.12.0) adds the fields below. Older baselines are
	// compared only on what they recorded.
	Coverage              int                 `json:"coverage,omitempty"`
	Lock                  string              `json:"lock,omitempty"` // none, swipe or secure
	WifiNetworks          []string            `json:"wifi_networks,omitempty"`
	Accessibility         []string            `json:"accessibility_services,omitempty"`
	NotificationListeners []string            `json:"notification_listeners,omitempty"`
	DeviceAdmins          []string            `json:"device_admins,omitempty"`
	BluetoothBonded       int                 `json:"bluetooth_bonded"`
	AlwaysOnVPN           string              `json:"always_on_vpn,omitempty"`
	VPNActive             int                 `json:"vpn_active"`
	Users                 int                 `json:"users"`
	Permissions           map[string][]string `json:"permissions,omitempty"` // granted runtime permissions of third-party apps
	// SharedFilesAdded counts files on shared storage (outside Android/) newer
	// than a stored baseline. Set only when comparing against one.
	SharedFilesAdded int `json:"shared_files_added,omitempty"`
}

const baselineCoverage = 2

var (
	componentRe  = regexp.MustCompile(`^[A-Za-z0-9_.]+/[A-Za-z0-9_.$]+$`)
	permissionRe = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)
)

var baselineSettings = []string{
	"global stay_on_while_plugged_in", "global airplane_mode_on", "global wifi_on", "global bluetooth_on",
	"global development_settings_enabled", "global adb_enabled", "global auto_time",
	"system screen_off_timeout", "system show_touches", "system accelerometer_rotation", "system font_scale",
	"secure default_input_method",
}

// Screenshot folders vary by maker; all are emptied by clean.
var userFileDirs = map[string][]string{
	"downloads":   {"/sdcard/Download"},
	"screenshots": {"/sdcard/Pictures/Screenshots", "/sdcard/DCIM/Screenshots"},
}

var accountRe = regexp.MustCompile(`Account \{name=([^,]*), type=([^}]*)\}`)

func (e *engine) deviceTool(ctx context.Context, s Session, args map[string]any) (any, error) {
	if s.Device.Platform != "android" || s.Device.Driver != "adb" {
		return nil, problem("unsupported", "baseline and clean need an Android phone on the ADB driver")
	}
	switch str(args, "action") {
	case "baseline_capture":
		b, err := e.drivers.captureBaseline(ctx, s.Device.UDID)
		if err != nil {
			return nil, err
		}
		_, err = e.db.Exec(`INSERT INTO mobile_baselines(project_id,device_id,captured_at,baseline_json) VALUES (?,?,?,?)
			ON CONFLICT(project_id,device_id) DO UPDATE SET captured_at=excluded.captured_at,baseline_json=excluded.baseline_json`, s.ProjectID, s.DeviceID, time.Now().Unix(), jsonText(b))
		return map[string]any{"baseline": b}, err
	case "baseline_get":
		b, err := e.baseline(s)
		return map[string]any{"baseline": b}, err
	case "verify_clean":
		b, err := e.baseline(s)
		if err != nil {
			return nil, err
		}
		now, err := e.drivers.captureBaselineSince(ctx, s.Device.UDID, b.PhoneTime)
		if err != nil {
			return nil, err
		}
		diff := compareBaseline(b, now)
		return map[string]any{"clean": len(diff) == 0, "differences": diff}, nil
	case "wake":
		return e.drivers.wake(ctx, s.Device.UDID)
	case "clean":
		if !boolean(args, "confirm") {
			return nil, problem("invalid_argument", "clean uninstalls apps, deletes files and resets settings added since the baseline; pass confirm=true")
		}
		b, err := e.baseline(s)
		if err != nil {
			return nil, err
		}
		return e.drivers.cleanPhone(ctx, s.Device.UDID, b, args)
	}
	return nil, problem("invalid_argument", "action must be baseline_capture, baseline_get, clean, verify_clean or wake")
}

func (e *engine) baseline(s Session) (Baseline, error) {
	var raw string
	err := e.db.QueryRow(`SELECT baseline_json FROM mobile_baselines WHERE project_id=? AND device_id=?`, s.ProjectID, s.DeviceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Baseline{}, problem("not_found", "no baseline for this phone; run baseline_capture first")
	}
	if err != nil {
		return Baseline{}, err
	}
	var b Baseline
	err = json.Unmarshal([]byte(raw), &b)
	return b, err
}

const baselineScript = `echo @time; date +%s; echo @apps; pm list packages -3; echo @accounts; dumpsys account 2>/dev/null | grep -E 'Account \{name='; echo @settings; {{settings}} echo @storage; df -k /data | tail -n 1; echo @downloads; ls -1 /sdcard/Download 2>/dev/null | wc -l; echo @screenshots; (ls -1 /sdcard/Pictures/Screenshots /sdcard/DCIM/Screenshots 2>/dev/null | grep -v ':$' | grep -v '^$') | wc -l; ` +
	`echo @wifi; cmd wifi list-networks 2>/dev/null; echo @a11y; settings get secure enabled_accessibility_services; echo @listeners; settings get secure enabled_notification_listeners; ` +
	`echo @admins; dumpsys device_policy 2>/dev/null | sed -n '/Enabled Device Admins/,/^ *$/p'; echo @bt; dumpsys bluetooth_manager 2>/dev/null | grep -m1 'Bonded devices:'; ` +
	`echo @vpn; echo "always_on=$(settings get secure always_on_vpn_app)"; echo "active=$(dumpsys connectivity 2>/dev/null | grep -c 'NetworkAgentInfo{.*Transports: VPN')"; echo @users; pm list users 2>/dev/null | grep -c 'UserInfo{'; ` +
	`echo @perms; for p in $(pm list packages -3 | cut -d: -f2); do echo "#$p"; dumpsys package "$p" 2>/dev/null | sed -n '/runtime permissions:/,/^ *$/p' | grep 'granted=true' | sed -E 's/^ +([^:]+):.*/\1/'; done; ` +
	lockScript

// sharedFilesScript counts files on shared storage newer than a phone time,
// skipping Android/ (app-private data, handled by uninstall and clear_data).
const sharedFilesFind = `find /sdcard/ -path /sdcard/Android -prune -o -type f -newermt @%d -print 2>/dev/null`

func (d *drivers) captureBaseline(ctx context.Context, serial string) (Baseline, error) {
	return d.captureBaselineSince(ctx, serial, 0)
}

// captureBaselineSince also counts shared-storage files newer than since
// (a stored baseline's phone time) when it is set.
func (d *drivers) captureBaselineSince(ctx context.Context, serial string, since int64) (Baseline, error) {
	// Fixed names only: nothing from callers reaches this script.
	var b strings.Builder
	for _, k := range baselineSettings {
		ns, key, _ := strings.Cut(k, " ")
		b.WriteString(`echo "` + k + `=$(settings get ` + ns + ` ` + key + `)"; `)
	}
	script := strings.Replace(baselineScript, "{{settings}}", b.String(), 1)
	if since > 0 {
		script += "; echo @newfiles; " + fmt.Sprintf(sharedFilesFind, since) + " | wc -l"
	}
	out, err := d.adb(ctx, serial, "shell", script)
	if err != nil {
		return Baseline{}, problem("device_unavailable", "could not read the phone's state")
	}
	return parseBaseline(string(out)), nil
}

func parseBaseline(out string) Baseline {
	b := Baseline{CapturedAt: time.Now().UTC().Format(time.RFC3339), Apps: []string{}, Accounts: []string{}, Settings: map[string]string{}, Coverage: baselineCoverage, Permissions: map[string][]string{}}
	b.Lock = parseLockOutput(out).Keyguard
	section, permApp := "", ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if strings.HasPrefix(line, "@") {
			section = line[1:]
			continue
		}
		if line == "" {
			continue
		}
		switch section {
		case "apps":
			if p, ok := strings.CutPrefix(line, "package:"); ok && appIDPattern.MatchString(p) {
				b.Apps = append(b.Apps, p)
			}
		case "accounts":
			if m := accountRe.FindStringSubmatch(line); m != nil {
				b.Accounts = append(b.Accounts, m[2]+": "+m[1])
			}
		case "settings":
			if k, v, ok := strings.Cut(line, "="); ok {
				b.Settings[k] = v
			}
		case "storage":
			f := strings.Fields(line)
			if len(f) >= 4 {
				if kb, err := strconv.ParseInt(f[3], 10, 64); err == nil {
					b.FreeStorageBytes = kb * 1024
				}
			}
		case "downloads":
			b.Downloads, _ = strconv.Atoi(line)
		case "screenshots":
			b.Screenshots, _ = strconv.Atoi(line)
		case "time":
			b.PhoneTime, _ = strconv.ParseInt(line, 10, 64)
		case "wifi":
			if n, ok := parseWifiNetwork(line); ok {
				b.WifiNetworks = append(b.WifiNetworks, n.SSID)
			}
		case "a11y":
			b.Accessibility = append(b.Accessibility, components(line)...)
		case "listeners":
			b.NotificationListeners = append(b.NotificationListeners, components(line)...)
		case "admins":
			// "Enabled Device Admins" lists each admin as "pkg/.Receiver:".
			if c, ok := strings.CutSuffix(line, ":"); ok && componentRe.MatchString(c) {
				b.DeviceAdmins = append(b.DeviceAdmins, c)
			}
		case "bt":
			if v, ok := strings.CutPrefix(line, "Bonded devices:"); ok {
				b.BluetoothBonded, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		case "vpn":
			if v, ok := strings.CutPrefix(line, "always_on="); ok && v != "null" {
				b.AlwaysOnVPN = v
			}
			if v, ok := strings.CutPrefix(line, "active="); ok {
				b.VPNActive, _ = strconv.Atoi(v)
			}
		case "users":
			b.Users, _ = strconv.Atoi(line)
		case "perms":
			if p, ok := strings.CutPrefix(line, "#"); ok {
				permApp = ""
				if appIDPattern.MatchString(p) {
					permApp = p
					b.Permissions[p] = []string{}
				}
			} else if permApp != "" && permissionRe.MatchString(line) {
				b.Permissions[permApp] = append(b.Permissions[permApp], line)
			}
		case "newfiles":
			b.SharedFilesAdded, _ = strconv.Atoi(line)
		}
	}
	for _, list := range []*[]string{&b.WifiNetworks, &b.Accessibility, &b.NotificationListeners, &b.DeviceAdmins} {
		sort.Strings(*list)
		*list = uniqueStrings(*list)
	}
	for p, perms := range b.Permissions {
		sort.Strings(perms)
		b.Permissions[p] = uniqueStrings(perms)
	}
	sort.Strings(b.Apps)
	sort.Strings(b.Accounts)
	b.Accounts = uniqueStrings(b.Accounts)
	return b
}

func uniqueStrings(in []string) []string {
	out := []string{}
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

type wifiNetwork struct{ ID, SSID string }

// parseWifiNetwork reads one `cmd wifi list-networks` row: id, SSID (may
// contain spaces), security type.
func parseWifiNetwork(line string) (wifiNetwork, bool) {
	f := strings.Fields(line)
	if len(f) < 3 {
		return wifiNetwork{}, false
	}
	if _, err := strconv.Atoi(f[0]); err != nil {
		return wifiNetwork{}, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
	ssid := strings.TrimSpace(strings.TrimSuffix(rest, f[len(f)-1]))
	if ssid == "" {
		return wifiNetwork{}, false
	}
	return wifiNetwork{ID: f[0], SSID: ssid}, true
}

// components splits a colon-separated settings list of component names.
func components(line string) []string {
	out := []string{}
	if line == "null" {
		return out
	}
	for _, c := range strings.Split(line, ":") {
		if c = strings.TrimSpace(c); componentRe.MatchString(c) {
			out = append(out, c)
		}
	}
	return out
}

func added(want, got []string) []string {
	known := map[string]bool{}
	for _, v := range want {
		known[v] = true
	}
	out := []string{}
	for _, v := range got {
		if !known[v] {
			out = append(out, v)
		}
	}
	return out
}

func compareBaseline(want, got Baseline) []map[string]any {
	diff := []map[string]any{}
	if got.SharedFilesAdded > 0 {
		diff = append(diff, map[string]any{"kind": "shared_files_added", "count": got.SharedFilesAdded})
	}
	if want.Coverage >= baselineCoverage {
		if want.Lock != "" && got.Lock != "" && want.Lock != got.Lock {
			diff = append(diff, map[string]any{"kind": "lock_changed", "expected": want.Lock, "actual": got.Lock})
		}
		for _, v := range added(want.WifiNetworks, got.WifiNetworks) {
			diff = append(diff, map[string]any{"kind": "wifi_added", "ssid": v})
		}
		for _, v := range added(want.Accessibility, got.Accessibility) {
			diff = append(diff, map[string]any{"kind": "accessibility_added", "component": v})
		}
		for _, v := range added(want.NotificationListeners, got.NotificationListeners) {
			diff = append(diff, map[string]any{"kind": "notification_listener_added", "component": v})
		}
		for _, v := range added(want.DeviceAdmins, got.DeviceAdmins) {
			diff = append(diff, map[string]any{"kind": "device_admin_added", "component": v})
		}
		if got.BluetoothBonded > want.BluetoothBonded {
			diff = append(diff, map[string]any{"kind": "bluetooth_bonds_added", "count": got.BluetoothBonded - want.BluetoothBonded})
		}
		if got.AlwaysOnVPN != want.AlwaysOnVPN {
			diff = append(diff, map[string]any{"kind": "always_on_vpn_changed", "expected": want.AlwaysOnVPN, "actual": got.AlwaysOnVPN})
		}
		if got.VPNActive > want.VPNActive {
			diff = append(diff, map[string]any{"kind": "vpn_active", "count": got.VPNActive - want.VPNActive})
		}
		if got.Users > want.Users {
			diff = append(diff, map[string]any{"kind": "users_added", "count": got.Users - want.Users})
		}
		apps := []string{}
		for p := range want.Permissions {
			apps = append(apps, p)
		}
		sort.Strings(apps)
		for _, p := range apps {
			if now, ok := got.Permissions[p]; ok {
				for _, perm := range added(want.Permissions[p], now) {
					diff = append(diff, map[string]any{"kind": "permission_granted", "app_id": p, "permission": perm})
				}
			}
		}
	}
	have := map[string]bool{}
	for _, a := range got.Apps {
		have[a] = true
	}
	known := map[string]bool{}
	for _, a := range want.Apps {
		known[a] = true
		if !have[a] {
			diff = append(diff, map[string]any{"kind": "app_missing", "app_id": a})
		}
	}
	for _, a := range got.Apps {
		if !known[a] {
			diff = append(diff, map[string]any{"kind": "app_added", "app_id": a})
		}
	}
	wantAcc := map[string]bool{}
	for _, a := range want.Accounts {
		wantAcc[a] = true
	}
	for _, a := range got.Accounts {
		if !wantAcc[a] {
			diff = append(diff, map[string]any{"kind": "account_added", "account": a})
		}
	}
	keys := []string{}
	for k := range want.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if got.Settings[k] != want.Settings[k] {
			diff = append(diff, map[string]any{"kind": "setting_changed", "setting": k, "expected": want.Settings[k], "actual": got.Settings[k]})
		}
	}
	if got.Downloads > want.Downloads {
		diff = append(diff, map[string]any{"kind": "downloads_added", "count": got.Downloads - want.Downloads})
	}
	if got.Screenshots > want.Screenshots {
		diff = append(diff, map[string]any{"kind": "screenshots_added", "count": got.Screenshots - want.Screenshots})
	}
	return diff
}

// cleanPhone returns the phone toward its baseline. Third-party apps added
// since the baseline are uninstalled; system apps are never uninstalled, and
// their data is cleared only when listed in the operator's clean_system_apps
// and named in clear_data. Everything still different afterwards is listed in
// not_undone, so a clean that could not finish never reports clean=true.
func (d *drivers) cleanPhone(ctx context.Context, serial string, b Baseline, args map[string]any) (any, error) {
	now, err := d.captureBaselineSince(ctx, serial, b.PhoneTime)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, a := range b.Apps {
		known[a] = true
	}
	system := map[string]bool{}
	for _, a := range d.cfg.CleanSystemApps {
		system[a] = true
	}
	// Validate clear_data before changing anything.
	clearData := []string{}
	if raw, ok := args["clear_data"].([]any); ok {
		for _, v := range raw {
			p, _ := v.(string)
			if !appIDPattern.MatchString(p) || (!known[p] && !system[p]) {
				return nil, problem("invalid_argument", "clear_data accepts third-party apps in the baseline and the system apps listed in clean_system_apps")
			}
			clearData = append(clearData, p)
		}
	}
	report := map[string]any{}
	coverage := b.Coverage >= baselineCoverage
	run := func(args ...string) bool {
		out, err := d.shell(ctx, serial, args...)
		low := strings.ToLower(string(out))
		return err == nil && !strings.Contains(low, "error") && !strings.Contains(low, "exception") && !strings.Contains(low, "failure")
	}
	if coverage {
		// Device admins first: an active admin app cannot be uninstalled.
		removed := []string{}
		for _, c := range added(b.DeviceAdmins, now.DeviceAdmins) {
			if run("dpm", "remove-active-admin", c) {
				removed = append(removed, c)
			}
		}
		report["device_admins_removed"] = removed
		kept := []string{}
		for _, c := range now.Accessibility {
			if slicesContains(b.Accessibility, c) {
				kept = append(kept, c)
			}
		}
		if len(kept) != len(now.Accessibility) {
			if len(kept) == 0 {
				run("settings", "delete", "secure", "enabled_accessibility_services")
				run("settings", "put", "secure", "accessibility_enabled", "0")
			} else {
				run("settings", "put", "secure", "enabled_accessibility_services", strings.Join(kept, ":"))
			}
		}
		report["accessibility_disabled"] = added(b.Accessibility, now.Accessibility)
		off := []string{}
		for _, c := range added(b.NotificationListeners, now.NotificationListeners) {
			if run("cmd", "notification", "disallow_listener", c) {
				off = append(off, c)
			}
		}
		report["notification_listeners_disabled"] = off
	}
	uninstalled, failed := []string{}, []string{}
	for _, a := range now.Apps {
		if known[a] || strings.HasPrefix(a, "io.appium.") {
			continue
		}
		out, err := d.shell(ctx, serial, "pm", "uninstall", a)
		if err != nil || !strings.Contains(string(out), "Success") {
			failed = append(failed, a)
			continue
		}
		uninstalled = append(uninstalled, a)
	}
	report["uninstalled"], report["uninstall_failed"] = uninstalled, failed
	if coverage {
		// Permissions granted since the baseline to apps it already had.
		revoked := []map[string]any{}
		for p, perms := range now.Permissions {
			want, ok := b.Permissions[p]
			if !ok {
				continue
			}
			for _, perm := range added(want, perms) {
				if run("pm", "revoke", p, perm) {
					revoked = append(revoked, map[string]any{"app_id": p, "permission": perm})
				}
			}
		}
		report["permissions_revoked"] = revoked
	}
	// Data is cleared only when asked by name: clearing it signs people out of
	// apps the baseline expects to be set up.
	cleared := []string{}
	for _, p := range clearData {
		if out, err := d.shell(ctx, serial, "pm", "clear", p); err == nil && strings.Contains(string(out), "Success") {
			cleared = append(cleared, p)
		}
	}
	report["data_cleared"] = cleared
	if coverage {
		forgotten := []string{}
		out, _ := d.shell(ctx, serial, "cmd", "wifi", "list-networks")
		seen := map[string]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			n, ok := parseWifiNetwork(strings.TrimSpace(line))
			if !ok || seen[n.ID] || slicesContains(b.WifiNetworks, n.SSID) {
				continue
			}
			seen[n.ID] = true
			if run("cmd", "wifi", "forget-network", n.ID) {
				forgotten = append(forgotten, n.SSID)
			}
		}
		report["wifi_forgotten"] = forgotten
		if now.AlwaysOnVPN != b.AlwaysOnVPN {
			if b.AlwaysOnVPN == "" {
				run("settings", "delete", "secure", "always_on_vpn_app")
				run("settings", "put", "secure", "always_on_vpn_lockdown", "0")
			} else if appIDPattern.MatchString(b.AlwaysOnVPN) {
				run("settings", "put", "secure", "always_on_vpn_app", b.AlwaysOnVPN)
			}
		}
	}
	// Clipboard, then files added to shared storage since the baseline. Files
	// that were there at capture time are part of the baseline and kept.
	_, _ = d.adb(ctx, serial, "shell", "cmd clipboard clear-primary-clip >/dev/null 2>&1 || service call clipboard 3 >/dev/null 2>&1; true")
	removed := 0
	if b.PhoneTime > 0 {
		t := strconv.FormatInt(b.PhoneTime, 10)
		find := fmt.Sprintf(sharedFilesFind, b.PhoneTime)
		out, _ := d.adb(ctx, serial, "shell", "n=$("+find+" | wc -l); "+find+" | while IFS= read -r f; do rm -f \"$f\"; done; "+
			"find /sdcard/ -mindepth 2 -path /sdcard/Android -prune -o -depth -type d -empty -newermt @"+t+" -print 2>/dev/null | while IFS= read -r f; do rmdir \"$f\" 2>/dev/null; done; echo $n")
		removed, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	} else {
		report["files_note"] = "baseline has no phone time; capture it again to clean files"
	}
	report["files_removed"] = removed
	// Close apps: force-stop every third-party app, then go home.
	for _, a := range now.Apps {
		if !strings.HasPrefix(a, "io.appium.") {
			_, _ = d.shell(ctx, serial, "am", "force-stop", a)
		}
	}
	_, _ = d.shell(ctx, serial, "input", "keyevent", "3")
	after, err := d.captureBaselineSince(ctx, serial, b.PhoneTime)
	if err != nil {
		report["clean"], report["not_undone"], report["verify_error"] = false, []map[string]any{}, "could not read the phone after cleaning"
		return report, nil
	}
	diff := compareBaseline(b, after)
	report["clean"], report["differences"], report["not_undone"] = len(diff) == 0, diff, diff
	return report, nil
}

func slicesContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
