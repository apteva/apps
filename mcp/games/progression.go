package main

// progression.go — statistics, leaderboards, periods, achievements.
//
// A statistic write is one fact from the game ("the player scored 120")
// and the definition decides how it folds into the stored value:
//
//	last  — replace                (current level, last login region)
//	max   — keep the best          (high score, longest streak)
//	min   — keep the lowest        (fastest lap)
//	sum   — accumulate the delta   (total kills, coins earned)
//
// Leaderboards over the statistic keep one entry per period with the
// same fold applied inside the period, so a weekly sum board restarts
// from zero every week while the all-time stat keeps counting.

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var (
	statNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	slugRe     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	dataKeyRe  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
)

const maxDataValueBytes = 256 * 1024

type statUpdate struct {
	Stat  string  `json:"stat"`
	Value float64 `json:"value"`
}

type statOutcome struct {
	Stats    []PlayerStat        `json:"stats"`
	Applied  []string            `json:"applied"`
	Unlocked []string            `json:"unlocked"`
	Rejected []map[string]string `json:"rejected"`
}

func validAggregation(a string) bool {
	switch a {
	case "last", "max", "min", "sum":
		return true
	}
	return false
}

func aggregate(agg string, existing bool, old, v float64) float64 {
	switch agg {
	case "max":
		if !existing || v > old {
			return v
		}
		return old
	case "min":
		if !existing || v < old {
			return v
		}
		return old
	case "sum":
		if !existing {
			return v
		}
		return old + v
	default:
		return v
	}
}

func achievementMet(d AchievementDef, value float64) bool {
	switch d.Op {
	case "gt":
		return value > d.Threshold
	case "lte":
		return value <= d.Threshold
	case "lt":
		return value < d.Threshold
	case "eq":
		return value == d.Threshold
	default:
		return value >= d.Threshold
	}
}

// applyStatUpdates is the single write path for statistics. clientCall
// enforces the definition's client_writable flag and refuses undefined
// stats; server callers (tools, dashboard) auto-define unknown stats as
// server-only last-value stats so an agent can start recording without
// a setup step.
func applyStatUpdates(ctx *sdk.AppCtx, pid string, player *Player, updates []statUpdate, source string, clientCall bool) (*statOutcome, error) {
	db := ctx.AppDB()
	out := &statOutcome{Applied: []string{}, Unlocked: []string{}, Rejected: []map[string]string{}}
	reject := func(stat, reason string) {
		out.Rejected = append(out.Rejected, map[string]string{"stat": stat, "reason": reason})
	}
	now := time.Now()
	for _, u := range updates {
		name := strings.TrimSpace(u.Stat)
		if !statNameRe.MatchString(name) {
			reject(name, "invalid stat name (lowercase letters, digits, underscores; max 64)")
			continue
		}
		if math.IsNaN(u.Value) || math.IsInf(u.Value, 0) {
			reject(name, "value must be a finite number")
			continue
		}
		def, err := dbGetStatDef(db, pid, name)
		if err != nil {
			return nil, err
		}
		if def == nil {
			if clientCall {
				reject(name, "unknown stat")
				continue
			}
			if def, err = dbUpsertStatDef(db, pid, name, "last", false, ""); err != nil {
				return nil, err
			}
		}
		if clientCall && !def.ClientWritable {
			reject(name, "stat is server-only")
			continue
		}
		cur, err := dbGetPlayerStat(db, pid, player.ID, name)
		if err != nil {
			return nil, err
		}
		existing := cur != nil
		old := 0.0
		if existing {
			old = cur.Value
		}
		newVal := aggregate(def.Aggregation, existing, old, u.Value)
		if existing && newVal == old {
			out.Applied = append(out.Applied, name)
			continue
		}
		if _, err := dbUpsertPlayerStat(db, pid, player.ID, name, newVal); err != nil {
			return nil, err
		}
		out.Applied = append(out.Applied, name)

		boards, err := dbListLeaderboardsForStat(db, pid, name)
		if err != nil {
			return nil, err
		}
		for i := range boards {
			lb := &boards[i]
			if changed, prev := ensureCurrentPeriod(ctx, pid, lb, now); changed {
				emitReset(ctx, lb, prev, false)
			}
			entry, err := dbGetEntry(db, pid, lb.ID, lb.CurrentPeriod, player.ID)
			if err != nil {
				return nil, err
			}
			eExisting := entry != nil
			eOld := 0.0
			if eExisting {
				eOld = entry.Score
			}
			eNew := aggregate(def.Aggregation, eExisting, eOld, u.Value)
			if !eExisting || eNew != eOld {
				if err := dbUpsertEntry(db, pid, lb.ID, lb.CurrentPeriod, player.ID, eNew); err != nil {
					return nil, err
				}
			}
		}

		defs, err := dbListAchievementDefsForStat(db, pid, name)
		if err != nil {
			return nil, err
		}
		for _, ad := range defs {
			if !achievementMet(ad, newVal) {
				continue
			}
			ok, err := dbUnlockAchievement(db, pid, player.ID, ad.Key, source)
			if err != nil {
				return nil, err
			}
			if ok {
				out.Unlocked = append(out.Unlocked, ad.Key)
				dbAudit(db, pid, player.ID, "achievement.unlocked", source, map[string]any{"key": ad.Key, "stat": name, "value": newVal})
				ctx.Emit("achievement.unlocked", map[string]any{"player_id": player.ID, "key": ad.Key, "source": source})
				trackEvent(ctx, pid, "achievement_unlocked", player, map[string]any{"key": ad.Key})
			}
		}

		dbAudit(db, pid, player.ID, "stat.updated", source, map[string]any{"stat": name, "value": newVal, "previous": old})
		ctx.Emit("stat.updated", map[string]any{"player_id": player.ID, "stat": name, "value": newVal, "previous": old, "source": source})
	}
	stats, err := dbGetPlayerStats(db, pid, player.ID)
	if err != nil {
		return nil, err
	}
	out.Stats = stats
	return out, nil
}

