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
	"encoding/json"
	"errors"
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
func applyStatUpdates(ctx *sdk.AppCtx, scope GameScope, player *Player, updates []statUpdate, source string, clientCall bool, operation ...string) (*statOutcome, error) {
	db, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer db.Rollback()
	if err := checkActiveGame(db, scope); err != nil {
		return nil, err
	}
	current, err := dbGetPlayer(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("player not found")
	}
	if clientCall {
		ban, err := dbActiveBan(db, scope, player.ID)
		if err != nil {
			return nil, err
		}
		if ban != nil {
			return nil, errors.New("banned")
		}
	}
	key := ""
	if len(operation) > 0 && operation[0] != "" {
		if len(operation[0]) > 128 {
			return nil, errors.New("idempotency key too long")
		}
		key = fmt.Sprintf("stats:%d:%s:%s", player.ID, source, operation[0])
	}
	fingerprint, _ := marshalResult(updates)
	if key != "" {
		var saved, fp string
		err := db.QueryRow(`SELECT fingerprint,result FROM game_operations WHERE project_id=? AND game_id=? AND key=?`, scope.ProjectID, scope.GameID, key).Scan(&fp, &saved)
		if err == nil {
			if fp != fingerprint {
				return nil, errors.New("idempotency key reused with different updates")
			}
			var out statOutcome
			if err = json.Unmarshal([]byte(saved), &out); err != nil {
				return nil, err
			}
			return &out, nil
		}
		if !isNoRows(err) {
			return nil, err
		}
	}
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
		def, err := dbGetStatDef(db, scope, name)
		if err != nil {
			return nil, err
		}
		if def == nil {
			if clientCall {
				reject(name, "unknown stat")
				continue
			}
			if def, err = dbUpsertStatDef(db, scope, name, "last", false, ""); err != nil {
				return nil, err
			}
		}
		if clientCall && !def.ClientWritable {
			reject(name, "stat is server-only")
			continue
		}
		cur, err := dbGetPlayerStat(db, scope, player.ID, name)
		if err != nil {
			return nil, err
		}
		existing := cur != nil
		old := 0.0
		if existing {
			old = cur.Value
		}
		newVal := aggregate(def.Aggregation, existing, old, u.Value)
		if math.IsNaN(newVal) || math.IsInf(newVal, 0) {
			return nil, errors.New("stat aggregate overflow")
		}
		if _, err := dbUpsertPlayerStat(db, scope, player.ID, name, newVal); err != nil {
			return nil, err
		}
		out.Applied = append(out.Applied, name)

		boards, err := dbListLeaderboardsForStat(db, scope, name)
		if err != nil {
			return nil, err
		}
		for i := range boards {
			lb := &boards[i]
			if _, _, err := advancePeriod(db, scope, lb, now); err != nil {
				return nil, err
			}
			entry, err := dbGetEntry(db, scope, lb.ID, lb.CurrentPeriod, player.ID)
			if err != nil {
				return nil, err
			}
			eExisting := entry != nil
			eOld := 0.0
			if eExisting {
				eOld = entry.Score
			}
			eNew := aggregate(def.Aggregation, eExisting, eOld, u.Value)
			if math.IsNaN(eNew) || math.IsInf(eNew, 0) {
				return nil, errors.New("leaderboard aggregate overflow")
			}
			if !eExisting || eNew != eOld {
				if err := dbUpsertEntry(db, scope, lb.ID, lb.CurrentPeriod, player.ID, eNew); err != nil {
					return nil, err
				}
			}
		}

		defs, err := dbListAchievementDefsForStat(db, scope, name)
		if err != nil {
			return nil, err
		}
		for _, ad := range defs {
			if !achievementMet(ad, newVal) {
				continue
			}
			ok, err := dbUnlockAchievement(db, scope, player.ID, ad.Key, source)
			if err != nil {
				return nil, err
			}
			if ok {
				out.Unlocked = append(out.Unlocked, ad.Key)
				if err := dbAudit(db, scope, player.ID, "achievement.unlocked", source, map[string]any{"key": ad.Key, "stat": name, "value": newVal}); err != nil {
					return nil, err
				}
				if err := queueEvent(db, scope, "achievement.unlocked", map[string]any{"player_id": player.ID, "key": ad.Key, "source": source}, false); err != nil {
					return nil, err
				}
				if cfgBool(ctx, "analytics_enabled", true) {
					if err := queueEvent(db, scope, "games.achievement_unlocked", map[string]any{"player_id": player.ID, "key": ad.Key}, true); err != nil {
						return nil, err
					}
				}
			}
		}

		if err := dbAudit(db, scope, player.ID, "stat.updated", source, map[string]any{"stat": name, "value": newVal, "previous": old}); err != nil {
			return nil, err
		}
		if err := queueEvent(db, scope, "stat.updated", map[string]any{"player_id": player.ID, "stat": name, "value": newVal, "previous": old, "source": source}, false); err != nil {
			return nil, err
		}
	}
	stats, err := dbGetPlayerStats(db, scope, player.ID)
	if err != nil {
		return nil, err
	}
	out.Stats = stats
	if key != "" {
		saved, err := marshalResult(out)
		if err != nil {
			return nil, err
		}
		if _, err = db.Exec(`INSERT INTO game_operations(project_id,game_id,key,fingerprint,result,created_at) VALUES(?,?,?,?,?,?)`, scope.ProjectID, scope.GameID, key, fingerprint, saved, nowRFC()); err != nil {
			return nil, err
		}
	}
	if err := db.Commit(); err != nil {
		return nil, err
	}
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
			return fmt.Sprintf("season-%d", n+int(now.Sub(started)/(time.Duration(days)*24*time.Hour)))
		}
		return fmt.Sprintf("season-%d", n)
	default:
		return "all"
	}
}

