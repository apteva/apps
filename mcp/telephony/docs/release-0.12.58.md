# Telephony 0.12.58

Continuous AI SIP playback now resumes on the next send tick after a short
queue starvation, without reapplying the entire startup reserve. Initial
playback and a stream idle for at least one second retain the configured
20–200 ms cushion. Explicit clears still reset priming; scoped cancellation
that retains subsequent speech keeps its existing schedule. Human microphone
pacing, codec state, ordering and backlog limits are unchanged.

The one-second idle boundary is a local recovery heuristic, not a declaration
that speech ended. It prevents brief upstream delivery gaps from automatically
adding another reserve-length pause, while preserving a cushion after long idle
periods. It cannot recreate audio that has not arrived or make a continuously
slower-than-realtime source play smoothly without gaps. No synthetic silence is
inserted to hide RTP gaps. Diagnostics expose immediate recovery counts and
the idle threshold alongside existing starts, exhaustions and padding.

Regression coverage includes real recorded speech in G.722, PCMA and PCMU;
regular and jittered chunks; delayed catch-up bursts; persistently slow output;
ten minutes of repeated bursts; idle-boundary/clear behavior; scoped cancellation;
and real loopback UDP with payload integrity and packet-gap gates.

The optional local live-model bench can now exercise G.722 and PCMA SIP,
retain received audio, RTP sequence/timing observations and Core events, and
explicitly test local interruption separately from provider-owned interruption.
No deployment or routing configuration is changed by this release.

A separate local Core companion fix retains pending silence accounting after
an observed quiet boundary until bridge writes settle. Core is not released
as part of this Telephony version.
