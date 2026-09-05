package main

// tools.go — MCP tools. Agents call these; the dashboard panel uses the
// /admin routes; game builds use /v1. All three meet in ops.go and
// progression.go so behaviour and audit rows do not depend on the caller.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// playerFromArgs resolves the player a tool call talks about. Accepts
// player_id (or id), auth_user_id, device_id, or custom_id; the last two
// are resolved through Auth so support staff can look a player up from
// what the game client knows.
func playerFromArgs(ctx *sdk.AppCtx, pid string, args map[string]any) (*Player, error) {
	db := ctx.AppDB()
	for _, key := range []string{"player_id", "id"} {
		if id, ok := intReq(args, key); ok {
			p, err := dbGetPlayer(db, pid, id)
			if err != nil {
				return nil, err
			}
			if p == nil {
				return nil, fmt.Errorf("player %d not found", id)
			}
			return p, nil
		}
	}
	if uid, ok := intReq(args, "auth_user_id"); ok {
		p, err := dbGetPlayerByAuthUser(db, pid, uid)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("no player for auth user %d", uid)
		}
		return p, nil
	}
	for _, pv := range []struct{ key, provider string }{{"device_id", "device"}, {"custom_id", "custom"}} {
		raw := stringArg(args, pv.key, "")
		if raw == "" {
			continue
		}
		uid, err := authResolveIdentity(ctx, pid, pv.provider, identitySubject(raw))
		if err != nil {
			return nil, err
		}
		if uid == 0 {
			return nil, fmt.Errorf("no player with that %s", pv.key)
		}
		p, err := dbGetPlayerByAuthUser(db, pid, uid)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("identity known to Auth but no player row (user %d)", uid)
		}
		return p, nil
	}
	return nil, errors.New("player_id (or id, auth_user_id, device_id, custom_id) required")
}

func parseStatUpdates(args map[string]any) ([]statUpdate, error) {
	var out []statUpdate
	if raw, ok := args["updates"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("updates must be a list of {stat, value}")
			}
			v, ok := floatArg(m, "value")
			if !ok {
				return nil, errors.New("every update needs a numeric value")
			}
			out = append(out, statUpdate{Stat: stringArg(m, "stat", ""), Value: v})
		}
	}
	if stat := stringArg(args, "stat", ""); stat != "" {
		v, ok := floatArg(args, "value")
		if !ok {
			return nil, errors.New("value required with stat")
		}
		out = append(out, statUpdate{Stat: stat, Value: v})
	}
	if len(out) == 0 {
		return nil, errors.New("pass updates: [{stat, value}] or stat + value")
	}
	if len(out) > 50 {
		return nil, errors.New("at most 50 updates per call")
	}
	return out, nil
}

