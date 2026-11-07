# Telephony 0.12.12

Local corrections and bounded diagnostics from the October 12 production audit.
Publishing this release does not install it or change any instance or carrier.

## Changes

- Inbound SIP/TLS dialogs reuse their established carrier connection. When a new
  connection to a numeric Contact/Route is necessary, certificate verification
  uses the configured carrier identity while retaining the SIP destination and
  carrier address allowlist. Trust validation remains enabled. The bounded
  `sip_tls_peer_names` map defaults Telnyx to `sip.telnyx.com`; other carriers can
  supply their documented identity. Outbound uses its configured SIP hostname.
- Preserve failed BYE results after local cleanup; report outbound termination
  errors to the caller. During shutdown, mark sessions ended before canceling
  their shared context, allowing their signaling owner to send a bounded BYE.
- Drain outbound early RTP immediately after socket allocation. Local ringback
  remains the behavior before answer. Provisional SDP does not prime the final
  codec or SRTP keys. After final answer, a single receive owner hands a reserve
  of at most eight fresh packets to the bridge; old activation audio expires.
- Capture Linux per-socket receive-buffer drops, separate pre-bridge drain and
  activation drops, and retain actual receive times through SIP decoding.
  Unsupported platforms explicitly report counter availability as false.
- Browser WebRTC ingress keeps a fixed 64-event ring with UTC timestamps,
  connection/path/SSRC identity, sequence and RTP timestamps, receipt gaps,
  processing wait and actual concealment. Sequence gaps, DTX and padding do not
  by themselves establish lost speech. Pion socket-drop counters remain unavailable.
- Correct browser processing-delay arithmetic to decoded samples, retain raw
  counters and omit the derived value when unsupported or after a counter/SSRC
  reset. No playback decision uses this diagnostic metric.
- Keep the 640 B/s browser control budget. Send compact current/incident samples,
  full periodic details at most every 30 seconds, coalesce stale successors and
  expire queued observations after 12 seconds. Retain full context locally and
  expose collection loss and queue age. Shutdown does not wait for diagnostics.
- Return the authenticated call header from softphone/listening handshakes,
  retain proxy connection identity, and correlate Core bridge handshakes without
  retaining tokens or credential-bearing URLs.
- Routing/recording/audio notifications use the existing acknowledged durable
  event outbox after persistence. Audio collection is bounded memory only;
  overflow is counted. Database timeouts retain telemetry batches and reduce
  retry batch size. Pending in-memory observations can still be lost on a crash.

The 0.12.11 SIP clock correction, bundled native Opus, optional FEC, large
recordings and previous features are retained. SDK v0.100.0, minimum Apteva
0.87.4 and exclusive activation remain.

## Verification

See `docs/production-followups-verification.md` for test scope and measurements.
Local evidence is retained in the task artifacts, including unsuccessful load
runs; passing reruns do not erase those results.

## Remaining operational work

- Schedule installation with no active calls: exclusive activation restarts the
  Telephony process. This task does not deploy to staging or production.
- Verify the actual native encoder/FEC state and long-recording playback after
  installation. Production was still 0.12.9 at the read-only audit.
- Obtain real office VPN exit addresses; an empty list cannot classify them.
  Unmatched IPs remain unknown and never influence media or admission.
- Validate intended inbound/outbound codec and encryption settings in staging.
- Investigate adviser device/network packet loss with the new incident evidence.
  Neither VPN causation nor every reported noise/cut has been established.
- Full external proxy/Core and real-browser capacity still needs a controlled
  staging workload. Local tests cannot guarantee zero loss under host starvation
  or network failure, and genuine stale audio is still rejected.
