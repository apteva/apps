# Auth v0.11.0

First-party authentication for Apteva SaaS applications. Each project install
contains separate organizations, users, clients, signing keys, roles and
permissions. This release fixes the v0.10.0 security and correctness audit.

## Upgrade contract

This is a **minor release with intentional compatibility changes**, not an
automatic patch. Migration `006_security.sql` invalidates existing sessions
and recovery links, increments authorization versions, and removes recovery
URLs from historical audit events. Users must sign in again. Back up the
application database before an operator deploys this release.

- Password changes always revoke all sessions and outstanding recovery links;
  the legacy `revoke_sessions: false` option no longer preserves access.
- Email verification and password reset return `{ "ok": true,
  "login_required": true }`, without access or refresh tokens.
- Confidential `web` clients must supply `client_secret` with signup, login and
  refresh, or use HTTP Basic authentication with their client ID and secret.
- M2M/client-credentials clients cannot use user authentication. OAuth/OIDC,
  PKCE authorization flows, magic-link login and MFA challenges are not
  implemented. Discovery explicitly reports these capabilities as unsupported.
  A client requiring MFA, or a user with a confirmed MFA factor, cannot obtain
  a session until a supported challenge flow is implemented.
- Guest upgrade revokes guest sessions, invalidates recovery links, and removes
  linked identities. An old device starts a new guest rather than entering the
  upgraded account. Pending verification applies to every account login path.
- Auth no longer issues independent `apteva_access_token` credentials. The
  platform mint callback has no Auth-session binding/revocation contract. Tokens
  previously issued by the platform remain subject to the platform's own
  expiry and revocation policy; this database migration cannot revoke them.
- Empty `allowed_origins` denies browser requests carrying an Origin header.
  Server/native calls without an Origin header are still subject to client and
  credential checks. The built-in recovery page is allowed from the canonical
  Auth origin because it requires a mailbox token.

See [SECURITY_FIXES.md](SECURITY_FIXES.md) for the audit mapping and validation.

## Public API and canonical URL

Set `app_url` to the complete public Auth route, without a query or fragment,
for example `https://agents.example.com/api/apps/auth/_install/42`. Otherwise
Auth uses the platform's public URL plus `/api/apps/auth/_install/<install-id>`
when the install ID is available. It never derives an issuer from request Host.
The server's install-path proxy selects the project-scoped sidecar. An explicit
project selector must agree with the installation.

| Method | Path | Behavior |
|---|---|---|
| POST | `/signup` | Register; issue a session or require verification |
| POST | `/login` | Email/password authentication |
| POST | `/refresh` | Refresh the same logical session |
| POST | `/logout` | Revoke the entire refresh family; returns 204 |
| GET | `/me` | Validate access and return current identity/authorization |
| PATCH | `/me/metadata` | Update untrusted profile metadata |
| POST | `/password/reset/request` | Queue delivery; always returns 202 for a valid request |
| POST | `/password/reset/confirm` | Atomically replace password and require login |
| POST | `/email/verification/resend` | Queue verification delivery |
| POST | `/email/verify` | Verify mailbox ownership and require login |
| GET | `/orgs/{slug}/password/reset` | Built-in password recovery form |
| GET | `/orgs/{slug}/email/verify` | Built-in verification form |
| GET | `/orgs/{slug}/.well-known/jwks.json` | Active and draining public keys |
| GET | `/orgs/{slug}/.well-known/openid-configuration` | Legacy URL for proprietary Auth discovery; not OIDC |

Requests creating or refreshing sessions include `client_id`. A multi-org
client also requires `organization_slug` when starting a session. A supplied
organization must match a single-org client's binding. Invalid selectors
never fall back to project-wide access. Legacy default-org discovery URLs
remain available.

Client grant/auth-method registry fields remain for compatibility with older
records; they do not implement an OAuth token endpoint. `refresh_token` must be
allowed to refresh. Client-credentials policy cannot authorize user login.

## Session and authorization semantics

Access tokens use Ed25519 and include issuer, audience, authorized client,
subject, organization, `sid`, `token_use: access`, issued-at, expiry,
`authorization_version`, roles and permissions. Auth's online routes check the
user, organization, client, verification/MFA/lockout policy, logical session
and current authorization version. Password changes and disable/re-enable
transitions cannot resurrect older sessions.

