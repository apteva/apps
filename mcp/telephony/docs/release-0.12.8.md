# Telephony 0.12.8

Fixes automatic Telnyx outbound SIP setup rejecting a valid managed credential
connection because its API response omits `transport_protocol`.

Telnyx's credential-connection create/retrieve schemas do not define that field.
Telephony no longer submits it or requires its echo when verifying setup.
Connection name, SIP username, linked outbound profile, active connection and
enabled profile checks remain required. Outbound TLS is selected by the existing
SIP client through `transport=tls` and port 5061; automatic setup still requires
a TLS gateway and selects SRTP media. Removing an invalid API-field assertion
does not change SIP signaling or media security.

The preserved setup journal and encrypted SIP credentials are reused when setup
is retried. A connection already created by 0.12.6/0.12.7 is verified by its saved
resource ID rather than created again. Runtime transport settings apply to new
calls only; this release does not change active calls or inbound routes.

Regression coverage uses a carrier response without the unsupported field and
reproduces the 0.12.7 failure before the fix. Tests verify resuming a saved setup
without a duplicate connection, new-call admission retaining TLS/SRTP settings,
and rejection of missing/mismatched identity, profile or inactive resources.
The provider fixture now checks documented request fields rather than assuming
every request field will be echoed by Telnyx. Full Go/frontend suites, focused
race checks, browser setup tests, vet and builds are publication gates.

Retains 0.12.7's token credential fix, all previous changes, SDK v0.100.0 and the
Apteva 0.87.4 minimum/exclusive activation contract. Publishing does not install,
configure or call anything on staging/production. Install with no active calls;
real outbound carrier signaling and two-way audio still require a controlled
test after installation.

References:

- [Telnyx credential-connection creation](https://developers.telnyx.com/api-reference/credential-connections/create-a-credential-connection)
- [Telnyx credential-connection retrieval](https://developers.telnyx.com/api-reference/credential-connections/retrieve-a-credential-connection)
- [Telnyx outbound TLS/SRTP setup](https://sip.telnyx.com/)
