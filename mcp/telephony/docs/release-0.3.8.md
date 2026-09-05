# Telephony 0.3.8

This release fixes call routing, browser audio, recording lifecycle, and call controls, and implements multi-destination ringing.

## Team ringing

- Simultaneous: ring every enabled destination together; the first accepted answer wins.
- Sequential: try members in the configured order.
- Round robin: persist and rotate the starting member across calls, then try the remaining members in order.
- Priority: ring the lowest numbered priority tier together before advancing to the next tier.

Groups support browser operator pools, agent/AI destinations, and outbound phone/SIP destinations on Twilio, Telnyx, and Plivo webhook routes. Native direct-SIP ingress supports browser/agent groups; external forwarding from that ingress transport remains unsupported.

Configure teams in Routing, or use Advanced flows for member order, individual timeouts, priority tiers, and an **If no one answers** fallback. Browser destinations represent shared operator pools rather than individual user accounts. Voicemail can be the fallback. Groups allow up to 25 members, individual timeouts of 5–300 seconds, and a maximum group wait of ten minutes within the caller's overall deadline.

Offers, round-robin cursors, and call legs persist across restarts. Atomic claims select one winner and cancel competing offers and phone legs. Repeated callbacks do not redial phones or rotate the cursor again. Uncertain carrier requests await reconciliation instead of generating another paid call.

## Reliability and performance

- Preserve published routing snapshots and IVR progress, validate ownership, and recover failed answering and event delivery.
- Correct Plivo answer state, SIP media startup and cleanup, and Twilio answering waits.
- Keep browser audio alive during panel navigation, retain controls for active calls, reject stale answers, and expose device/reconnect errors.
- Bound WebSocket message sizes and audio queues; close replaced sockets and retain buffered handshake audio.
- Improve streaming resampling, limiter processing, mute behavior, short-burst playback, RTP recovery, and current-window audio meters.
- Reduce frequent call-list payloads by loading detailed diagnostics only for the selected call.
- Make recording import/deletion claims atomic, retry storage/provider cleanup, share bounded playback conversions, and preserve uncertain purchase intents for reconciliation.
- Update the app SDK to v0.74.1.

## Upgrade and validation

The app applies database migrations 021 (recording cleanup) and 022 (ring-group runtime) at startup. Existing published flow dependencies are snapshotted during upgrade; configuration changed before that upgrade cannot be reconstructed retrospectively.

Validation includes the complete standard Go suite, focused race checks, Go vet, 14 Bun tests, TypeScript checking, the panel build and host-import verification, browser UI fixtures, and compiled-sidecar browser/three-phone integration tests with signed callbacks and bidirectional carrier audio. The full race suite reached its eight-minute timeout without a reported data race; it is not counted as a passing full race run.

Carrier APIs and audio devices in the integration/UI tests are controlled fixtures. Physical phones/headsets, real carrier billing and recording operations, and mobile-network acoustic quality were not exercised by these checks.
