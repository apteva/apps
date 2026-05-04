# Auth (v0.1.0)

Identity layer for Apteva-deployed SaaS. The shape borrows from Auth0 / AWS Cognito / Keycloak: **one user pool per project, many `clients` (frontends / mobile apps / backend services / M2M integrations) inside it.**

## Pipeline of an Apteva-deployed SaaS

```
SaaS frontend ─POST /apps/auth/signup──▶  auth-app sidecar ──▶ users.db (SQLite)
                                                │
                                                ├──▶ /.well-known/jwks.json (public)
                                                │
                                                └──▶ messaging app (verify, reset, magic link)
```

Agents administer the pool via MCP tools; the deployed SaaS frontend hits the HTTP routes; the dashboard renders Users / Clients / Settings panels.

## What ships in v0.1.0

**Schema (9 tables)** — `users`, `clients`, `oauth_identities`, `sessions`, `verification_tokens`, `mfa_factors`, `recovery_codes`, `signing_keys`, `audit_log`. Every row partitioned by `project_id`.

**HTTP surface** — `/health`, `/.well-known/jwks.json`, `/.well-known/openid-configuration`, `/signup`, `/login`, `/refresh`, `/logout`, `/me`. Email + password core. Refresh-token rotation is on by default; replaying a rotated token is rejected with 401.

**MCP surface (12 tools)** — `auth_users_search`, `_get`, `_get_context`, `_disable`, `_enable`, `_revoke_sessions`, `auth_audit_search`, `auth_stats`, `auth_clients_list`, `_create`, `_rotate_secret`, `_disable`.

**Crypto** — argon2id passwords (PHC string format, portable), EdDSA JWTs with rotating signing keys, sha256-hashed refresh + verification tokens. JWT verification uses JWKS — every consumer (other apps, the SaaS's own backend) verifies offline, no network call to auth.

## What's deliberately deferred

- `/password/forgot` + `/password/reset` (mail.go has the helper plumbing)
- `/email/verify` consumer endpoint (token issuance is wired; the consume route is the next step)
- `/magic-link/request` + `/magic-link/consume`
- `/mfa/totp/enroll` + `/mfa/totp/verify` (table is shipped)
- `/oauth/{provider}` + `/oauth/{provider}/callback` (Google, GitHub, Apple)
- Admin MCP tools: `auth_users_create`, `_invite`, `_delete`, `_set_email`, `_send_password_reset`
- UI panels (Users / Clients / Settings)
- SAML, WebAuthn, SMS OTP — out of scope for v0.x

## Local development

```bash
cd mcp/auth
go build .
APTEVA_PROJECT_ID=test-proj ./auth          # binds :8080
curl http://localhost:8080/health
```

## Tests (three tiers)

| Tier | Where | What | Speed |
|---|---|---|---|
| 1 | `handlers_test.go`, `crypto_test.go`, `manifest_test.go` | Direct handler calls against in-memory SQLite. Real argon2id, real EdDSA, real migrations. | ~1s |
| 2 | `integration_test.go` (build tag `integration`) | `tk.SpawnSidecar` boots the real binary. Tests sign up, log in, refresh, JWKS, OIDC discovery, and MCP tools over HTTP. | ~3s |
| 3 | `scenarios/*.yaml` | Real apteva-core spawned, real LLM tool calls. Each scenario gives an agent a directive and asserts on outcomes via the REST surface. | ~minutes, real $$ |

```bash
go test ./...                    # Tier 1
go test -tags integration ./...  # Tier 1 + Tier 2
apteva test ./scenarios/         # Tier 3 — needs an LLM key + apteva runner
```

Tier 3 covers: register a client, disable a spam user, revoke sessions during an incident, audit-trail investigation (read-only), rotate a client secret.

`/me`'s JWT-verification path is exercised in Tier 1 only: the testkit's HTTP helpers always send the sidecar's `APTEVA_APP_TOKEN` as the Authorization header, so a Tier 2 sidecar can't receive a *user* JWT. Tier 1's `httptest.NewRequest` route can.

## Auth flow at a glance

```
POST /signup    { email, password, client_id }        → 201 { user, access_token, refresh_token, expires_in }
                                                      → 202 { user } when email_verification_required=true
POST /login     { email, password, client_id }        → 200 { user, access_token, refresh_token, expires_in }
                                                      → 401 invalid_grant
                                                      → 423 account_locked
POST /refresh   { refresh_token, client_id }          → 200 { access_token, refresh_token, expires_in } (rotated)
POST /logout    { refresh_token }                     → 204
GET  /me        Authorization: Bearer <access_token>  → 200 { user }
GET  /.well-known/jwks.json                           → public keys for JWT verification
```

## Composing with messaging

`auth` declares `messaging` as an optional dep. When messaging is installed and bound, transactional emails go through `ctx.PlatformAPI().CallApp("messaging", "send", …)`. When it isn't, links are written to the audit log only — a development escape hatch (see `mail.go`).

## Schema scope

Every table carries `project_id` so the same code would serve `scope: global` later if shared user pools across projects ever become useful. v0.1 ships project-only.

## File layout

```
mcp/auth/
├── apteva.yaml          ← manifest (single source of truth for tool list)
├── main.go              ← App, embedded manifest, route + tool wiring, helpers
├── crypto.go            ← argon2id, sha256, EdDSA JWT sign/verify, JWKS, randSlug
├── types.go             ← User / Client / Session / AuditEvent JSON shapes
├── db.go                ← all SQL (no business logic)
├── handlers.go          ← HTTP handlers
├── tools.go             ← MCP tool handlers
├── mail.go              ← composes with messaging app
├── migrations/001_init.sql
├── manifest_test.go     ← drift between disk + embedded + implementation
├── crypto_test.go       ← password / JWT / token round-trips
└── integration_test.go  ← signup→login→refresh→me→logout end-to-end
```
