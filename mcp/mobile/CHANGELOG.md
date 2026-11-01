# Changelog

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
