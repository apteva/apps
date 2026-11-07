# Telephony 0.12.4

Cumulative release retaining all changes in 0.12.3.

## Generic outbound SIP

Telnyx and Twilio can now place outbound calls through the shared direct SIP
engine. Operators can select the transport for an authorized carrier connection
or an individual owned number, independently of inbound routes. The default
remains Programmable Voice with WebSocket media; SIP must be explicitly enabled.

Telnyx requires an existing SIP connection and enabled outbound voice profile.
Twilio requires an existing Elastic SIP trunk with termination and authentication.
Configuration verification is read-only. This release does not provision carrier
resources or enable SIP listeners. TLS signaling and SRTP/SDES media are the
default, with existing gateway security policy enforced.

Caller-ID ownership, authorization, readiness and runtime disablement checks
remain in place. Separate SIP credentials are encrypted with a persistent
installation key and never returned to clients. Settings apply to new calls;
existing calls retain their original carrier and dialog context. Provider AMD is
unsupported on native SIP and must be disabled for that transport.

The existing browser WebRTC and PCM/WebSocket paths are retained. Direct SIP
uses Telephony's local recording mechanism when recording is enabled, including
pause/resume and the existing asynchronous Storage import when Storage is bound.
Native SIP recordings are excluded from Voice API recording reconciliation.

## Verification and limits

Local tests exercise Telnyx and Twilio through a simulated TLS SIP carrier,
Digest authentication, ACK, SRTP, two-way AI/browser media, supported codecs,
hangup, cancellation and local WAV recording. Regression tests cover scoped
settings, encrypted credentials, readiness, dialog timers and recording dispatch.
These tests are included in CI's SIP race checks.

Full Go/frontend suites, race checks, vet, typecheck and builds are release gates.
No real carrier calls or complete SIP-to-Storage end-to-end validation have been
performed. Actual carrier signaling/media and durable Storage should be verified
in a controlled environment before enabling the transport operationally.

SDK v0.99.0 remains pinned. Publication changes only Telephony source and release
metadata. No staging or production installation, calls or settings are changed.
