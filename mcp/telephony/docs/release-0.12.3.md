# Telephony 0.12.3

Cumulative release retaining all changes in 0.12.2. No running installation,
carrier configuration, route or SIP listener is changed by publication.

## Telnyx direct SIP provisioning

Translate shared SIP codec names only when constructing the Telnyx FQDN
connection request: `G722` → `G722`, `PCMU` → `G711U`, `PCMA` → `G711A`.
Default provisioning now sends `[G722, G711U, G711A]`. Project defaults and
route overrides preserve their order and selected subset. Invalid shared codec
settings fail before connection creation.

Stored settings and SIP SDP negotiation continue using standard `PCMU`/`PCMA`
names. No media processing, quality, latency budgets, browser transport or other
provider provisioning behavior changes. Existing G.722-only configurations are
not automatically expanded or modified; the mapping applies on future setup.

SDK v0.99.0 remains pinned and is still the latest release on origin/main.
No SDK, Server, Core, integration or client-app source changes.

## Verification

Regression tests inspect serialized provisioning requests for the default codec
list, reordered project preferences, route override and each single-codec choice.
They verify stored shared settings remain unchanged and reject invalid codec
names/duplicates. Complete Go/frontend suites, SIP/recovery race checks, vet,
typecheck, panel build and native/Linux builds are required before publication.
Carrier setup/restore and rollback tests remain included.

Reference: https://developers.telnyx.com/api-reference/fqdn-connections/create-an-fqdn-connection

All verification is local or CI. No staging/production calls or settings changed.
