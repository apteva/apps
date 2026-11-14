# Guarded Android native input

Check the exact foreground package, visible screen markers and unique target
before native input. `native_values` asserts exact values of unique fields,
such as the active account header. `native_read` returns only a selected,
bounded regular field. Regular typing, replacement and reads reject password
fields. Secure input uses the separate private path described in [the Instagram notes](examples/instagram.md).

Android direct-child selectors scope repeated form controls to an exact field.
`native_read` records only the selected value in the normal run dataset, after
Computer returns a successful receipt. `native_clear` is an explicit input
action with an exact target and screen guards; it rejects replacement text.

ADB text replacement checks the focused package/resource/current value before
clearing, then verifies the resulting text. It accepts printable ASCII except
percent; Unicode and multiline input require a structured driver.

Released in Actors 0.7.0. The iOS native guard implementation remains supported.

## Local Instagram acceptance, 2026-11-14

The single saved Instagram actor retains all 44 original iOS operations and
adds Android variants bound to the Samsung device context. Android profile
operations check the exact account header before opening an editor. A live
wrong-account attempt failed before opening the editor.

On the dedicated test account, the actor read an empty bio, saved a temporary
bio, reopened the editor to read the persisted value, then cleared and read it
back as empty. `read_profile_bio`, `set_profile_bio`, and `clear_profile_bio`
return the selected bio value in the run dataset. ADB bio input currently
supports single-line printable ASCII excluding percent, with Instagram's
150-character limit. Clearing requires an explicit confirmation input.

`set_profile_picture` checks the account, an explicit selected-photo marker,
and the current picker controls before saving. It allows bounded time for the
asynchronous save, returns to the verified account profile, and retains review
and outcome screenshots. The existing synthetic test image was visible as the
profile picture after switching away and back. The image was staged separately
and verified by checksum; this does not add generic Mobile file-upload support.
Photo access was restricted to the selected test image.

Account switching passed in both directions. Inspect the current account list
before scrolling: Instagram moves recently used accounts near the top.
The actor supports guarded scrolling in either direction and fails if a target
is absent. Notification and location access were denied; contact syncing was
skipped. The phone was returned to the original main account.

An earlier photo-save verification timed out and remained a failed run.
Recovery observed the current state separately, without replaying a save.
The later successful run used the current picker layout and a bounded settling
wait. A completed navigation step alone does not establish image identity;
the saved screenshot evidence was also inspected during this acceptance run.
