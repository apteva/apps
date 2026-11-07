# Telephony 0.12.25

## Generic outbound verification during temporary carrier API failures

Previously verified, configured caller IDs can remain eligible during temporary
carrier inventory or readiness API failures. The same policy applies to the
connected-numbers HTTP/MCP interfaces, caller-ID selection, programmable
outbound readiness and Telnyx/Twilio SIP trunk verification. No CRM-specific
logic, callback policy or consuming UI code is included.

Successful number ownership and outbound readiness are retained for at most
24 hours in installation-local SQLite evidence. Evidence is scoped to project,
carrier binding, provider, current credential digest and exact outbound
resource/config. Saved routes or caller IDs alone cannot introduce unverified
numbers. A healthy verification after upgrading is required to populate it.

Only HTTP 408/429/500/502/503/504 and timeouts qualify. Missing evidence,
expiration, permanent/unknown failures, current admission restrictions, binding
drains and revoked user access still reject placement. A successful inventory
that excludes or disables a number is authoritative. Fresh disabled profiles
also override old readiness even when application inspection then fails.
Timestamped revocations prevent older in-flight successes from restoring proof;
failed reads never extend its lifetime. SIP signaling and actual carrier dial
commands must still succeed independently; dial commands are not retried by
this fallback.

## Compatible inventory contract and bounded work

Eligible configured fallback numbers retain outbound `ready` for existing
clients. Top-level inventory is `partial` if usable fallback data exists;
provider inventory remains `unavailable` with a warning. Explicit
`outbound_verification` status/timestamps and `outbound_fallback_allowed` reveal
cached verification. Webhook health remains unverified and local SIP
listener/certificate health stays current. Empty capabilities retain the
existing JSON array contract.

Caller-ID selection no longer independently retrieves inventory twice. The
selected number's verified application metadata is reused for outbound resource
resolution. Evidence excludes credential-bearing webhook/callback URLs and
stores credential digests, not API keys or SIP passwords. Indexed, bounded
cleanup runs on changed evidence; there is no new polling worker or media-path
work.

## Validation and packaging

Adds mocked 503 regressions for generic inventory consumers and actual
Telephony placement, Telnyx/Twilio programmable paths, SIP INVITE admission,
current user grants, per-number/account/provider disables, binding drains,
credential/project/account isolation, expired/missing proof, fresh exclusions,
disabled profiles, concurrent observation ordering and evidence redaction.
These are local/mocked tests, not real carrier calls during an API outage.

The full Go suite and focused race checks pass locally. The release additionally
runs the existing CI browser, native-codec, long-recording, mixed-media load,
race, frontend and packaging gates. New outage regressions join CI race checks.

Both manifests, installer source references and generated browser metadata use
0.12.25. SDK remains pinned to v0.102.0. Publishing does not install, restart or
change production or staging.
