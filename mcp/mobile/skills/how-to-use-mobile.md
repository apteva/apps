# Mobile

1. Call `mobile_devices` before choosing a target. Read setup diagnostics and
   capabilities. `configured` means registered inventory, not a verified connection.
2. Open `mobile_session` with an exact device_id or platform/physical selector.
   Use screen mode for visual/native control, structured mode for Chrome/Safari
   element automation through Appium. Keep the returned session_id.
3. Call `mobile_observe` to see the screenshot. Ask for source=true when useful.
4. Call `mobile_action`: navigate to HTTP(S) URLs, tap/swipe using normalized
   0..1 coordinates, type into the focused field, or press home/back/enter.
   Observe again after actions; do not assume the screen stayed unchanged.
5. Appium sessions also support tap_element(using,value) and context switching.
   ADB supports printable ASCII text excluding percent signs; use Appium for Unicode.
6. `mobile_app` installs runner-local APK/IPA artifacts, launches/stops apps by
   package/bundle ID, and uninstalls only with confirm=true. Mobile does not build
   or sign artifacts. Files must be in the runner's configured artifact directory.
7. Close sessions when done, or renew before expiry. Closing preserves phone data.
   Sessions are exclusive across projects; do not create a second controller.
8. For repeatable testing, close interactive sessions first and call
   `mobile_test_run` with device_id and 1..50 steps. Steps have action, optional
   label and params. The test owns its session and captures per-step screenshots.
   Poll `mobile_test_get` with run_id or await mobile.test.completed. Cancellation
   is available through mobile_test_cancel.
9. assert_text/wait_for_text match literal UI/HTML source, not OCR/visibility.
   Treat infrastructure errors, unavailable devices and assertions separately.
10. Live view is screenshot polling in this version. Request mobile_stream for
    details. Avoid putting credentials in step labels or test names. Screenshots
    may show account data. Do not claim automation is undetectable.

If an Appium creation outcome is unknown, stop the orphaned session on its host
before explicitly confirming mobile_session(action=recover,confirm=true).
