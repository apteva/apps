# Audio health overview and dashboard widget

Introduced in Telephony 0.10.3, with widget presentation and SSE improvements in
0.10.4. The release retains all earlier Telephony functionality.
Only Telephony is changed.

## Using the app

Open Telephony → **Audio health**. The operator view covers the selected project,
with filters for last-report time window (one hour, 24 hours, seven days or a
custom range of up to 31 days), provider, current/last adviser, destination,
audio stage, observation type, call state, call ID, number or destination name.

The totals cover **all matching indexed calls**, independently of pagination.
Current degradation excludes ended calls and telemetry older than 20 seconds.
Missing telemetry, stale telemetry and explicitly inactive browser audio are
labelled separately. Historical observations include background scheduling pauses
and reconnections; these are not proof of audible cuts. The time filter selects
calls by latest recorded telemetry time. Counters remain cumulative per call,
including earlier owners after takeover; they are not per-adviser or per-window
loss totals. Adviser display labels use routing destination names and the existing
issuer-scoped user identity; Telephony does not fetch external CRM profiles.

Expand a row for stage health and detailed metrics. The nested recorded JSON
provides peer hash/epoch, session events, RTT samples, close codes/reasons,
AudioContext transitions, queue/drop/sequence measurements and carrier timing.
Counters from different observation boundaries are displayed separately rather
than summed as disjoint audio loss. RTT is browser ↔ Telephony, not mouth-to-ear
latency. Providers without source timing cannot supply measurements they do not
send.

Recent project alerts show the latest 20 persisted correlation/recovery events
in the selected time window, respecting provider/stage filters. Other filters
apply to the call list only. Events created before this feature are not
retroactively reconstructable; existing per-call diagnostics are backfilled.

## Dashboard

**Telephony audio health** is registered using the same app-owned `ui_components`
contract as CRM's Customer inbox: project visibility, `dashboard.home`, half/full
sizes, suggested placement, settings schema and refresh topics. Add it using the
dashboard's existing widget picker; no dashboard or CRM code is modified and no
user's dashboard layout is overwritten.

Settings: report window (one hour / 24 hours / seven days), optional provider,
and 3–12 recent calls. The widget highlights issue badges and separate drop,
reception-gap, sequence and reconnect measurements; call-ended status does not
hide historical errors. It contains time/problem filters and optional provider,
adviser, direction, state and search filters, pagination and expandable metrics.
It has no navigation links.

The widget and Audio health view reuse the host's shared, authenticated project
SSE channel. Outside the dashboard they subscribe to the app event API with a
bounded reconnect budget. App/project/installation/topic filtering prevents an
unrelated event from refreshing this view. The five-second indexing worker emits
one `telephony.audio.reports.changed` hint per project/batch **after commit**;
rapid hints are coalesced before fetching the indexed endpoint. No media frames
are added to the event bus. Connection/reconnection and tab resumption reconcile
with durable state. A visible-tab 60-second reconciliation covers missed hints,
staleness and rolling time windows; standalone SSE failure restores 30-second
widget / 15-second view polling. Request errors and timeouts remain visible,
with last-success timestamps instead of suggesting stale data is current.

## Storage, performance and access

- `GET /audio-health` is an authenticated, project-scoped **operator** endpoint.
  Delegated application-user tokens cannot enumerate project diagnostics. Their
  existing authorized per-call APIs and softphone functionality are unchanged.
- `telephony_audio_reports` stores indexed per-call summaries. Diagnostic writes
  enqueue only a call primary key; the existing five-second telemetry worker
  computes at most 100 summaries in a FIFO batch, with a two-second deadline.
  Summary work runs outside microphone/carrier frame handling.
- Atomic processing preserves pending work if a transaction fails and does not
  lose concurrent diagnostic updates. Deleted calls remove their summaries;
  current call status/owner are joined rather than copied into stale summaries.
- Migration 040 queues existing human-call records once. The UI shows the pending
  count while background backfill/update work is incomplete. A large history can
  take multiple ticks to finish; polling does not scan/decode it repeatedly.
- Reads use project/time/provider/issue indexes and bounded 100-row pages. Filter
  options are limited to 500 distinct values per field. Alerts are persisted in
  a separate indexed history, using the existing emission/correlation semantics.
- Raw browser peer IP addresses are not added. Existing diagnostic retention and
  process-scoped HMAC semantics remain. The view does not restore absent browser
  reports or missing audio, configure external notifications, or activate any
  installation.

## Local verification (6 October 2026)

- Full final Go regression suite: **662 cases/subtests passed**, zero failures;
  two live Twilio cases skipped.
- **147 frontend/audio tests passed** (80 frontend + 67 audio), including four
  dashboard URL/filter/settings regressions.
- Focused race checks passed for dashboard indexing, concurrency, telemetry and
  diagnostic persistence, without race reports.
- Three Chromium monitor/widget scenarios passed: filters, pagination, expanded
  diagnostics, HTTP failure notices, widget scoping and deep links.
- Real-media headless and Calls panel scenarios passed. Opening Audio health
  during an active call preserved audio and existing call controls.
- Existing-report upgrade/backfill, failed indexing with queued retry, project
  isolation, delegated-user denial, current/stale/ended states and issuer-scoped
  adviser filtering passed. Counts/pagination cover more than 100 matching calls.
- A 3,641-report local dataset averaged **22 ms** for complete aggregates, one
  page and filter options (five queries). This is local evidence, not a capacity
  guarantee for every installation.
- App/widget bundles, typechecks, Go vet/build and manifest parity passed. The
  pre-existing missing disk declaration for `/call-control-settings` now matches
  the embedded manifest. The shared headless audio client, Worker/worklet and DSP
  were not changed; prior network benchmark evidence remains in the repository.
  No new live-carrier or network-quality benchmark is claimed for the dashboard.

Verification was local. Publishing does not activate installations. Production
and staging, routes, numbers, carrier settings and dashboard layouts were not
changed. No live calls were made.

## Telephony 0.10.4 verification

- 663 Go cases/subtests and 154 frontend/audio tests passed. Two live Twilio
  cases were skipped.
- Four Chromium dashboard scenarios passed, including issue emphasis, filters,
  details, absence of widget navigation links, scoped SSE updates, coalescing,
  stable subscription during filtering and disconnect/error handling.
- Post-commit notifications preserve project isolation and the 100-call batch
  ceiling; idle batches and failed transactions do not emit changes.
- Three completed synthetic calls were displayed in the local installation.
  A changed dropped-audio counter appeared through actual local SSE within four
  seconds without a manual refresh. The carrier-gap filter selected only its
  matching test call.
- The shared headless client, media handlers, Worker/worklet, DSP, routing and
  carrier commands are unchanged. No new live-carrier benchmark is claimed.
