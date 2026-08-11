# Auth (v0.9.0)

Identity and authorization layer for Apteva-deployed SaaS. One project-scoped
install owns multiple organizations; each has isolated users, clients, signing
keys, sessions, roles, and permissions.

## Trusted authorization context

Login, session-creating signup, refresh, and `/me` return a server-managed
`authorization` object, and the same fields are signed into the access JWT:

```json
{
  "user_id": "123",
  "organization_id": "8",
  "organization_slug": "acme",
  "roles": ["supervisor"],
  "permissions": ["calls:view_team", "commercials:supervise"],
  "authorization_version": 4
}
```

Auth never derives trusted authorization from self-service user metadata.
Changing a user's roles, changing an assigned role's permissions, or deleting
an effective role or permission increments affected users'
`authorization_version`. Auth rejects stale access tokens on its authenticated
routes; refresh tokens mint a current context. Offline consumers that require
immediate revocation must compare the signed version with current server state.

The dashboard **Roles** tab manages roles and permissions, and the user drawer
manages role assignments. The equivalent MCP tools are
`auth_roles_{list,create,update,delete}`,
`auth_permissions_{list,create,update,delete}`,
`auth_role_permissions_set`, and `auth_user_roles_set`.

## Pipeline of an Apteva-deployed SaaS

```
SaaS frontend ─POST /apps/auth/signup──▶  auth-app sidecar ──▶ users.db (SQLite)
                                                │
                                                ├──▶ /.well-known/jwks.json (public)
                                                │
                                                └──▶ messaging app (verify, reset, magic link)
```

Agents administer the pool via MCP tools; deployed frontends use the HTTP
routes; the dashboard manages organizations, users, roles, permissions,
clients, and OIDC settings.

## Core capabilities

**Identity** — email/password signup and login, verification, password reset,
magic links, TOTP MFA, rotating refresh tokens, per-organization EdDSA signing
keys and JWKS, and an audit trail.

**Organizations** — users, OAuth clients, sessions, signing keys, roles,
permissions, and audit events are partitioned by `(project_id,
organization_id)`.

**Authorization** — organization-owned roles and permissions with atomic
role-permission and user-role replacement. Machine keys are immutable.

**Crypto** — argon2id passwords (PHC string format, portable), EdDSA JWTs with rotating signing keys, sha256-hashed refresh + verification tokens. JWT verification uses JWKS — every consumer (other apps, the SaaS's own backend) verifies offline, no network call to auth.

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
POST /signup    { email, password, client_id }        → 201 { user, authorization, access_token, refresh_token, expires_in }
                                                      → 202 { user } when email_verification_required=true
POST /login     { email, password, client_id }        → 200 { user, authorization, access_token, refresh_token, expires_in }
                                                      → 401 invalid_grant
                                                      → 423 account_locked
POST /refresh   { refresh_token, client_id }          → 200 { authorization, access_token, refresh_token, expires_in } (rotated)
POST /logout    { refresh_token }                     → 204
GET  /me        Authorization: Bearer <access_token>  → 200 { user, authorization }
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
