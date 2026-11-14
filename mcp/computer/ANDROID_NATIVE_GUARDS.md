# Guarded Android native input

Check the exact foreground package, visible screen markers and unique target
before native input. `native_values` asserts exact values of unique fields,
such as the active account header. `native_read` returns only a selected,
bounded regular field. Password fields are rejected.

For repeated Android form IDs, use an exact direct-child path such as
`com.example:id/bio > com.example:id/form > android.widget.EditText`.
Paths are bounded to eight segments and must identify one visible enabled
target. Disabled layout containers do not disable enabled Android children;
the actionable target's own enabled state is checked.

ADB text replacement checks the focused package/resource/current value before
clearing, then verifies the resulting text. It accepts printable ASCII except
percent; Unicode and multiline input require a structured driver.
Anonymous Android edit fields are bound to the freshly focused class, bounds
and current value. `native_clear` explicitly restores an empty field; it never
interprets an omitted replacement value as permission to erase text.

Released in Computer 0.12.0. The iOS native guard implementation remains supported.
