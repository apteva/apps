# Telephony 0.12.19

Includes all SIP clock recovery, regional TLS hangup, runtime placement and browser
preparation fixes through 0.12.18. No production or staging deployment is part of
this release.

## Remaining code corrections from the 13 October audit

- Declare `platform.threads.write` in both manifests. AI inactivity reminders use
  `SendThreadEvent` with `tel-<call>`; the Server requires this permission for any
  non-main thread. `platform.realtime.spawn` does not authorize event delivery.
  Existing installations must approve the additional permission during upgrade.
- Record bounded AI control error categories and HTTP status, and acceptance
  separately from confirmed reminder playback. Keep original reminder evidence
  through a later policy timeout. Never retain raw SDK error bodies or URLs.
  Do not replay a possibly accepted reminder; preserve the existing one-reminder,
  caller-response, ownership and maximum-duration policy.
- Separate microphone attachment discard totals into initial attachment,
  reconnection, explicit ending and hold. Retained frames carry their original
  phase; an explicit terminal/hold transition overrides it. This applies to
  decoded WebRTC and PCM/WebSocket, inbound and outbound. The historical
  `startup.dropped_ms` remains the all-phase compatibility total. New consumers
  use `phase_schema: 1` and phase totals; old reports cannot be reconstructed.
  A detach before a terminal event is reconnection, not proven hangup.
- Expose these totals in `/audio-health` summary metrics. Histories remain bounded
  to 16 drop events; counters outlive the history. No socket or database work is
  added to the audio loop, and the audio bounds/expiry rules remain unchanged.
- Record one safe client build marker during browser connection preparation and retain the safe version in full reports on
  both transports, so future incidents can identify the loaded client version.
- Record whether RTC setup has a configured TURN relay. This is configuration
  evidence, not proof that the relay is reachable or selected. Existing selected
  candidate telemetry records the actual path. No TURN service/credentials are
  created and no transport preference is silently changed.

## Remaining deployment/environment work

An app release cannot remove client TCP resets, repair a blocked UDP path without
an available TURN service, or reserve CPU away from other applications. Production
had no configured TURN relay and significant shared host contention. Those are
separate from this code correction; uninterrupted external networks are not
promised. Existing `auto` transport supports PCM fallback on failed RTC setup;
explicit `webrtc` preserves the caller's chosen transport. Live RTC recovery stays
bounded and keeps the carrier call alive.

Regression coverage includes phase accounting/legacy data, frame ownership and
queue limits, safe error persistence, no duplicate reminder delivery, required
permissions, TURN detection and browser build identity. Full suite, race tests,
browser integrations and paced mixed SIP load are release gates.
