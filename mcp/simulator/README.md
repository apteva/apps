# Apteva Simulator

iOS and Android simulators on demand. Boot a device, build a repo's
source into a `.apk`/`.app`, install + launch on a headless
emulator/Simulator, stream the screen back to the browser, and relay
touch + key input.

Called by the **Code** app on `repos_dev_start` when it detects an
iOS/Android repo; also usable standalone (Simulator panel) for any flow
that produces a mobile artifact.

## Host requirements

Per-platform, probed at runtime by `sims_capabilities`. Boot/build
availability is reported separately from live streaming/input, because
iOS can boot with Xcode alone while live view needs idb.

| Platform | Host | Tools on PATH |
|---|---|---|
| Android | Linux (KVM recommended) or macOS | `adb`, `emulator`, `avdmanager`, `gradle` (or a repo `./gradlew`), `aapt`, a JDK 17 |
| iOS | **macOS only** | `xcrun`, `xcodebuild`, `simctl`, `idb` (`pipx install fb-idb`), `idb_companion` (`brew install idb-companion`) |

The Linux production host can run the Android backend; iOS requires
running apteva on a Mac. A Mac runner pool is future work — v0.1 runs
sims on the install's own host.

System images / runtimes are **not** auto-installed (they're large and
slow). Install them once:

```sh
# Android
sdkmanager --install "system-images;android-34;google_apis;x86_64"
# iOS — open Xcode → Settings → Platforms, or:
xcodebuild -downloadPlatform iOS
```

## MCP tools

| Tool | Purpose |
|---|---|
| `sims_capabilities` | Per-platform availability + missing-dep hints |
| `sims_list` | Sims known to the project |
| `sims_boot` | Boot an emulator/Simulator (auto-creates a device) |
| `sims_shutdown` | Shut a sim down (idempotent) |
| `sims_build` | Build source → APK/.app (no launch) |
| `sims_install` | Install a built artifact onto a booted sim |
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

The live screen is a raw H.264 elementary stream:

- **Android**: `adb exec-out screenrecord --output-format=h264 -`
- **iOS**: `idb video-stream --format h264 -`

`stream.go` reassembles H.264 access units (`annexb.go`) and ships one
per WebSocket binary message. The browser (`ui/components/DeviceFrame.tsx`)
decodes with WebCodecs `VideoDecoder` → canvas, and forwards pointer +
keyboard as JSON control messages. Input also has discrete tool form
(`sims_input`) for headless agent flows.

`screenrecord` has a 180s hard cap per invocation — when it elapses the
stream ends and the panel reconnects (tokens last an hour). Seamless
in-process respawn is a future enhancement.

## Disk layout (per-install data dir)

```
simulator.db                    app DB
artifacts/<sha>.apk             built APKs
artifacts/<sha>.app/            built .app bundles
sim-logs/<sim_run_id>.log       per-run build/install log
boot-logs/<sim_id>.log          per-sim emulator boot log
```

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
