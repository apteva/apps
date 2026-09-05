package main

// ops.go — shared player operations used by the MCP tools, the dashboard
// REST surface, and the public API. Anything that changes a player's
// state goes through here so audit rows and AppBus events stay the same
// regardless of who called.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// trackEvent forwards telemetry to the Analytics app when installed and
// enabled. Failures are swallowed: telemetry never blocks gameplay.
func trackEvent(ctx *sdk.AppCtx, pid, event string, player *Player, props map[string]any) {
	if ctx == nil || ctx.PlatformAPI() == nil || player == nil || !cfgBool(ctx, "analytics_enabled", true) {
		return
	}
	if props == nil {
		props = map[string]any{}
	}
	props["player_id"] = player.ID
	var out map[string]any
	_ = ctx.PlatformAPI().CallAppResult("analytics", "analytics_track", map[string]any{
		"_project_id": pid,
		"event":       "games." + event,
		"app":         "games",
		"user_id":     fmt.Sprintf("player:%d", player.ID),
		"props":       props,
	}, &out)
}

func publicProfile(p *Player) map[string]any {
	return map[string]any{
		"id": p.ID, "display_name": p.DisplayName, "avatar_url": p.AvatarURL,
		"region": p.Region, "metadata": p.Metadata, "created_at": p.CreatedAt,
	}
}

// ─── bans ────────────────────────────────────────────────────────────

func banPlayer(ctx *sdk.AppCtx, pid string, player *Player, reason, expiresAt, source string) (*Ban, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.New("reason required")
	}
	if expiresAt = strings.TrimSpace(expiresAt); expiresAt != "" {
		t, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return nil, errors.New("expires_at must be RFC3339, e.g. 2026-09-10T00:00:00Z")
		}
		if !t.After(time.Now()) {
			return nil, errors.New("expires_at is in the past")
		}
		expiresAt = t.UTC().Format(time.RFC3339)
	}
	db := ctx.AppDB()
	id, err := dbCreateBan(db, pid, player.ID, reason, source, expiresAt)
	if err != nil {
		return nil, err
	}
	if err := dbSetPlayerStatus(db, pid, player.ID, "banned"); err != nil {
		return nil, err
	}
	if err := authDisableUser(ctx, pid, player.AuthUserID, "games ban: "+reason); err != nil {
		ctx.Logger().Warn("auth disable failed; ban is enforced locally", "player_id", player.ID, "err", err)
	}
	dbAudit(db, pid, player.ID, "player.banned", source, map[string]any{"reason": reason, "expires_at": expiresAt})
	ctx.Emit("player.banned", map[string]any{"player_id": player.ID, "reason": reason, "expires_at": expiresAt, "source": source})
	player.Status = "banned"
	return &Ban{ID: id, PlayerID: player.ID, Reason: reason, Source: source, ExpiresAt: expiresAt, CreatedAt: nowRFC()}, nil
}

func unbanPlayer(ctx *sdk.AppCtx, pid string, player *Player, source string) (int64, error) {
	db := ctx.AppDB()
	n, err := dbLiftBans(db, pid, player.ID)
	if err != nil {
		return 0, err
	}
	if err := dbSetPlayerStatus(db, pid, player.ID, "active"); err != nil {
		return 0, err
	}
	if err := authEnableUser(ctx, pid, player.AuthUserID); err != nil {
		ctx.Logger().Warn("auth enable failed", "player_id", player.ID, "err", err)
	}
	dbAudit(db, pid, player.ID, "player.unbanned", source, map[string]any{"lifted": n})
	ctx.Emit("player.unbanned", map[string]any{"player_id": player.ID, "source": source})
	player.Status = "active"
	return n, nil
}

// activeBanFor returns the ban currently blocking the player, lifting an
// expired one lazily (the Auth user is re-enabled at the same time).
func activeBanFor(ctx *sdk.AppCtx, pid string, player *Player) (*Ban, error) {
	if player.Status != "banned" {
		return nil, nil
	}
	ban, err := dbActiveBan(ctx.AppDB(), pid, player.ID)
	if err != nil {
		return nil, err
	}
	if ban != nil {
		return ban, nil
	}
	if _, err := unbanPlayer(ctx, pid, player, "expiry"); err != nil {
		return nil, err
	}
	return nil, nil
}

