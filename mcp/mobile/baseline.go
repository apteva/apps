package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
}

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
		now, err := e.drivers.captureBaseline(ctx, s.Device.UDID)
		if err != nil {
			return nil, err
		}
		diff := compareBaseline(b, now)
		return map[string]any{"clean": len(diff) == 0, "differences": diff}, nil
	case "clean":
		if !boolean(args, "confirm") {
			return nil, problem("invalid_argument", "clean uninstalls apps and deletes downloads and screenshots; pass confirm=true")
		}
		b, err := e.baseline(s)
		if err != nil {
			return nil, err
		}
		return e.drivers.cleanPhone(ctx, s.Device.UDID, b, args)
	}
	return nil, problem("invalid_argument", "action must be baseline_capture, baseline_get, clean or verify_clean")
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

const baselineScript = `echo @time; date +%s; echo @apps; pm list packages -3; echo @accounts; dumpsys account 2>/dev/null | grep -E 'Account \{name='; echo @settings; {{settings}} echo @storage; df -k /data | tail -n 1; echo @downloads; ls -1 /sdcard/Download 2>/dev/null | wc -l; echo @screenshots; (ls -1 /sdcard/Pictures/Screenshots /sdcard/DCIM/Screenshots 2>/dev/null | grep -v ':$' | grep -v '^$') | wc -l`

func (d *drivers) captureBaseline(ctx context.Context, serial string) (Baseline, error) {
	// Fixed names only: nothing from callers reaches this script.
	var b strings.Builder
	for _, k := range baselineSettings {
		ns, key, _ := strings.Cut(k, " ")
		b.WriteString(`echo "` + k + `=$(settings get ` + ns + ` ` + key + `)"; `)
	}
	script := strings.Replace(baselineScript, "{{settings}}", b.String(), 1)
	out, err := d.adb(ctx, serial, "shell", script)
	if err != nil {
		return Baseline{}, problem("device_unavailable", "could not read the phone's state")
	}
	return parseBaseline(string(out)), nil
}

func parseBaseline(out string) Baseline {
	b := Baseline{CapturedAt: time.Now().UTC().Format(time.RFC3339), Apps: []string{}, Accounts: []string{}, Settings: map[string]string{}}
	section := ""
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
		}
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

func compareBaseline(want, got Baseline) []map[string]any {
	diff := []map[string]any{}
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

// cleanPhone returns the phone toward its baseline. Only third-party apps
// installed after the baseline are uninstalled; system apps are never touched.
func (d *drivers) cleanPhone(ctx context.Context, serial string, b Baseline, args map[string]any) (any, error) {
	now, err := d.captureBaseline(ctx, serial)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, a := range b.Apps {
		known[a] = true
	}
	report := map[string]any{}
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
	// Data of baseline apps is cleared only when asked by name: clearing it
	// signs people out of apps the baseline expects to be set up.
	cleared := []string{}
	if raw, ok := args["clear_data"].([]any); ok {
		for _, v := range raw {
			p, _ := v.(string)
			if !known[p] || !appIDPattern.MatchString(p) {
				return nil, problem("invalid_argument", "clear_data accepts only third-party apps in the baseline")
			}
			if out, err := d.shell(ctx, serial, "pm", "clear", p); err == nil && strings.Contains(string(out), "Success") {
				cleared = append(cleared, p)
			}
		}
	}
	report["data_cleared"] = cleared
	// Clipboard, then downloads and screenshots added since the baseline.
	// Files that were there at capture time are part of the baseline and kept.
	_, _ = d.adb(ctx, serial, "shell", "cmd clipboard clear-primary-clip >/dev/null 2>&1 || service call clipboard 3 >/dev/null 2>&1; true")
	removed := 0
	if b.PhoneTime > 0 {
		for _, dirs := range userFileDirs {
			for _, dir := range dirs {
				find := "find " + shellQuote(dir) + " -mindepth 1 -maxdepth 1 -newermt @" + strconv.FormatInt(b.PhoneTime, 10) + " 2>/dev/null"
				out, _ := d.adb(ctx, serial, "shell", "n=$("+find+" | wc -l); "+find+" -exec rm -rf {} + ; echo $n")
				n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
				removed += n
			}
		}
	} else {
		report["files_note"] = "baseline has no phone time; capture it again to clean downloads and screenshots"
	}
	report["files_removed"] = removed
	// Close apps: force-stop every third-party app, then go home.
	for _, a := range now.Apps {
		if !strings.HasPrefix(a, "io.appium.") {
			_, _ = d.shell(ctx, serial, "am", "force-stop", a)
		}
	}
	_, _ = d.shell(ctx, serial, "input", "keyevent", "3")
	after, err := d.captureBaseline(ctx, serial)
	if err == nil {
		diff := compareBaseline(b, after)
		report["clean"], report["differences"] = len(diff) == 0, diff
	}
	return report, nil
}
