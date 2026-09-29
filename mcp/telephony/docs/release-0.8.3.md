# Telephony 0.8.3

This release adds generic multi-carrier outbound caller-ID routing. An explicit
`from` number is resolved against every authorized carrier binding, so a Twilio
number uses Twilio and a Telnyx number uses Telnyx even when the other provider
is the default binding. Calls continue to use the configured default carrier
when no `from` number is supplied.

The connected-number inventory now aggregates authorized numbers across all
carrier bindings and preserves each number's provider identity. Ownership,
voice capability, carrier readiness, and the selected connection are validated
before placement; the actual carrier and connection remain persisted on the
call row.

## Verification

The full standalone Go test suite, Go vet/build, TypeScript typechecks, frontend
build, and 46 UI audio/frontend tests pass. Cross-carrier regression tests cover
explicit non-default selection, default fallback, unauthorized caller IDs, and
the combined number inventory.

The six legacy `frontend/tests/*.test.ts` files still report the pre-existing
Bun module-loader error for `ui/softphone-worklet.js` (missing `default`
export); this release does not change that file or the frontend implementation.

No staging, production, carrier, or live-call configuration was changed by
this release.