// ─── erase / export / context ────────────────────────────────────────

func erasePlayer(ctx *sdk.AppCtx, pid string, player *Player, source string) error {
	if err := authDisableUser(ctx, pid, player.AuthUserID, "games: player erased"); err != nil {
		ctx.Logger().Warn("auth disable on erase failed", "player_id", player.ID, "err", err)
	}
	if err := authRevokeSessions(ctx, pid, player.AuthUserID); err != nil {
		ctx.Logger().Warn("auth session revoke on erase failed", "player_id", player.ID, "err", err)
	}
	if err := dbDeletePlayer(ctx.AppDB(), pid, player.ID); err != nil {
		return err
	}
	ctx.Emit("player.erased", map[string]any{"player_id": player.ID, "auth_user_id": player.AuthUserID, "source": source})
	return nil
}

func exportPlayer(ctx *sdk.AppCtx, pid string, player *Player) (map[string]any, error) {
	db := ctx.AppDB()
	bans, err := dbListBans(db, pid, player.ID)
	if err != nil {
		return nil, err
	}
	data, err := dbListData(db, pid, player.ID, nil)
	if err != nil {
		return nil, err
	}
	stats, err := dbGetPlayerStats(db, pid, player.ID)
	if err != nil {
		return nil, err
	}
	achievements, err := dbPlayerAchievements(db, pid, player.ID)
	if err != nil {
		return nil, err
	}
	audit, err := dbListAudit(db, pid, player.ID, 500)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"exported_at":  nowRFC(),
		"player":       player,
		"bans":         bans,
		"data":         data,
		"stats":        stats,
		"achievements": achievements,
		"audit":        audit,
		"note":         "Identity records (device and custom ids, sessions) are held by the Auth app under auth_user_id.",
	}, nil
}

func playerContext(ctx *sdk.AppCtx, pid string, player *Player) (map[string]any, error) {
	db := ctx.AppDB()
	ban, err := activeBanFor(ctx, pid, player)
	if err != nil {
		return nil, err
	}
	bans, err := dbListBans(db, pid, player.ID)
	if err != nil {
		return nil, err
	}
	stats, err := dbGetPlayerStats(db, pid, player.ID)
	if err != nil {
		return nil, err
	}
	data, err := dbListData(db, pid, player.ID, nil)
	if err != nil {
		return nil, err
	}
	achievements, err := dbPlayerAchievements(db, pid, player.ID)
	if err != nil {
		return nil, err
	}
	audit, err := dbListAudit(db, pid, player.ID, 20)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"player": player, "active_ban": ban, "bans": bans, "stats": stats,
		"data": data, "achievements": achievements, "audit": audit,
	}, nil
}

// ─── profile ─────────────────────────────────────────────────────────

func applyProfilePatch(ctx *sdk.AppCtx, pid string, player *Player, in map[string]any, source string) (*Player, error) {
	var patch playerPatch
	changed := map[string]any{}
	if v, ok := in["display_name"]; ok {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 64 {
			return nil, errors.New("display_name must be 1-64 characters")
		}
		patch.DisplayName = &s
		changed["display_name"] = s
	}
	if v, ok := in["avatar_url"]; ok {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		if s != "" {
			u, err := url.Parse(s)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s) > 512 {
				return nil, errors.New("avatar_url must be an http(s) URL of at most 512 characters")
			}
		}
		patch.AvatarURL = &s
		changed["avatar_url"] = s
	}
	for _, key := range []string{"region", "locale"} {
		v, ok := in[key]
		if !ok {
			continue
		}
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		if len(s) > 16 {
			return nil, fmt.Errorf("%s must be at most 16 characters", key)
		}
		if key == "region" {
			patch.Region = &s
		} else {
			patch.Locale = &s
		}
		changed[key] = s
	}
	if v, ok := in["metadata"]; ok {
		b, err := json.Marshal(v)
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
			return nil, errors.New("metadata must be a JSON object")
		}
		if len(b) > 16*1024 {
			return nil, errors.New("metadata must be 16 KB or less")
		}
		s := string(b)
		patch.Metadata = &s
		changed["metadata"] = true
	}
	if len(changed) == 0 {
		return nil, errors.New("nothing to update (display_name, avatar_url, region, locale, metadata)")
	}
	if err := dbUpdatePlayer(ctx.AppDB(), pid, player.ID, patch); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, player.ID, "player.updated", source, changed)
	return dbGetPlayer(ctx.AppDB(), pid, player.ID)
}

