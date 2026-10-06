# Telephony 0.10.4

Cumulative release retaining all Telephony 0.10.3 features and earlier routing,
provider/compliance, inbound protection, multi-carrier outbound calling, AI
handoff, call duration, listening, private coaching and browser media recovery.

- Make Audio health widget problems visible with red/amber issue badges and
  separate stage measurements. Ended calls retain their recorded issue history;
  current live degradation remains separate. Show readable seconds/milliseconds.
- Add time/problem filters, optional provider/adviser/direction/state/search
  filters, pagination and expandable diagnostics. Remove widget navigation links.
- Reuse the host's authenticated project SSE subscription. Standalone views use
  the app event API with scoped events, duplicate protection and bounded retries.
  Coalesce refresh hints, reconcile on reconnect/tab resume, retain periodic
  recovery and show request errors with last-success timestamps.
- Emit bounded project-scoped `telephony.audio.reports.changed` hints after
  indexing commits. Failed/idle batches do not emit changes. Notifications run
  outside media frame handling.

## Verification

663 Go cases/subtests, 154 frontend/audio tests and four Chromium dashboard
scenarios passed; two live Twilio cases skipped. Build/typecheck and manifest
parity passed. Actual local SSE updated a synthetic dropped-audio counter within
four seconds without a manual refresh; filtering isolated the expected report.

The shared headless client, media handlers, Worker/worklet, DSP, routing and
carrier commands are unchanged. No new live-carrier or network benchmark is
claimed for this widget release. See [behavior and verification](audio-health-dashboard.md).

Publishing does not activate production or staging. No carrier settings, routes,
numbers or dashboard layouts are changed. Local synthetic diagnostics are not
included in the release and no live calls were made.
