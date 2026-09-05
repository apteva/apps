# Games (v0.1)

Game backend for Apteva: players and progression, operated by an agent.
The pieces a studio would otherwise take from PlayFab, Nakama, or Unity
Gaming Services, composed from sibling apps wherever one already exists.

## What's in v0.1

- **Players** — guest login by device id and custom id, profiles
  (display name, avatar, region, locale, public metadata), bans with
  expiry, data-subject export and erase.
- **Player data** — versioned JSON keys (cloud save) with `public`,
  `private`, and `server` visibility and optimistic writes.
- **Statistics** — server-authoritative, with `last`, `max`, `min`, and
  `sum` aggregation and a per-stat `client_writable` flag.
- **Leaderboards** — one per statistic, `desc` or `asc`, with `none`,
  `daily`, `weekly`, `monthly`, or `season` periods. Past periods stay
  readable; a manual reset starts a fresh one.
- **Achievements** — unlocked when a statistic meets a threshold, or
  granted by hand. Hidden achievements stay hidden until unlocked.
- **Surfaces** — `/v1` player API for game builds, 23 MCP tools for
  agents, a dashboard panel, an agent skill, and eight AppBus events.

## How identity works

Games owns no credentials. The **Auth app** (v0.10.0 or later) owns the
account:

1. On first login Games registers one native OAuth client named `games`
   in the configured Auth organization and remembers its `client_id`.
2. `POST /v1/login/device` hashes the device id and calls Auth's
   `auth_public_login_identity`. Auth finds or creates a guest user for
   the `(device, <sha256>)` pair and mints an access + refresh token.
3. Games upserts the player row keyed by `auth_user_id` and returns the
   tokens with the player.
4. Every other `/v1` request carries `Authorization: Bearer <access>`.
   Games verifies the EdDSA signature against Auth's JWKS (fetched once
   through `auth_jwks_get` and cached), checks expiry, and maps `sub`
   to the player.
5. Refresh goes to Auth (`/api/apps/auth/refresh`, or the
   `/v1/session/refresh` proxy when the gateway URL is configured).
   Bans disable the Auth user, so refresh stops working too.

Upgrading a guest to an email account and linking Steam, Apple, or
Google Play identities are Auth features (`auth_guest_upgrade`,
`auth_identities_link`); provider ticket verification arrives in Games
v0.2.

## Player API

Base path: `/api/apps/games/v1` (append `?project_id=` on a global
install; project-scoped installs need nothing).

| Method | Path | Body / query | Notes |
|---|---|---|---|
| POST | `/login/device` | `{device_id, display_name?}` | 201 on first login, 200 after |
| POST | `/login/custom` | `{custom_id, display_name?}` | same shape |
| POST | `/login/link` | `{device_id}` or `{custom_id}` | bearer required; 409 if owned by another player |
| POST | `/session/refresh` | `{refresh_token}` | proxied to Auth |
| GET | `/me` | | player |
| PATCH | `/me` | `{display_name?, avatar_url?, region?, locale?, metadata?}` | |
| GET | `/players/{id}` | | public profile + public data |
| GET | `/data` | | public + private keys |
| GET/PUT/DELETE | `/data/{key}` | `{value, visibility?, version?}` | 409 on version conflict, 403 on server keys |
| GET | `/stats` | | |
| POST | `/stats` | `{updates: [{stat, value}]}` | only `client_writable` stats apply; others are listed in `rejected` |
| GET | `/leaderboards/{name}` | `?period=&limit=&offset=` | includes `me` |
| GET | `/leaderboards/{name}/around` | `?radius=&period=` | |
| GET | `/achievements` | | hidden ones appear once unlocked |

Errors are JSON: `{"error": "banned", "reason": "...", "expires_at": "..."}`,
`{"error": "invalid_token"}`, `{"error": "version_conflict", "current": {...}}`.

```bash
# first login
curl -s -X POST "$BASE/api/apps/games/v1/login/device" \
  -H 'Content-Type: application/json' \
  -d '{"device_id":"ios-6F2A","display_name":"Ada"}'

# write a save with optimistic concurrency
curl -s -X PUT "$BASE/api/apps/games/v1/data/save" \
  -H "Authorization: Bearer $ACCESS" -H 'Content-Type: application/json' \
  -d '{"value":{"level":4,"gold":120},"version":1}'
```

## MCP tools

`players_search`, `players_get`, `players_get_context`, `players_update`,
`players_ban`, `players_unban`, `players_export`, `players_erase`,
`data_get`, `data_set`, `data_delete`, `stats_define`, `stats_list`,
`stats_get`, `stats_update`, `leaderboards_create`, `leaderboards_list`,
`leaderboards_get`, `leaderboards_around_player`,
`leaderboards_reset_now`, `achievements_define`, `achievements_list`,
`achievements_grant`.

Every player-scoped tool accepts `player_id`, `auth_user_id`,
`device_id`, or `custom_id`; the last two are hashed and resolved through
Auth, so support staff can start from what the game client knows.

## Events

`player.created`, `player.linked`, `player.banned`, `player.unbanned`,
`player.erased`, `stat.updated`, `leaderboard.reset`,
`achievement.unlocked`. Payloads are declared in `apteva.yaml`.

## Configuration

| Key | Default | Purpose |
|---|---|---|
| `auth_organization_slug` | `default` | Auth organization holding this title's players |
| `analytics_enabled` | `true` | send `games.session_start` and unlock events to Analytics when installed |
| `default_display_name_prefix` | `Player` | name for players who log in without one |

## Local development

```bash
cd mcp/games
GOWORK=off go build .
GOWORK=off go test ./...
APTEVA_PROJECT_ID=test ./games      # boots on :8080; /v1 needs a running Auth to log in
```

Tier 1 tests replace Auth and Analytics with an in-process fake that
signs real EdDSA tokens (`handlers_test.go`). Tier 3 scenarios live in
`scenarios/`.

## Limits worth knowing

- SQLite is the write ceiling: stat writes and leaderboard updates are
  short transactions, and ranking is computed per read, which is fine
  for indie and mid-size titles and not for a viral hit.
- Erasing a player deletes Games rows and disables the Auth user; the
  Auth user row itself is retained by Auth.
- No provider identities (Steam, Apple, Google Play) and no economy in
  v0.1; see the proposal roadmap for v0.2 onward.
