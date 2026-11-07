# Call statistics

Open **Calls → Statistics**. Choose Today, Last 24 hours, Last 7 days or a custom
range up to 31 days. The timezone is the browser's IANA timezone. Click a chart
interval to select its calls, then a call to open live or retained diagnostics.
The detail focus can show audio problems, missing telemetry, failed calls or
missed inbound calls. Detail filtering does not change period denominators.

## Cohort and evidence

GET /audio-health?view=statistics uses existing operator/project authorization.
The half-open [from, until) cohort contains calls **started** in the range.
Counters are retained lifetime/session observations as of the read, not only
audio occurring within that range. The previous comparison is the immediately
preceding equal-duration range. Active-call durations continue growing.
Connected duration is elapsed time since answer, not voiced speech; missing
duration endpoints are excluded from averages.

A problem means at least the selected threshold (20/200/1000 ms) at one
discard/concealment/playback-gap boundary, or reported packet loss/transport
errors. The rate divides affected calls by observed calls; telemetry coverage
uses all calls. Unknown telemetry is never interpreted as perfect audio.
Presence of telemetry does not imply every media leg was measured; each audio
indicator reports its own coverage.

SIP receive discard, concealment, browser playback/capture, server ingress and
carrier-send counters remain separate. They can overlap and include silence.
SIP send-discard rate uses sum(dropped)/(sum(sent)+sum(dropped)) from paired
same-pacer observations. Old reports missing either counter are excluded.
No exact audible-cut percentage or end-to-end delay is inferred.

## Bounded reads

Migration 065 indexes (project_id, unixepoch(placed_at), id) and queues retained
summaries for background enrichment. Queries read compact summary fields, not
raw media or diagnostics. One transaction consistently reads metadata, owner
and summary rows. A five-second query deadline returns an error instead of
partial success. Pending summary counts explicitly mark provisional rates.

All period totals and histogram counts are complete. Calls use 50-row keyset
pages. Dimension lists expose the top 50 plus omitted/total counts; cards show
the top 10. JSON export contains the displayed snapshot and current call page,
not every paginated call. Adviser filters preserve the full principal key.
Polling runs only while visible, at 30-second intervals, without overlap.
Inspector polling remains independently scoped to the selected call.

## Repeatable checks

- GOWORK=off go test -run '^TestCallStatistics' -count=1 -v .
- GOWORK=off go test -race -run 'Test(CallStatistics|AudioDashboard|AudioHistory)' -skip '^TestCallStatisticsDSTAndIndexWithLargeHistory$' .
- bun run test:call-statistics
- bun run test:frontend
- bun run typecheck

Fixtures cover half-open boundaries, timezone offsets/DST, current versus
previous cohorts, keyset ties, project/owner/number isolation, unavailable
telemetry, paired rates, pending reports and database errors. The performance
fixture exercises both 20,000 old calls and 20,000 in-window compact summaries.


Run the large-history performance fixture without race instrumentation. The
race-instrumented SQLite interpreter can exceed the production five-second
query deadline; the performance run keeps that deadline intact. The remaining
statistics, dashboard and history checks run separately with the race detector.
