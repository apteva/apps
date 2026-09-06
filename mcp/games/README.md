# Games v0.2

A game backend for Apteva with multiple independent games per project: players,
cloud saves, statistics, leaderboards, achievements, and moderation. Identity and
sessions come from the Auth app. The dashboard opens with a game catalog; every
workspace shows the selected game and keeps its data separate.

## Games and isolation

Create games from the dashboard or `games_create {name, slug, description?}`. Each has an
immutable ID and slug, a mutable title (`name`) and description, and a separate Auth organization
and native OAuth client provisioned on first login. Games can be archived and
restored. Titles are 1–100 bytes; descriptions are optional, up to 4,000 bytes,
and can be edited or cleared independently. Archiving blocks gameplay and scoped administration without deleting
data; catalog inspection and restoration remain available.

Every data operation checks both project and game. Definitions with the same
name can coexist in different games. Bans and erasures affect game membership;
they do not disable a shared Auth account. An erasure leaves a minimal scoped
Auth-user tombstone to prevent old sessions from recreating the membership.
Identity/session records must be handled separately through Auth.

## Upgrading from v0.1

Stop the old sidecar and take a consistent SQLite backup before the first v0.2
start. Allow space for a full table rebuild and its journal/WAL. Do not run v0.1
and v0.2 against the same database or roll the binary back against a migrated
database; rollback requires restoring the backup.

At startup, before serving requests, Games creates one persistent **Legacy game**
for each existing project, rebuilds the tables in one transaction, verifies row
counts and foreign keys, and commits the version marker with the data. Player
IDs, Auth mappings/client IDs, settings, save versions, statistics, all periods,
achievements, and audit history are copied. Invalid cross-project references
abort the migration and leave the original tables intact. A repeated start is
safe. Migration errors require repair of the original inconsistent rows before
retrying; do not suppress integrity checks.

`/v1` remains permanently bound to that project's legacy game. Creating another
game never redirects old clients. On admin/MCP surfaces, always pass `game_id`;
omission is accepted only if exactly one game is active. The legacy Auth
organization is preserved; new games receive dedicated organizations.

A ban created by v0.1 may also have disabled Auth. Recovery only re-enables that
account when Auth's latest disable audit record matches the recorded Games ban
reason. An unrelated suspension or missing evidence requires operator review.

## Player API

New client base: `/api/apps/games/v2/games/{game_id}`. A global installation also
needs `?project_id=...`. Legacy base: `/api/apps/games/v1`.

| Method | Path | Body or query |
|---|---|---|
| POST | `/login/device` | `{}` generates a guest secret; or `{device_id, display_name?}` |
| POST | `/login/custom` | `{login_ticket, display_name?}` |
| POST | `/login/link` | `{device_id}`; player bearer required |
| POST | `/session/refresh` | `{refresh_token}` |
| GET / PATCH | `/me` | PATCH: `{display_name?, avatar_url?, region?, locale?, metadata?}` |
| GET | `/players/{id}` | Public profile and public data |
| GET | `/data` | Own public/private saves |
| GET / PUT / DELETE | `/data/{key}` | PUT: `{value, visibility?, version?}` |
| GET / POST | `/stats` | POST: `{updates:[{stat,value}]}`; only client-writable definitions |
| GET | `/leaderboards/{name}` | `period`, `limit`, `cursor`; optional `include_me=true`, `include_total=true` |
| GET | `/leaderboards/{name}/around` | `period`, `radius`; optional `include_rank=true`, `include_total=true` |
| GET | `/achievements` | Hidden achievements appear after unlocking |

All non-login gameplay requests need `Authorization: Bearer <access_token>`.
JWT verification requires the signature, expiration, issuer, organization,
audience, and authorized client for this game. JWKS refreshes are shared between
concurrent requests with bounded stale-key use and outage backoff.

A v2 guest `device_id` is a **secret credential**, not a hardware identifier: use
at least 32 unpredictable bytes and store it securely, or retain the generated
64-character hex `device_id` returned by an empty-object login. Custom identities
require a trusted backend to authenticate the external player and call
`games_login_ticket {game_id, custom_id}` (or the protected
`POST /admin/games/{game_id}/login-ticket`). Exchange the returned one-time ticket
within 60 seconds. Never expose platform credentials to a game build. V2 does
not accept bare custom IDs or client-side custom-ID linking; provider/account
linking requires a trusted Auth workflow.

For compatibility, legacy `/v1` still accepts the original short device IDs and
bare custom IDs. **This retains the old credential-guessing risk.** Migrate clients
to secrets and tickets, then disable `legacy_custom_login_enabled`. Provider
verification for Steam/Apple/Google is not implemented here.

Refresh is proxied to Auth; refreshed tokens still have to pass the game's
membership, archive, and ban checks before accessing gameplay state.

