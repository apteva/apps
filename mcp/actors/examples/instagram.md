# Instagram with iOS and Android variants

Import `instagram.json` as one actor. Use saved Computer Mobile contexts bound to
specific devices. Supply `ios_context_id` or `android_context_id` for the chosen
platform, and select the `ios` or `android` preset. Discovery with an operation
and platform returns that implementation's input contract. Unsupported variants
fail before opening a session.

All 44 existing iOS operations are retained. The original iOS-only example and
its sign-in/setup instructions remain in `instagram-iphone.json` and
`instagram-iphone.md`.

On Android, `open_current_profile` and `open_account_switcher` require `handle`.
Inspect the current list before using `scroll_account_switcher` or
`scroll_account_switcher_up` with a currently visible account name. Instagram
reorders recently used accounts. `switch_account` requires `target_handle` and
verifies the resulting account header. Missing or ambiguous targets stop the run.

`read_profile_bio` requires `target_handle`. `set_profile_bio` additionally
requires `bio`; `clear_profile_bio` requires `confirm_clear=clear_profile_bio`.
Writes verify the active account before entering the editor, reopen the saved
field for readback, and return the selected value in the dataset. ADB replacement
supports single-line printable ASCII excluding percent; the bio limit is 150
characters. Private/password fields are not read through Android ADB.

For a picture change, stage the intended image separately on the device and
verify the staged filename/checksum. The actor does not implement generic Mobile
file delivery. First-run access operations support limiting Instagram to a
selected photo. Review the selected preview and its current accessibility marker.
Run `set_profile_picture` with `target_handle`, that exact `selected_photo_marker`,
and `confirm_change=change_profile_picture`. The workflow checks account identity,
selection and current picker controls, waits for the asynchronous save, and
retains review and result screenshots. Inspect the result image; navigation alone
does not prove image identity. Unexpected screens and uncertain saves fail without
automatic input replay. `restart_and_open_profile` is a separate recovery read.

The example's observed notification-denial, location-denial and contact-skip
operations are explicit setup choices. Adapt the example's protected-account
assertions to the operator's account policy before using it elsewhere.

Android login uses the same actor's `android` variants. From the account switcher,
scroll until “Add Instagram account” is visible, then run `open_add_account_menu`
and `open_sign_in`. `focus_sign_in_username` chooses “Use another profile” on the
saved-profile chooser. If Google Password Manager appears, explicitly run
`dismiss_sign_in_password_manager` to cancel it. Unexpected screens stop input.

Use `fill_empty_sign_in_username` when the username field is blank.
`fill_sign_in_username` requires `username` and `expected_username` (the exact
current username field value). It replaces and verifies that field. `sign_in`
requires the same `username` and a `password_ref` in Actors' encrypted
`browser_secrets` settings. Bind the reference to the selected Android Computer
context, `native_app=com.instagram.android`, and a future RFC3339 `expires_at`.
Never put a password in actor inputs, definitions, presets, or traces.

The private operation checks the username, login markers and a unique password
field before sending the secret through Mobile's private stdin pipe. Mobile
requires an empty, focused, editable password field and rechecks the app, bounds,
username and screen markers immediately before setting its text. No screenshot,
clipboard or secret-bearing ADB argument is used. Missing secure-input capability
or confirmation stops before tapping Log in. Active Mobile recordings block
secure entry. The operation verifies the resulting account profile header;
challenges, invalid credentials and unknown prompts stop for operator inspection,
without automatically replaying the password or submission. Resume the observed
save-login prompt with `finish_sign_in`, or the notification reminder with
`complete_sign_in_setup`; neither operation re-enters credentials. Login adds an
existing account; it does not create an account or bypass verification.
