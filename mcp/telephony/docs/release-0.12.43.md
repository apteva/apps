# Telephony 0.12.43

Selecting a live or finished call now opens readable diagnostics in the Calls
panel. The same inspector is available from Audio Health. It includes browser
WebRTC and SIP/carrier counters, AI/Core reply timing, retained time-series
charts, a bounded event timeline, an expanded accessible view and JSON export.
It follows the Apteva theme on narrow and wide screens.

Current counters load first, with incremental history fetched separately.
Requests are single-flight, pause in hidden tabs, back off on failures and stop
after a finished call's settling window. Switching calls or losing access clears
old evidence. Missing and stale observations remain explicit, and transport
measurements are not presented as end-to-end audible delay. Inspection does not
open the microphone, enable packet capture, or send call-control commands.

The diagnostics endpoint adds scoped call lifecycle metadata and response time.
Existing project/operator checks remain intact. Both source manifests point to
`telephony/v0.12.43`; panel and softphone artifacts are rebuilt for this version.

Validation includes the full Go suite, targeted dashboard/history race checks,
Go vet, frontend unit tests, browser selection/expansion/export/access-revocation
and Audio Health navigation tests, typechecking, release consistency tests and
panel builds. The new browser checks are also included in CI.

Publishing this release does not deploy or restart an installation.
