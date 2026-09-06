package main

// ops.go — shared player operations used by the MCP tools, the dashboard
// REST surface, and the public API. Anything that changes a player's
// state goes through here so audit rows and AppBus events stay the same
// regardless of who called.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// trackEvent forwards telemetry to the Analytics app when installed and
// enabled. Failures are swallowed: telemetry never blocks gameplay.
func trackEvent(ctx *sdk.AppCtx, scope GameScope, event string, player *Player, props map[string]any) {
	if ctx == nil || player == nil || !cfgBool(ctx, "analytics_enabled", true) {
		return
	}
	if props == nil {
		props = map[string]any{}
	}
	props["player_id"] = player.ID
	if err := queueEvent(ctx.AppDB(), scope, "games."+event, props, true); err != nil {
		ctx.Logger().Warn("analytics queue failed", "error", err)
	}
}

func publicProfile(p *Player) map[string]any {
	return map[string]any{
		"id": p.ID, "display_name": p.DisplayName, "avatar_url": p.AvatarURL,
		"region": p.Region, "metadata": p.Metadata, "created_at": p.CreatedAt,
	}
}

// ─── bans ────────────────────────────────────────────────────────────

func banPlayer(ctx *sdk.AppCtx, scope GameScope, player *Player, reason, expiresAt, source string) (*Ban, error) {
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
	db, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer db.Rollback()
	id, err := dbCreateBan(db, scope, player.ID, reason, source, expiresAt)
	if err != nil {
		return nil, err
	}
	if err := dbSetPlayerStatus(db, scope, player.ID, "banned"); err != nil {
		return nil, err
	}
	if err := dbAudit(db, scope, player.ID, "player.banned", source, map[string]any{"reason": reason, "expires_at": expiresAt}); err != nil {
		return nil, err
	}
	if err := queueEvent(db, scope, "player.banned", map[string]any{"player_id": player.ID, "reason": reason, "expires_at": expiresAt, "source": source}, false); err != nil {
		return nil, err
	}
	if err := db.Commit(); err != nil {
		return nil, err
	}
	player.Status = "banned"
	return &Ban{ID: id, PlayerID: player.ID, Reason: reason, Source: source, ExpiresAt: expiresAt, CreatedAt: nowRFC()}, nil
}

func unbanPlayer(ctx *sdk.AppCtx, scope GameScope, player *Player, source string) (int64, error) {
	if _, err := recoverLegacyBan(ctx, scope, player.AuthUserID, true); err != nil {
		return 0, err
	}
	db, err := ctx.AppDB().Begin()
	if err != nil {
		return 0, err
	}
	defer db.Rollback()
	n, err := dbLiftBans(db, scope, player.ID)
	if err != nil {
		return 0, err
	}
	if err := dbSetPlayerStatus(db, scope, player.ID, "active"); err != nil {
		return 0, err
	}
	if err := dbAudit(db, scope, player.ID, "player.unbanned", source, map[string]any{"lifted": n}); err != nil {
		return 0, err
	}
	if err := queueEvent(db, scope, "player.unbanned", map[string]any{"player_id": player.ID, "source": source}, false); err != nil {
		return 0, err
	}
	if err := db.Commit(); err != nil {
		return 0, err
	}
	player.Status = "active"
	return n, nil
}

