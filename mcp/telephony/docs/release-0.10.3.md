# Telephony 0.10.3

Cumulative release retaining all Telephony 0.10.2 functionality, including
routing, inbound protection, provider/compliance support, multi-carrier outbound
calls, AI handoffs, configurable call duration, listening, private coaching and
resilient browser media recovery.

- Add an **Audio health** tab with project-wide filters for report time window,
  current/last adviser, provider, destination, audio direction, observation type,
  call state, call ID and number. Totals cover all matching indexed calls,
  independently of pagination.
- Distinguish current degradation, ended calls, stale/missing telemetry and
  inactive browser audio. Expand a call for timings, losses, connection/session
  events, peer hashes and complete recorded diagnostics.
- Add a project-scoped **Telephony audio health** dashboard widget, using the
  same app-owned widget contract as CRM. Settings control time window, provider
  and number of rows; links open the corresponding filtered Audio health view.
- Persist recent correlated audio alerts/recoveries for inspection. Compute
  indexed summaries in bounded background batches, preserving concurrent
  updates and retrying failed indexing without changing authoritative call data.
- Backfill existing human-call records and display indexing progress. Keep
  project-wide diagnostics restricted to operators; delegated application-user
  permissions and call controls remain unchanged.
- Align the existing `/call-control-settings` disk manifest declaration with
  the embedded manifest. Keep the standalone app SDK pinned to v0.95.0.

## Verification

662 Go cases/subtests, 147 frontend/audio tests, focused race checks and three
Chromium monitor/widget scenarios passed. Real-media headless and Calls panel
scenarios passed; opening the monitor during an active call preserved audio.
Builds, typechecks, Go vet and manifest parity passed. Two live Twilio tests
were skipped.

A 3,641-report local dataset averaged 22 ms for complete aggregates, one page
and filter options. This is local evidence, not a capacity guarantee. The shared
headless audio client, Worker/worklet and DSP are unchanged from 0.10.2; prior
network benchmark evidence remains in the repository. No new live-carrier or
network-quality benchmark is claimed for this dashboard release.

The time window selects calls by latest report time; counters are cumulative
per call and may include previous owners. Background pauses/reconnections are
observations, not proof of audible cuts. Historical alerts cannot be reconstructed
before this feature. See [behavior and verification](audio-health-dashboard.md).

Publishing does not activate production or staging. Carrier settings, routes,
numbers and existing dashboard layouts are untouched. No live calls were made.
