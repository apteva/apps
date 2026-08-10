# Apteva Simulator

iOS and Android simulators on demand. Boot a device, build a repo's
source into a `.apk`/`.app`, install + launch on a headless
emulator/Simulator, stream the screen back to the browser, and relay
touch + key input.

Called by the **Code** app on `repos_dev_start` when it detects an
iOS/Android repo; also usable standalone (Simulator panel) for any flow
that produces a mobile artifact.

The standalone panel can now run source archives directly: pick
Android or iOS, upload a `.zip`, `.tar.gz`, or `.tgz`, then the sidecar
boots a device if needed, builds, installs, launches, and opens live
view.

## Host requirements

Per-platform, probed at runtime by `sims_capabilities`. Boot/build
availability is reported separately from live streaming/input. iOS can
boot and stream with Xcode alone, while idb improves live view and
enables clicks.

| Platform | Host | Tools on PATH |
|---|---|---|
| Android | Linux (KVM recommended) or macOS | `adb`, `emulator`, `avdmanager`, `aapt`, `gradle` (or a repo `./gradlew`), a JDK 17 |
| iOS | **macOS only** | `xcrun`, `xcodebuild`, `simctl`; optional `idb` (`pipx install fb-idb`) + `idb_companion` (`brew install idb-companion`) for faster raw-frame live view and input |

The Linux production host can run the Android backend; iOS requires macOS.
Simulator runs locally by default and can optionally use a host managed by the
**Instances** app. Provider-provisioned macOS compute, such as a Scaleway Apple
silicon Mac mini, can be an iOS runner while the Simulator panel and public API
remain on the primary Apteva installation.

System images / runtimes are **not** auto-installed (they're large and
slow). Install them once:

```sh
# Android
sdkmanager --install "system-images;android-34;google_apis;x86_64"
# iOS — open Xcode → Settings → Platforms, or:
xcodebuild -downloadPlatform iOS
```

## Optional remote runners

Install and bind the Instances app, then provision compute with
`instance_create`. Simulator accepts provider-managed Instances only: Linux or
macOS VPS/bare-metal resources created through a configured provider adapter.
For iOS, select a ready `platform=macos` instance such as Scaleway Apple
silicon. Arbitrary externally registered SSH hosts are not supported.

Choose that instance in Simulator's optional `ios_host_id` or
`android_host_id` setting. A single operation can override the configured host
with `host_id`; omit it to use the platform setting, and leave both unset for
the original local behavior.

Simulator bootstraps an authenticated, loopback-only worker on the selected
host and reaches it through an Instances SSH tunnel. Device control, builds,
artifacts, screenshots, logs, and live WebSockets stay on that runner. Public
stream URLs still terminate at the Simulator app, which proxies the selected
worker without exposing it publicly.

By default the bootstrap resolves the immutable `simulator/v0.1.26` app release
tag and uses the remote Go toolchain to install that exact commit. Set
`remote_worker_binary_path` when the matching worker binary is already
installed, or change `remote_worker_module_ref` to a Go revision for
development. Remote iOS hosts still need
Xcode and an installed Simulator runtime; remote Android hosts need the Android
SDK/emulator and an appropriate system image.

## MCP tools

| Tool | Purpose |
|---|---|
| `sims_capabilities` | Per-platform availability + missing-dep hints |
| `sims_list` | Sims known to the project |
| `sims_boot` | Boot an emulator/Simulator locally or on optional `host_id` (auto-creates a device) |
| `sims_shutdown` | Shut a sim down (idempotent) |
| `sims_build` | Build source → APK/.app locally or on optional `host_id` (no launch) |
| `sims_install` | Install a local artifact path or remote opaque artifact ID onto a booted sim |
| `sims_launch` | Launch an installed bundle |
| `sims_run` | **Composite**: boot + build + install + launch + stream URL |
| `sims_input` | tap / swipe / key / text (normalized 0..1 coords) |
| `sims_logs` | logcat (android) / unified log (ios) |
| `sims_screenshot` | One-off PNG |
| `sims_stream_url` | Mint a short-lived live-stream WebSocket URL |

