package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/apteva/apps/mcp/instances/internal/monitor"
)

//go:embed collector/checksums.json
var collectorChecksums []byte
var collectorDownloads sync.Mutex
var collectorBinaries = map[string][]byte{}
var monitoringRunSSH = runSSH
var monitoringUploadSSH = uploadSSH
var collectorHTTP = &http.Client{Timeout: 60 * time.Second}

func collectorBinary(ctx context.Context, platform, arch string) ([]byte, error) {
	key := platform + "-" + arch
	collectorDownloads.Lock()
	defer collectorDownloads.Unlock()
	if data := collectorBinaries[key]; data != nil {
		return data, nil
	}
	var sums map[string]string
	if err := json.Unmarshal(collectorChecksums, &sums); err != nil {
		return nil, err
	}
	asset := "instances-collector-" + key
	want := sums[asset]
	if len(want) != 64 {
		return nil, fmt.Errorf("no released collector for %s", key)
	}
	endpoint := "https://github.com/apteva/apps/releases/download/" + url.PathEscape("instances/v"+monitor.CollectorVersion) + "/" + asset
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := collectorHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download collector: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileTransferBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileTransferBytes {
		return nil, errors.New("collector exceeds upload limit")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != want {
		return nil, errors.New("collector SHA-256 verification failed")
	}
	collectorBinaries[key] = data
	return data, nil
}
func collectorServiceScript(platform, home, user string) (string, error) {
	if !strings.HasPrefix(home, "/") || strings.ContainsAny(home+user, "\n\r\x00") {
		return "", errors.New("invalid remote account path")
	}
	dir := home + "/.local/share/apteva-instances-monitor"
	prefix := `set -eu
monitor_dir=` + quoteShellArg(dir) + `
chmod 700 "$monitor_dir"
chmod 700 "$monitor_dir/collector.new"
mv "$monitor_dir/collector.new" "$monitor_dir/collector"
if [ "$(id -u)" = 0 ]; then monitor_sudo=""; else monitor_sudo="sudo -n"; $monitor_sudo true; fi
`
	switch platform {
	case "linux":
		// Root owns the unit; the daemon runs as the SSH account, never with elevated
		// permissions merely to collect metrics. The user-owned binary is intentional.
		unit := fmt.Sprintf("[Unit]\nDescription=Apteva instance monitoring\nAfter=network.target\n[Service]\nType=simple\nUser=%s\nExecStart=%s --data-dir %s\nRestart=always\nRestartSec=5\nNoNewPrivileges=true\nStandardOutput=null\nStandardError=journal\n[Install]\nWantedBy=multi-user.target\n", user, systemdQuote(dir+"/collector"), systemdQuote(dir))
		return prefix + "command -v systemctl >/dev/null || { echo 'systemd is required'; exit 1; }\nprintf '%s' " + quoteShellArg(unit) + " | $monitor_sudo tee /etc/systemd/system/apteva-instances-monitor.service >/dev/null\n$monitor_sudo systemctl daemon-reload\n$monitor_sudo systemctl enable apteva-instances-monitor.service >/dev/null\n$monitor_sudo systemctl restart apteva-instances-monitor.service\n", nil
	case "darwin":
		escape := func(s string) string {
			r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
			return r.Replace(s)
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>com.apteva.instances-monitor</string><key>UserName</key><string>%s</string><key>ProgramArguments</key><array><string>%s/collector</string><string>--data-dir</string><string>%s</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>5</integer><key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string></dict></plist>`, escape(user), escape(dir), escape(dir))
		return prefix + "printf '%s' " + quoteShellArg(plist) + " | $monitor_sudo tee /Library/LaunchDaemons/com.apteva.instances-monitor.plist >/dev/null\n$monitor_sudo chmod 644 /Library/LaunchDaemons/com.apteva.instances-monitor.plist\n$monitor_sudo chown root:wheel /Library/LaunchDaemons/com.apteva.instances-monitor.plist\n$monitor_sudo launchctl bootout system /Library/LaunchDaemons/com.apteva.instances-monitor.plist >/dev/null 2>&1 || true\n$monitor_sudo launchctl bootstrap system /Library/LaunchDaemons/com.apteva.instances-monitor.plist\n", nil
	}
	return "", fmt.Errorf("unsupported collector OS: %s", platform)
}
func systemdQuote(s string) string {
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "\"", "\\\""), "%", "%%") + "\""
}
func ensureRemoteCollector(ctx context.Context, inst *Instance) error {
	output, code, err := monitoringRunSSH(inst, `set -eu
mkdir -p "$HOME/.local/share/apteva-instances-monitor"
chmod 700 "$HOME/.local/share/apteva-instances-monitor"
uname -s
uname -m
printf '%s\n' "$HOME"
id -un
if [ -x "$HOME/.local/share/apteva-instances-monitor/collector" ]; then "$HOME/.local/share/apteva-instances-monitor/collector" --version; fi
`, 15*time.Second)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("collector probe exit %d: %s", code, truncate(output, 500))
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 4 {
		return errors.New("collector probe returned invalid host facts")
	}
	platform := strings.ToLower(lines[0])
	arch := strings.ToLower(lines[1])
	if arch == "aarch64" {
		arch = "arm64"
	}
	if arch == "x86_64" {
		arch = "amd64"
	}
	if platform != "linux" && platform != "darwin" {
		return fmt.Errorf("unsupported monitoring platform: %s", platform)
	}
	home, user := lines[2], lines[3]
	if !strings.HasPrefix(home, "/") || strings.ContainsAny(home+user, "\r\n\x00") {
		return errors.New("invalid monitoring host account")
	}
	dir := home + "/.local/share/apteva-instances-monitor"
	// Always ensure the service is enabled/restarted after reconnect. Re-upload
	// only when version changes; the original install uses an atomic replacement.
	if len(lines) > 4 && lines[4] == monitor.CollectorVersion {
		cmd := `if [ "$(id -u)" = 0 ]; then monitor_sudo=""; else monitor_sudo="sudo -n"; fi
`
		if platform == "linux" {
			cmd += `$monitor_sudo systemctl start apteva-instances-monitor.service`
		} else {
			cmd += `$monitor_sudo launchctl kickstart system/com.apteva.instances-monitor`
		}
		_, code, err = monitoringRunSSH(inst, cmd, 15*time.Second)
		if err != nil {
			return err
		}
		if code == 0 {
			return nil
		}
	}
	data, err := collectorBinary(ctx, platform, arch)
	if err != nil {
		return err
	}
	if _, err := monitoringUploadSSH(inst, dir+"/collector.new", base64.StdEncoding.EncodeToString(data)); err != nil {
		return err
	}
	script, err := collectorServiceScript(platform, home, user)
	if err != nil {
		return err
	}
	output, code, err = monitoringRunSSH(inst, script, 30*time.Second)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("collector install exit %d: %s", code, truncate(output, 500))
	}
	return nil
}
func fetchRemoteBatch(inst *Instance, since int64) (monitor.Batch, error) {
	var batch monitor.Batch
	command := fmt.Sprintf(`"$HOME/.local/share/apteva-instances-monitor/collector" --data-dir "$HOME/.local/share/apteva-instances-monitor" --export --since %d`, since)
	out, code, err := monitoringFetchSSH(inst, command, 10*time.Second)
	if err != nil {
		return batch, err
	}
	if code != 0 {
		return batch, fmt.Errorf("collector export exit %d: %s", code, truncate(out, 300))
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return batch, err
	}
	err = monitor.Decode(data, &batch)
	return batch, err
}
func stopRemoteCollector(inst *Instance) error {
	command := `set -eu
if [ "$(id -u)" = 0 ]; then monitor_sudo=""; else monitor_sudo="sudo -n"; fi
case "$(uname -s)" in
 Linux) if [ -e /etc/systemd/system/apteva-instances-monitor.service ]; then $monitor_sudo systemctl disable --now apteva-instances-monitor.service; fi ;;
 Darwin) if [ -e /Library/LaunchDaemons/com.apteva.instances-monitor.plist ]; then $monitor_sudo launchctl bootout system /Library/LaunchDaemons/com.apteva.instances-monitor.plist; fi ;;
 *) exit 1 ;;
esac`
	output, code, err := monitoringRunSSH(inst, command, 15*time.Second)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("collector stop exit %d: %s", code, truncate(output, 300))
	}
	return nil
}

var monitoringSSHPool = &sshPool{clients: map[int64]*ssh.Client{}}
var monitoringFetchSSH = func(inst *Instance, command string, timeout time.Duration) (string, int, error) {
	client, _, err := monitoringSSHPool.getWithTimeout(inst, timeout)
	if err != nil {
		return "", -1, err
	}
	out, code, err := runSSHOnce(client, command, timeout)
	if err != nil {
		monitoringSSHPool.drop(inst.ID, client)
	}
	return out, code, err
}
