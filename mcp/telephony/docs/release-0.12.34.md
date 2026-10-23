# Telephony 0.12.34

## AI call evidence and interruption handling

Final carrier audio reports now persist for AI and external bridges as well as
human calls. Existing bridge-generation fencing remains authoritative. Audio
health retains those reports after the live sample is absent, including scoped
clear evidence, configured policy, input timing and generated-audio counters.

Pending voiced audio is tracked separately from ordered silence in Telnyx and
Twilio WebSockets, binary carriers and direct SIP. Silence continues through
its existing cadence, packetization and receipt path, but cannot on its own
trigger local barge-in or keep AI inactivity waiting for speech playback. Real
queued/in-flight speech remains interruptible; cancelled marks and late output
retain their existing fences. Queue size, codec and receive jitter settings
are unchanged.

Agent profiles accept the authenticated `_project_id` transport metadata sent
by Server/SDK. Mismatched or malformed metadata is rejected; scope still comes
from the authenticated app context and cannot be selected by the client.

## Reply timing

Live and retained `agent_audio.reply_timing` separates the interval from last
locally observed caller speech to fresh Core audio, and the subsequent interval
to the first successful carrier write. The first stage includes upstream input,
Core and provider work; it is not an isolated model-generation measurement. A
carrier write is not handset playback confirmation.

The observational detector requires 200 ms of observed quiet before pairing a
reply. It does not gate audio or configure provider turn detection. Silent and
cancelled output cannot complete a reply timing. History is bounded to 32
records per bridge with lifetime maxima. Collection adds no database work,
periodic worker, per-frame allocation or steady-state clock read. Audio health
summaries expose the two maxima without adding an arbitrary latency alarm.

## Language and verification

Opening language remains a route/agent instruction, using existing directive
and greeting settings. To require French, explicitly request a French opening
and disallow inferring language from the caller's telephone prefix. No global
language override or provider-specific model selection is introduced.

Regression coverage includes ended inbound/outbound AI diagnostics, replacement
fencing, audio-health readback and summaries, authenticated profile context,
silent/voiced receipt accounting, bounded reply timing and concurrent snapshots.
The carrier harness tests continuous caller speech during silent playback,
real interruptions, next replies, and persisted diagnostics across inbound and
outbound Telnyx/Twilio, SIP PCMA/G.722, Vonage and Sinch paths.

Matching Core/Server capability support is still required for continuous mode.
This release does not publish Core or Server. Local carrier fixtures and model
tests do not certify physical handset quality or production call capacity.