func (a *App) MCPTools() []sdk.Tool {
	playerSelector := map[string]any{
		"player_id":    map[string]any{"type": "integer"},
		"auth_user_id": map[string]any{"type": "integer"},
		"device_id":    map[string]any{"type": "string"},
		"custom_id":    map[string]any{"type": "string"},
	}
	withPlayer := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range playerSelector {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	return []sdk.Tool{
		// ─── players ─────────────────────────────────────────────
		{
			Name:        "players_search",
			Description: "Search players. Args: q (substring of display_name, or an exact player id / auth user id), status (active | banned), limit (default 25, max 200), offset. Returns {players, count, total}.",
			InputSchema: schemaObject(map[string]any{
				"q":      map[string]any{"type": "string"},
				"status": map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolPlayersSearch,
		},
		{
			Name:        "players_get",
			Description: "Fetch one player. Args: player_id OR auth_user_id OR device_id OR custom_id (device and custom ids are hashed and resolved through Auth). Returns {player, active_ban}.",
			InputSchema: schemaObject(playerSelector, nil),
			Handler:     a.toolPlayersGet,
		},
		{
			Name:        "players_get_context",
			Description: "AUTHORITATIVE READ before support or moderation actions: player, active_ban, ban history, statistics, every data key including server-only ones, achievements, and the last 20 audit events.",
			InputSchema: schemaObject(playerSelector, nil),
			Handler:     a.toolPlayersGetContext,
		},
		{
			Name:        "players_update",
			Description: "Update a player's profile. Args: player selector plus any of display_name (1-64 chars), avatar_url, region, locale, metadata (public JSON object, 16 KB max).",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"display_name": map[string]any{"type": "string"},
				"avatar_url":   map[string]any{"type": "string"},
				"region":       map[string]any{"type": "string"},
				"locale":       map[string]any{"type": "string"},
				"metadata":     map[string]any{"type": "object"},
			}), nil),
			Handler: a.toolPlayersUpdate,
		},
		{
			Name:        "players_ban",
			Description: "Ban a player: records the ban, disables the Auth user, and revokes sessions. Args: player selector, reason (required), expires_at (RFC3339, omit for permanent). Read the player's context first and confirm with the operator when the request is ambiguous.",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"reason":     map[string]any{"type": "string"},
				"expires_at": map[string]any{"type": "string"},
			}), []string{"reason"}),
			Handler: a.toolPlayersBan,
		},
		{
			Name:        "players_unban",
			Description: "Lift every active ban on a player and re-enable the Auth user. Args: player selector.",
			InputSchema: schemaObject(playerSelector, nil),
			Handler:     a.toolPlayersUnban,
		},
		{
			Name:        "players_export",
			Description: "Export everything Games stores about one player (profile, bans, data, statistics, achievements, audit) for a data-subject access request. Identity records stay in Auth.",
			InputSchema: schemaObject(playerSelector, nil),
			Handler:     a.toolPlayersExport,
		},
		{
			Name:        "players_erase",
			Description: "IRREVERSIBLE. Delete a player's rows and disable the Auth user. Requires confirm=true. Export first when the request is a data-subject request.",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"confirm": map[string]any{"type": "boolean"},
			}), []string{"confirm"}),
			Handler: a.toolPlayersErase,
		},

		// ─── player data ─────────────────────────────────────────
		{
			Name:        "data_get",
			Description: "Read a player's data keys (all visibilities), or one key when key is given. Args: player selector, key (optional).",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"key": map[string]any{"type": "string"},
			}), nil),
			Handler: a.toolDataGet,
		},
		{
			Name:        "data_set",
			Description: "Write a player data key. Args: player selector, key ([A-Za-z0-9_.:-], max 128), value (any JSON), visibility (public | private | server; default keeps the current one, private for a new key), version (expected current version for an optimistic write; omit to overwrite). Server-only keys are never returned to game clients.",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"key":        map[string]any{"type": "string"},
				"value":      map[string]any{},
				"visibility": map[string]any{"type": "string"},
				"version":    map[string]any{"type": "integer"},
			}), []string{"key"}),
			Handler: a.toolDataSet,
		},
		{
			Name:        "data_delete",
			Description: "Delete a player data key. Args: player selector, key.",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"key": map[string]any{"type": "string"},
			}), []string{"key"}),
			Handler: a.toolDataDelete,
		},

		// ─── statistics ──────────────────────────────────────────
		{
			Name:        "stats_define",
			Description: "Define or update a statistic. Args: name (lowercase, digits, underscores), aggregation (last | max | min | sum; default last), client_writable (default false — keep anything feeding leaderboards or rewards server-only), description.",
			InputSchema: schemaObject(map[string]any{
				"name":            map[string]any{"type": "string"},
				"aggregation":     map[string]any{"type": "string"},
				"client_writable": map[string]any{"type": "boolean"},
				"description":     map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolStatsDefine,
		},
		{
			Name:        "stats_list",
			Description: "List statistic definitions.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolStatsList,
		},
		{
			Name:        "stats_get",
			Description: "Read one player's statistics. Args: player selector.",
			InputSchema: schemaObject(playerSelector, nil),
			Handler:     a.toolStatsGet,
		},
		{
			Name:        "stats_update",
			Description: "Server-authoritative statistic write. Args: player selector, updates [{stat, value}] (or a single stat + value). Applies the stat's aggregation (for sum, value is the increment), updates every leaderboard over the stat in its current period, and unlocks achievements. Undefined stats are auto-defined as server-only last-value stats. Returns {stats, applied, unlocked, rejected}.",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"updates": map[string]any{"type": "array"},
				"stat":    map[string]any{"type": "string"},
				"value":   map[string]any{"type": "number"},
			}), nil),
			Handler: a.toolStatsUpdate,
		},

		// ─── leaderboards ────────────────────────────────────────
		{
			Name:        "leaderboards_create",
			Description: "Create a leaderboard over a statistic. Args: name (slug), display_name, stat, sort (desc | asc; default desc), reset (none | daily | weekly | monthly | season; default none), season_days (season only; default 30). An unknown stat is auto-defined as a server-only max stat; define it first for sum or min semantics.",
			InputSchema: schemaObject(map[string]any{
				"name":         map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
				"stat":         map[string]any{"type": "string"},
				"sort":         map[string]any{"type": "string"},
				"reset":        map[string]any{"type": "string"},
				"season_days":  map[string]any{"type": "integer"},
			}, []string{"name", "stat"}),
			Handler: a.toolLeaderboardsCreate,
		},
		{
			Name:        "leaderboards_list",
			Description: "List leaderboards with their current period.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolLeaderboardsList,
		},
		{
			Name:        "leaderboards_get",
			Description: "Read a leaderboard page. Args: name, period (omit for the current one; past periods stay readable), limit (default 50, max 200), offset, plus an optional player selector to include that player's rank as `me`.",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"name":   map[string]any{"type": "string"},
				"period": map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer"},
			}), []string{"name"}),
			Handler: a.toolLeaderboardsGet,
		},
		{
			Name:        "leaderboards_around_player",
			Description: "Entries around one player with the player's rank. Args: name, player selector, radius (default 5, max 50), period (optional).",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"name":   map[string]any{"type": "string"},
				"period": map[string]any{"type": "string"},
				"radius": map[string]any{"type": "integer"},
			}), []string{"name"}),
			Handler: a.toolLeaderboardsAroundPlayer,
		},
		{
			Name:        "leaderboards_reset_now",
			Description: "Start a fresh period on a leaderboard right now; previous entries stay readable by period. Confirm with the operator first — players see an empty board immediately.",
			InputSchema: schemaObject(map[string]any{
				"name": map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolLeaderboardsResetNow,
		},

		// ─── achievements ────────────────────────────────────────
		{
			Name:        "achievements_define",
			Description: "Define or update an achievement. Args: key (slug), name, description, stat + threshold + op (gte | gt | lte | lt | eq; default gte) for automatic unlocks, hidden (not shown to players until unlocked). Omit stat for manual-grant achievements.",
			InputSchema: schemaObject(map[string]any{
				"key":         map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"stat":        map[string]any{"type": "string"},
				"threshold":   map[string]any{"type": "number"},
				"op":          map[string]any{"type": "string"},
				"hidden":      map[string]any{"type": "boolean"},
			}, []string{"key", "name"}),
			Handler: a.toolAchievementsDefine,
		},
		{
			Name:        "achievements_list",
			Description: "List achievement definitions.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolAchievementsList,
		},
		{
			Name:        "achievements_grant",
			Description: "Manually unlock an achievement for a player. Args: player selector, key. Idempotent.",
			InputSchema: schemaObject(withPlayer(map[string]any{
				"key": map[string]any{"type": "string"},
			}), []string{"key"}),
			Handler: a.toolAchievementsGrant,
		},
	}
}

