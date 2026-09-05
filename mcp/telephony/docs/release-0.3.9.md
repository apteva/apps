# Telephony 0.3.9

This release improves the native SIP implementation's call lifecycle, RTP/SRTP audio, session refreshes, and TLS certificate renewal.

## SIP reliability

- Handle existing-dialog re-INVITE and UPDATE refreshes with unchanged media, validating source networks, dialog tags, and remote sequence numbers.
- Negotiate session timers, generate local refreshes, and terminate expired sessions or calls missing required ACKs.
- Send BYE after post-answer failures and cancel pending refreshes when the remote party hangs up.
- Reject unmatched CANCEL requests without terminating another dialog.
- Reserve incoming call capacity atomically and wait for signaling, media, and cleanup during gateway shutdown.
- Reload renewed TLS certificates during handshakes, retain a valid previous certificate during partial renewal writes, and expose readiness, expiry, and renewal errors.

## Audio quality and security

- Preserve the selected SDES crypto tag and reject replayed SRTP packets.
- Validate codec names, sample rates, channel counts, static payloads, and SDP direction inheritance.
- Drain short speech bursts, resume playback after silence-suppression gaps, and bound jitter-buffer and Core audio backlogs while preserving recent speech.
- Maintain RTP timestamps across silence and mark new talkspurts; rotate RTP port allocation.
- Add inbound dropped-audio, jitter, and queue-age diagnostics.
- Update the app SDK to v0.75.0.

## Scope and validation

Session refreshes support unchanged media. Changes to media endpoints, codecs, or keys receive an explicit rejection while preserving the existing call. Unsupported SDES lifetime, MKI, and session parameters are also rejected.

Native outbound origination, forwarding from native SIP ingress to external phone/SIP destinations, native IVR menus, voicemail/recording, and hold/transfer remain unsupported. The carrier webhook team-ringing support introduced in 0.3.8 remains available.

Validation covers the standard Go suite, race detection, Go vet, native SIP/RTP/SRTP socket tests, and compiled-sidecar browser and three-phone carrier integration tests. Audio regression tests also pass. Tests use controlled carrier/Core peers and generated audio; live carriers and physical headsets were not exercised.