// activeBanFor returns the ban currently blocking the player, lifting an
// expired one lazily. Auth recovery applies only to verified legacy bans.
func activeBanFor(ctx *sdk.AppCtx, scope GameScope, player *Player) (*Ban, error) {
	if player.Status != "banned" {
		return dbActiveBan(ctx.AppDB(), scope, player.ID)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ban, err := dbActiveBan(tx, scope, player.ID)
	if err != nil {
		return nil, err
	}
	if ban != nil {
		return ban, nil
	}
	// Lift only expired bans under the same write lock as the status change.
	// A concurrent new ban must never be lifted by lazy expiry.
	if _, err = tx.Exec(`UPDATE player_bans SET lifted_at=? WHERE project_id=? AND game_id=? AND player_id=? AND lifted_at IS NULL AND expires_at IS NOT NULL AND expires_at<=?`, nowRFC(), scope.ProjectID, scope.GameID, player.ID, nowRFC()); err != nil {
		return nil, err
	}
	fresh, err := dbGetPlayer(tx, scope, player.ID)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, errors.New("player not found")
	}
	if fresh.Status == "banned" {
		if err = dbSetPlayerStatus(tx, scope, player.ID, "active"); err != nil {
			return nil, err
		}
		if err = dbAudit(tx, scope, player.ID, "player.unbanned", "expiry", nil); err != nil {
			return nil, err
		}
		if err = queueEvent(tx, scope, "player.unbanned", map[string]any{"player_id": player.ID, "source": "expiry"}, false); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	player.Status = "active"
	return nil, nil
}

// ─── erase / export / context ────────────────────────────────────────

func erasePlayer(ctx *sdk.AppCtx, scope GameScope, player *Player, source string) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT OR IGNORE INTO game_tombstones(project_id,game_id,auth_user_id) VALUES(?,?,?)`, scope.ProjectID, scope.GameID, player.AuthUserID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM game_outbox WHERE project_id=? AND game_id=? AND json_extract(payload,'$.player_id')=?`, scope.ProjectID, scope.GameID, player.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM game_operations WHERE project_id=? AND game_id=? AND key LIKE ?`, scope.ProjectID, scope.GameID, fmt.Sprintf("stats:%d:%%", player.ID)); err != nil {
		return err
	}
	if err = dbDeletePlayer(tx, scope, player.ID); err != nil {
		return err
	}
	if err = queueEvent(tx, scope, "player.erased", map[string]any{"player_id": player.ID, "auth_user_id": player.AuthUserID, "source": source}, false); err != nil {
		return err
	}
	return tx.Commit()
}

func exportPlayer(ctx *sdk.AppCtx, scope GameScope, player *Player) (map[string]any, error) {
	db, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer db.Rollback()
	player, err = dbGetPlayer(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	if player == nil {
		return nil, errors.New("player not found")
	}
	bans, err := dbListBans(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	data, err := dbListData(db, scope, player.ID, nil)
	if err != nil {
		return nil, err
	}
	stats, err := dbGetPlayerStats(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	achievements, err := dbPlayerAchievements(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	audit, err := dbListAudit(db, scope, player.ID, 0)
	if err != nil {
		return nil, err
	}
	entries, err := exportEntries(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"exported_at":         nowRFC(),
		"leaderboard_entries": entries,
		"player":              player,
		"bans":                bans,
		"data":                data,
		"stats":               stats,
		"achievements":        achievements,
		"audit":               audit,
		"note":                "Identity records (device and custom ids, sessions) are held by the Auth app under auth_user_id.",
	}, nil
}

func playerContext(ctx *sdk.AppCtx, scope GameScope, player *Player) (map[string]any, error) {
	db := ctx.AppDB()
	ban, err := activeBanFor(ctx, scope, player)
	if err != nil {
		return nil, err
	}
	bans, err := dbListBans(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	stats, err := dbGetPlayerStats(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	data, err := dbListData(db, scope, player.ID, nil)
	if err != nil {
		return nil, err
	}
	achievements, err := dbPlayerAchievements(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	audit, err := dbListAudit(db, scope, player.ID, 20)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"player": player, "active_ban": ban, "bans": bans, "stats": stats,
		"data": data, "achievements": achievements, "audit": audit,
	}, nil
}

// ─── profile ─────────────────────────────────────────────────────────

func applyProfilePatch(ctx *sdk.AppCtx, scope GameScope, player *Player, in map[string]any, source string) (*Player, error) {
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
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := checkActiveGame(tx, scope); err != nil {
		return nil, err
	}
	if source == "client" {
		ban, err := dbActiveBan(tx, scope, player.ID)
		if err != nil {
			return nil, err
		}
		if ban != nil {
			return nil, errors.New("banned")
		}
	}
	if err := dbUpdatePlayer(tx, scope, player.ID, patch); err != nil {
		return nil, err
	}
	if err := dbAudit(tx, scope, player.ID, "player.updated", source, changed); err != nil {
		return nil, err
	}
	out, err := dbGetPlayer(tx, scope, player.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// ─── definitions ─────────────────────────────────────────────────────

func defineStat(ctx *sdk.AppCtx, scope GameScope, name, aggregation string, clientWritable bool, description string) (*StatDef, error) {
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
	return dbUpsertStatDef(ctx.AppDB(), scope, name, aggregation, clientWritable, strings.TrimSpace(description))
}

func createLeaderboard(ctx *sdk.AppCtx, scope GameScope, name, displayName, stat, sort, reset string, seasonDays int) (*Leaderboard, error) {
	lb, err := newLeaderboard(name, displayName, stat, sort, reset, seasonDays, time.Now())
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if existing, err := dbGetLeaderboard(db, scope, lb.Name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("leaderboard %q already exists", lb.Name)
	}
	if def, err := dbGetStatDef(db, scope, lb.Stat); err != nil {
		return nil, err
	} else if def == nil {
		// A leaderboard over an unknown stat defines it as a server-only
		// high score. Define the stat first for sum/min semantics.
		if _, err := dbUpsertStatDef(db, scope, lb.Stat, "max", false, "auto-defined by leaderboard "+lb.Name); err != nil {
			return nil, err
		}
	}
	return dbCreateLeaderboard(db, scope, lb)
}

func defineAchievement(ctx *sdk.AppCtx, scope GameScope, d AchievementDef) (*AchievementDef, error) {
	d.Key = strings.ToLower(strings.TrimSpace(d.Key))
	if !slugRe.MatchString(d.Key) {
		return nil, errors.New("key must be a slug (lowercase letters, digits, - or _; max 64)")
	}
	if math.IsNaN(d.Threshold) || math.IsInf(d.Threshold, 0) {
		return nil, errors.New("threshold must be finite")
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
	return dbUpsertAchievementDef(ctx.AppDB(), scope, d)
}

func grantAchievement(ctx *sdk.AppCtx, scope GameScope, player *Player, key, source string) (bool, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := checkActiveGame(tx, scope); err != nil {
		return false, err
	}
	def, err := dbGetAchievementDef(tx, scope, key)
	if err != nil {
		return false, err
	}
	if def == nil {
		return false, fmt.Errorf("achievement %q not defined", key)
	}
	ok, err := dbUnlockAchievement(tx, scope, player.ID, key, source)
	if err != nil || !ok {
		return false, err
	}
	if err := dbAudit(tx, scope, player.ID, "achievement.unlocked", source, map[string]any{"key": key, "manual": true}); err != nil {
		return false, err
	}
	if err := queueEvent(tx, scope, "achievement.unlocked", map[string]any{"player_id": player.ID, "key": key, "source": source}, false); err != nil {
		return false, err
	}
	if cfgBool(ctx, "analytics_enabled", true) {
		if err := queueEvent(tx, scope, "games.achievement_unlocked", map[string]any{"player_id": player.ID, "key": key, "manual": true}, true); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
