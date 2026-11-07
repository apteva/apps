# Telephony 0.12.38

## AI media and conversation lifecycle

- Add optional, generic Core/provider transport (`websocket`, `webrtc`) and
  bridge format (`pcm16`, `g711_ulaw`, `g711_alaw`) to agent audio profiles.
  Resolve and freeze the profile before Core starts; verify the audio handshake.
- Keep codec conversion state across chunks and account for encoded audio using
  its actual byte rate. Preserve native Twilio mu-law bytes where preprocessing
  and post-interruption smoothing do not require conversion. Recording and
  listening taps continue using PCM24.
- Let providers that advertise conversational interruption own automatic
  overlap handling. Explicit local interruption remains available; older Core
  contracts retain their existing behavior.
- Accept explicit conversation completion from the authenticated Core bridge
  as a carrier hangup request, checking current project, agent and thread
  ownership. Generic closes retain recovery behavior. Successful completion
  no longer appears as a transport failure.
- Add repeatable matched latency replays through local Core, Server and
  Telephony for PCM, Twilio and SIP in both directions, alongside completion,
  format, profile-freezing and native-byte regression tests.

## Compatibility and scope

Uses published app-sdk v0.107.0. Optional transport/format overrides and explicit
Core completion require companion Server/Core support. Older servers may ignore
transport-selection fields; format mismatches fail explicitly. Existing PCM
and provider-transport defaults remain unchanged. This release does not update
Server or Core.

Both manifests and the generated softphone client identify 0.12.38, and the
source reference points to `telephony/v0.12.38`.

These changes do not establish that all provider response pauses or reported
handset crackling are resolved. The optional latency bench includes a deliberately
failing paused-provider scenario to expose remaining Core queue recovery work;
see `benchmarks/agent-audio/LATENCY.txt`. Publishing does not deploy any instance.
