package main

// Pre-flight + on-demand install of ffmpeg/ffprobe on a remote
// instance managed by the `instances` app. Mirrors how the platform
// installer fetches the same johnvansickle static binaries locally
// (see apteva.yaml binaries: block), so a render running on a remote
// host uses the exact same ffmpeg version + flags as one running
// locally.
//
// Properties:
//
//   - No sudo / no package manager. Static binary extracted under
//     $HOME/.apteva-render/ on the remote — works on every glibc
//     Linux the VPS providers serve (Ubuntu / Debian / Alpine).
//   - Single-flight per host_id: concurrent first-renders to the same
//     remote share one install attempt rather than racing the same
//     download.
//   - sha256 verified against the manifest spec; corrupted downloads
//     fail loudly rather than running the wrong binary.
//   - Idempotent: existence + version probe up front. Re-running is
//     a one-RTT no-op.
//   - Cache scope is process-lifetime. Restarting the media sidecar
//     re-runs the existence probe (cheap). We deliberately don't
//     persist this to media's DB — the remote host is the source of
//     truth, and a stale "installed" bit can't outlive a process
//     wipe of the remote anyway.
//
// Versioning: the manifest binaries: block is the source of truth
// for local installs. We re-state the URLs + hashes here because Go
// code doesn't have the parsed manifest at runtime (apteva.yaml is
// embedded as a YAML string in main.go for the platform installer).
// When bumping ffmpeg, update both places — and the install dir name
// changes too (ffmpeg-7.0.x), which auto-invalidates the on-disk
// cache on each remote.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// ─── manifest mirror ───────────────────────────────────────────────

const ffmpegVersion = "7.0.2"

// remoteInstallDir is the per-version directory under $HOME on the
// remote. Bumping ffmpegVersion changes this string, so on-remote
// installs of the new version land in a fresh path and the old one
// stays usable until garbage-collected (deliberately not GC'd today —
// 80 MB per version is cheap; left for a later cleanup pass).
var remoteInstallDir = "$HOME/.apteva-render/ffmpeg-" + ffmpegVersion

// ffmpegSource is the per-arch download spec. Keep in sync with the
// binaries: block in apteva.yaml.
type ffmpegSource struct {
	URL    string
	SHA256 string
}

var ffmpegSources = map[string]ffmpegSource{
	"amd64": {
		URL:    "https://johnvansickle.com/ffmpeg/releases/ffmpeg-7.0.2-amd64-static.tar.xz",
		SHA256: "abda8d77ce8309141f83ab8edf0596834087c52467f6badf376a6a2a4c87cf67",
	},
	"arm64": {
		URL:    "https://johnvansickle.com/ffmpeg/releases/ffmpeg-7.0.2-arm64-static.tar.xz",
		SHA256: "f4149bb2b0784e30e99bdda85471c9b5930d3402014e934a5098b41d0f7201b1",
	},
}

// ─── cache ─────────────────────────────────────────────────────────

// installedPaths is what every render needs back from Ensure: the
// absolute remote paths to the two executables.
type installedPaths struct {
	FFmpeg  string
	FFprobe string
}

// hostInstallState is the per-host single-flight + result cache. The
// mutex serialises concurrent Ensure() calls for the same host; once
// resolved (ready or failed), subsequent callers read the cached
// answer without touching the remote.
type hostInstallState struct {
	mu    sync.Mutex
	ready bool
	paths installedPaths
	err   error
}

// remoteFFmpegInstaller manages the per-host install lifecycle.
// Construct once at executor startup; share across renders.
type remoteFFmpegInstaller struct {
	hosts sync.Map // map[int64]*hostInstallState
}

func newRemoteFFmpegInstaller() *remoteFFmpegInstaller {
	return &remoteFFmpegInstaller{}
}

// sharedRemoteInstaller — process-wide singleton. Workers share the
// install cache so the second worker to claim a render on a
// first-time host doesn't re-run the install probe that the first
// worker just completed.
var (
	sharedInstallerOnce sync.Once
	sharedInstaller     *remoteFFmpegInstaller
)

func sharedRemoteInstaller() *remoteFFmpegInstaller {
	sharedInstallerOnce.Do(func() {
		sharedInstaller = newRemoteFFmpegInstaller()
	})
	return sharedInstaller
}

