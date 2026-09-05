---
name: games
description: Use Games tools for players, bans, player data, statistics, leaderboards, and achievements. Activate when the user asks about players, scores, rankings, saves, cheating, or a game's backend.
compatibility: Requires the Games MCP tools supplied by an Apteva app installation, with the Auth app installed alongside.
metadata:
  author: apteva
  version: "1.0"
---

# Games

Use the Games tools as the authoritative, project-scoped source for
players, player data, statistics, leaderboards, and achievements. One
Apteva project is one game title.

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
milestones or moderation changes instead of polling.
