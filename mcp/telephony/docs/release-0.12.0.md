# Telephony 0.12.0

Cumulative release retaining 0.11.9 and every earlier Telephony change.

## Generic direct SIP media parity

- Per-number inbound transport selection and project/route media settings.
- Local carrier-independent prompts, DTMF menus, hold, voicemail, recording,
  outbound DTMF, G.722 and G.711 codec negotiation.
- Existing calls retain their provider resources and transport; unused old
  resources retire durably. Failed transport saves roll back carrier changes.
- SIP AI activation failures execute the configured terminal fallback.
- Programmable carrier WebSocket transport remains the route default. Browser
  WebRTC and PCM/WebSocket transport code is unchanged.

## Deployment preparation without enabling SIP

Host preparation does not select SIP on any route or provision carrier routing.
For a host running multiple Telephony installations, give each a distinct SIP
signaling port and RTP range, separate from all browser WebRTC ranges. Permit
only the supported carrier networks through the host firewall. TLS certificates
can reuse the platform hostname and managed ingress cache.

The Flexylead preparation plan uses staging TCP 5062 / UDP 20400–20599 and
production TCP 5061 / UDP 20600–20799. Existing browser WebRTC ranges remain
20000–20127 and 20200–20327. All routes remain programmable_websocket; gateways
are lazy and remain inactive until a route explicitly selects direct SIP.
Carrier configuration, actual TLS/SRTP negotiation and two-way audio validation
are deferred to a separately authorized staging test.

## Verification

The implementation passed the complete Go suite, targeted SIP race tests,
frontend and browser audio suites, typechecks, vet and native/Linux builds.
Codec-only benchmarks do not establish end-to-end SIP CPU usage or live carrier
interoperability. The final release gate is rerun on this release source.

### Cleanup regression test

The cleanup timeout assertion now validates timestamped local-shutdown
attribution and exact timeout/forced-close counters without assuming the
independent cleanup timer cannot fire under scheduler load. Live write failures
must still remain zero. Runtime close deadlines and media behavior are unchanged.
