# Changelog

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
