# Guarded Android native input

Check the exact foreground package, visible screen markers and unique target
before native input. `native_values` asserts exact values of unique fields,
such as the active account header. `native_read` returns only a selected,
bounded regular field. Password fields are rejected.

ADB text replacement checks the focused package/resource/current value before
clearing, then verifies the resulting text. It accepts printable ASCII except
percent; Unicode and multiline input require a structured driver.
Replacement supports explicit empty text for clearing, with no empty `input
text` command. Missing text is rejected. Fields without a resource ID require
the expected focused EditText class and exact observed bounds as well as the
package and current value. All replacements are read back before confirmation.

Released in Mobile 0.6.0. The iOS native guard implementation remains supported.


Android source collection uses a bounded, read-only `app_process` helper instead
of waiting for UI animations to become idle. It installs no APK and uses
`FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES`; the connection is disconnected after
each read. A stock collector fallback remains available, but missing or partial
trees fail closed. Password text and descriptions are omitted by the helper.

Rebuild its embedded artifact with a JDK and Android SDK:

```sh
python3 android-source/build.py --sdk "$HOME/Library/Android/sdk"
```
