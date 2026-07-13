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
go_ready() {
  command -v go >/dev/null 2>&1 || return 1
  go_version=$(go env GOVERSION 2>/dev/null || true)
  go_minor=$(printf '%s\n' "$go_version" | sed -n 's/^go[0-9][0-9]*\.\([0-9][0-9]*\).*/\1/p')
  case "$go_minor" in ''|*[!0-9]*) return 1;; esac
  [ "$go_minor" -ge 22 ]
}

required_ready() {
  for cmd in node npm python3 tar gzip curl git setsid base64 df tail dirname mktemp mv grep sed; do
    command -v "$cmd" >/dev/null 2>&1 || return 1
  done
  major=$(node -p 'Number(process.versions.node.split(".")[0])' 2>/dev/null || echo 0)
  [ "$major" -ge 18 ] && go_ready
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
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl git python3 tar gzip util-linux coreutils grep sed bash
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq golang-go || true
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
    $SUDO apk add --no-cache nodejs npm python3 tar gzip curl git ca-certificates util-linux coreutils grep sed
    $SUDO apk add --no-cache go || true
  elif command -v dnf >/dev/null 2>&1; then
    $SUDO dnf install -y nodejs npm python3 tar gzip curl git ca-certificates util-linux coreutils grep sed
    $SUDO dnf install -y golang || true
  elif command -v yum >/dev/null 2>&1; then
    $SUDO yum install -y nodejs npm python3 tar gzip curl git ca-certificates util-linux coreutils grep sed
    $SUDO yum install -y golang || true
  else
    echo "unsupported package manager; Fleet supports apt, apk, dnf, and yum" >&2
    exit 127
  fi

  if ! go_ready; then
    arch=$(uname -m)
    case "$arch" in
      x86_64|amd64) go_arch=amd64;;
      aarch64|arm64) go_arch=arm64;;
      *) echo "unsupported architecture for Go runtime: $arch" >&2; exit 127;;
    esac
    metadata=$(mktemp /tmp/fleet-go-metadata-XXXXXX.json)
    archive=$(mktemp /tmp/fleet-go-XXXXXX.tar.gz)
    trap 'rm -f "$metadata" "$archive"' EXIT
    curl -fsSL --retry 3 --connect-timeout 15 'https://go.dev/dl/?mode=json' -o "$metadata"
    go_fields=$(python3 - "$metadata" "$go_arch" <<'PY'
import json, sys
releases = json.load(open(sys.argv[1]))
arch = sys.argv[2]
for release in releases:
    if not release.get("stable"):
        continue
    for item in release.get("files", []):
        if item.get("os") == "linux" and item.get("arch") == arch and item.get("kind") == "archive":
            print(release["version"], item["filename"], item["sha256"])
            raise SystemExit(0)
raise SystemExit("go.dev returned no stable Linux archive")
PY
)
    set -- $go_fields
    go_release=${1:-}
    go_file=${2:-}
    go_sha=${3:-}
    [ -n "$go_release" ] && [ -n "$go_file" ] && [ -n "$go_sha" ] || { echo "invalid Go release metadata" >&2; exit 1; }
    curl -fsSL --retry 3 --connect-timeout 15 "https://go.dev/dl/$go_file" -o "$archive"
    printf '%s  %s\n' "$go_sha" "$archive" | sha256sum -c - >/dev/null
    $SUDO rm -rf /usr/local/go
    $SUDO tar -C /usr/local -xzf "$archive"
    $SUDO ln -sf /usr/local/go/bin/go /usr/local/bin/go
    $SUDO ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
    rm -f "$metadata" "$archive"
    trap - EXIT
  fi
fi

required_ready || { echo "Fleet runtime preparation did not provide Node >=18, npm, Go >=1.22, Python 3, Git, and required transfer/process utilities" >&2; exit 127; }
mkdir -p /var/lib/apteva-fleet
disk_line=$(df -Pk /var/lib/apteva-fleet | tail -n 1)
set -- $disk_line
available_kb=${4:-}
case "$available_kb" in ''|*[!0-9]*) echo "could not determine available disk" >&2; exit 1;; esac
[ "$available_kb" -ge 2097152 ] || { echo "Fleet host requires at least 2 GiB free under /var/lib" >&2; exit 1; }
printf 'FLEET_RUNTIME_READY node=%s npm=%s go=%s available_kb=%s\n' "$(node --version)" "$(npm --version)" "$(go env GOVERSION)" "$available_kb"
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
