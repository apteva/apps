# Auth v0.12.1

First-party authentication for Apteva SaaS applications. Each project install
contains separate organizations, users, clients, signing keys, roles and
permissions. v0.12.1 distinguishes retryable refresh failures from uncertain
rotation outcomes for persistent SDK sessions. v0.12.0 added opt-in role-bound
platform credentials and a renewal
endpoint for unified Web SDK sessions. Upgrading from v0.11.x adds no database
migration and preserves existing Auth sessions. Configure explicit role bindings
and platform policies before enabling delegated access.

## Upgrading from versions before v0.11.0

The v0.11.0 security release introduced intentional compatibility changes. Migration `006_security.sql` invalidates existing sessions
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
- v0.11.x disabled independent `apteva_access_token` issuance. v0.12.0 restores
  it only through the explicit role bindings and short-lived policies documented
  below. Previously issued platform tokens retain their own expiry/revocation
  policy; the v0.11.0 database migration cannot revoke those credentials.
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

## Unified SDK sessions (v0.12.0)

Auth can issue a platform credential alongside the normal Auth session on login,
refresh, and other successful session-creation flows. Configure the Web SDK's
`auth: { clientId, installId?, organizationSlug?, profile? }` with `projectId`;
Web SDK v0.8.0 manages both credentials through `client.auth`.

Minting is **disabled by default**. Set the Auth install's
`delegated_token_bindings` JSON configuration to trusted mappings, for example:

```json
[{
  "project_id": "YOUR_PROJECT",
  "organization_slug": "default",
  "client_id": "YOUR_AUTH_CLIENT",
  "profile": "commercial",
  "policy_client_id": "flexylead-commercial",
  "roles": ["commercial"],
  "permissions": ["assistant:use"]
}]
```

All listed roles AND permissions must be present in the live authorization
snapshot. At least one requirement is mandatory. Selection is scoped by project,
organization and the actual session client. With `?delegated_profile=commercial`,
only that authorized profile can be selected; without a profile exactly one
binding must qualify. Unknown, unauthorized or ambiguous profiles deny minting.
The profile is not an authorization claim and never accepts arbitrary scopes.

For the Auth install, configure matching entries using the existing platform
`GET/PUT /api/apps/installs/:id/delegated-access-policies` API. The policy's
`oauth_client_id` is the binding's `policy_client_id` (a trusted policy selector,
not a browser login client). Use explicit apps/actions/agent IDs and
`token_ttl_seconds: 60`. Policies must accurately represent the corresponding
role requirements. Auth cannot inspect or narrow their scopes: the platform
owns and enforces them. Preserve unrelated entries when PUT replaces the full
policy set; replacement also revokes existing delegated keys for that install.
Do not use the compatibility mint path that omits `oauth_client_id`.

Responses add `apteva_access_token`, `apteva_expires_in` (remaining whole seconds)
and `apteva_expires_at` (absolute UTC expiry). Auth checks the platform's actual
returned identity, project, policy and expiry; requesting 60 seconds alone is
insufficient because platform policy overrides the request. Credentials with
lifetimes over 60 seconds or beyond the Auth session's absolute expiry are never
returned. Keep normal Auth access and refresh credentials separate from the
platform token; they serve different audiences.

`POST /delegated-token` accepts the Auth access token in `Authorization: Bearer`,
uses the same routing/project context, and returns only those three platform
fields. It revalidates the signed Auth token, session, active organization/user,
client, origin and current authorization version. A stale/invalid session returns
401 (normal refresh can obtain current authorization); missing permission or
configuration returns 403; gateway/configuration/lifetime failures return 503.
The normal login/refresh still succeeds when platform minting fails, preserving
the Auth credentials needed for recovery. It never falls back to a broader key.

Minting holds the same SQLite writer reservation as session/RBAC state changes,
with a five-second gateway timeout and twelve successful mints per session per
minute. A revocation committed before validation prevents issuance. Tokens issued
before logout/disablement/role changes remain independently valid until expiry;
this is bounded credential issuance, not immediate platform token revocation.
Already-open streams and in-flight work can outlive admission unless their target
app enforces expiry. Do not promise immediate stream termination from this feature.
There is no session schema migration and existing Auth sessions remain valid.

### Refresh failures and persistent browser sessions (v0.12.1)

Refresh clients must distinguish rejection from a failed or uncertain rotation:

- `401 {"error":"invalid_grant"}`: invalid/revoked session or an account/client
  that is no longer eligible. Discard the saved session. Reusing a previously
  rotated credential still revokes its entire session family.
- `503 {"error":"refresh_unavailable"}`: failure before rotation committed,
  including a rolled-back database operation. `Retry-After` is supplied; the
  same saved credential can be retried explicitly.
- `503 {"error":"refresh_uncertain"}`: commit outcome cannot be confirmed. Do not
  replay the old credential. Require login if no replacement was safely saved.

A generic proxy 5xx or lost response also leaves rotation uncertain. Browser
clients should serialize refreshes, re-read the saved credential under a cross-tab
lock, and save an in-progress marker before sending the credential. Persist the
replacement before releasing the lock. Platform mint denial still permits a
successful normal Auth refresh. Logout database lookup failures report an error
instead of falsely confirming revocation.

The opt-in `TestWebSDKPersistentBrowserIntegration` exercises the generic Web SDK
in real Chromium tabs against these handlers, including reload restoration,
rotation, logout and recovery after a rolled-back database failure. Set
`AUTH_WEB_SDK_TEST_DIR` to a checkout with Playwright Chromium installed to run it.