// Ensure guarantees ffmpeg + ffprobe are present on the remote and
// returns the absolute paths to invoke. First call per host runs the
// arch detect → existence probe → (download+verify+extract on miss)
// sequence; subsequent calls are a map lookup.
//
// On install failure, the error is cached so we don't hammer a host
// that's missing curl, has no disk space, etc. Operators must restart
// the media sidecar to retry after fixing the remote.
func (i *remoteFFmpegInstaller) Ensure(ctx context.Context, app *sdk.AppCtx, hostID int64) (installedPaths, error) {
	v, _ := i.hosts.LoadOrStore(hostID, &hostInstallState{})
	st := v.(*hostInstallState)

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ready {
		return st.paths, st.err
	}

	paths, err := i.install(ctx, app, hostID)
	st.paths = paths
	st.err = err
	st.ready = true
	return paths, err
}

// install runs the full provision flow. Caller holds st.mu.
//
// Preference order:
//
//  1. System ffmpeg already on PATH (apt-installed or pre-baked image).
//     Uses /usr/bin/ffmpeg + /usr/bin/ffprobe. Dynamically linked
//     against the distro's OpenSSL → HTTPS works.
//  2. apt-installed if `apt-get` is available — we're root on default
//     Hetzner Ubuntu/Debian, so this is the common-case install path.
//     Reliable HTTPS support is the whole reason we don't ship our own
//     static binary anymore (v0.11.3): the johnvansickle build SIGSEGV's
//     on HTTPS URLs even though `-protocols` lists it, which breaks
//     the indexer's signed-URL flow on R2/S3/any TLS-served storage.
//  3. Vendored static binary as a last-resort fallback for non-Debian
//     hosts (Alpine, RHEL-based, etc.). Documented to lack HTTPS — works
//     when sources are reachable over plain HTTP. The static path is
//     preserved so the remote render path keeps working on those hosts
//     (it downloads via curl first, so ffmpeg never speaks TLS).
func (i *remoteFFmpegInstaller) install(ctx context.Context, app *sdk.AppCtx, hostID int64) (installedPaths, error) {
	log := app.Logger()

	// Step 1: check for system ffmpeg first. Cheapest win.
	if paths, ok, err := probeSystemFFmpeg(ctx, app, hostID); err != nil {
		return installedPaths{}, fmt.Errorf("probe system ffmpeg: %w", err)
	} else if ok {
		log.Info("remote ffmpeg: using system install",
			"host_id", hostID, "path", paths.FFmpeg)
		return paths, nil
	}

	// Step 2: apt-get install if available.
	if hasApt, err := probeApt(ctx, app, hostID); err != nil {
		return installedPaths{}, fmt.Errorf("probe apt: %w", err)
	} else if hasApt {
		log.Info("remote ffmpeg: installing via apt", "host_id", hostID)
		if err := runAptInstall(ctx, app, hostID); err != nil {
			return installedPaths{}, fmt.Errorf("apt install ffmpeg on host_id=%d: %w", hostID, err)
		}
		// Re-probe — apt landed it at /usr/bin/, but be defensive
		// against distro layout quirks by re-resolving.
		paths, ok, err := probeSystemFFmpeg(ctx, app, hostID)
		if err != nil || !ok {
			return installedPaths{}, fmt.Errorf("apt install reported success but ffmpeg still not on PATH (err=%v, found=%v)", err, ok)
		}
		log.Info("remote ffmpeg: apt install complete", "host_id", hostID, "path", paths.FFmpeg)
		return paths, nil
	}

	// Step 3: static binary fallback (non-Debian distros). HTTPS won't
	// work — indexer's signed-URL probe will fail. Renders work because
	// they curl-download sources first.
	arch, err := detectRemoteArch(ctx, app, hostID)
	if err != nil {
		return installedPaths{}, fmt.Errorf("detect arch: %w", err)
	}
	src, ok := ffmpegSources[arch]
	if !ok {
		return installedPaths{}, fmt.Errorf("no system ffmpeg, no apt, and unsupported arch %q for static fallback", arch)
	}

	ffmpegPath := remoteInstallDir + "/ffmpeg"
	ffprobePath := remoteInstallDir + "/ffprobe"

	already, version, err := probeInstalled(ctx, app, hostID, ffmpegPath)
	if err != nil {
		return installedPaths{}, fmt.Errorf("probe install: %w", err)
	}
	if already {
		log.Info("remote ffmpeg: using vendored static fallback (HTTPS-limited)",
			"host_id", hostID, "arch", arch, "path", ffmpegPath, "version", version)
		return installedPaths{FFmpeg: ffmpegPath, FFprobe: ffprobePath}, nil
	}

	log.Warn("remote ffmpeg: no system install + no apt; downloading static fallback (HTTPS will not work)",
		"host_id", hostID, "arch", arch, "version", ffmpegVersion, "url", src.URL)
	if err := runInstall(ctx, app, hostID, src); err != nil {
		return installedPaths{}, fmt.Errorf("install ffmpeg %s on host_id=%d: %w", ffmpegVersion, hostID, err)
	}
	log.Info("remote ffmpeg static install complete",
		"host_id", hostID, "arch", arch, "path", ffmpegPath)
	return installedPaths{FFmpeg: ffmpegPath, FFprobe: ffprobePath}, nil
}

