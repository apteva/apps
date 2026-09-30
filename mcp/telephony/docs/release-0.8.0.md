# Telephony 0.8.0

DIDWW French number ordering and business registration support. This is a source
release; no staging or production installation is updated.

## Changes

- Add provider-neutral DIDWW French number search and idempotent order
  reconciliation, including exact inventory and group/SKU quotes when the
  account does not expose individual DIDs.
- Add DIDWW inbound trunk assignment, SIP credentials, outbound SIP support,
  and persisted provider order/resource metadata.
- Add DIDWW business and personal identities, identity-bound French addresses,
  dynamic requirements, proof types, encrypted document uploads, identity and
  address proofs, and post-allocation address verifications.
- Expose DIDWW registration through the existing Telephony address and
  compliance APIs and UI. Twilio and Telnyx compliance flows keep their
  existing behavior.
- Encrypt DIDWW documents locally with the provider's two public keys before
  upload. Plaintext documents are not sent to the integration service.
- Add regression coverage for DIDWW inventory, idempotent orders, JSON:API
  relationships, registration resources, encryption, proof attachment, and
  provider isolation.

## Upgrade behavior

Migration 037 adds DIDWW order-resource metadata and is applied by the normal
Telephony migration runner. Existing number orders and carrier connections are
preserved.

## Verification

Full Telephony Go tests, Go vet, DIDWW integration catalog tests, frontend
regression tests, frontend typecheck, panel build, headless client build, and
`git diff --check` pass locally.

## Limits

No DIDWW credentials were used, no number was purchased, and no staging or
production instance was changed. DIDWW approval remains provider-controlled;
an allocated number must have an approved address verification before use.
