# Changelog

## 0.9.1 — 2026-11-27

- Live video recovers on its own when its connection to the phone breaks
  while the session stays active (a USB drop shorter than the 3 s presence
  check, adb restarting): it restarts with backoff (0.5 s up to 10 s), tells
  viewers `{"type":"reconnecting"}` and sends a fresh key frame. The panel
  shows "Reconnecting…".
- A new adb transport id for a phone that is still listed counts as a drop:
  its session pauses and resumes, `mobile.device.offline`/`online` are sent,
  and streams restart.
- An action cancelled by `mobile_session stop` returns `session_stopped`
  instead of `infrastructure_error`.

## 0.9.0 — 2026-11-27

- Stop: `mobile_session action=stop` (session_id, device_id or all=true, with
  an optional reason) cancels the running action without waiting for the phone
  lock, kills half-done input on the phone and cancels any held touch, ends
  streams and recordings, and records `ended_at`, `end_reason`, `stop_reason`
  and `stopped_by`. Nothing new runs on a stopping session.
- Events on the app bus: `mobile.session.started`, `mobile.session.ended`
  (duration and end reason), `mobile.device.online`/`offline`, and
  `mobile.action.performed` when `action_events=all`. Optional `caller_ref` on
  open is copied onto the session's events. Text is reported as a length,
  secrets as `[secret]`; no screens or elements.
- Health: every 10 s per Android phone (battery, temperature, charging,
  thermal, Wi-Fi, signal, screen, free storage, uptime) with alerts, shown in
  `mobile_devices` with `last_seen_at`. A phone that drops off is detected
  within ~3 s; its session is paused (it keeps the phone), adb reconnect is
  retried with backoff, and the session resumes when the phone returns, or
  ends as `device_lost` after `offline_grace_seconds`.
- Runner: token-protected `GET /v1/health` with versions, phones, sessions
  and streams; the open `/health` stays minimal.
- Live video: `mobile_stream mode=video` returns a WebSocket URL with a
  5-minute renewable token. H.264 from scrcpy-server (default 1280 px, 4 Mbps),
  frames with scrcpy's 12-byte header, a key frame for each new viewer, slow
  viewers skip to the next key frame, touch/key/text input on the same socket
  with one event per gesture. The panel plays it with WebCodecs.
- Input: `long_press`, `drag` (paths), any Android keycode with an operator
  block-list (`blocked_keycodes`, default power/sleep), Unicode `replace` via
  accessibility set-text, and `wait_for` (text or element).
- `mobile_device`: `baseline_capture`, `clean` (confirm=true) and
  `verify_clean` for shared phones. Clean uninstalls apps added since the
  baseline, clears the clipboard, deletes downloads and screenshots newer than
  the baseline (older files stay) and closes apps. No factory reset.

## 0.8.0 — 2026-11-18

- Report Android selection state in UI source: the bundled collector now writes
  `checkable`, `checked`, `selected` and `scrollable` with the same names and
  values as `uiautomator dump`. Previously these were missing on the fast path,
  so callers could not tell checked from unchecked rows.
- Advertise `selection_state` on the ADB driver only (not Appium, not iOS).
- Rebuilt `source.dex.jar` (android-35, build-tools 36.1.0); the secure-input
  helper in the same jar is unchanged in source.

## 0.7.0 — 2026-11-14

- Add guarded Android password entry using accessibility ACTION_SET_TEXT and a
  private stdin pipe. Require an empty focused password field, screen/account
  guards and a positive input receipt; never expose the secret in command
  arguments, helper files, output, errors or screenshots.
- Add source-only Android observation with no screenshot fallback. Wait for the
  accessibility root before checking guards and reject active/finalizing captures.
- Retain iOS/Android recording, embedded panels, keep-awake controls, and verified
  regular-field replacement/clearing. Pin app-sdk v0.115.2 by tag ancestry.

## 0.6.0 — 2026-11-14

- Collect bounded, read-only Android UI source during animations, with a stock
  collector fallback. Embed the collector and its reproducible SDK build script.
- Add opt-in verified text replacement and explicit clearing. Check foreground
  package, focused field identity and current value before input; require exact
  readback afterward. Bind anonymous EditText fields to their class and bounds.
- Reject unsupported bytes before modifying fields. ADB input supports printable
  ASCII excluding percent; structured drivers remain required for other text.