// probeSystemFFmpeg checks whether ffmpeg + ffprobe are already on
// the host's PATH (apt-installed, pre-baked image, etc.). Returns
// (paths, found, err). A missing binary is the common case and
// returns (zero, false, nil) — not an error.
//
// We require BOTH ffmpeg and ffprobe; an install where only one is
// present is treated as not-installed so the apt path can complete
// it. Resolves the actual absolute path via `command -v` rather than
// hardcoding /usr/bin/ — some distros use /usr/local/bin or have
// alternatives via update-alternatives.
func probeSystemFFmpeg(ctx context.Context, app *sdk.AppCtx, hostID int64) (installedPaths, bool, error) {
	cmd := `FFMPEG=$(command -v ffmpeg 2>/dev/null || true); ` +
		`FFPROBE=$(command -v ffprobe 2>/dev/null || true); ` +
		`if [ -n "$FFMPEG" ] && [ -n "$FFPROBE" ]; then ` +
		`  printf 'FFMPEG=%s\nFFPROBE=%s\n' "$FFMPEG" "$FFPROBE"; ` +
		`else echo MISSING; fi`
	out, exit, err := runRemote(ctx, app, hostID, cmd, 10)
	if err != nil {
		return installedPaths{}, false, err
	}
	if exit != 0 {
		return installedPaths{}, false, fmt.Errorf("system probe exit=%d: %s", exit, truncate(out, 200))
	}
	if strings.Contains(out, "MISSING") {
		return installedPaths{}, false, nil
	}
	var paths installedPaths
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case strings.HasPrefix(line, "FFMPEG="):
			paths.FFmpeg = strings.TrimPrefix(line, "FFMPEG=")
		case strings.HasPrefix(line, "FFPROBE="):
			paths.FFprobe = strings.TrimPrefix(line, "FFPROBE=")
		}
	}
	if paths.FFmpeg == "" || paths.FFprobe == "" {
		return installedPaths{}, false, nil
	}
	return paths, true, nil
}

// probeApt returns true when apt-get is available on the remote.
// Used to decide whether the apt-install branch is viable.
func probeApt(ctx context.Context, app *sdk.AppCtx, hostID int64) (bool, error) {
	out, exit, err := runRemote(ctx, app, hostID,
		`if command -v apt-get >/dev/null 2>&1; then echo HAS_APT; else echo NO_APT; fi`, 10)
	if err != nil {
		return false, err
	}
	if exit != 0 {
		return false, fmt.Errorf("apt probe exit=%d: %s", exit, truncate(out, 200))
	}
	return strings.Contains(out, "HAS_APT"), nil
}

// runAptInstall installs ffmpeg via apt-get on the remote. Runs
// non-interactively (DEBIAN_FRONTEND=noninteractive) so it never
// prompts for confirmation or service restarts. The `apt-get update`
// is necessary on cloud-init-fresh boxes whose package lists may be
// stale; on a recently-updated host it's a cheap no-op.
//
// 4-minute timeout: covers a slow mirror + first-time apt-get update
// + downloading ffmpeg's deps (Ubuntu 22.04 pulls ~80 packages,
// ~150 MB; Ubuntu 24.04 has a slimmer set). Real-world: usually
// under 90 seconds.
func runAptInstall(ctx context.Context, app *sdk.AppCtx, hostID int64) error {
	script := `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends ffmpeg
ffmpeg -version | head -1
ffprobe -version | head -1
`
	out, exit, err := runRemote(ctx, app, hostID, script, 240)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("apt install script exit=%d: %s", exit, truncate(out, 800))
	}
	return nil
}

// ─── remote operations ─────────────────────────────────────────────