Refresh rotation is transactional. Reusing a spent credential revokes its
entire family, including a concurrently issued successor. **Clients must
serialize refreshes**; duplicate retries can require a new login. Refreshes
never extend the family's original absolute expiry. With rotation disabled,
refresh returns the same credential and does not insert another refresh row.

Role/permission reads use a consistent snapshot. Assignments use strict positive
integer arrays; only an explicit empty array clears a set. Token size and
assignment limits reject oversized contexts. Profile metadata never supplies
trusted permissions.

Offline JWT consumers must validate issuer, audience, purpose and expiry and
use the correct organization's keys. They cannot observe immediate session
revocation by signature alone: use Auth's `/me` when immediate revocation is
required. Previously cached keys/tokens retain their offline lifetime.

`POST /admin/signing_keys/rotate?organization_slug=…` with
`{"emergency": false}` creates a new signing key and drains the old public key
for at most 24 hours. `{"emergency": true}` removes all old verification keys
and revokes organization sessions. Retired private material is cleared on
rotation. Active private keys remain in the application database: protect DB
and backup access; external key wrapping/HSM integration is a separate change.

## Recovery and delivery

Configure the messaging app and `from_email`. Missing delivery configuration
is an error, never a successful "sent" result. Admin provisioning preserves the
created user and returns `delivery_error` when its requested email fails.
Verification-required signup also returns a delivery error without losing the
registered account.

Public reset/resend requests write a durable queue containing user/client
references, not raw tokens. The worker attempts up to 20 jobs per minute, leases
jobs atomically, retries failures up to five times at five-minute intervals,
and expires jobs after seven days. Mail-provider latency is outside the public
request. Tokens are generated at delivery time, stored only as hashes, scoped
to organization/client, and never placed in audit records. Failed delivery
invalidates its generated credential.

Default email links use URL fragments. The built-in page removes the fragment,
uses a restrictive CSP and no-referrer policy, and posts the mailbox token to
Auth. A custom `continue_url` must pass the client's registered redirect/origin
validation; it receives a `#verify=…` or `#reset=…` fragment and must implement
the corresponding confirmation form.

## Policy, administration and maintenance

Validated organization overrides take precedence over installation settings:
password character minimum/classes, email verification, access/refresh
lifetimes and lockout thresholds/durations. Password bytes, including leading
and trailing spaces, are preserved; minimum length counts Unicode characters.
Lockout updates are atomic with bounded exponential backoff.

Public JSON bodies are capped at 128 KiB; passwords at 4096 bytes. At most four
Argon2 operations run concurrently (normal hashes use 64 MiB each). Fixed-window
SQLite quotas cover requests per network peer, login attempts per client/email,
signups per client and recovery requests. The default request cap is 300/minute
per peer; behind a proxy this may be shared by many users. Production capacity
planning must account for the proxy topology and edge limits. Caller-supplied
forwarding headers do not override this trusted peer identity.

The dashboard scopes requests by project and install. Switching project,
organization or user resets relevant state; stale fetches are discarded.
Passwords are masked; failed edits stay open. Clients expose origin editing
and synchronization retry status. Browser-origin changes serialize; mount and
hourly maintenance reconcile failed platform updates.

User search applies MFA in SQL, uses indexed descending-ID cursor pagination,
and performs one query per page. Role listing batches permissions. Session
views are capped at 200 records, prioritize active credentials and check expiry.
Hourly cleanup removes expired families/credentials and recovery tokens after
a one-day grace period, expired quotas, old delivery jobs and audit events
older than `audit_retention_days` (default 90; range 7–3650). Spent credentials
are retained while their family is live so replay detection continues working.

Trusted identity login remains an SDK `app_only` tool. Bound sibling apps are
trusted to prove provider/device identity. Do not bind an untrusted application
that can assert arbitrary provider subjects; per-caller provider/client
allowlists and possession proofs belong in that integration boundary.

## Development and tests

Use the pinned Go 1.25.13+ toolchain and SDK v0.74.1. `GOWORK=off` prevents the
workspace overlay from replacing the release SDK dependency.

```sh
# From mcp/auth
GOWORK=off go test ./...
GOWORK=off go test -race -tags integration -coverprofile=coverage.out ./...
GOWORK=off go vet ./...
GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@latest ./...
GOWORK=off go test -run '^$' -bench Benchmark -benchmem

# From mcp/auth/ui
bun install --frozen-lockfile
bun run typecheck
bun test

# From repository root; rebuilds AuthPanel.mjs and its source map
bun run scripts/build-panels.ts --app auth
```
