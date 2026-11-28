# Guarded Android native input

Check the exact foreground package, visible screen markers and unique target
before native input. `native_values` asserts exact values of unique fields,
such as the active account header. `native_read` returns only a selected,
bounded regular field. `native_read_list` returns the visible text of one
named scrollable list and nothing outside it; it never taps, rechecks the
screen markers and value guards on every screen, skips text-entry and password
fields, and is bounded to 50 rows, 40 strings per row, 64 KB and about 25 s.
Regular typing, replacement and reads reject password fields. Secure input uses the separate private path described in [the secure-input notes](internal/browser/mobile/ANDROID_SECURE_INPUT.md).

For repeated Android form IDs, use an exact direct-child path such as
`com.example:id/bio > com.example:id/form > android.widget.EditText`.
Paths are bounded to eight segments and must identify one visible enabled
target. Disabled layout containers do not disable enabled Android children;
the actionable target's own enabled state is checked.

ADB text replacement checks the focused package/resource/current value before
clearing, then verifies the resulting text. `native_type` accepts printable
ASCII except percent, up to 256 bytes. `native_replace` accepts up to 4096
bytes of any text without line breaks or control characters (tab allowed);
Unicode or text over 256 bytes needs Mobile 0.11.0+ (`replace_text_resizable`),
which lets the field wrap and change height. After input, Computer confirms the
one focused field is the same field (same class, and the same resource ID, or
for an anonymous field the same left and right edges with an overlapping
vertical range) and holds the new text, so a field found by a placeholder that
disappears still verifies. Other screen markers and account guards are
rechecked.
Anonymous Android edit fields are bound to the freshly focused class, bounds
and current value. `native_clear` explicitly restores an empty field; it never
interprets an omitted replacement value as permission to erase text.

Released in Computer 0.12.0. The iOS native guard implementation remains supported.