// ─── players ─────────────────────────────────────────────────────────

func (a *App) toolPlayersSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	status := stringArg(args, "status", "")
	if status != "" && status != "active" && status != "banned" {
		return nil, errors.New("status must be active or banned")
	}
	limit := intArg(args, "limit", 25, 1, 200)
	offset := intArg(args, "offset", 0, 0, 1_000_000)
	players, total, err := dbSearchPlayers(ctx.AppDB(), pid, stringArg(args, "q", ""), status, limit, offset)
	if err != nil {
		return nil, err
	}
	return map[string]any{"players": players, "count": len(players), "total": total, "offset": offset}, nil
}

func (a *App) toolPlayersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	ban, err := activeBanFor(ctx, pid, p)
	if err != nil {
		return nil, err
	}
	return map[string]any{"player": p, "active_ban": ban}, nil
}

func (a *App) toolPlayersGetContext(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	return playerContext(ctx, pid, p)
}

func (a *App) toolPlayersUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	for _, k := range []string{"display_name", "avatar_url", "region", "locale", "metadata"} {
		if v, ok := args[k]; ok {
			fields[k] = v
		}
	}
	updated, err := applyProfilePatch(ctx, pid, p, fields, "agent")
	if err != nil {
		return nil, err
	}
	return map[string]any{"player": updated}, nil
}