// ensureCurrentPeriod moves lb to the period it should be in. A manual
// reset names its period "<expected>-r<stamp>", which keeps the board on
// that period until the schedule itself advances.
func ensureCurrentPeriod(ctx *sdk.AppCtx, scope GameScope, lb *Leaderboard, now time.Time) error {
	expected := periodKey(lb, now)
	if lb.CurrentPeriod == expected || strings.HasPrefix(lb.CurrentPeriod, expected+"-r") {
		return nil
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err = advancePeriod(tx, scope, lb, now); err != nil {
		return err
	}
	return tx.Commit()
}
func advancePeriod(db DBTX, scope GameScope, lb *Leaderboard, now time.Time) (bool, string, error) {
	fresh, err := dbGetLeaderboard(db, scope, lb.Name)
	if err != nil {
		return false, "", err
	}
	if fresh == nil {
		return false, "", errors.New("leaderboard not found")
	}
	*lb = *fresh
	expected := periodKey(lb, now)
	if lb.CurrentPeriod == expected || strings.HasPrefix(lb.CurrentPeriod, expected+"-r") {
		return false, "", nil
	}
	prev := lb.CurrentPeriod
	startedAt := now.UTC()
	if lb.Reset == "season" {
		if start, err := time.Parse(time.RFC3339, lb.PeriodStartedAt); err == nil {
			days := lb.SeasonDays
			if days <= 0 {
				days = 30
			}
			duration := time.Duration(days) * 24 * time.Hour
			startedAt = start.Add(time.Duration(now.Sub(start)/duration) * duration)
		}
	}
	if err := dbSetLeaderboardPeriod(db, scope, lb.ID, expected, startedAt.Format(time.RFC3339)); err != nil {
		return false, "", err
	}
	lb.CurrentPeriod = expected
	lb.PeriodStartedAt = startedAt.Format(time.RFC3339)
	return true, prev, queueEvent(db, scope, "leaderboard.reset", map[string]any{"leaderboard": lb.Name, "previous_period": prev, "period": expected, "manual": false}, false)
}

func resetLeaderboardNow(ctx *sdk.AppCtx, scope GameScope, lb *Leaderboard, now time.Time, operation ...string) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkActiveGame(tx, scope); err != nil {
		return err
	}
	key := ""
	if len(operation) > 0 && operation[0] != "" {
		if len(operation[0]) > 128 {
			return errors.New("idempotency key too long")
		}
		key = fmt.Sprintf("reset:%d:%s", lb.ID, operation[0])
		var saved string
		err := tx.QueryRow(`SELECT result FROM game_operations WHERE project_id=? AND game_id=? AND key=?`, scope.ProjectID, scope.GameID, key).Scan(&saved)
		if err == nil {
			return json.Unmarshal([]byte(saved), lb)
		}
		if !isNoRows(err) {
			return err
		}
	}

	fresh, err := dbGetLeaderboard(tx, scope, lb.Name)
	if err != nil {
		return err
	}
	if fresh == nil {
		return errors.New("leaderboard not found")
	}
	*lb = *fresh
	prev := lb.CurrentPeriod
	period := periodKey(lb, now) + "-r" + randomID()
	if err = dbSetLeaderboardPeriod(tx, scope, lb.ID, period, now.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if err = queueEvent(tx, scope, "leaderboard.reset", map[string]any{"leaderboard": lb.Name, "previous_period": prev, "period": period, "manual": true}, false); err != nil {
		return err
	}
	lb.CurrentPeriod = period
	lb.PreviousPeriod = prev
	lb.PeriodStartedAt = now.UTC().Format(time.RFC3339)
	if key != "" {
		saved, err := marshalResult(lb)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO game_operations(project_id,game_id,key,fingerprint,result,created_at) VALUES(?,?,?,?,?,?)`, scope.ProjectID, scope.GameID, key, "reset", saved, nowRFC()); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	lb.CurrentPeriod = period
	lb.PreviousPeriod = prev
	lb.PeriodStartedAt = now.UTC().Format(time.RFC3339)
	return nil
}
func rolloverLeaderboards(ctx *sdk.AppCtx, scope GameScope, now time.Time) error {
	boards, err := dbListLeaderboards(ctx.AppDB(), scope)
	if err != nil {
		return err
	}
	for i := range boards {
		tx, err := ctx.AppDB().Begin()
		if err != nil {
			return err
		}
		_, _, err = advancePeriod(tx, scope, &boards[i], now)
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
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
	if seasonDays > 3650 {
		return Leaderboard{}, fmt.Errorf("season_days must be at most 3650")
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
	Total       *int               `json:"total,omitempty"`
	Entries     []LeaderboardEntry `json:"entries"`
	Me          *LeaderboardEntry  `json:"me,omitempty"`
	NextCursor  string             `json:"next_cursor,omitempty"`
}

type leaderboardReadOptions struct {
	Cursor    string
	OmitTotal bool
}

func leaderboardPageFor(ctx *sdk.AppCtx, scope GameScope, lb *Leaderboard, period string, limit, offset int, mePlayerID int64, options ...leaderboardReadOptions) (*leaderboardPage, error) {
	db := ctx.AppDB()
	if err := ensureCurrentPeriod(ctx, scope, lb, time.Now()); err != nil {
		return nil, err
	}
	if period == "" {
		period = lb.CurrentPeriod
	}
	var entries []LeaderboardEntry
	var err error
	opts := leaderboardReadOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.Cursor != "" {
		anchor, e := decodeEntryCursor(scope, lb.ID, period, opts.Cursor)
		if e != nil {
			return nil, e
		}
		entries, err = seekEntries(db, scope, lb, period, anchor, false, limit)
	} else {
		entries, err = dbTopEntries(db, scope, lb.ID, period, lb.Sort, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	page := &leaderboardPage{Leaderboard: lb, Period: period, Entries: entries}
	if !opts.OmitTotal {
		total, err := dbEntryCount(db, scope, lb.ID, period)
		if err != nil {
			return nil, err
		}
		page.Total = &total
	}
	if len(entries) == limit {
		page.NextCursor = makeEntryCursor(scope, lb.ID, period, entries[len(entries)-1])
	}
	if mePlayerID > 0 {
		page.Me, err = rankedEntry(db, scope, lb, period, mePlayerID)
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}

func rankedEntry(db DBTX, scope GameScope, lb *Leaderboard, period string, playerID int64) (*LeaderboardEntry, error) {
	entry, err := dbGetEntry(db, scope, lb.ID, period, playerID)
	if err != nil || entry == nil {
		return nil, err
	}
	rank, err := dbEntryRank(db, scope, lb.ID, period, lb.Sort, entry.Score, entry.UpdatedAt, entry.PlayerID)
	if err != nil {
		return nil, err
	}
	entry.Rank = rank
	return entry, nil
}

func leaderboardAround(ctx *sdk.AppCtx, scope GameScope, lb *Leaderboard, period string, playerID int64, radius int, includeRank ...bool) (*leaderboardPage, error) {
	db := ctx.AppDB()
	if err := ensureCurrentPeriod(ctx, scope, lb, time.Now()); err != nil {
		return nil, err
	}
	if period == "" {
		period = lb.CurrentPeriod
	}
	page := &leaderboardPage{Leaderboard: lb, Period: period, Entries: []LeaderboardEntry{}}
	if len(includeRank) < 2 || includeRank[1] {
		total, err := dbEntryCount(db, scope, lb.ID, period)
		if err != nil {
			return nil, err
		}
		page.Total = &total
	}
	me, err := dbGetEntry(db, scope, lb.ID, period, playerID)
	if err == nil && me != nil && (len(includeRank) == 0 || includeRank[0]) {
		me, err = rankedEntry(db, scope, lb, period, playerID)
	}
	if err != nil {
		return nil, err
	}
	if me == nil {
		return page, nil
	}
	page.Me = me
	before, err := seekEntries(db, scope, lb, period, me, true, radius)
	if err != nil {
		return nil, err
	}
	after, err := seekEntries(db, scope, lb, period, me, false, radius)
	if err != nil {
		return nil, err
	}
	page.Entries = append(append(before, *me), after...)
	if me.Rank > 0 {
		start := me.Rank - int64(len(before))
		for i := range page.Entries {
			page.Entries[i].Rank = start + int64(i)
		}
	}
	return page, nil
}
