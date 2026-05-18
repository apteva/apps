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
func (i *remoteFFmpegInstaller) install(ctx context.Context, app *sdk.AppCtx, hostID int64) (installedPaths, error) {
	log := app.Logger()

	arch, err := detectRemoteArch(ctx, app, hostID)
	if err != nil {
		return installedPaths{}, fmt.Errorf("detect arch: %w", err)
	}
	src, ok := ffmpegSources[arch]
	if !ok {
		return installedPaths{}, fmt.Errorf("unsupported remote arch %q (supported: amd64, arm64)", arch)
	}

	ffmpegPath := remoteInstallDir + "/ffmpeg"
	ffprobePath := remoteInstallDir + "/ffprobe"

	// Existence probe — cheap one-RTT skip when the binary's already
	// where we expect it.
	already, version, err := probeInstalled(ctx, app, hostID, ffmpegPath)
	if err != nil {
		return installedPaths{}, fmt.Errorf("probe install: %w", err)
	}
	if already {
		log.Info("remote ffmpeg already installed",
			"host_id", hostID, "arch", arch, "path", ffmpegPath, "version", version)
		return installedPaths{FFmpeg: ffmpegPath, FFprobe: ffprobePath}, nil
	}

	log.Info("remote ffmpeg install starting",
		"host_id", hostID, "arch", arch, "version", ffmpegVersion, "url", src.URL)
	if err := runInstall(ctx, app, hostID, src); err != nil {
		return installedPaths{}, fmt.Errorf("install ffmpeg %s on host_id=%d: %w", ffmpegVersion, hostID, err)
	}
	log.Info("remote ffmpeg install complete",
		"host_id", hostID, "arch", arch, "path", ffmpegPath)

	return installedPaths{FFmpeg: ffmpegPath, FFprobe: ffprobePath}, nil
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
