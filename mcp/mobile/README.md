# Mobile

Standalone mobile automation for Apteva: physical Android and iOS phones,
exclusive sessions, website navigation, native app installation/control,
screenshots, UI inspection, and repeatable smoke tests with saved evidence.

This app is self-contained. It does not require or modify Deploy, Code,
Computer, Devices, Instances, or Simulator. Other apps can call its MCP surface
through the normal app-to-app bridge when integrations are added separately.

## What works

| Backend | Discovery | Screen/input | Structured interaction | Installation |
|---|---|---|---|---|
| Android ADB | Automatic, USB or already-paired Wi-Fi | Screenshot, tap, swipe, ASCII text, keys, Chrome URL opening | Native UI source only | APK |
| Android Appium / UiAutomator2 | ADB discovery or configured inventory | Screenshot, touch actions, focused-field text, keys | Native elements, browser sessions, contexts | APK, Appium on the same runner |
| iOS native (Xcode on the Mac running Mobile) | Automatic: USB or Wi-Fi paired iPhones/iPads via devicectl, and booted iOS Simulators | Screenshot, tap, swipe, long press, Unicode text, home/enter, Safari and deep links | Native UI source, tap by accessibility id or label | Device-signed IPA; `.app` bundles on Simulators |
| iOS Appium / XCUITest | Configured inventory | Screenshot, touch actions, focused-field text, home, Safari URL opening | Native elements, Safari sessions, contexts | Device-signed IPA, Appium on the same Mac |

ADB text is limited to printable ASCII excluding `%`; use Appium for Unicode.
Structured mode starts Chrome/Safari. Screen mode starts a native automation
session and can launch apps or open browser URLs. Use `mobile_action(context)`
to list/switch Appium contexts for hybrid/native flows. Available operations
depend on the installed Appium driver and device configuration. iOS does not
provide an Android-style system Back key; Back in a browser session navigates
browser history.

The dashboard includes device inventory, manual control, screenshot polling,
source inspection, app installation/launch/stop, store and TestFlight installs,
screen recording with markers, a recordings library with playback and export,
test authoring, run history, and per-step screenshots. Live view is
**screenshot polling**; recordings are real video files.

| Recording | Android | iOS |
|---|---|---|
| `standard` | `adb screenrecord`, joined across its 170 s limit (needs ffmpeg past 170 s). No audio. | Appium `start/stop_recording_screen` (ffmpeg on the Appium host). No audio. |
| `high` | scrcpy 2.1+ on the runner: full resolution, audio on Android 11+. | USB capture on the Mac running Mobile, the path QuickTime uses: full resolution, up to 60 fps, with audio. |
| Simulator | — | `simctl io recordVideo`: full resolution H.264, no audio (both qualities). |

Emulator/simulator booting, building/signing apps, cloud device providers,
network traces, and deployment gates are not part of this release.
Already-running Android emulators and booted iOS Simulators are discovered,
but Mobile does not own their boot/shutdown lifecycle.

## Build and test

```sh
cd mcp/mobile
GOWORK=off go build -o /tmp/apteva-mobile .
GOWORK=off go test -race ./...
```

`GOWORK=off` makes this independently installable module use its published SDK
pin without changing the workspace's go.work. The SDK pin is v0.108.0, selected
from fetched tags by ancestry (it descends from v0.107.0 and local HEAD's
v0.82.0). No local SDK replace directive is distributed.

Build just this panel from the apps root:

```sh
bun run scripts/build-panels.ts --app mobile
```

The shared script also verifies other apps' existing bundles. Mobile's
generated bundle is `ui/MobilePanel.mjs` with React externalized for the
dashboard import map.

## Local Android setup

1. Install Android Platform Tools and put `adb` on the runner's PATH.
2. Enable Developer options and USB debugging on a dedicated test phone.
3. Plug it in, unlock it, and accept its debugging authorization prompt.
4. Confirm it appears as `device` in `adb devices -l`.
5. Install Mobile in an Apteva project and open its panel.

