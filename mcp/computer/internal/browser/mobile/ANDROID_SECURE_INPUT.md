# Guarded Android secrets

`native_type_secure` requires a private (`extraction_only`) Computer session and
`observation=none`. It is advertised only when Mobile supplies both `secure_text`
and `source_only`. Regular typing and replacement continue rejecting password
fields. Use `password` for the secure input payload.

Exact app, markers and regular-field value guards are checked from image-free
source before focusing a unique password EditText. The selector
`android.widget.EditText[password=true]` supports Compose fields without IDs;
multiple password fields remain ambiguous. Mobile independently validates the
focused field's identity/bounds, the original screen guards and an empty field
before input. A missing or negative Mobile confirmation fails the action without
falling back to regular typing. Computer returns success only after the private
input receipt. Service login and account identity need separate actor checks.