// ─── definitions ─────────────────────────────────────────────────────

func defineStat(ctx *sdk.AppCtx, pid, name, aggregation string, clientWritable bool, description string) (*StatDef, error) {
	name = strings.TrimSpace(name)
	if !statNameRe.MatchString(name) {
		return nil, errors.New("name must be lowercase letters, digits, or underscores (max 64), starting with a letter")
	}
	if aggregation == "" {
		aggregation = "last"
	}
	if !validAggregation(aggregation) {
		return nil, errors.New("aggregation must be last, max, min, or sum")
	}
	return dbUpsertStatDef(ctx.AppDB(), pid, name, aggregation, clientWritable, strings.TrimSpace(description))
}

func createLeaderboard(ctx *sdk.AppCtx, pid, name, displayName, stat, sort, reset string, seasonDays int) (*Leaderboard, error) {
	lb, err := newLeaderboard(name, displayName, stat, sort, reset, seasonDays, time.Now())
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if existing, err := dbGetLeaderboard(db, pid, lb.Name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("leaderboard %q already exists", lb.Name)
	}
	if def, err := dbGetStatDef(db, pid, lb.Stat); err != nil {
		return nil, err
	} else if def == nil {
		// A leaderboard over an unknown stat defines it as a server-only
		// high score. Define the stat first for sum/min semantics.
		if _, err := dbUpsertStatDef(db, pid, lb.Stat, "max", false, "auto-defined by leaderboard "+lb.Name); err != nil {
			return nil, err
		}
	}
	return dbCreateLeaderboard(db, pid, lb)
}

func defineAchievement(ctx *sdk.AppCtx, pid string, d AchievementDef) (*AchievementDef, error) {
	d.Key = strings.ToLower(strings.TrimSpace(d.Key))
	if !slugRe.MatchString(d.Key) {
		return nil, errors.New("key must be a slug (lowercase letters, digits, - or _; max 64)")
	}
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return nil, errors.New("name required")
	}
	d.Stat = strings.TrimSpace(d.Stat)
	if d.Stat != "" && !statNameRe.MatchString(d.Stat) {
		return nil, errors.New("stat must be a valid stat name")
	}
	if d.Op == "" {
		d.Op = "gte"
	}
	switch d.Op {
	case "gte", "gt", "lte", "lt", "eq":
	default:
		return nil, errors.New("op must be gte, gt, lte, lt, or eq")
	}
	return dbUpsertAchievementDef(ctx.AppDB(), pid, d)
}

func grantAchievement(ctx *sdk.AppCtx, pid string, player *Player, key, source string) (bool, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	def, err := dbGetAchievementDef(ctx.AppDB(), pid, key)
	if err != nil {
		return false, err
	}
	if def == nil {
		return false, fmt.Errorf("achievement %q not defined", key)
	}
	ok, err := dbUnlockAchievement(ctx.AppDB(), pid, player.ID, key, source)
	if err != nil || !ok {
		return false, err
	}
	dbAudit(ctx.AppDB(), pid, player.ID, "achievement.unlocked", source, map[string]any{"key": key, "manual": true})
	ctx.Emit("achievement.unlocked", map[string]any{"player_id": player.ID, "key": key, "source": source})
	trackEvent(ctx, pid, "achievement_unlocked", player, map[string]any{"key": key, "manual": true})
	return true, nil
}