func (a *App) toolPlayersBan(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	ban, err := banPlayer(ctx, pid, p, stringArg(args, "reason", ""), stringArg(args, "expires_at", ""), "agent")
	if err != nil {
		return nil, err
	}
	return map[string]any{"ban": ban, "player": p}, nil
}

func (a *App) toolPlayersUnban(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	n, err := unbanPlayer(ctx, pid, p, "agent")
	if err != nil {
		return nil, err
	}
	return map[string]any{"lifted": n, "player": p}, nil
}

func (a *App) toolPlayersExport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	out, err := exportPlayer(ctx, pid, p)
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, p.ID, "player.exported", "agent", nil)
	return map[string]any{"export": out}, nil
}

func (a *App) toolPlayersErase(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if !boolArg(args, "confirm", false) {
		return nil, errors.New("pass confirm=true to erase a player; this cannot be undone")
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if err := erasePlayer(ctx, pid, p, "agent"); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "player_id": p.ID, "auth_user_id": p.AuthUserID}, nil
}

// ─── player data ─────────────────────────────────────────────────────

func (a *App) toolDataGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if key := stringArg(args, "key", ""); key != "" {
		e, err := dbGetData(ctx.AppDB(), pid, p.ID, key)
		if err != nil {
			return nil, err
		}
		if e == nil {
			return map[string]any{"data": []DataEntry{}, "found": false}, nil
		}
		return map[string]any{"data": []DataEntry{*e}, "found": true}, nil
	}
	entries, err := dbListData(ctx.AppDB(), pid, p.ID, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"data": entries, "count": len(entries)}, nil
}

func (a *App) toolDataSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	key := stringArg(args, "key", "")
	if !dataKeyRe.MatchString(key) {
		return nil, errors.New("key must match [A-Za-z0-9_.:-]{1,128}")
	}
	raw, ok := args["value"]
	if !ok || raw == nil {
		return nil, errors.New("value required (any JSON value)")
	}
	value, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("value must be JSON-encodable: %w", err)
	}
	if len(value) > maxDataValueBytes {
		return nil, errors.New("value must be 256 KB or less")
	}
	visibility := stringArg(args, "visibility", "")
	switch visibility {
	case "", "public", "private", "server":
	default:
		return nil, errors.New("visibility must be public, private, or server")
	}
	var version int64
	if v, ok := intReq(args, "version"); ok {
		version = v
	}
	e, err := dbSetData(ctx.AppDB(), pid, p.ID, key, string(value), visibility, version)
	if errors.Is(err, errVersionConflict) {
		current, _ := dbGetData(ctx.AppDB(), pid, p.ID, key)
		if current != nil {
			return nil, fmt.Errorf("version_conflict: key %q is at version %d", key, current.Version)
		}
		return nil, fmt.Errorf("version_conflict: key %q does not exist yet", key)
	}
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, p.ID, "data.set", "agent", map[string]any{"key": key, "visibility": e.Visibility, "version": e.Version})
	return map[string]any{"data": e}, nil
}

func (a *App) toolDataDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	key := stringArg(args, "key", "")
	if key == "" {
		return nil, errors.New("key required")
	}
	deleted, err := dbDeleteData(ctx.AppDB(), pid, p.ID, key)
	if err != nil {
		return nil, err
	}
	if deleted {
		dbAudit(ctx.AppDB(), pid, p.ID, "data.deleted", "agent", map[string]any{"key": key})
	}
	return map[string]any{"deleted": deleted}, nil
}

// ─── statistics ──────────────────────────────────────────────────────

