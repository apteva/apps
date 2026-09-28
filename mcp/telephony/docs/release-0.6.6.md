# Telephony 0.6.6

This release corrects the inbound burst guard introduced in 0.6.5.

- The per-caller rule still suppresses new calls after 12 distinct carrier call IDs from the same displayed caller to the same number in 60 seconds. Suppression lasts five minutes and never offers those calls to advisers. The thresholds and cooldown remain configurable.
- The destination-wide rule still detects a burst after 60 distinct call IDs to one number in 60 seconds, including calls with rotating displayed caller IDs. It emits `telephony.burst.detected` and logs a warning, but does not suppress calls to the public number. One alert is emitted per cooldown period.
- Trusted callers remain exempt from the per-caller limit. An existing destination-wide suppression cooldown from 0.6.5 is ignored after upgrading, so it cannot keep an IVR number blocked.

Displayed caller ID is not a verified source identity. A rotating-ID attack requires carrier signaling data or carrier-side controls to identify and block its source safely. The destination alert gives operators a signal without closing the IVR to legitimate callers.

Verification covers webhook retries, per-caller suppression, a destination burst with rotating callers and continued adviser offers, signed Telnyx announcement callbacks, and the native Tier 1/2 suite. A controlled live call remains necessary to verify audio in production.
