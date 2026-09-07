---
name: games
description: Use Games tools for players, bans, player data, statistics, leaderboards, and achievements. Activate when the user asks about players, scores, rankings, saves, cheating, or a game's backend.
compatibility: Requires the Games MCP tools supplied by an Apteva app installation, with the Auth app installed alongside.
metadata:
  author: apteva
  version: "3.0"
---

# Games

Use the Games tools as the authoritative, project-scoped source for
players, player data, statistics, leaderboards, and achievements. A project
can contain multiple games. Start with `games_list`, select the intended game,
and pass its immutable `game_id` on every scoped call. Never infer the target
from a player name shared by several games.

## Operating rules

1. Read before writing. Fetch the player (`players_get_context` before
   any moderation or support action), the stat definition, or the
   leaderboard before changing it.
2. Never invent ids, scores, ranks, or ban reasons. Report what the tools
   return.
3. Statistics are server-authoritative. `stats_update` applies the stat's
   aggregation (for `sum`, the value is an increment), feeds leaderboards,
   and unlocks achievements. Keep stats that feed leaderboards or rewards
   server-only; set `client_writable` only for harmless telemetry the
   game client may report directly.
4. Bans and erasures are consequential. Confirm the target player and
   the reason with the operator when the request is ambiguous, and
   prefer a temporary ban (`expires_at`) unless told otherwise.
   `players_erase` is irreversible and needs `confirm=true`; run
   `players_export` first when the request is a data-subject request.
5. Player data (`data_*`) is the player's cloud save. Server-only keys
   (`visibility: server`) never reach game clients; use them for
   anti-cheat flags and server state. Pass `version` when the write must
   not clobber a newer save.
6. A leaderboard reset empties the board players see immediately.
   Use `leaderboards_reset_now` only on an explicit request; scheduled
   periods (daily, weekly, monthly, season) roll over on their own and
   past periods stay readable.

Use `operation_id` for stat updates and manual resets, reusing it only for
retries of the same operation within seven days. Bans and erasures apply to the
selected game; they do not disable a shared Auth account. Archive games with
`games_archive` and restore them with `games_restore`.

V2 custom login requires `games_login_ticket`, issued only after a trusted
backend authenticates the external player. Treat guest device IDs as secrets.

## Finding players

- `players_search` by display name or exact ids; `players_get` also
  accepts the device id or custom id the game client uses (Games hashes
  it and resolves it through Auth).
- Identity (device ids, sessions, email upgrades) lives in the Auth app
  under the player's `auth_user_id`. Use the Auth tools for account
  questions and the Games tools for gameplay state.

## Events

Games publishes `player.created`, `player.linked`, `player.banned`,
`player.unbanned`, `player.erased`, `stat.updated`, `leaderboard.reset`,
and `achievement.unlocked` on the AppBus. Subscribe to react to
milestones or moderation changes instead of polling. Filter by `game_id` and
deduplicate `event_id` per installation. Delivery is at least once; inspect
`games_get` for failed delivery counts and use `games_events_retry` after repair.

## Source, delivery and reporting

Use `games_sources_list` and `games_targets_list` to discover links for the chosen
game. `games_studio_discover` lists authorized Code repositories, Deploy targets
or reporting inventories; never guess repository, deployment or provider IDs.
Use `games_source_set` and `games_target_set` to attach existing resources.

Before a build or release, inspect `games_release_plan`. Keep engine commands,
signing, publisher accounts and release policy in Deploy. `games_build`,
`games_release` and `games_promote` require a unique request key; retain and reuse
it only for a retry of the same request. An unknown/dispatching result is not an
invitation to create a new request. Inspect its downstream outcome and reconcile
with the exact verified build/release ID. Use `resolution: not_created` only after
verifying that no operation was created, with explicit confirmation and notes.

Approval remains with the authorized Deploy caller. Do not invent or forward an
approver identity in Games arguments. Publication is not proof of availability;
report Deploy's observation state without promoting an unknown result to live.

Use `games_metric_source_set` for authorized AdMob, GA4 or Apple sales sources.
`games_metrics_sync` is bounded to 31 completed provider-local dates and upserts
reports; `games_metrics_query` always enforces game scope. Show missing/delayed
reports as unavailable, not zero. Preserve report currencies and estimated versus
sales-proceeds bases. Network and mediation totals overlap; daily active users
cannot be summed into monthly unique users. Connecting AdMob does not authorize
adding an advertising SDK or changing a paid game's monetization.

`games_portfolio` reads cached release summaries with timestamps and does not
contact providers. Use `games_release_status` for a current Deploy view.