// detectRemoteArch runs `uname -m` and normalises to our two
// supported buckets. Unknown machines (riscv, ppc, …) return their
// raw `uname` string so the caller's error message names what was
// seen — easier to diagnose than a generic "unsupported".
func detectRemoteArch(ctx context.Context, app *sdk.AppCtx, hostID int64) (string, error) {
	out, exit, err := runRemote(ctx, app, hostID, "uname -m", 10)
	if err != nil {
		return "", err
	}
	if exit != 0 {
		return "", fmt.Errorf("uname -m exit=%d: %s", exit, truncate(out, 200))
	}
	m := strings.TrimSpace(out)
	switch m {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return m, nil // caller resolves against ffmpegSources and errors with the raw name
}

// probeInstalled returns (true, versionLine, nil) when the binary
// exists and `-version` succeeds. A missing binary is the common
// case and returns (false, "", nil) — not an error.
func probeInstalled(ctx context.Context, app *sdk.AppCtx, hostID int64, ffmpegPath string) (bool, string, error) {
	// `-x` test + version probe in one round-trip. Output is the
	// version line OR "MISSING" (no binary); exit_code stays 0 in
	// both branches so we don't need to distinguish ENOENT from a
	// real run-failure here.
	cmd := fmt.Sprintf(`if [ -x %q ]; then %q -version 2>/dev/null | head -1; else echo MISSING; fi`,
		ffmpegPath, ffmpegPath)
	out, exit, err := runRemote(ctx, app, hostID, cmd, 15)
	if err != nil {
		return false, "", err
	}
	if exit != 0 {
		return false, "", fmt.Errorf("existence probe exit=%d: %s", exit, truncate(out, 200))
	}
	first := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	if first == "MISSING" || first == "" {
		return false, "", nil
	}
	return true, first, nil
}

// runInstall is the actual download → verify → extract script. Runs
// under a longer timeout because the download (~25 MB compressed) +
// extract (~80 MB on disk) takes real wall-clock on a small VPS.
//
// `set -euo pipefail` so any step's failure aborts the script with a
// non-zero exit — the caller surfaces that with the captured output.
// Working dir is the install parent so curl + extract land where we
// want without needing absolute paths.
//
// We extract with `--strip-components=1` because the upstream tarball
// has a top-level directory (`ffmpeg-7.0.2-amd64-static/`) that we
// don't want in our path.
func runInstall(ctx context.Context, app *sdk.AppCtx, hostID int64, src ffmpegSource) error {
	script := fmt.Sprintf(`set -euo pipefail
mkdir -p %[1]s
cd %[1]s
# Free-space guard: ~250 MB headroom covers compressed + extracted
# + a margin. df reports 1K blocks, so 250000 = ~250 MB.
FREE=$(df -P . | tail -1 | awk '{print $4}')
if [ "$FREE" -lt 250000 ]; then
  echo "insufficient disk: $FREE KB free, need ~250000 KB"
  exit 1
fi
curl -sS -L --fail -o ffmpeg.tar.xz %[2]q
echo "%[3]s  ffmpeg.tar.xz" | sha256sum -c -
tar -xJf ffmpeg.tar.xz --strip-components=1
rm -f ffmpeg.tar.xz
test -x ./ffmpeg
test -x ./ffprobe
./ffmpeg -version | head -1
`, remoteInstallDir, src.URL, src.SHA256)

	// 5 min cap. Real-world: ~10s download + ~5s extract on a
	// reasonable VPS. The cap mostly defends against curl hangs.
	out, exit, err := runRemote(ctx, app, hostID, script, 300)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("install script exit=%d: %s", exit, truncate(out, 1000))
	}
	return nil
}

// ─── helpers ───────────────────────────────────────────────────────

// runRemote is a thin wrapper around instances.instance_run_command
// that surfaces the platform's two failure modes — process couldn't
// start (Err) vs. ran with non-zero exit — separately. Matches the
// pattern in apps/mcp/vpn/orchestrator.go::hostExec.Run.
func runRemote(ctx context.Context, app *sdk.AppCtx, hostID int64, cmd string, timeoutS int) (string, int, error) {
	if app.PlatformAPI() == nil {
		return "", 0, errors.New("platform API unavailable")
	}
	var resp struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		Err      string `json:"error"`
	}
	err := app.PlatformAPI().CallAppResult("instances", "instance_run_command", map[string]any{
		"id":        hostID,
		"cmd":       cmd,
		"timeout_s": timeoutS,
	}, &resp)
	if err != nil {
		return "", 0, fmt.Errorf("instance_run_command host_id=%d: %w", hostID, err)
	}
	if resp.Err != "" {
		return resp.Output, resp.ExitCode, errors.New(resp.Err)
	}
	return resp.Output, resp.ExitCode, nil
}
