# Mobile

1. Call `mobile_devices` before choosing a target. Read setup diagnostics and
   capabilities. `configured` means registered inventory, not a verified connection.
   Android ADB phones carry `health` (battery, temperature, charging, thermal,
   Wi-Fi, signal, screen, free storage, uptime) with `alerts` (hot, low_battery,
   wifi_down, low_storage, health_check_failed) and `last_seen_at`. Prefer a
   phone without alerts.
2. Open `mobile_session` with an exact device_id or platform/physical selector.
   Use screen mode for visual/native control, structured mode for Chrome/Safari
   element automation through Appium. Keep the returned session_id. Pass
   caller_ref (your task or run id, up to 200 chars) to find this session's
   events later; it is copied onto mobile.session.* and mobile.action.performed.
3. Call `mobile_observe` to see the screenshot. Ask for source=true when useful.
   On devices with the selection_state capability (Android ADB), source nodes
   carry checkable/checked/selected/scrollable as "true"/"false"; use them to
   confirm a radio or checkbox changed and to find scrollable lists.
4. Call `mobile_action`: navigate to HTTP(S) URLs, tap/swipe using normalized
   0..1 coordinates, type into the focused field, or press home/back/enter.
   long_press (x, y, duration_ms default 800) and drag (path of 2..50 [x, y]
   points, or x/y/to_x/to_y, duration_ms default 600) hold the finger down.
   key accepts names (home, back, enter, menu, app_switch, tab, escape,
   delete, dpad_*, volume_*, page_up/down…) or any Android keycode; power and
   sleep are blocked unless the operator allows them (forbidden error).
   wait_for(text or element, timeout_ms ≤ 30000) polls the UI source and
   returns found or timed_out instead of fixed sleeps.
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
   ADB types printable ASCII. For Unicode or %, use text with replace=true: it
   sets the focused regular field's whole value through accessibility and
   verifies it (not for password fields). Before typing, the field must match
   expected_bounds exactly; afterwards it may have grown to wrap the text, and
   the result's bounds field gives its new box (use it for later steps).
6. `mobile_app` installs runner-local APK/IPA artifacts, launches/stops apps by
   package/bundle ID, and uninstalls only with confirm=true. Mobile does not build
   or sign artifacts. Files must be in the runner's configured artifact directory.
   install with source=store or beta drives the Play Store, App Store or
   TestFlight (app_id for Play, store_id for the App Store, app_name or store_id
   for TestFlight). It returns once Install/Update is tapped; poll
   action=status until installed=true. Phones must already be signed in.
7. Close sessions when done, or renew before expiry. Closing preserves phone data.
   To end at once, mobile_session(action=stop, session_id or device_id or
   all=true, reason?) cancels the running action, lifts any finger, ends
   streams and recordings and records end_reason=stopped. Nothing else runs on
   a stopped session; an action it interrupted returns session_stopped, which
   is not a phone or host failure, so do not retry or alert on it. If the phone drops off USB, its session becomes paused
   (actions return device_unavailable; it keeps the phone) and resumes when
   the phone returns, or ends as device_lost after the offline grace period.
   Sessions are exclusive across projects; do not create a second controller.
   For a device with keep_awake capability, mobile_power(status, session_id)
   returns keep_awake and scope. Android ADB uses scope=while_charging; native
   iOS uses scope=always (Auto-Lock Never, including on battery). Use action=set,
   enabled=true only when requested: this changes a persistent phone preference,
   kept after closing the session. enabled=false restores Android's normal idle
   behavior or the iOS timeout saved when enabling (30 seconds if none was saved).
   iOS reads/changes visit Settings and restore the known foreground app or Home;
   stop recording first, do not poll while testing. Unlock the iPhone and turn
   off Low Power Mode if requested by the error. Unsupported Settings languages,
   layouts or device policy return an error; do not guess coordinate taps.
   Android uses its normal timeout on battery. Manual locking still works.
8. For repeatable testing, close interactive sessions first and call
   `mobile_test_run` with device_id and 1..50 steps. Steps have action, optional
   label and params. The test owns its session and captures per-step screenshots.
   Poll `mobile_test_get` with run_id or await mobile.test.completed. Cancellation
   is available through mobile_test_cancel.
9. assert_text/wait_for_text match literal UI/HTML source, not OCR/visibility.
   Treat infrastructure errors, unavailable devices and assertions separately.
10. Live view: mobile_stream(session_id) gives screenshot polling details;
    mode=video on Android ADB with scrcpy gives a WebSocket url with a
    5-minute token (renew by passing token) streaming H.264 with scrcpy's
    12-byte frame headers, and accepting JSON input: {type:"touch", action:
    down|move|up, x, y} (0..1), {type:"key", key|keycode}, {type:"text"},
    {type:"keyframe"}. The panel plays it. Avoid
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

14. Shared phones: mobile_device(action=baseline_capture) records installed
    apps, accounts (type and name), key settings, free storage and download /
    screenshot counts. verify_clean lists differences. clean with confirm=true
    uninstalls apps added since the baseline, clears the clipboard, deletes
    downloads and screenshots added since the baseline (older files stay),
    force-stops third-party apps (they get no notifications until opened
    again) and re-verifies; clear_data names
    baseline apps whose data to wipe. It is not a factory reset; only run clean
    when the user asked, because it deletes files on the phone.
15. Events: mobile.session.started/ended (end_reason closed, stopped, expired,
    device_lost, interrupted, with duration_ms) and mobile.device.online/offline
    are always published. mobile.action.performed is published only when the
    operator sets action_events=all; typed text appears as a length and secrets
    as [secret].

If an Appium creation outcome is unknown, stop the orphaned session on its host
before explicitly confirming mobile_session(action=recover,confirm=true).

## Android password fields

Use Actors' encrypted, context-bound password references for actor logins. Never
put passwords in definitions, presets, run inputs, screenshots or recording
scenarios. Private Android observation is mobile_observe(source=true,image=false);
it requires Android ADB and fails without a screenshot fallback.

Pass secret_ref (a reference name the operator configured in Mobile's
mobile_secrets setting) instead of password whenever one exists: Mobile looks
the value up itself and you never see it. Ask the operator for a reference name;
never ask the user to paste a password into chat. A reference may be bound to a
package, field and phone; secret_unavailable means it is missing, expired or
belongs to another project, and forbidden means this screen, field or phone is
not the one it is bound to. Do not retry with a literal password.

The secure_text action receives a password only at runtime, plus exact package,
resource ID (empty for anonymous fields), bounds, native_required screen markers
and native_values regular-field guards. It requires a live exclusive session,
an empty focused password EditText, and no active/finalizing recording. Input
uses a private stdin pipe and Android accessibility; only secure_input=true and
verified=true confirms entry. Failures require private inspection, never blind
replay. Confirm the service's signed-in account separately; stop at challenges.
