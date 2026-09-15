# Telephony 0.6.1

Two follow-ups to 0.6.0. Both are additive.

- **Ringback for attached calls.** The softphone SDK derived "outbound" from
  the method that opened the call, so a call placed by a backend and joined
  with `attach()` or `takeover()` never played ringback. The SDK now takes the
  direction from the call itself: one read after attaching, plus a new
  `direction` field on pushed `call.status` frames. `dial()` and `answer()`
  behave as before.
- **Project ring timeout.** `telephony_outbound_settings_set` accepts
  `default_timeout_sec`. When a caller omits `timeout_sec`, `telephony_place_call`
  and the softphone use it instead of the built-in 30 and 60 seconds. Zero
  keeps the built-in defaults; explicit per-call timeouts still win. Migration
  028 adds the column with a default of zero.