- Preserve the iOS/Android Keep awake controls, recording timing and embedded UI
  assets from 0.5.0 and earlier releases. Pin app-sdk v0.115.2 by tag ancestry.

## 0.5.0 — 2026-11-14

- Share the Keep awake control and `mobile_power` status/set API between Android
  ADB and native iOS. Return `keep_awake` and `scope` so clients show charging-only
  Android behavior and always-on iPhone Auto-Lock behavior accurately.
- Use native iOS Settings automation to read and verify Auto-Lock changes, save
  the previous timeout across sessions, and restore it when disabling (30 seconds
  if no timeout was saved). Surface Low Power Mode, policy and unsupported layout
  errors. No Appium dependency or app-specific idle-timer workaround.
- Read power settings on connection or explicit refresh, without background
  Settings polling. Block iOS power operations during recordings.
- Pin app-sdk v0.115.2, selected by tag ancestry.

## 0.4.1 — 2026-11-14

- Embed the production panel, source map and icon in the app binary and serve
  them through exact asset routes. Packaged installs with an empty runtime UI
  directory can load the Mobile panel, including Android power controls.
- Add an asset-serving regression check using an empty working directory.

## 0.4.0 — 2026-11-14

- Add an Android **Stay awake while charging** option under session controls.
  Read and verify the actual phone preference; changes persist on the device
  after the session closes. AC, USB, wireless and dock charging are supported.
- Add the mobile_power status/set tool and keep_awake capability for Android
  ADB sessions. Require an owned active session, explicit boolean changes,
  and successful readback; surface policy/permission failures.
- Keep the normal unplugged timeout unchanged and leave manual locking available.
  Opening a session does not automatically alter power preferences.

Validation: race-enabled Go suite, vet, macOS/Linux builds, nine UI tests,
panel bundle/import checks, and a physical Samsung SM-A176B preference toggle
off/on with persistence verified after closing and reopening its session.

## 0.3.3 — 2026-11-14

- Fix Android take duration by freezing the monotonic stop boundary before
  encoder shutdown and USB copy, trimming encoder drain and preserving idle
  tails with held frames. Finalize H.264 at 30 fps; require ffmpeg/ffprobe.
- Run scrcpy headlessly without a desktop window or input controller. Read
  its actual muxer-ready message through a private terminal before starting
  the timer, and finalize cleanly on interrupt. Require requested device audio.
- Fail recorded smoke tests when capture is missing, unplayable, interrupted
  early, or cannot finalize. Retain step evidence and the original assertion
  or cancellation failure classification.
- Add timeline trim/pad/audio fixtures, recorder startup/shutdown regressions,
  failure-propagation tests, and opt-in physical Android recording checks.
- Pin app-sdk v0.115.1, selected by tag ancestry.

Validation: race-enabled Go suite (64.0% coverage), vet, Darwin/Linux builds,
and a physical Samsung SM-A176B on Android 16. Eight-second standard and
high-quality video takes measured 8.000 s and 8.033 s; an 18-second take with
device audio measured 18.000 s. Failure regressions cover invalid media,
shutdown errors, missing audio, early exit, and preservation of assertion errors.

## 0.3.2 — 2026-11-07

- Fix shortened iPhone USB recordings caused by overlapping QuickTime edit
  lists and discontinuous device timestamps. Write H.264/AAC on a shared
  monotonic host timeline, preserving continuous audio sample timing.
- Hold the latest video frame on a host-clock cadence and through stop so
  static screens and idle tails retain their real elapsed duration.
- Start the panel timer after capture is ready, excluding compilation and
  encoder warm-up; align markers and recording limits with the same boundary.
- Add compiled Swift regression fixtures for backward device-clock jumps,
  audio/video sync, static tails, exact duration and monotonic packet timestamps.

Validation: Go race suite, vet, Darwin/Linux builds, compiled Swift media
fixtures, and a physical iPhone capture measuring 18.0005 seconds elapsed
versus 18.0017 seconds of media, with monotonic audio/video packet timestamps.

## 0.3.1 — 2026-11-07

- Bootstrap native iPhone signing from Xcode team metadata before a development
  certificate exists. Automatically provision certificates, app IDs, profiles
  and device registration through Xcode; remember an explicit team choice.
- Add mobile_setup and a live setup panel with Apple sign-in guidance and team
  selection. Keep manual steps to Apple account/device approvals.