## Writes and limits

Stat batches atomically update lifetime stats, period scores, achievements,
audit records, and the event outbox. A worse lifetime high score can still set
the current period's high score. Non-finite values and aggregate overflow fail
without partial writes. Aggregations are `last`, `max`, `min`, and `sum`;
`sum` inputs are increments. Keep competitive/reward stats server-only.

Send `Idempotency-Key` for HTTP stat updates and manual resets, or `operation_id`
for the corresponding MCP tools. Replays retain the first result for seven days;
a stat key reused with different input fails. After that window a retry is a new
operation. Reuse a key only for retries of the same logical operation.

Cloud saves are limited to 128 keys, 16 MiB of JSON per player, and 256 KiB per
value. `version` enables optimistic concurrency; omitted/zero version is an
unconditional write. Public/private/server visibility is checked inside the
write transaction, including client deletion restrictions. Server keys are never
returned to game clients.

Request limits are per minute: 300 authenticated requests per player, 30 logins
per credential, and 1,000 logins per game. These are application limits, not a
replacement for gateway abuse protection.

Leaderboard order is score, update time, then player ID. Cursor pagination and
nearby-player reads use bounded index ranges. V2 omits totals and personal rank
by default; exact totals/ranks and deep legacy offsets still scale with board
size. The v1/admin/MCP response defaults retain exact counts and ranks. Seasons
advance through missed periods without shifting their original schedule.

## Administration, events, and retention

New tools: `games_create`, `games_list`, `games_get`, `games_update`,
`games_archive`, `games_restore`, `games_login_ticket`, `games_events_retry`.
Existing `players_*`, `data_*`, `stats_*`, `leaderboards_*`, and `achievements_*`
tools remain available with `game_id`. Numeric player selectors and Auth identity
lookups are scoped to the selected game.

Protected `/admin/games` GET/POST and `/admin/games/{game_id}` GET/PATCH expose the
catalog, with POST `/archive` and `/restore`. Existing admin routes take a
`game_id` query parameter. `players_export` returns the complete retained audit
and historical leaderboard entries from a consistent snapshot.

AppBus topics: `player.created`, `player.linked`, `player.banned`,
`player.unbanned`, `player.erased`, `stat.updated`, `leaderboard.reset`, and
`achievement.unlocked`. Payloads contain `project_id`, `game_id`, and `event_id`.
Delivery is at least once: consumers should deduplicate by installation and
`event_id`. Events and optional Analytics calls are delivered by a worker, away
from gameplay requests, with exponential backoff. After ten failures the record
is retained; inspect `games_get`'s `events_pending`/`events_failed`, inspect the
outbox's `last_error` when diagnosing, and use `games_events_retry` after repair.

| Configuration | Default | Purpose |
|---|---|---|
| `auth_organization_slug` | `default` | Legacy game's existing Auth organization |
| `analytics_enabled` | `true` | Queue optional Analytics telemetry |
| `default_display_name_prefix` | `Player` | Generated names |
| `legacy_custom_login_enabled` | `true` | Old v1 custom-ID login compatibility |
| `audit_retention_days` | `0` | Positive number enables audit pruning; zero retains history |
| `history_retention_days` | `0` | Positive number prunes old non-current board entries |

SQLite remains the write ceiling. Monitor retained history and failed deliveries;
exports currently materialize all retained data in memory. The per-game Auth
provisioning lock is local to one sidecar process. Horizontal replicas against
the same SQLite file are not a supported deployment model.

## Validation and development

Use the published SDK pin; this worktree is outside the workspace's Go overlay:

```sh
cd mcp/games
GOWORK=off go test -short ./...       # unit, migration, regression, concurrency
GOWORK=off go test -race -cover ./... # includes actual Auth + Games sidecars
GOWORK=off go vet ./...
GOWORK=off go build -o /tmp/games .
GOWORK=off GAMES_PERF=1 go test -run TestPerformance -v .
cd ui
bun install --frozen-lockfile
bun run typecheck
bun run test
```

The integration test builds the sibling `mcp/auth` app, starts temporary sidecars
and databases, and exercises real SDK authentication, JWT login, refresh, linking,
and tickets through a local platform proxy. It needs Go, that sibling source,
and local loopback ports. No production services or credentials are used.

Rebuild the panel from the apps repository root:

```sh
APTEVA_DASHBOARD_DIR=/absolute/path/to/dashboard bun run scripts/build-panels.ts --app games
```

`performance_test.go` provides opt-in diagnostics at 1k, 100k, and 1m entries.
These are serial warm local reads, not end-to-end load or latency guarantees.
Paid live-agent scenarios in `scenarios/` are separate from the deterministic
suite and are not run by `go test`.