func (a *App) toolStatsDefine(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	def, err := defineStat(ctx, pid, stringArg(args, "name", ""), stringArg(args, "aggregation", ""),
		boolArg(args, "client_writable", false), stringArg(args, "description", ""))
	if err != nil {
		return nil, err
	}
	return map[string]any{"stat": def}, nil
}

func (a *App) toolStatsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	defs, err := dbListStatDefs(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"stats": defs, "count": len(defs)}, nil
}

func (a *App) toolStatsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	stats, err := dbGetPlayerStats(ctx.AppDB(), pid, p.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"player_id": p.ID, "stats": stats}, nil
}

func (a *App) toolStatsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	updates, err := parseStatUpdates(args)
	if err != nil {
		return nil, err
	}
	return applyStatUpdates(ctx, pid, p, updates, "agent", false)
}

// ─── leaderboards ────────────────────────────────────────────────────

func (a *App) toolLeaderboardsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	lb, err := createLeaderboard(ctx, pid, stringArg(args, "name", ""), stringArg(args, "display_name", ""),
		stringArg(args, "stat", ""), stringArg(args, "sort", ""), stringArg(args, "reset", ""),
		intArg(args, "season_days", 0, 0, 3650))
	if err != nil {
		return nil, err
	}
	return map[string]any{"leaderboard": lb}, nil
}

func (a *App) toolLeaderboardsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	boards, err := dbListLeaderboards(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	for i := range boards {
		if changed, prev := ensureCurrentPeriod(ctx, pid, &boards[i], time.Now()); changed {
			emitReset(ctx, &boards[i], prev, false)
		}
	}
	return map[string]any{"leaderboards": boards, "count": len(boards)}, nil
}

func (a *App) leaderboardFromArgs(ctx *sdk.AppCtx, pid string, args map[string]any) (*Leaderboard, error) {
	name := strings.ToLower(stringArg(args, "name", ""))
	if name == "" {
		return nil, errors.New("name required")
	}
	lb, err := dbGetLeaderboard(ctx.AppDB(), pid, name)
	if err != nil {
		return nil, err
	}
	if lb == nil {
		return nil, fmt.Errorf("leaderboard %q not found", name)
	}
	return lb, nil
}

func (a *App) toolLeaderboardsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	lb, err := a.leaderboardFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	var me int64
	if p, err := playerFromArgs(ctx, pid, args); err == nil && p != nil {
		me = p.ID
	}
	return leaderboardPageFor(ctx, pid, lb, stringArg(args, "period", ""),
		intArg(args, "limit", 50, 1, 200), intArg(args, "offset", 0, 0, 1_000_000), me)
}

func (a *App) toolLeaderboardsAroundPlayer(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	lb, err := a.leaderboardFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	return leaderboardAround(ctx, pid, lb, stringArg(args, "period", ""), p.ID, intArg(args, "radius", 5, 1, 50))
}

func (a *App) toolLeaderboardsResetNow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	lb, err := a.leaderboardFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	previous := lb.CurrentPeriod
	if err := resetLeaderboardNow(ctx, pid, lb, time.Now()); err != nil {
		return nil, err
	}
	return map[string]any{"leaderboard": lb, "previous_period": previous}, nil
}

// ─── achievements ────────────────────────────────────────────────────

func (a *App) toolAchievementsDefine(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	threshold, _ := floatArg(args, "threshold")
	def, err := defineAchievement(ctx, pid, AchievementDef{
		Key: stringArg(args, "key", ""), Name: stringArg(args, "name", ""),
		Description: stringArg(args, "description", ""), Stat: stringArg(args, "stat", ""),
		Threshold: threshold, Op: stringArg(args, "op", ""), Hidden: boolArg(args, "hidden", false),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"achievement": def}, nil
}

func (a *App) toolAchievementsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	defs, err := dbListAchievementDefs(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"achievements": defs, "count": len(defs)}, nil
}

func (a *App) toolAchievementsGrant(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	p, err := playerFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	unlocked, err := grantAchievement(ctx, pid, p, stringArg(args, "key", ""), "agent")
	if err != nil {
		return nil, err
	}
	return map[string]any{"unlocked": unlocked, "player_id": p.ID}, nil
}
