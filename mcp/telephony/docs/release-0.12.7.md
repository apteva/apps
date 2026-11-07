# Telephony 0.12.7

Fixes Telnyx outbound SIP provisioning and verification with Apteva's actual
integration credential format. The shared provider request helper now uses
`token` first and accepts legacy `api_key` when `token` is absent. Both automatic
and manual setup, saved-selection verification, and new-call SIP admission use
this helper. No credential alias or integration change is required.

Regression tests reproduce 0.12.6's failure using token-only credentials, then
cover complete provisioning, idempotent reuse, new-call verification, legacy
compatibility, token precedence and missing-credential rejection. Shared
integration credentials remain unchanged, and failed setup retains the existing
outbound transport. Existing Telnyx/Twilio tests now use the integration's token
format for Telnyx rather than relying on the compatibility alias.

Retains all 0.12.6 functionality, SDK v0.100.0, Apteva 0.87.4 minimum version and
exclusive activation. No media, routing or carrier retry behavior changes.
Local regressions, full Go/frontend tests, race checks, browser setup tests, vet
and builds are release gates. Provider tests use local fixtures; no real carrier
resources or test calls are created by these checks.

Publishing this release does not install or configure staging or production.
Install with no active calls: app upgrades restart the sidecar. Real outbound
SIP calling still needs controlled carrier validation after installation.