USB is a convenient starting point. Paired wireless ADB works too; Mobile uses
the existing ADB connection and does not pair devices or expose ADB to the
network. See the [Android ADB documentation](https://developer.android.com/tools/adb).

## iOS native setup (no Appium)

With `ios_driver=native` (the default) on a Mac with Xcode 15+, Mobile lists
connected iPhones/iPads (`xcrun devicectl`) and booted Simulators (`simctl`) on
its own. Touch input, typing, the UI tree and screenshots go through a small
XCUITest bundle shipped in `runner/`: on the first session for a device Mobile
builds it with `xcodebuild`, installs it and keeps it running, reaching it over
usbmuxd (devices) or loopback (Simulators). Later sessions reuse the warm
runner. If opening a session returns `setup_in_progress`, the build is still
running; open again in a minute.

One-time steps Apple requires for a physical device:

1. Sign in to an Apple ID under Xcode > Settings > Accounts. A free account
   works (its runner signature lasts 7 days; Mobile rebuilds when needed). The
   team comes from the Apple Development certificate in the keychain, or set
   `ios_team_id`.
2. Connect the device, unlock it and tap Trust.
3. Enable Developer Mode (Settings > Privacy & Security > Developer Mode) and
   restart the device.
4. Free accounts only: after the first install, trust the developer app under
   Settings > General > VPN & Device Management.

Simulators need none of this. Errors from these steps come back as
`setup_required` with the step to take. The runner listens on device port
22087 and is reachable only through the Mac's USB mux. Mobile must run outside
any sandbox that blocks `xcodebuild` from reaching `testmanagerd`.

The opt-in end-to-end test drives a booted Simulator through the real runner:

```sh
MOBILE_IOS_E2E=1 GOWORK=off go test -run TestNativeIOSEndToEnd -v .
```

## iOS through Appium (optional)

Set `ios_driver=appium` to use Appium instead. Use a Mac with Xcode for the supported iOS runner configuration. Prepare
Appium and its XCUITest driver, trust the phone, enable Developer Mode/UI
automation, and configure a valid signing/provisioning profile for
WebDriverAgent. Safari structured automation also needs Web Inspector and
Remote Automation enabled. These device approvals and Apple provisioning
steps remain operator setup.

Set `appium_url` (usually `http://127.0.0.1:4723`) and `devices_json`:

```json
[
  {
    "id": "iphone-test",
    "name": "Test iPhone",
    "platform": "ios",
    "udid": "YOUR-DEVICE-UDID",
    "physical": true,
    "browser": "Safari"
  }
]
```

For Android element/browser automation, install Appium's UiAutomator2 driver,
set `android_driver=appium`, and configure `appium_url`. Android devices on the
local ADB host are discovered automatically. For devices on a separate Appium
host, add inventory records with `platform=android`, `driver=appium`, and their
UDIDs. Configured inventory reports `configured`, not a verified online state;
opening a session performs the connection check.

Keep Appium private. Mobile does not expose arbitrary WebDriver scripts,
arbitrary shell commands, or driver capabilities supplied by tool callers.
The supported installation path puts Appium and Mobile on the same runner.
A separately hosted Appium server can handle interactive sessions, but file
installation is rejected because the artifact filesystem is not shared.

References: [XCUITest requirements](https://appium.github.io/appium-xcuitest-driver/latest/getting-started/system-requirements/),
[device setup](https://appium.github.io/appium-xcuitest-driver/latest/getting-started/device-setup/).

## Recording and export

Start a capture on an open session, mark moments, and stop it:

```json
{"action":"start","session_id":"ses_...","quality":"high","audio":true,"max_seconds":600}
{"action":"mark","recording_id":"rec_...","name":"gameplay"}
{"action":"stop","recording_id":"rec_..."}
```

`quality=auto` (default) picks high when its tool is available. Asking for
`audio:true` on a method without audio fails instead of silently recording
without sound. One capture runs per session, up to `max_seconds` (default 600,
maximum 1800). Closing, expiring or recovering a session finishes its capture
first, so the driver session is never torn down under a recorder. A runner
restart marks unfinished captures as failed.

Stopping returns the finished recording with duration, resolution, frame rate,
codecs, size and markers (read with ffprobe; without it the file is kept but
unverified). `file_url` streams it with range support for playback and
`&download=1` downloads it. Files stay on the runner for 7 days; when Storage
is installed and bound, Mobile also copies each finished recording to
`/.mobile/recordings/` and each export to `/.mobile/exports/` (1 MiB parts,
SHA-256 verified, retried with backoff, `mobile_record(action=upload)` to retry
now) and emits `mobile.recording.uploaded`.

Export a clip with a preset:

```json
{"recording_id":"rec_...","preset":"app_store_preview","variant":"iphone","from_mark":"gameplay","to":31.5}
```

| Preset | Output |
|---|---|
| `raw` | Trim only, streams copied (cuts land on keyframes) |
| `app_store_preview` | 15–30 s, H.264 High ≤ level 4.0, ≤30 fps, 10–12 Mbps, stereo 256 kbps AAC (silent track added if the capture has none), ≤500 MB. Variants: `iphone` 886×1920, `iphone_home_large` 1080×1920, `iphone_home_medium` 750×1334, `ipad` 1200×1600, `ipad_9_7` 900×1200, `mac` / `apple_tv` 1920×1080 landscape only |
| `play_promo` | 1080p H.264 for a YouTube upload linked from Google Play |
| `social_vertical`, `social_square` | 1080×1920 and 1080×1080 |

Sizes are portrait and swap for landscape; orientation follows the recording
unless set. `fit` is `pad` (default, black bars), `crop` or `stretch`. Trim
with `from`/`to` seconds or `from_mark`/`to_mark`; a selection outside the
preset's duration limits is rejected before encoding. Exports run in the
background (two at a time per runner). When done, the export lists `checks`
(duration, resolution, codec, frame rate, audio, size) and `valid`. App Store
sizes follow Apple's [app preview specifications](https://developer.apple.com/help/app-store-connect/reference/app-information/app-preview-specifications).

Projects add presets in `export_presets_json`, and callers can pass a one-off
`custom` preset with the same fields. Presets are validated data; ffmpeg
arguments are never taken from callers.

For a repeatable take, record a test run. Each step adds a marker named after
its label, `record_mark` adds a named one, and `wait` holds the shot:

```json
{"device_id":"ios-test","record":{"quality":"high","audio":true},"steps":[
  {"action":"launch","params":{"app_id":"com.example.game"}},
  {"action":"wait","params":{"ms":3000}},
  {"action":"record_mark","params":{"name":"gameplay"}},
  {"action":"tap","label":"Start","params":{"x":0.5,"y":0.8}},
  {"action":"wait","params":{"ms":25000}}
]}
```

`mobile_test_get` then reports `recording_id`.

### iOS USB capture

On first use Mobile compiles a small Swift helper (`capture/ioscapture.swift`)
with `xcrun swiftc` into its data directory. The phone must be connected by
cable, unlocked and trusted. macOS asks for Camera permission for the app that
runs Mobile (Terminal, the Apteva app or the runner's service); grant it under
System Settings > Privacy & Security > Camera. If the capture device ID
differs from the UDID, set `capture_id` on the device in `devices_json`.

## Remote runner

The same binary can serve the device execution API without an Apteva server
on that machine. It owns device locks, session records, test execution and
evidence. The Apteva app proxies the API and images and emits completion events.

```sh
export APTEVA_MOBILE_RUNNER_TOKEN="$(openssl rand -hex 32)"
/tmp/apteva-mobile --runner \
  --listen 127.0.0.1:9086 \
  --data-dir "$HOME/.apteva/mobile-runner" \
  --appium-url http://127.0.0.1:4723 \
  --devices-file /path/to/phones.json
```

Omit the last two flags for ADB-only Android. Optional flags:
`--android-driver adb|appium`, `--artifact-dir`, `--lock-dir`.

Recording and export run on the runner, so install ffmpeg (and scrcpy, or
Xcode for iOS USB capture) there. The app streams videos from the runner for
playback and Storage uploads.

The runner only binds loopback. Use an SSH tunnel or an HTTPS reverse proxy,
then configure the project's Mobile app with `runner_url` and the same
`runner_token`. Remote URLs require HTTPS; HTTP is accepted only on loopback.
Tokens must be at least 32 characters, stay out of URLs/logs, and are not
forwarded on redirects. The runner key is a trusted administrative credential:
it authorizes project-scoped calls from Mobile installations, not end users.

All Mobile processes accessing the same host's phones must use the same
`lock_dir` (default `~/.apteva/mobile-locks`). One Mobile runner must own a
given Appium host/device pool. Locks cannot coordinate independent runners on
different machines targeting the same external Appium server, or unrelated
ADB/Appium clients outside Mobile. Do not run other controllers concurrently.

## Tools and API

| Tool | Operations |
|---|---|
| `mobile_devices` | Inventory, platform/physical filters, setup diagnostics |
| `mobile_session` | open, list, get, renew, close, recover |
| `mobile_observe` | Screenshot image content; optional UI source |
| `mobile_action` | navigate, tap, swipe, text, key, tap_element, context |
| `mobile_app` | install (artifact, store, beta), launch, stop, status, uninstall (confirm required) |
| `mobile_stream` | Screenshot polling metadata |
| `mobile_record` | start, stop, mark, get, list, delete, presets, upload |
| `mobile_record_export` | Export a ready recording with a preset |
| `mobile_test_run` | Submit a deterministic test, return run_id |
| `mobile_test_get` | Retrieve a report or list recent runs |
| `mobile_test_cancel` | Request cancellation |

Reserve by exact device ID or a selector:

```json
{"action":"open","platform":"android","physical":true,"mode":"screen","ttl_seconds":900}
```

Then call `mobile_action` with the returned `session_id`:

```json
{"session_id":"ses_...","action":"navigate","url":"https://example.com"}
```

Coordinates are normalized 0..1 relative to the displayed device screen.
`tap_element` accepts `using` (`accessibility id`, `id`, `xpath`, `css selector`)
and `value`. These depend on the active native/web context. For Appium
`context`, omit `context` to list contexts, or provide the desired context name.

Dashboard routes, authenticated by the platform:

- `POST /api/apps/mobile/api/call` with `{ "tool": "...", "args": {} }`
- `GET /api/apps/mobile/api/screen/{session_id}` for a PNG
- `GET /api/apps/mobile/api/artifacts/{artifact_id}` for saved test evidence
- `GET /api/apps/mobile/api/recordings/{recording_id}/file` for a video (Range supported, `?download=1` to download)

An optional `project_id` query must match this installation's project. Session
IDs can be handed between authorized callers within that project. They do not
grant access across projects. This version uses project authority, not
per-agent session ownership, so panel operators can take over an agent's session.

## Installing an application

Put the file into the runner's `artifact_dir`, which defaults to
`<data-dir>/uploads`. Pass its relative filename to `mobile_app`:

```json
{"session_id":"ses_...","action":"install","artifact":"my-app.apk"}
```

Only regular files inside that directory are accepted; path traversal and
symlink escapes are rejected. Limit: 512 MiB. Android takes an APK, not an AAB;
use [bundletool](https://developer.android.com/tools/bundletool) externally to
prepare device APKs. iOS takes a device-compatible signed IPA, not a simulator
`.app`. Mobile does not build, sign, re-sign or fetch arbitrary artifact URLs.
Populate remote runner artifacts directly via your existing file-transfer tools.

### From the App Store, Play Store or TestFlight

```json
{"session_id":"ses_...","action":"install","source":"beta","app_name":"My Game"}
{"session_id":"ses_...","action":"install","source":"store","app_id":"com.example.game"}
{"session_id":"ses_...","action":"status","app_id":"com.example.game"}
```

Mobile opens the store page (Play: `app_id`; App Store: numeric `store_id`;
TestFlight: `app_name` as listed, or `store_id`), taps Install or Update and
returns `result: installing` or `already_installed`. It does not wait for the
download: poll `status`, or use a `wait_for_app` test step. Play testing
tracks install from the normal Play listing once the signed-in account has
joined the test, so `beta` and `store` behave the same on Android.

The phone must already be signed in to the store account, with any TestFlight
invitation accepted or acceptable in the app. App Store purchases that need
Face ID or a password are not automated. Store layouts change: pass `flow`
(steps of `launch`, `open_url`, `tap`, `expect` or `wait`, matching visible
text) to override the built-in steps without waiting for a release.

## Smoke tests

```json
{
  "device_id": "android:YOUR-SERIAL",
  "name": "Website smoke",
  "build_ref": "build-or-commit-reference",
  "mode": "screen",
  "steps": [
    {"action":"navigate","params":{"url":"https://example.com"}},
    {"action":"wait_for_text","params":{"text":"Example Domain","timeout_ms":10000}},
    {"action":"screenshot","label":"Landing page"}
  ]
}
```

Tests reserve their own device session, so close an interactive session first.
Tests fail promptly with `device_unavailable` when the phone is busy; there is
no unbounded device queue. Limits: 8 concurrent/pending runs per runner, 50 steps,
10 minutes per test, 30 seconds per text wait or `wait` step, 5 minutes per
`wait_for_app`, 12 MiB per PNG, 4 MiB per UI source. `record` films the run;
see Recording and export.

`assert_text` and `wait_for_text` perform literal matches against UI/HTML source;
they are not OCR or proof that a matched node is visible. Browser accessibility
exposure varies. Use Appium browser mode/element actions for structured web
flows, and inspect screenshots to assess visual layout. Test plans execute
deterministically; exploratory AI reasoning belongs to the calling agent.

Reports distinguish `assertion_failed`, `device_unavailable`, infrastructure
errors, cancellation, timeout and restart interruption. Each executed step
captures a screenshot where possible. A screenshot-capture failure fails the
run rather than reporting a complete pass without evidence. Reports retain
`build_ref`; Mobile does not act on it or promote releases. Text inputs and
test plans are not saved in reports, but screenshots can contain sensitive data.
Evidence and history remain on disk until the operator removes them; configure
appropriate disk capacity and cleanup for your test volume.

The app tracks submitted runs and emits `mobile.test.completed` after observing
a terminal report (including when the runner is remote). Callers can poll
`mobile_test_get` independently. The event integration does not require changes
to any other app.

## Leases and failure recovery

Leases default to 15 minutes, accept 30..3600 seconds, and can be renewed.
Commands on one device are serialized, including screenshots and manual input.
Expired sessions cannot send commands. Closing a session preserves app data;
there is no implicit device wipe, app data reset, network switch, or account reset.

Host file locks prevent a second Mobile process from controlling the same
device. SQLite preserves lease/run history across restarts. On restart,
unfinished runs become `interrupted`; old device sessions are closed before
reuse. If Appium cleanup fails, the device remains reserved and cleanup retries.

An Appium session-creation timeout has an unknown outcome. Mobile quarantines
the lease instead of guessing whether the phone is free. Stop any orphaned
session on the Appium host, then explicitly call:

```json
{"action":"recover","session_id":"ses_...","confirm":true}
```

Use dedicated phones/accounts. Ordinary screen interaction does not promise
undetectable automation. The device's actual network carries browser/app traffic;
it must be able to reach any preview URL being tested. `localhost` on a phone
refers to the phone itself.