// ─── periods ─────────────────────────────────────────────────────────

func validReset(r string) bool {
	switch r {
	case "none", "daily", "weekly", "monthly", "season":
		return true
	}
	return false
}

func seasonNumber(period string) int {
	rest := strings.TrimPrefix(period, "season-")
	if i := strings.Index(rest, "-"); i > 0 {
		rest = rest[:i]
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// periodKey is the period the leaderboard should be in at `now`.
func periodKey(lb *Leaderboard, now time.Time) string {
	now = now.UTC()
	switch lb.Reset {
	case "daily":
		return now.Format("2006-01-02")
	case "weekly":
		y, w := now.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	case "monthly":
		return now.Format("2006-01")
	case "season":
		days := lb.SeasonDays
		if days <= 0 {
			days = 30
		}
		started, err := time.Parse(time.RFC3339, lb.PeriodStartedAt)
		if err != nil {
			return "season-1"
		}
		n := seasonNumber(lb.CurrentPeriod)
		if now.Sub(started) >= time.Duration(days)*24*time.Hour {
			return fmt.Sprintf("season-%d", n+1)
		}
		return fmt.Sprintf("season-%d", n)
	default:
		return "all"
	}
}

// ensureCurrentPeriod moves lb to the period it should be in. A manual
// reset names its period "<expected>-r<stamp>", which keeps the board on
// that period until the schedule itself advances.
func ensureCurrentPeriod(ctx *sdk.AppCtx, pid string, lb *Leaderboard, now time.Time) (bool, string) {
	expected := periodKey(lb, now)
	if lb.CurrentPeriod == expected || strings.HasPrefix(lb.CurrentPeriod, expected+"-r") {
		return false, ""
	}
	prev := lb.CurrentPeriod
	started := now.UTC().Format(time.RFC3339)
	if err := dbSetLeaderboardPeriod(ctx.AppDB(), pid, lb.ID, expected, started); err != nil {
		ctx.Logger().Warn("leaderboard period rollover failed", "leaderboard", lb.Name, "err", err)
		return false, ""
	}
	lb.CurrentPeriod = expected
	lb.PeriodStartedAt = started
	return true, prev
}

func emitReset(ctx *sdk.AppCtx, lb *Leaderboard, previous string, manual bool) {
	ctx.Emit("leaderboard.reset", map[string]any{
		"leaderboard": lb.Name, "previous_period": previous, "period": lb.CurrentPeriod, "manual": manual,
	})
}

func resetLeaderboardNow(ctx *sdk.AppCtx, pid string, lb *Leaderboard, now time.Time) error {
	prev := lb.CurrentPeriod
	period := periodKey(lb, now) + "-r" + strconv.FormatInt(now.Unix(), 36)
	started := now.UTC().Format(time.RFC3339)
	if err := dbSetLeaderboardPeriod(ctx.AppDB(), pid, lb.ID, period, started); err != nil {
		return err
	}
	lb.CurrentPeriod = period
	lb.PeriodStartedAt = started
	emitReset(ctx, lb, prev, true)
	return nil
}

func rolloverLeaderboards(ctx *sdk.AppCtx, pid string, now time.Time) error {
	boards, err := dbListLeaderboards(ctx.AppDB(), pid)
	if err != nil {
		return err
	}
	for i := range boards {
		lb := &boards[i]
		if changed, prev := ensureCurrentPeriod(ctx, pid, lb, now); changed {
			emitReset(ctx, lb, prev, false)
		}
	}
	return nil
}

func newLeaderboard(name, displayName, stat, sort, reset string, seasonDays int, now time.Time) (Leaderboard, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !slugRe.MatchString(name) {
		return Leaderboard{}, fmt.Errorf("name must be a slug (lowercase letters, digits, - or _; max 64)")
	}
	stat = strings.TrimSpace(stat)
	if !statNameRe.MatchString(stat) {
		return Leaderboard{}, fmt.Errorf("stat must be a valid stat name")
	}
	if sort == "" {
		sort = "desc"
	}
	if sort != "desc" && sort != "asc" {
		return Leaderboard{}, fmt.Errorf("sort must be desc or asc")
	}
	if reset == "" {
		reset = "none"
	}
	if !validReset(reset) {
		return Leaderboard{}, fmt.Errorf("reset must be none, daily, weekly, monthly, or season")
	}
	if reset == "season" && seasonDays <= 0 {
		seasonDays = 30
	}
	if reset != "season" {
		seasonDays = 0
	}
	lb := Leaderboard{
		Name: name, DisplayName: strings.TrimSpace(displayName), Stat: stat, Sort: sort, Reset: reset,
		SeasonDays: seasonDays, PeriodStartedAt: now.UTC().Format(time.RFC3339),
	}
	lb.CurrentPeriod = periodKey(&lb, now)
	return lb, nil
}

// ─── reads ───────────────────────────────────────────────────────────

type leaderboardPage struct {
	Leaderboard *Leaderboard       `json:"leaderboard"`
	Period      string             `json:"period"`
	Total       int                `json:"total"`
	Entries     []LeaderboardEntry `json:"entries"`
	Me          *LeaderboardEntry  `json:"me,omitempty"`
}

func leaderboardPageFor(ctx *sdk.AppCtx, pid string, lb *Leaderboard, period string, limit, offset int, mePlayerID int64) (*leaderboardPage, error) {
	db := ctx.AppDB()
	if changed, prev := ensureCurrentPeriod(ctx, pid, lb, time.Now()); changed {
		emitReset(ctx, lb, prev, false)
	}
	if period == "" {
		period = lb.CurrentPeriod
	}
	entries, err := dbTopEntries(db, pid, lb.ID, period, lb.Sort, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := dbEntryCount(db, pid, lb.ID, period)
	if err != nil {
		return nil, err
	}
	page := &leaderboardPage{Leaderboard: lb, Period: period, Total: total, Entries: entries}
	if mePlayerID > 0 {
		page.Me, err = rankedEntry(db, pid, lb, period, mePlayerID)
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}

func rankedEntry(db *sql.DB, pid string, lb *Leaderboard, period string, playerID int64) (*LeaderboardEntry, error) {
	entry, err := dbGetEntry(db, pid, lb.ID, period, playerID)
	if err != nil || entry == nil {
		return nil, err
	}
	rank, err := dbEntryRank(db, pid, lb.ID, period, lb.Sort, entry.Score, entry.UpdatedAt)
	if err != nil {
		return nil, err
	}
	entry.Rank = rank
	return entry, nil
}

func leaderboardAround(ctx *sdk.AppCtx, pid string, lb *Leaderboard, period string, playerID int64, radius int) (*leaderboardPage, error) {
	db := ctx.AppDB()
	if changed, prev := ensureCurrentPeriod(ctx, pid, lb, time.Now()); changed {
		emitReset(ctx, lb, prev, false)
	}
	if period == "" {
		period = lb.CurrentPeriod
	}
	total, err := dbEntryCount(db, pid, lb.ID, period)
	if err != nil {
		return nil, err
	}
	page := &leaderboardPage{Leaderboard: lb, Period: period, Total: total, Entries: []LeaderboardEntry{}}
	me, err := rankedEntry(db, pid, lb, period, playerID)
	if err != nil {
		return nil, err
	}
	if me == nil {
		return page, nil
	}
	page.Me = me
	start := int(me.Rank) - 1 - radius
	if start < 0 {
		start = 0
	}
	page.Entries, err = dbTopEntries(db, pid, lb.ID, period, lb.Sort, 2*radius+1, start)
	return page, err
}
