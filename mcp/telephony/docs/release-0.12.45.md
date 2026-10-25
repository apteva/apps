# Telephony 0.12.45

Includes the new Statistics tab from 0.12.44: date filters, previous-period
comparisons, clickable call-activity charts, per-call diagnostics, separate
audio-loss indicators and explicit telemetry coverage.

Corrects browser-test screenshot paths to use the operating system's temporary
directory on Linux and macOS. Splits the lifecycle and efficiency race suites
into independent CI jobs so each gets its existing time budget. No tests are
removed and no production query deadline is increased.

The full application Go suite, frontend unit tests, focused race checks and
Linux build passed for the unchanged 0.12.44 implementation. Browser tests,
typechecking and release consistency checks were rerun for this packaging
follow-up. Both manifests and source refs point to telephony/v0.12.45.

Media processing, phone routes and deployment resource policies are unchanged.
