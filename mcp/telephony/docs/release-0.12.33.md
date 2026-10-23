# Telephony 0.12.33

## Continuous AI conversations

Adds a generic, negotiated agent-audio contract across media WebSockets and
native SIP. Caller audio continues while the agent speaks or executes backend
work. Playback cancellation uses session generation and playback identity;
late cancelled frames and cleared marks cannot revive an interrupted reply.
Unrelated queued speech is retained when the carrier permits it.

Continuous sessions default to analysis-only caller preprocessing, a 160 ms
local speech detector, a 60 ms WebSocket carrier lead and a 1000 ms generated
speech backlog. A recent modulation window rejects the tested steady music,
noise and short acknowledgements without attenuating forwarded PCM. SIP keeps
its 20 ms RTP cadence and assembles partial model chunks without inserting
silence at every chunk boundary. Bounded overflow is explicit.

The administrative `telephony_agent_audio_profile` tool and authenticated
`/numbers/agent-audio-profile` route support project, direction, AI agent,
route, carrier binding and number scopes, with temporary overrides. Profiles
are pinned per call; changes apply to new calls without restart or redeploy.

Live `/audio-health` detail and retained carrier reports expose agent policy,
capabilities, scoped cancellation and receipt evidence. Carrier marks, timer
estimates and successful RTP writes remain distinct. Carrier-wide clear
commands report collateral playback identities and do not claim remote
confirmation or selective clearing. Normal queue admission and cancellation
fencing add no allocations or periodic worker.

## Compatibility and scope

Automatic negotiation requires the corresponding Core/Server protocol support
implemented and tested locally alongside this app. Until that support is
available, automatic mode preserves legacy AI behavior; explicit continuous
mode rejects an incompatible pair. This app release does not publish Core or
Server. Human browser/softphone policy and the SIP receive jitter buffer remain
unchanged. Frontend assets are rebuilt with matching release metadata.

SDK is pinned to v0.105.0, verified by commit ancestry. Disk/embedded versions,
installer source refs and generated frontend metadata all identify 0.12.33.

## Verification

- Full Telephony suite on SDK v0.105.0, final agent/frontend race tests, overflow
  regressions, related Core tests/races, Server proxy races and local builds pass.
- Twelve loopback carrier paths cover inbound/outbound Telnyx/Twilio media
  WebSockets, SIP PCMA/G.722, and Vonage/Sinch binary audio. Forty-eight clears
  include recorded natural-speech interruptions, next-reply progress,
  continuous caller input and delivery-ledger drain.
- A real `gpt-live-1` run through actual local Telephony, Server and Core passed:
  the caller interrupted the greeting, the model recognized the spoken request,
  and replied “Vous avez demandé mardi.” Clip start to Telephony's clear command
  was about 220 ms. Both audio directions were recorded.

The carrier is a local protocol fixture in that paid test. Real carrier/PSTN
calling, physical handset audibility and concurrent-call capacity are not
certified by these checks. Details: [continuous agent audio](continuous-agent-audio.md)
and [verification summary](verification/continuous-agent-audio.json).

Publishing does not install, restart or configure staging or production.
