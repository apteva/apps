# Call diagnostics inspector

In **Calls**, select any live or finished call. The inspector appears immediately below the selected call header. **Expand** opens the same inspector in a modal; Escape or Close returns focus to Expand. Audio Health rows and existing deep links use the same inspector.

- **Overview**: reported stage states, key timings, browser and SIP history charts, and AI reply timing when present.
- **Browser**: WebRTC candidate/relay transport, RTT, jitter, receive loss, replacement audio, bitrate, microphone delivery/queue drops, reconnects and available server runtime observations.
- **SIP / carrier**: codec, RTP packet counts/loss/reordering, arrival/send gaps, supported kernel drop counters, buffer and send-pacer age/discard/concealment, signaling errors and carrier WebSocket observations.
- **AI / Core**: Telephony-observed first-audio/dispatch delays, reconnects and staging. These are not isolated model inference timings or confirmed audible playback.
- **Timeline**: latest 100 retained browser, SIP and Core events. Clear receipts distinguish remote confirmation from a sent clear command.
- **Export**: download the authorized diagnostic snapshot as JSON. Raw evidence is rendered only when expanded.

No microphone access, packet capture, media reconnection or settings changes are triggered by inspection. Existing operator permissions and project isolation apply. Missing measurements stay unknown; no telemetry does not imply healthy audio. Measurements are cumulative within the reporting session and can reset on reconnection. Retained history is bounded and is not a complete packet capture. Discard and replacement-audio totals can include silence and must not be added together as speech loss.

## Update policy

The initial read requests `history=none`; bounded incremental history follows. Live counters update every three seconds after the preceding request completes, with history at most every fifteen seconds except pagination/manual refresh. Only one request is in flight. Requests time out after twelve seconds and transient failures back off to thirty seconds. The inspector pauses in hidden tabs and unmounts when leaving Calls. It stops after a finished call's thirty-second settling window; manual refresh remains available. Changing call/project/installation aborts the previous read and clears its view. Access denial/not-found clears evidence and stops automatic retries.

The detail API includes additive `call` lifecycle metadata and `generated_at`. Historical observation times remain distinct from response generation time. Current SIP/browser metrics may arrive at different times; individual sections show their observation timestamps.

## Repeatable verification

From `mcp/telephony`:

```sh
bun run test:frontend
bun run typecheck
bun run test:call-inspector
env GOWORK=off go test -run 'Test(AudioDashboard|TransportHistory)' -count=1 .
```

Browser tests serve synthetic fixtures on loopback port 5298, including the real Calls panel; they do not call any carrier or deployment. Set `TELEPHONY_DASHBOARD_CSS` to the local dashboard's built `dist/style.css` to verify the actual Apteva dark/light theme. Screenshot outputs are `/private/tmp/telephony-call-inspector-{expanded,dark,light}.png`.

The tests cover live/finished selection, expansion/focus, narrow screens, exports, no microphone/call mutations, access revocation, request single-flight/timeouts/backoff, visibility/unmount cleanup, history cursors/reset/bounds, unknown-vs-zero values, timestamp freshness, units, AI leg applicability and backend scope/lifecycle metadata.

Rebuild app panel artifacts from the apps repository root:

```sh
bun run scripts/build-panels.ts --app telephony
```
