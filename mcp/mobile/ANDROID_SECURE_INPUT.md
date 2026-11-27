# Android password entry

`mobile_observe(source=true, image=false)` returns the bounded accessibility tree
without capturing an image. It requires Android ADB and the bundled collector;
there is no screenshot or legacy collector fallback. Password text and descriptions
are omitted by the collector.

`mobile_action(action=secure_text)` requires exactly one of `secret_ref` or
`password`, plus `expected_package`,
`expected_resource_id` (may be empty for Compose), `expected_bounds`, 1–12 exact
`native_required` markers and 1–12 `native_values` regular-field guards. Call it
through a live exclusive device session. Active and finalizing device recordings reject entry.
The helper waits briefly for the accessibility root, validates all guards, and
requires exactly one focused, visible, enabled, editable password EditText with
no existing text. It uses Android's ACTION_SET_TEXT and checks the resulting
value privately. Input is single-line, bounded and passed only on stdin; argv,
helper files, stdout, stderr and errors never contain the password. Only the
fixed positive `secure_input=true, verified=true` receipt confirms delivery.
Failures require private inspection before another attempt; input is never
replayed automatically. This confirms entry, not successful service login.

## Password references

Prefer `secret_ref`. The operator stores passwords in the encrypted
`mobile_secrets` setting:

```json
{"<project_id>": {"bank-login": {"value": "...", "package": "com.example.bank",
  "resource_id": "com.example.bank:id/password", "device_id": "R5GYC4D9S5M",
  "expires_at": "2027-01-01T00:00:00Z"}}}
```

`value`, `package` and `expires_at` (RFC3339, in the future) are required.
Mobile resolves the reference inside the sidecar, so the value never appears in
the agent's context, Core tool telemetry, app-to-app calls or Mobile's events.
The entry's package and optional field fill `expected_package` and
`expected_resource_id`; a call naming different ones is refused. An optional
`device_id` (inventory ID such as `android:R5GYC4D9S5M`, or the serial) limits
the reference to that phone. References are per project; another project's
names are unavailable. Turn `allow_literal_secure_text` off to refuse
`password` entirely.

Rebuild both bundled helpers with:

```sh
python3 android-source/build.py --sdk "$ANDROID_SDK_ROOT"
```

The reproducible DEX jar is versioned alongside its Java sources. No APK,
keyboard replacement, clipboard write, root access or accessibility-service
suppression is needed. The helper disconnects its automation session on exit.
