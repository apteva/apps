# Telephony 0.12.46

Fixes Calls and network-check requests blocked by the dashboard Content Security
Policy when Telephony runs on a managed remote backend. The active execution
registers its exact managed HTTPS browser/media/signaling origins using the SDK;
this requires the new `platform.dashboard.connect` permission and Server support.
Reload the dashboard after upgrading to receive its updated security policy.
Local placement clears the prior registration; standby and fenced executions
cannot replace it.

In automatic transport mode, network-blocked direct GET requests can fall back to
the authenticated gateway, with a short cooldown. Explicit direct connections,
authorization failures, aborted reads, and writes are not retried through it.
Failed initial call reads display “Call list unavailable”, rather than a false
zero; subsequent failures mark retained counts stale.

Audio Health now includes call-observation, issue-rate, and discard charts that
follow the time picker and all call filters across the complete matching cohort.
Calls are grouped by latest audio observation; retained counters are cumulative
per call, not exact incident timestamps or measured speech loss. SIP and browser
discards remain separate and missing telemetry remains unknown. Chart queries
are opt-in, use compact summaries, have a five-second deadline, and return at
most 60 intervals. Widgets do not request the extra aggregation.

Validation covers remote origin registration and ownership fencing, a real
browser CSP block and gateway recovery, unavailable-versus-empty call lists,
chart filters, pagination independence, project isolation, boundary timestamps,
missing telemetry and query cancellation. Full Go and frontend suites, focused
race checks, browser regressions, typechecking, vet, release consistency and a
Linux amd64 build are required before publishing.

Media processing, routes, audio settings and deployment resource policies are
unchanged.
