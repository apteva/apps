# Optional AI media transport/format profiles

`telephony_agent_audio_profile` config now accepts `transport` (`websocket` or
`webrtc`) and `format` (`pcm16`, `g711_ulaw`, `g711_alaw`). These select the
Core/provider leg and internal AI audio format. They do not change SIP, carrier
webhooks, or the human softphone. Omission retains PCM24 and the existing
provider transport.

Example tool arguments:

```json
{"scope":"agent/1194","config":{"transport":"webrtc","format":"pcm16"},"ttl_seconds":1800}
```

Use your own agent ID. Profiles are complete, not partial merges; other omitted
fields receive normal defaults. Existing project, direction, agent, route,
account and number scopes apply. The profile is resolved before spawning Core
and pinned for the call, so a mid-call setting change cannot alter codecs.
Call handshakes verify format, rate and channel count. A mismatched format handshake causes an explicit failure rather than playing bytes
as the wrong codec. Provider transport selection requires the companion Server/Core
implementation; older servers may ignore the optional spawn field.

Both directions and carrier bridge types use the same format converters. Matching
Twilio mu-law input/output preserves encoded bytes when no preprocessing or
intentional post-cancellation fade is needed. Other codecs are converted at the
carrier boundary; Telnyx's existing L16 and SIP jitter/decoding behavior remains.
Recording/listening taps retain PCM24. Reconnect buffers, playback durations,
credits and forwarding diagnostics use the selected byte rate.

WebRTC/PCM requires libopus on Core's host. Alternatives are experimental and
remain off by default. No provider-specific logic belongs in Telephony's policy.
The spawn fields are supplied by app-sdk v0.107.0. Optional transport/format
overrides require companion Server/Core support; publishing Telephony does not
upgrade them. PCM defaults and legacy capability behavior remain available.

### Overlapping speech

When Core advertises `provider_interruption`, the `auto` agent audio profile
leaves conversational yielding to that full-duplex provider. It does not turn
short greetings/backchannels into a hard playback clear. Caller PCM remains
continuous and unchanged with `analysis_only` preprocessing. `interruption:
local` explicitly retains local detection and scoped cancellation. Older Core
contracts without this capability keep their prior behavior. SIP, Twilio and
other carrier bridges use this shared policy; human softphone paths are unchanged.

Core's continuous phone completion review can finish a mutually ended call
without waiting for Telephony's maximum-duration timer. Core drains playback
receipts before closing the authenticated bridge with an explicit completion
reason. Telephony verifies the current project/agent/thread ownership and calls
its generic carrier hangup adapter, for inbound and outbound calls. Ordinary
EOF, replacement and normal socket cleanup retain their recovery behavior.
Failed hangup commands are not reported as completed. Speech-only farewells
are not themselves hangup confirmations.
