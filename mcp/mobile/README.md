# Mobile

Standalone mobile automation for Apteva: physical Android and iOS phones,
exclusive sessions, website navigation, native app installation/control,
screenshots, UI inspection, and repeatable smoke tests with saved evidence.

This app is self-contained. It does not require or modify Deploy, Code,
Computer, Devices, Instances, or Simulator. Other apps can call its MCP surface
through the normal app-to-app bridge when integrations are added separately.

## What works in v0.1

| Backend | Discovery | Screen/input | Structured interaction | Installation |
|---|---|---|---|---|
| Android ADB | Automatic, USB or already-paired Wi-Fi | Screenshot, tap, swipe, ASCII text, keys, Chrome URL opening | Native UI source only | APK |
| Android Appium / UiAutomator2 | ADB discovery or configured inventory | Screenshot, touch actions, focused-field text, keys | Native elements, browser sessions, contexts | APK, Appium on the same runner |
| iOS Appium / XCUITest | Configured inventory | Screenshot, touch actions, focused-field text, home, Safari URL opening | Native elements, Safari sessions, contexts | Device-signed IPA, Appium on the same Mac |

ADB text is limited to printable ASCII excluding `%`; use Appium for Unicode.
Structured mode starts Chrome/Safari. Screen mode starts a native automation
session and can launch apps or open browser URLs. Use `mobile_action(context)`
to list/switch Appium contexts for hybrid/native flows. Available operations
depend on the installed Appium driver and device configuration. iOS does not
provide an Android-style system Back key; Back in a browser session navigates
browser history.

The dashboard includes device inventory, manual control, screenshot polling,
source inspection, app installation/launch/stop, test authoring, run history,
and per-step screenshots. Live view is **screenshot polling**, not video.

Emulator/simulator booting, building/signing apps, cloud device providers,
recorded video, network traces, and deployment gates are not part of v0.1.
Already-running Android emulators can be discovered, but Mobile does not own
their boot/shutdown lifecycle. No simulator-specific iOS artifacts are accepted.

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

## iOS / Appium setup

Use a Mac with Xcode for the supported iOS runner configuration. Prepare
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
| `mobile_app` | install, launch, stop, uninstall (confirm required) |
| `mobile_stream` | Screenshot polling metadata |
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
10 minutes per test, 30 seconds per text wait, 12 MiB per PNG, 4 MiB per UI source.

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