## How Code drives it

```
CodePanel "Run" → repos_dev_start (code)
  detectDevIOS / detectDevAndroid match → RemoteRunner="simulator"
  dev_remote.go tars the repo source, strips build caches
  → CallAppResult("simulator", "sims_run", {framework, source_tgz_b64, …})
      capability check → boot-if-needed → build → install → launch
      → mint stream token → return {sim_id, stream_url, …}
  code persists dev_runs row (runner=simulator, sim_id, stream_url)
CodePanel renders <DeviceFrame streamUrl=…> (live H.264 + input)
repos_dev_stop → CallAppResult("simulator","sims_shutdown",…)
```

## Streaming

The live screen is streamed over a WebSocket:

- **Android**: `adb exec-out screenrecord --output-format=h264 -`
- **iOS**: `idb video-stream --format rbga` raw BGRA frames converted to JPEG; falls back to native `xcrun simctl io <udid> screenshot --type=jpeg`

`stream.go` reassembles Android H.264 access units (`annexb.go`) and
ships one per WebSocket binary message. iOS sends complete JPEG frames.
The browser (`ui/components/DeviceFrame.tsx`) draws to canvas and
forwards pointer + keyboard as JSON control messages. Input also has
discrete tool form (`sims_input`) for headless agent flows.

`screenrecord` has a 180s hard cap per invocation. The sidecar reaps and
restarts it behind the same WebSocket, so Code and Simulator panels keep a
continuous session without reconnecting.

## Build and stream safety

Source archives are streamed into bounded extraction: 512 MiB compressed,
2 GiB expanded, 512 MiB per file, and 100,000 entries. Builds time out after
20 minutes, terminate their whole process group, and do not inherit Apteva or
common secret environment variables. Operators can explicitly pass named
project secrets with `build_env_allowlist`; `APTEVA_*` credentials are never
forwarded. The sidecar/container remains the filesystem and network isolation
boundary for project-controlled Gradle and Xcode build logic.

Only one live stream encoder is allowed per simulator. Browser WebSockets are
same-origin, input messages and log reads are bounded, and stored artifacts may
only be installed from this app's content-addressed artifact directory.

## Disk layout (per-install data dir)

```
simulator.db                    app DB
artifacts/<sha>.apk             built APKs
artifacts/<sha>.app/            built .app bundles
sim-logs/<sim_run_id>.log       per-run build/install log
boot-logs/<sim_id>.log          per-sim emulator boot log
```

Remote equivalents live under the configured worker data directory on each
runner. The control-plane database stores opaque artifact and device IDs, not
remote filesystem paths.

Inactive run history, unreferenced artifacts, and logs are retained for 30
days and cleaned at mount and periodically after builds.

## Build & test

```sh
GOWORK=off go build ./...
GOWORK=off go test ./...        # store + annex-b framer + manifest + capability probe
```

Panels (TSX → bundled .mjs):

```sh
cd ../.. && bun run scripts/build-panels.ts
```

## Testing against a real device

Tool-level smoke test (Android, with the SDK installed):

```sh
APTEVA_GATEWAY_URL=http://localhost:5280 APTEVA_APP_TOKEN=dev-token \
APTEVA_INSTALL_ID=0 APTEVA_PROJECT_ID=<proj> go run .
# then drive sims_capabilities / sims_boot / sims_run via the gateway
```

End-to-end: install Code + Simulator in a project, create/import an
Android repo (a `settings.gradle` + an `app/` module applying
`com.android.application`), click **Run** in the Code panel.

> Note: the streaming + device-control paths require a real
> emulator/Simulator on the host and a WebCodecs-capable browser; they
> can't be exercised by `go test` alone.
