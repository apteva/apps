package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	hostedRuntimeCacheTTL = 5 * time.Minute
	hostedTunnelProbePort = 9
)

const hostedRuntimeBootstrapScript = `set -eu
required_ready() {
  for cmd in node npm python3 tar gzip curl setsid base64 df tail dirname mktemp mv grep; do
    command -v "$cmd" >/dev/null 2>&1 || return 1
  done
  major=$(node -p 'Number(process.versions.node.split(".")[0])' 2>/dev/null || echo 0)
  [ "$major" -ge 18 ]
}

if ! required_ready; then
  if [ "$(id -u)" -eq 0 ]; then
    SUDO=""
  elif command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
  else
    echo "root or sudo is required to prepare this Fleet host" >&2
    exit 126
  fi

  if command -v apt-get >/dev/null 2>&1; then
    $SUDO apt-get update -qq
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl python3 tar gzip util-linux coreutils grep bash
    if ! command -v node >/dev/null 2>&1 || ! command -v npm >/dev/null 2>&1 || [ "$(node -p 'Number(process.versions.node.split(".")[0])' 2>/dev/null || echo 0)" -lt 18 ]; then
      setup=$(mktemp /tmp/fleet-nodesource-XXXXXX.sh)
      trap 'rm -f "$setup"' EXIT
      curl -fsSL --retry 3 --connect-timeout 15 https://deb.nodesource.com/setup_22.x -o "$setup"
      $SUDO bash "$setup"
      $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nodejs
      rm -f "$setup"
      trap - EXIT
    fi
  elif command -v apk >/dev/null 2>&1; then
    $SUDO apk add --no-cache nodejs npm python3 tar gzip curl ca-certificates util-linux coreutils grep
  elif command -v dnf >/dev/null 2>&1; then
    $SUDO dnf install -y nodejs npm python3 tar gzip curl ca-certificates util-linux coreutils grep
  elif command -v yum >/dev/null 2>&1; then
    $SUDO yum install -y nodejs npm python3 tar gzip curl ca-certificates util-linux coreutils grep
  else
    echo "unsupported package manager; Fleet supports apt, apk, dnf, and yum" >&2
    exit 127
  fi
fi

required_ready || { echo "Fleet runtime preparation did not provide Node >=18, npm, Python 3, and required transfer/process utilities" >&2; exit 127; }
mkdir -p /var/lib/apteva-fleet
disk_line=$(df -Pk /var/lib/apteva-fleet | tail -n 1)
set -- $disk_line
available_kb=${4:-}
case "$available_kb" in ''|*[!0-9]*) echo "could not determine available disk" >&2; exit 1;; esac
[ "$available_kb" -ge 2097152 ] || { echo "Fleet host requires at least 2 GiB free under /var/lib" >&2; exit 1; }
printf 'FLEET_RUNTIME_READY node=%s npm=%s available_kb=%s\n' "$(node --version)" "$(npm --version)" "$available_kb"
`

func (a *App) ensureHostedRuntime(ctx *sdk.AppCtx, instanceID int64) error {
	if instanceID <= 0 {
		return errors.New("hosted runtime requires instance_id")
	}
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	if checked := a.runtimeReady[instanceID]; !checked.IsZero() && time.Since(checked) < hostedRuntimeCacheTTL {
		return nil
	}

	out, code, err := instanceRunCommand(ctx, instanceID, hostedRuntimeBootstrapScript, 600)
	if err != nil || code != 0 {
		if err == nil {
			err = fmt.Errorf("command exited with status %d", code)
		}
		return fmt.Errorf("prepare hosted runtime on instance %d: %w (exit %d): %s", instanceID, err, code, strings.TrimSpace(out))
	}
	if !strings.Contains(out, "FLEET_RUNTIME_READY ") {
		return fmt.Errorf("prepare hosted runtime on instance %d returned no readiness marker", instanceID)
	}
	if err := verifyHostedFileRoundTrip(ctx, instanceID); err != nil {
		return fmt.Errorf("verify instance file streaming: %w", err)
	}
	if err := verifyHostedTunnelLifecycle(ctx, instanceID); err != nil {
		return fmt.Errorf("verify instance tunnel: %w", err)
	}
	a.runtimeReady[instanceID] = time.Now()
	return nil
}

func verifyHostedFileRoundTrip(ctx *sdk.AppCtx, instanceID int64) error {
	token, err := randomHex(24)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/tmp/apteva-fleet-preflight-%d-%s", instanceID, token[:12])
	payload := []byte("apteva-fleet-preflight:" + token)
	defer instanceRunCommand(ctx, instanceID, fmt.Sprintf("rm -f %s", sh(path)), 10)
	var uploaded struct {
		BytesWritten int `json:"bytes_written"`
	}
	if err := callSiblingTool(ctx, "instances", "", "instance_upload_file", map[string]any{
		"id": instanceID, "path": path, "content_b64": base64.StdEncoding.EncodeToString(payload),
	}, &uploaded); err != nil {
		return err
	}
	if uploaded.BytesWritten != len(payload) {
		return fmt.Errorf("uploaded %d bytes, expected %d", uploaded.BytesWritten, len(payload))
	}
	var downloaded struct {
		ContentB64 string `json:"content_b64"`
		Bytes      int    `json:"bytes"`
	}
	if err := callSiblingTool(ctx, "instances", "", "instance_download_file", map[string]any{
		"id": instanceID, "path": path,
	}, &downloaded); err != nil {
		return err
	}
	got, err := base64.StdEncoding.DecodeString(downloaded.ContentB64)
	if err != nil {
		return fmt.Errorf("decode downloaded file: %w", err)
	}
	if downloaded.Bytes != len(payload) || !bytes.Equal(got, payload) {
		return errors.New("downloaded file did not match uploaded content")
	}
	return nil
}

func verifyHostedTunnelLifecycle(ctx *sdk.AppCtx, instanceID int64) error {
	var opened struct {
		LocalHost string `json:"local_host"`
		LocalPort int    `json:"local_port"`
	}
	if err := callSiblingTool(ctx, "instances", "", "instance_open_tunnel", map[string]any{
		"id": instanceID, "target_port": hostedTunnelProbePort,
	}, &opened); err != nil {
		return err
	}
	closedOK := false
	defer func() {
		if !closedOK {
			_ = callSiblingTool(ctx, "instances", "", "instance_close_tunnel", map[string]any{
				"id": instanceID, "target_port": hostedTunnelProbePort,
			}, nil)
		}
	}()
	if opened.LocalPort <= 0 || (opened.LocalHost != "" && opened.LocalHost != "127.0.0.1" && opened.LocalHost != "localhost") {
		return errors.New("instances returned an invalid tunnel endpoint")
	}
	var closed struct {
		Closed bool `json:"closed"`
	}
	if err := callSiblingTool(ctx, "instances", "", "instance_close_tunnel", map[string]any{
		"id": instanceID, "target_port": hostedTunnelProbePort,
	}, &closed); err != nil {
		return err
	}
	if !closed.Closed {
		return errors.New("instances did not close the preflight tunnel")
	}
	closedOK = true
	return nil
}