- Show first-build progress and reconnect automatically; smoke tests also wait
  for native runner preparation. Re-provision expired cached device builds.
- Fix configured native iPhone inventory and report the iOS 17 minimum.
- Authenticate every native runner request; restrict simulator listeners to
  loopback and cancel background preparation when Mobile shuts down.
- Pin app-sdk v0.115.0, selected by tag ancestry.

## 0.3.0 — 2026-11-01

- Native iOS control without Appium (`ios_driver=native`, the default on a
  Mac with Xcode). iPhones and iPads are discovered through `devicectl` with
  pairing, Developer Mode and connection state; booted iOS Simulators through
  `simctl`.
- A bundled XCUITest runner (`runner/`) serves screenshots, the UI tree,
  taps, long presses, swipes, Unicode typing, home/enter, links and
  tap-by-accessibility-id. Mobile builds it on first use (automatic signing
  for devices, with the team from the keychain or `ios_team_id`), keeps it
  warm across sessions and stops it on shutdown. Devices are reached through
  usbmuxd, Simulators over loopback.
- Native iOS app install (devicectl/simctl; `.app` bundles on Simulators),
  launch, stop, uninstall and status; App Store and TestFlight flows run
  through the runner.
- Simulator screen recording through `simctl io recordVideo` (full
  resolution, no audio).
- Signing, trust, Developer Mode and lock-screen failures come back as
  `setup_required` with the step to take; a first-time build returns a
  retryable `setup_in_progress`.

Validation: unit tests for discovery, signing-team detection, the usbmux
protocol (fake usbmuxd), runner protocol and error mapping; an opt-in
end-to-end test that discovered a booted iOS 18.5 Simulator, built and
started the runner, observed, launched Settings, tapped by label, swiped,
opened Safari and recorded a 17.6 s H.264 video. Physical-device signing and
the usbmux path against a real iPhone are not yet exercised.

## 0.2.0 — 2026-11-01

- Screen recording: `mobile_record` start/stop/mark/get/list/delete. Standard
  capture uses adb screenrecord (Android, chunked past 170 s) or Appium (iOS);
  high quality uses scrcpy (Android, audio on 11+) or USB capture on a Mac
  through AVFoundation (iOS, full resolution with audio). Captures finish
  before their session closes and are failed cleanly after a runner restart.
- Recordings are probed with ffprobe, streamed with range support, kept on the
  runner for 7 days and copied to Storage under `/.mobile/` when Storage is bound
  (`mobile.recording.uploaded`).
- `mobile_record_export` with data-driven presets: raw, app_store_preview
  (Apple display classes), play_promo, social_vertical, social_square, project
  presets from `export_presets_json`, or a one-off custom preset. Trims by
  seconds or markers, refuses selections outside the preset's limits, and
  validates the output (checks + valid).
- Recorded test runs (`record`), with a marker per step, plus `wait`,
  `record_mark` and `wait_for_app` steps.
- `mobile_app` installs from the Play Store, App Store and TestFlight through
  editable flows, and reports install status.
- Panel: recording controls with markers, a Recordings tab with playback,
  export and per-check results, store install sources, and recorded test runs.

Validation: Go vet, race-enabled tests including real ffmpeg capture, export
and Storage upload paths, and browser checks of the panel against the real
HTTP handlers with a simulated phone. Not yet exercised on a physical phone.

## 0.1.0 — 2026-10-26

- Initial standalone Mobile app for physical Android and iOS automation.
- Android ADB and Android/iOS Appium adapters, device inventory and setup diagnostics.
- Exclusive device sessions with expiry, renewal, host-wide locks and restart recovery.
- Website navigation, screen gestures, UI source inspection and native MCP screenshots.
- APK/IPA installation from the runner artifact directory, plus app launch, stop and confirmed uninstall.
- Authenticated remote runner using the same binary.
- Bounded asynchronous smoke tests, cancellation, per-step screenshots and durable reports.
- Native Apteva panel following CRM/Deploy layout and Terminal/Clean light/dark themes.

Validation: 32 Go tests with race detection, Go vet, macOS/Linux builds,
runner/sidecar API smoke checks and browser UI checks. Physical-device
acceptance testing remains pending; no phone was connected during this release.
Live view uses screenshot polling rather than video.
