# Instagram on the paired iPhone

This example uses the released Computer and Actors iOS native screen actions.
For one actor with iOS and Android variants, see [instagram.json](instagram.json)
and [platform notes](../ANDROID_NATIVE_GUARDS.md). Use a Computer Mobile context bound to the paired iPhone, and set
`context_id` when importing the example or running an operation.

For an existing account, run `open_sign_in` from the welcome screen.
`focus_sign_in_username` focuses the username field. If iOS opens its
saved-password chooser, dismiss it to use the keyboard, or select the saved
account on the phone. With the keyboard visible and an empty field,
`fill_sign_in_username` enters the username.

Run `sign_in` with that `username` and a `password_ref` to fill the empty
secure password field and submit the form once. The install's encrypted
`browser_secrets` map must contain the reference under the project ID with
`value`, the exact saved `context_id`, `native_app` set to `Instagram`, and
an RFC3339 `expires_at`. Keep the password out of actor inputs. Complete any
sign-in link or verification challenge on the iPhone, then run `verify_account`
on the profile. Login and registration operations skip saved screenshots.

### Create an account from a signed-in profile

For the Accounts Centre path, run `create_and_switch_back` from the signed-in
profile with `source_handle`, a new `username`, and `confirm_creation` set to
`create_in_accounts_centre`. This authorizes adding the account to the
current Accounts Centre and accepting the Instagram signup terms. The actor
verifies the new profile, then returns to the original profile. It restarts
Instagram after the profile setup screen appears to leave optional permissions
untouched. If Instagram asks for verification or the run stops after submitting
the signup, check the account switcher before repeating creation.

Use the individual operations below to resume from a verified screen or to use
the contact-based signup path.

1. On the current profile, run `open_account_switcher` with its exact
   `handle`. If **Add Instagram Account** is below the visible rows,
   run `scroll_account_switcher`.
2. Run `open_add_account_menu` and `begin_linked_signup`. On **Create a
   username**, run `set_linked_signup_username` with the new `username`.
   It replaces the suggestion and verifies the field before
   `advance_linked_signup_username` continues.
3. Review the Accounts Centre choice on the phone. Run
   `choose_linked_signup_method` with the explicitly selected
   `signup_method`, either **Allow and continue** for shared Accounts Centre
   sign-in or **Use mobile number or email address** for the contact flow.
   On **Create a password**, run `set_linked_signup_password` with a
   provisioned `password_ref`, or set the password directly in Instagram.
   For email signup, set the birthday in the iOS picker and run
   `advance_email_signup_birthday` with `birthday_ready` set to
   `birthday_entered`. Run `open_email_signup`, then
   `enter_email_signup_address` with an encrypted `email_ref`.
   Verify the address on the phone before `request_email_signup_code`.
   Use `enter_email_signup_code` with a short-lived encrypted `code_ref`
   while the confirmation keypad is open. If Instagram leaves the code
   screen open, use `advance_email_signup_code`. Run
   `verify_email_signup_review` to stop at the terms screen.
4. On **Agree to Instagram's terms and policies**, run
   `create_linked_account` with `confirm_creation` set to
   `create_instagram_account` once the operator has authorized creation.
   Wait for onboarding or the new profile. If the result is uncertain,
   check the switcher for the username before retrying.
5. Use `skip_signup_profile_picture` if prompted. Use
   `review_signup_permissions` only if you're ready to handle any system
   permission dialogs on the phone. Use `decline_signup_optional_cookies`
   on the cookie consent screen. Run `open_current_profile` with the new
   handle to verify the account.
6. To switch, open the switcher from the current profile, then run
   `switch_account` with `target_handle` (the username without the leading
   `@`). Scroll to the row if needed. The actor matches the exact handle
   even if Instagram adds a badge, then waits up to 15 seconds for the
   target's editable profile to appear.

### Disable profile suggestions on a new account

Sign into the new account at Instagram.com in a dedicated saved Computer
context with the `local` backend. Run `disable_profile_suggestions` with
`web_context_id` set to that context's ID and `target_handle` set to the
account's lowercase username without the leading `@`. The operation uses
the English Edit profile page. It verifies the signed-in profile, rejects
`marcoschwartz`, turns off **Show account suggestions on profiles**, and
reopens Edit profile to check that the setting saved. This controls similar
account suggestions shown on Instagram profiles.

### Signup from the welcome screen and photo preparation

1. Run `open_instagram` to open the app. On the welcome screen with **Get
   started**, run `open_signup_intro`. On the login screen,
   run `begin_signup` with the observed entry marker, signup
   button, and expected initial form marker. This opens and checks the signup
   form. Wait for the actor to finish before entering your recovery contact,
   password, and verification code directly in Instagram.
2. At the public name and username forms, use `fill_signup_name` and
   `fill_signup_username` with the exact visible form and field markers. Review
   what was typed, then continue on the phone. Complete consent and the final
   account creation step on the phone. Run screen inspections only after
   leaving any screen that shows credentials or recovery information.
3. Open the intended account's profile. Use `verify_account` with the exact
   handle as displayed by Instagram and a visible profile marker such as the
   profile edit control. Keep that account selected throughout the photo run.
4. In Instagram, open Create → Post and select the intended image. Use
   `advance_photo_picker` with distinct markers for the picker and photo
   editor. If the picker is empty with limited access, open the image in
   Photos and choose Instagram from the iOS share sheet. Run
   `import_selected_photo` with the exact handle when the format choices
   appear. The operation selects the Post format and opens the Instagram editor.
5. Run `prepare_photo_post` in the editor. It taps **Next** and checks for
   the **New post** review screen. If Instagram displays its **Sharing posts**
   notice, run `dismiss_post_sharing_notice` followed by `verify_photo_review`.
   The review check captures the preview with the **Share** button untouched.
6. To add a caption, open Instagram's caption editor on the phone and run
   `enter_caption` with an observed screen marker and the exact accessibility
   label or identifier of an empty text field. The caption is a single line of
   at most 256 bytes. Inspect the result on the phone.

Use exact, visible accessibility text for the markers. Instagram can vary the
labels across app versions, locales, and accounts. If a check fails, inspect
the screen and continue from the stage that is actually open. The actor does
not include a Share action or replay a failed Mobile step. The operator selects
and confirms the account and photo during this pilot.
