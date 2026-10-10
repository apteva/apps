# RTP send, ICE path and browser loss observations

Telephony-only local work. No staging/production calls, installations or
configuration changes, and no Core, server, SDK or carrier integration changes.
The published SDK pin remains v0.99.0 (latest origin/main tag by topology).

## Recorded evidence

`browser_audio_diagnostics.server.webrtc.rtp_send` contains:

- Cumulative send attempts/errors, total/max send duration in microseconds,
  and skipped telemetry observation counts.
- The latest 64 RTP send observations: UTC send-start timestamp, browser
  connection ID, RTP sequence/timestamp, actual sender SSRC, payload length,
  duration, outcome and ICE path revision observed at send start.
- The latest 16 send errors or writes taking at least 10 ms. This separate ring
  retains incidents after newer normal packets replace the recent packet ring.
- The latest eight selected-path changes: timestamp, connection ID, revision,
  protocol and local/remote candidate types. Path skip counts are separate from
  skipped packet observations.

A successful write means Pion's send stack accepted the packet, not proof of
browser reception, playout or intelligible speech. Sequence and RTP timestamp
wraps retain their wire values; connection ID and SSRC scope comparisons.
Normal samples are bounded, not a complete packet capture. At 50 packets/sec,
64 recent packets cover roughly 1.28 seconds; persistence snapshots can omit
older normal observations. Cumulative counters and retained incident samples
remain available independently of the recent ring.

## Actual media endpoint

`/audio-health?call_id=...` returns operator-only network events with
`event: softphone.browser.media_path_selected` and `media_path`:

- Local/remote selected ICE IP and port, canonical IPv4/IPv6 only.
- Protocol, candidate types, path revision and browser connection ID.
- `address_source: ice_selected_remote` and an observational match against the
  existing known VPN exits, otherwise `unknown`.

The endpoint is read from Pion's selected ICE pair, not HTTP headers, SDP text
or a browser-supplied address. A TURN relay is recorded as a relay; its address
is not necessarily the browser's public IP. The validated HTTP client IP,
socket-peer IP and their classification remain separate. Neither unmatched
endpoint nor HTTP address proves VPN absence.

Raw ICE addresses are excluded from ordinary call/browser diagnostics and use
the existing project-scoped, operator-only, expiring network store (seven days
by default). The path event carries the same connection ID as attachment and
reconnection events. No media token, authorization header, URL, SDP, ICE
credentials, audio payload or raw send error is recorded.

## Browser loss

The existing one-second `getStats()` sampling now computes loss deltas and
windows for both browser `inbound-rtp` and `remote-inbound-rtp`, retaining SSRC.
The six added allowlisted metrics are `receiver_ssrc`, `receiver_loss_delta`,
`receiver_loss_window_ms`, and the equivalent `remote_receiver_*` fields.

Positive deltas also produce bounded timestamped `webrtc_packet_loss` events
with `packet_count`, `ssrc`, `window_ms` and direction. Audio duration remains
zero: packet loss is not converted into an invented missing-speech duration.
First observations, new stats identities/SSRCs, counter resets and backward
sample clocks do not manufacture deltas. Reconnection starts a new baseline.

These are sampled intervals, not timestamps or sequence IDs of individual
lost packets. Browser/server wall clocks must be aligned or their uncertainty
considered when correlating events; RTP timestamps are media clocks, not UTC.
Transport summaries keep the existing bounded pacing and sample coalescing.

## Audio priority and overhead

The RTP sender updates atomic counters and attempts a `TryLock` on fixed rings.
If a snapshot owns the lock, it skips the sample, counts it and continues the
same RTP write. There is no database access, JSON encoding, logging, queue wait,
audio payload copy or new timer/goroutine in the packet observation path.
ICE collection uses the existing bounded nonblocking network collector.
Snapshots and persistence use the existing diagnostics watcher and detach path.
This cannot promise literally zero CPU cost; diagnostic observations have small
measured overhead and are shed under contention rather than delaying media.

Three local benchmark runs on Apple M1 Pro measured the complete send-observer
wrapper around a no-op write: 93.03–93.30 ns/packet, zero allocations. With the
snapshot lock held: 84.37–213.2 ns/packet, zero allocations (one noisier run).
This isolates incremental collection cost; it does not measure total Opus,
network, background serialization, SQLite or production CPU use.

## Tests

- Real local backbone signaling/ICE/DTLS/SRTP/Opus: stored sequence/timestamp
  and actual SSRC match received wire packets; selected endpoint is recorded.
- RTP continues with the telemetry ring locked; skipped observations counted.
- Fixed bounds, snapshot isolation, sequence wrap, normalized errors/timeouts,
  cumulative counters under concurrent snapshots, reconnect merge bounds.
- IPv4/IPv6 selected paths, relay labels, private endpoint persistence,
  operator-only access and project isolation.
- Loss delta baselines, positive/zero changes, stream/SSRC replacement,
  reconnection, counter reset and backward clocks, plus wire persistence.
- Existing carrier, human routing, media recovery, PCM playback and WebRTC
  constrained-network regression suites remain required.

Local verification artifacts: `/private/tmp/telephony-rtp-*.log` and
`/private/tmp/telephony-rtp-go.jsonl`. No live carrier verification is claimed.

Snapshot collection also uses `TryLock`; a busy ring returns cumulative counters
with `samples_unavailable: true`, preserving the buffered samples for a later
snapshot. This avoids adding a wait to the existing media diagnostic locks.
Reconnect merges copy bounded samples so concurrent readers never alias buffers.
A populated 64-packet/16-incident snapshot plus JSON encoding measured about
34.4 microseconds locally and 25.9 KiB allocated per serialization. This work
runs outside the RTP observer and is not part of its zero-allocation benchmark.

Final verification: complete Go suite 985 passed, zero failures, three opt-in
skips; frontend/audio suites 227 passed, zero failures; type check, vet and build
passed. Final focused race checks passed three times, including real SRTP
backbone delivery while collection is busy, nonblocking snapshots, error/loss
persistence and operator/project boundaries. The first full run exposed an
incorrect new test comparison against a pre-insert fixture rather than saved
SQLite defaults; the fixture assertion was corrected and the full suite passed.
No application state mutation by telemetry was observed.
