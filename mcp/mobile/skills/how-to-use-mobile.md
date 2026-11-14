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
   Native iOS sessions (driver xctest: iPhones, iPads and booted Simulators on a
   Mac) support tap_element with using=accessibility id or id, Unicode text,
   home and enter; iOS has no Back key, so tap the app's back button.
   The first native iOS session builds and installs a test runner; if open
   returns setup_in_progress, retry after a few seconds. The panel and smoke
   tests wait automatically. For signing setup_required, call mobile_setup:
   status discovers Xcode teams without an existing certificate; select_team
   chooses a discovered team; open_xcode opens Xcode for Apple sign-in. Ask
   operators only for Apple sign-in/2FA, team choice when ambiguous, phone trust,
   Developer Mode and developer trust if iOS asks. Do not ask them to create
   certificates or provisioning profiles manually.
   ADB supports printable ASCII text excluding percent signs; use Appium for Unicode.
6. `mobile_app` installs runner-local APK/IPA artifacts, launches/stops apps by
   package/bundle ID, and uninstalls only with confirm=true. Mobile does not build
   or sign artifacts. Files must be in the runner's configured artifact directory.
   install with source=store or beta drives the Play Store, App Store or
   TestFlight (app_id for Play, store_id for the App Store, app_name or store_id
   for TestFlight). It returns once Install/Update is tapped; poll
   action=status until installed=true. Phones must already be signed in.
7. Close sessions when done, or renew before expiry. Closing preserves phone data.
   Sessions are exclusive across projects; do not create a second controller.
   For an Android ADB device with keep_awake capability, mobile_power(status,
   session_id) reads Stay awake while charging. Use action=set, enabled=true
   only when requested: this changes a persistent phone preference, kept after
   closing the session. enabled=false restores normal idle timeout behavior.
   Unplugged the phone uses its normal timeout; manual locking still works.
8. For repeatable testing, close interactive sessions first and call
   `mobile_test_run` with device_id and 1..50 steps. Steps have action, optional
   label and params. The test owns its session and captures per-step screenshots.
   Poll `mobile_test_get` with run_id or await mobile.test.completed. Cancellation
   is available through mobile_test_cancel.
9. assert_text/wait_for_text match literal UI/HTML source, not OCR/visibility.
   Treat infrastructure errors, unavailable devices and assertions separately.
10. Live view is screenshot polling. Request mobile_stream for details. Avoid
    putting credentials in step labels or test names. Screenshots and videos
    may show account data. Do not claim automation is undetectable.
11. Video: check the device's capabilities for record, record_high and
    record_audio, then mobile_record(action=start, session_id, quality, audio).
    Add markers with action=mark at moments worth cutting to, then stop. Only
    one capture per session; closing the session also finishes it.
12. For a repeatable take (store previews, demos), prefer mobile_test_run with
    record={quality, audio}: every step becomes a marker, record_mark adds a
    named one and wait holds the shot. The report gives recording_id.
13. mobile_record_export(recording_id, preset, variant?, from/to or
    from_mark/to_mark) produces a clip in the background. List presets with
    mobile_record(action=presets). For App Store previews use
    preset=app_store_preview and pick variant by display class (iphone, ipad, …);
    the selection must be 15–30 s. Poll mobile_record(action=get) until ready,
    then report the checks and valid flag rather than assuming the store will
    accept it. Ready files carry file_url and, when Storage is bound, a Storage
    file id under /.mobile/.

If an Appium creation outcome is unknown, stop the orphaned session on its host
before explicitly confirming mobile_session(action=recover,confirm=true).
