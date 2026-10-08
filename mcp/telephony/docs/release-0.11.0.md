# Telephony 0.11.0

Cumulative release retaining all Telephony 0.10.5 functionality: routing,
providers/compliance, inbound protection, multi-carrier outbound, bounded AI
handoff, call duration, listening, private coaching, browser recovery and the
filterable SSE Audio health widget.

## Changes

- Add optional, carrier-neutral WebRTC/Opus to the shared softphone/headless
  client and Calls panel. PCM/WebSocket remains the default; WebRTC remains
  disabled until configured. Both use the existing media hub and call
  authorities. No additional carrier stream is created.
- Add real Chromium/TURN network benchmarks with shared audio/signaling
  bandwidth, native Opus, encrypted UDP accounting, mute and reconnect checks.
  Local 64/96/128 kbit/s per-direction profiles passed their explicit marker
  latency/loss/level gates; inadequate bandwidth profiles reported degradation.
- Add project-scoped runtime outbound controls for a number, carrier account or
  provider, separate from adviser permissions. Disabled choices disappear from
  the softphone list; cached new dial attempts are denied at placement.
- Enable/disable individual inbound routes without deconfiguring carrier
  resources. Existing calls, media renewal/reattachment, routing snapshots and
  callbacks continue. New disabled ingress receives terminal carrier control
  without adviser offers or missed-call projection.
- Isolate inventory, credential, readiness and dial errors per carrier account,
  including multiple accounts on one provider. Healthy choices and placement
  remain available. Production SDK inventory requests use an eight-second
  budget and independent account reads; stalled accounts cannot starve others.
  Account-specific warnings explain partial/unavailable inventory.
- Use bounded, short-lived, project-scoped inventory hints to prioritize the
  selected caller ID's owning account. Hints never bypass current authorization,
  ownership, readiness or admission checks. Calls without a caller ID retain the
  configured default; failed calls do not silently substitute caller IDs.
- Add Telephony's authenticated, durable prepare/commit/drain endpoint for live
  carrier binding updates. Removed connections retain their context until calls,
  media, effects and recording imports drain, with terminal callback grace.
- Retain app SDK v0.96.0, confirmed latest by tag ancestry at release.

## Platform requirement for full binding removal

Number/account/provider policies and inbound route toggles run within Telephony
and need no binding edit or sidecar restart. **Changing/removing the actual
installation binding additionally requires the matching platform live-binding
implementation.** That server work is separate and is not published by this
Telephony release. An older platform can still restart apps on binding edits;
use the runtime controls to pause new calls without changing bindings.

See [runtime controls and protocol](runtime-number-controls.md) for endpoints,
drain behavior and compatibility limits of custom non-cancellable SDK clients.

## Verification and limits

739 Go tests/subtests and 171 frontend/audio tests passed; two opt-in live-carrier
tests were skipped. Focused account-isolation and platform drain race checks,
Go vet, build and frontend typecheck passed. Tests include two-way WebSocket
audio during disable and browser reattachment, same-provider accounts, carrier
dial rejection, stalled credentials, and four stalled inventories ahead of a
healthy fifth account. Source and packaged headless/panel assets are rebuilt for
this release.

WebRTC network evidence and its limits are recorded in
[transport verification](softphone-transports-verification.md) and
[bandwidth verification](softphone-bandwidth-verification.md). These are local
software/network-model checks, not production carrier validation, lossless Opus
proof or a guarantee for every VPN/device. UDP reachability or TURN is required
before enabling WebRTC. Existing PCM DSP and stale-audio limits are preserved.

Publishing does not install or activate staging or production, enable WebRTC,
edit instance bindings or change carrier accounts, routes, numbers or live calls.
