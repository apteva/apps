# Telephony 0.18.0

Browser calls reconnect sooner after a network fault, and the next incident can
be read from stored evidence: what stopped first on each call, and what each
reconnection attempt reached. Two ways of keeping audio through a fault ship
turned off.

## On after upgrade

- **Reconnect limits.** The first automatic retry now gives up on a socket after
  4 s and on an audio path after 5 s, the second after 6 s and 8 s. Later retries
  and the first connection keep the full limit. Healthy setups finish within
  1.7 s, and on 2026-11-04 attempts that had not finished in that time mostly hung
  until 18 s while the next connected in 1 s.
- **Recovery record.** Each browser recovery posts one record over HTTP: what
  triggered it, the last packet age, whether the browser was online, whether
  Telephony and the page host answered, and the stage each attempt reached.
  A healthy call sends nothing. WebRTC only, not the WebSocket audio transport.
- **Setup timings.** The server stores offer, answer and connected or failed
  times for each attach.
- **What stopped first.** Each disconnect now reads as audio, signaling, both or
  neither, computed when read from fields already stored, so older events work too.
- **Incident view.** A collapsed section on the audio dashboard and the read-only
  tool `telephony_audio_incident`: per call, what stopped first, when it came back,
  which calls stayed healthy, alerts and network checks, for up to 6 hours. It
  loads only when asked.
- **Network checks.** When 3 or more browser calls drop within 15 s, the server
  opens a TCP connection to each target, at most once every 5 minutes, and stores
  the result for 7 days. Nothing is sent. The default targets are the platform
  gateway, `sip.telnyx.com:5060` and `1.1.1.1:443`. Change them or set `off` with
  `audio_incident_probe_targets`.
- **SIP hangup.** A BYE that gets no reply is retried for up to 10 s, in the same
  dialog. Any SIP response is final. Other calls are not affected. A 481 after a
  lost try counts as ended, because the first BYE may have arrived.
- **False alarm.** Time spent reconnecting is no longer counted as a main thread
  pause in audio health.

## Off until enabled

Each applies to new browser connections, per install.

- `softphone_webrtc_path_recovery`: after 3 s with the audio path disconnected,
  the browser asks for an ICE restart on the same connection. If signaling does
  not answer and audio has stopped, it reconnects at once instead of waiting.
- `softphone_webrtc_handover`: when signaling drops while audio is still flowing,
  the server keeps the current audio for up to 20 s while the browser sets up a
  replacement, then moves the call in one step. It runs only while RTP is arriving.
  The replacement holds a separate grant that opens only the WebRTC attach and TURN
  refresh, and an ordinary attach cancels it. Revocation, takeover, lease expiry
  and hangup still stop audio at once. If the replacement fails, ordinary recovery
  runs.
- `audio_telemetry_local_proxy_cidrs`: empty by default. Set it to `127.0.0.1/32`
  or `::1/128` only when a proxy on the same server connects directly to Telephony
  and overwrites `X-Forwarded-For`. Audio events then store the real browser
  address, which is personal data, so this is a privacy decision.

## Evidence

- Handover in real Chromium against the real server, signed in as an application
  user through the Auth app, with only the signaling TCP socket cut: the longest
  gap between received audio packets was 49 ms and 86 ms over two runs on loopback,
  the screen stayed live throughout, and afterwards audio reached the speaker and
  the carrier on the new connection. Loopback says nothing about gaps on a real
  network.
- ICE restart: real Pion on both ends keeps the same connection and recovers.
  The browser side of the stall detection ran against a fake peer connection, not
  real Chromium.
- SIP hangup: real sipgo dialogs, inbound and outbound, with the first BYE left
  unanswered: a second BYE goes out in the same dialog and the hangup reports it
  confirmed.
- Revocation during a hold, candidates fenced by a takeover or an ordinary attach,
  a revoked user, an expired candidate, lease expiry and call end each have a test
  against real Pion.
- Full suite 1,412 Go tests and 483 browser unit tests, the race suite (none
  retried), 11 real-browser surfaces including 40 s, 90 s and 120 s outages, and
  the network, auto-answer, managed-media, call inspector and call statistics
  suites all pass.
- Three timing tests failed in one run each on a machine at load 25 or more and
  passed on every rerun: `TestNetworkBrowserDirectVersusRelayRealPacketLoss/impaired-direct`,
  `TestRTCTimelineRecoveryReachesSIPWire/delivery500` and
  `TestSIPRefreshMissingACKAndExpirySendBYE`. None depends on new code, except that
  the last sends a BYE, whose first try is unchanged.
- The `application-user` browser surface had been failing since 0.13.0, because its
  Auth URL was on loopback. It now goes through the gateway, as production does.

## Limits

- Not tried on a real or staging call. Upgrading also brings 0.17.1 and 0.17.2
  behaviour to any install still on 0.17.0, and those were not tried on a real call
  either. Upgrade outside calling hours.
- The cause of the 2026-11-04 incident is not confirmed. This release records more
  about the next one. It does not prevent a network fault.
- A path that needs more than about 14 s to connect now fails the short attempts,
  and the 30 s window usually ends the third. It then connects through the slower
  recovery loop, up to about 30 s later than before.
- During a carrier outage a hangup can wait up to 11 s for a reply, up from about
  3 to 4 s. Other operations on that call wait with it. Other calls do not.
- After a BYE is sent on an outbound call, sipgo stops matching that dialog. A BYE
  from the carrier that crosses a retry can get a 481. The retry still ends the call.
- Handover is for application users. A trusted operator's first session has no
  generation, so its first drop takes ordinary recovery.
- Handover needs WebRTC. DTMF sent in the moment between the signaling drop and the
  swap is lost, because the old socket is gone.
- The browser client ships with this app. An application that bundles its own copy
  needs a rebuild to get the browser changes.
- With both settings off, the reconnect limits, recovery records, network checks
  and BYE retry above still apply.
- ICE restart has not run in real Chromium.

Adds migration 073: three columns on `telephony_media_sessions` with defaults, one
index on `telephony_browser_network_events`, and the table `telephony_audio_probes`.
Adds one tool and four settings. No new work per packet or per timer tick on a
healthy call.
