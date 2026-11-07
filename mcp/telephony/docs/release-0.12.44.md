# Telephony 0.12.44

The Calls panel now includes a separate Statistics tab with date selection,
previous-period comparisons, call totals, connected durations, reported audio
problem rates and telemetry coverage. Filter by carrier, adviser, own phone
number and direction. Clicking an activity histogram interval narrows the call
list; selecting a live or finished call opens the existing diagnostics inspector.
The page supports Apteva light/dark themes, narrow layouts and JSON export.

Rates use explicit denominators. Calls without telemetry remain unknown.
Separate SIP, microphone, browser and forwarding counters are never added as
though they were independent lost speech. The SIP send-discard percentage uses
paired sent/discarded counters from the same pacer. Metrics may include silence
and are not a measurement of spoken words lost.

The backend reads indexed call-start cohorts and compact audio summaries in one
consistent read transaction, with a five-second timeout. It does not load call
recordings, packet histories or full diagnostics for statistics. Migration 065
adds the call-start index and schedules retained summaries for the existing
bounded background reindexer. Operator/project access checks remain unchanged.

Statistics polling is single-flight, every 30 seconds while visible, with
timeouts and error backoff. Losing access clears results. All media processing,
routing and phone-number configuration remain unchanged.

Validation: full Go suite, focused statistics/dashboard race tests, Go vet,
frontend unit tests, typechecking, browser statistics/inspector/Audio Health
checks, indexed large-history tests and Linux build. Browser coverage includes
chart drilldown, dates across daylight-saving changes, access revocation,
unknown/empty results and hidden-tab polling.

Both manifests and runtime source refs point to telephony/v0.12.44.
Publishing the release alone does not deploy or restart an installation.
