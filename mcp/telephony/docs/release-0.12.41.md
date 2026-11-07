# Telephony 0.12.41

Adds generic Apteva resource policies (SDK 0.108.0): bounded recording-import,
playback, maintenance and diagnostic workloads; configurable SIP RTP/RTCP and
WebRTC UDP buffers and DSCP with effective kernel readback. OS buffer and DSCP
defaults remain unchanged. Live media does not acquire background-work permits.

Includes optional browser network preflight, transport/region policy, adaptive
recovery, and network diagnostics for calls and listener/coaching connections.
The custom guarded bitrate controller remains experimental and opt-in; the
standard browser controller remains the default. Adverse-network acceptance
results are retained, including conditions that do not meet the audio gate.

Local verification includes the complete Go suite, frontend tests, resource
race tests and an eight-call recording-enabled browser-to-SIP soak with jitter
and repeated pauses. The soak passed its existing gates, without claiming that
injected 420 ms network stalls are inaudible or that arbitrary networks are safe.
Prior MP3 storage and call lifecycle behavior are retained.
