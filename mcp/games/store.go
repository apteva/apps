package main

// store.go — types and SQL. No business logic, no HTTP, no tokens.
// Every function takes (db, projectID, …); project_id appears in every
// WHERE clause so a global install could share the file unchanged.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var errVersionConflict = errors.New("version_conflict")

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }

type Player struct {
	ID           int64           `json:"id"`
	AuthUserID   int64           `json:"auth_user_id"`
	DisplayName  string          `json:"display_name"`
	AvatarURL    string          `json:"avatar_url,omitempty"`
	Region       string          `json:"region,omitempty"`
	Locale       string          `json:"locale,omitempty"`
	Metadata     json.RawMessage `json:"metadata"`
	Status       string          `json:"status"`
	Kind         string          `json:"kind"`
	LoginCount   int64           `json:"login_count"`
	FirstLoginAt string          `json:"first_login_at,omitempty"`
	LastLoginAt  string          `json:"last_login_at,omitempty"`
	CreatedAt    string          `json:"created_at,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
}

type Ban struct {
	ID        int64  `json:"id"`
	PlayerID  int64  `json:"player_id"`
	Reason    string `json:"reason,omitempty"`
	Source    string `json:"source,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	LiftedAt  string `json:"lifted_at,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type DataEntry struct {
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value"`
	Visibility string          `json:"visibility"`
	Version    int64           `json:"version"`
	UpdatedAt  string          `json:"updated_at,omitempty"`
}

type StatDef struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Aggregation    string `json:"aggregation"`
	ClientWritable bool   `json:"client_writable"`
	Description    string `json:"description,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
}

type PlayerStat struct {
	Stat      string  `json:"stat"`
	Value     float64 `json:"value"`
	Version   int64   `json:"version"`
	UpdatedAt string  `json:"updated_at,omitempty"`
}

type Leaderboard struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	DisplayName     string `json:"display_name,omitempty"`
	Stat            string `json:"stat"`
	Sort            string `json:"sort"`
	Reset           string `json:"reset"`
	SeasonDays      int    `json:"season_days,omitempty"`
	CurrentPeriod   string `json:"current_period"`
	PeriodStartedAt string `json:"period_started_at,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
}

type LeaderboardEntry struct {
	Rank        int64   `json:"rank"`
	PlayerID    int64   `json:"player_id"`
	DisplayName string  `json:"display_name"`
	Score       float64 `json:"score"`
	UpdatedAt   string  `json:"updated_at,omitempty"`
}

type AchievementDef struct {
	ID          int64   `json:"id"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Stat        string  `json:"stat,omitempty"`
	Threshold   float64 `json:"threshold"`
	Op          string  `json:"op"`
	Hidden      bool    `json:"hidden"`
	CreatedAt   string  `json:"created_at,omitempty"`
}

type PlayerAchievement struct {
	Key        string `json:"key"`
	Source     string `json:"source,omitempty"`
	UnlockedAt string `json:"unlocked_at"`
}

type AuditEvent struct {
	ID         int64  `json:"id"`
	PlayerID   int64  `json:"player_id"`
	Event      string `json:"event"`
	Source     string `json:"source,omitempty"`
	Metadata   string `json:"metadata,omitempty"`
	OccurredAt string `json:"occurred_at"`
}

// ─── settings ────────────────────────────────────────────────────────

func dbGetSetting(db *sql.DB, pid, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM settings WHERE project_id = ? AND key = ?`, pid, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func dbSetSetting(db *sql.DB, pid, key, value string) error {
	_, err := db.Exec(`
		INSERT INTO settings(project_id, key, value, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(project_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		pid, key, value, nowRFC())
	return err
}

// ─── players ─────────────────────────────────────────────────────────

const playerCols = `id, auth_user_id, display_name, IFNULL(avatar_url,''), IFNULL(region,''), IFNULL(locale,''),
	COALESCE(metadata_json,'{}'), status, kind, login_count, IFNULL(first_login_at,''), IFNULL(last_login_at,''),
	created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanPlayer(sc rowScanner) (*Player, error) {
	var p Player
	var meta string
	if err := sc.Scan(&p.ID, &p.AuthUserID, &p.DisplayName, &p.AvatarURL, &p.Region, &p.Locale,
		&meta, &p.Status, &p.Kind, &p.LoginCount, &p.FirstLoginAt, &p.LastLoginAt,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if strings.TrimSpace(meta) == "" {
		meta = "{}"
	}
	p.Metadata = json.RawMessage(meta)
	return &p, nil
}

func dbGetPlayer(db *sql.DB, pid string, id int64) (*Player, error) {
	p, err := scanPlayer(db.QueryRow(`SELECT `+playerCols+` FROM players WHERE project_id = ? AND id = ?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func dbGetPlayerByAuthUser(db *sql.DB, pid string, authUserID int64) (*Player, error) {
	p, err := scanPlayer(db.QueryRow(`SELECT `+playerCols+` FROM players WHERE project_id = ? AND auth_user_id = ?`, pid, authUserID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func dbCreatePlayer(db *sql.DB, pid string, authUserID int64, displayName, kind string) (int64, error) {
	now := nowRFC()
	res, err := db.Exec(`
		INSERT INTO players(project_id, auth_user_id, display_name, kind, created_at, updated_at)
		VALUES(?,?,?,?,?,?)`,
		pid, authUserID, displayName, kind, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbTouchLogin(db *sql.DB, pid string, id int64, kind string) error {
	now := nowRFC()
	_, err := db.Exec(`
		UPDATE players
		   SET login_count = login_count + 1, last_login_at = ?, first_login_at = COALESCE(first_login_at, ?),
		       kind = ?, updated_at = ?
		 WHERE project_id = ? AND id = ?`,
		now, now, kind, now, pid, id)
	return err
}

type playerPatch struct {
	DisplayName *string
	AvatarURL   *string
	Region      *string
	Locale      *string
	Metadata    *string
}

func dbUpdatePlayer(db *sql.DB, pid string, id int64, patch playerPatch) error {
	sets := []string{"updated_at = ?"}
	args := []any{nowRFC()}
	if patch.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *patch.DisplayName)
	}
	if patch.AvatarURL != nil {
		sets = append(sets, "avatar_url = ?")
		args = append(args, nullStr(*patch.AvatarURL))
	}
	if patch.Region != nil {
		sets = append(sets, "region = ?")
		args = append(args, nullStr(*patch.Region))
	}
	if patch.Locale != nil {
		sets = append(sets, "locale = ?")
		args = append(args, nullStr(*patch.Locale))
	}
	if patch.Metadata != nil {
		sets = append(sets, "metadata_json = ?")
		args = append(args, *patch.Metadata)
	}
	if len(sets) == 1 {
		return nil
	}
	args = append(args, pid, id)
	_, err := db.Exec(`UPDATE players SET `+strings.Join(sets, ", ")+` WHERE project_id = ? AND id = ?`, args...)
	return err
}

func dbSetPlayerStatus(db *sql.DB, pid string, id int64, status string) error {
	_, err := db.Exec(`UPDATE players SET status = ?, updated_at = ? WHERE project_id = ? AND id = ?`,
		status, nowRFC(), pid, id)
	return err
}

func dbSearchPlayers(db *sql.DB, pid, q, status string, limit, offset int) ([]Player, int, error) {
	conds := []string{"project_id = ?"}
	args := []any{pid}
	if q = strings.TrimSpace(q); q != "" {
		conds = append(conds, "(LOWER(display_name) LIKE ? OR CAST(id AS TEXT) = ? OR CAST(auth_user_id AS TEXT) = ?)")
		args = append(args, "%"+strings.ToLower(q)+"%", q, q)
	}
	if status != "" {
		conds = append(conds, "status = ?")
		args = append(args, status)
	}
	where := strings.Join(conds, " AND ")
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM players WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := db.Query(`SELECT `+playerCols+` FROM players WHERE `+where+
		` ORDER BY last_login_at DESC, id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Player{}
	for rows.Next() {
		p, err := scanPlayer(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	return out, total, rows.Err()
}

func dbDeletePlayer(db *sql.DB, pid string, id int64) error {
	if _, err := db.Exec(`DELETE FROM player_audit WHERE project_id = ? AND player_id = ?`, pid, id); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM players WHERE project_id = ? AND id = ?`, pid, id)
	return err
}

type playerCounts struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Banned    int `json:"banned"`
	New7d     int `json:"new_7d"`
	Active24h int `json:"active_24h"`
	Active7d  int `json:"active_7d"`
}

func dbPlayerCounts(db *sql.DB, pid string) (playerCounts, error) {
	var c playerCounts
	dayAgo := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	weekAgo := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	q := func(extra string, extraArgs ...any) (int, error) {
		var n int
		args := append([]any{pid}, extraArgs...)
		err := db.QueryRow(`SELECT COUNT(*) FROM players WHERE project_id = ?`+extra, args...).Scan(&n)
		return n, err
	}
	var err error
	if c.Total, err = q(""); err != nil {
		return c, err
	}
	if c.Active, err = q(" AND status = 'active'"); err != nil {
		return c, err
	}
	if c.Banned, err = q(" AND status = 'banned'"); err != nil {
		return c, err
	}
	if c.New7d, err = q(" AND created_at > ?", weekAgo); err != nil {
		return c, err
	}
	if c.Active24h, err = q(" AND last_login_at > ?", dayAgo); err != nil {
		return c, err
	}
	c.Active7d, err = q(" AND last_login_at > ?", weekAgo)
	return c, err
}

// ─── bans ────────────────────────────────────────────────────────────

func dbCreateBan(db *sql.DB, pid string, playerID int64, reason, source, expiresAt string) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO player_bans(project_id, player_id, reason, source, expires_at, created_at)
		VALUES(?,?,?,?,?,?)`,
		pid, playerID, nullStr(reason), nullStr(source), nullStr(expiresAt), nowRFC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const banCols = `id, player_id, IFNULL(reason,''), IFNULL(source,''), IFNULL(expires_at,''), IFNULL(lifted_at,''), created_at`

func scanBan(sc rowScanner) (*Ban, error) {
	var b Ban
	if err := sc.Scan(&b.ID, &b.PlayerID, &b.Reason, &b.Source, &b.ExpiresAt, &b.LiftedAt, &b.CreatedAt); err != nil {
		return nil, err
	}
	return &b, nil
}

func dbActiveBan(db *sql.DB, pid string, playerID int64) (*Ban, error) {
	b, err := scanBan(db.QueryRow(`
		SELECT `+banCols+` FROM player_bans
		WHERE project_id = ? AND player_id = ? AND lifted_at IS NULL AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY created_at DESC LIMIT 1`, pid, playerID, nowRFC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

func dbLiftBans(db *sql.DB, pid string, playerID int64) (int64, error) {
	res, err := db.Exec(`UPDATE player_bans SET lifted_at = ? WHERE project_id = ? AND player_id = ? AND lifted_at IS NULL`,
		nowRFC(), pid, playerID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func dbListBans(db *sql.DB, pid string, playerID int64) ([]Ban, error) {
	rows, err := db.Query(`SELECT `+banCols+` FROM player_bans WHERE project_id = ? AND player_id = ? ORDER BY created_at DESC`, pid, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ban{}
	for rows.Next() {
		b, err := scanBan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ─── audit ───────────────────────────────────────────────────────────

func dbAudit(db *sql.DB, pid string, playerID int64, event, source string, meta map[string]any) {
	var m any
	if len(meta) > 0 {
		if b, err := json.Marshal(meta); err == nil {
			m = string(b)
		}
	}
	_, _ = db.Exec(`INSERT INTO player_audit(project_id, player_id, event, source, metadata, occurred_at) VALUES(?,?,?,?,?,?)`,
		pid, playerID, event, nullStr(source), m, nowRFC())
}

func dbListAudit(db *sql.DB, pid string, playerID int64, limit int) ([]AuditEvent, error) {
	rows, err := db.Query(`
		SELECT id, player_id, event, IFNULL(source,''), IFNULL(metadata,''), occurred_at
		FROM player_audit WHERE project_id = ? AND player_id = ? ORDER BY occurred_at DESC, id DESC LIMIT ?`,
		pid, playerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var a AuditEvent
		if err := rows.Scan(&a.ID, &a.PlayerID, &a.Event, &a.Source, &a.Metadata, &a.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ─── player data ─────────────────────────────────────────────────────

const dataCols = `key, value, visibility, version, updated_at`

func scanData(sc rowScanner) (*DataEntry, error) {
	var e DataEntry
	var raw string
	if err := sc.Scan(&e.Key, &raw, &e.Visibility, &e.Version, &e.UpdatedAt); err != nil {
		return nil, err
	}
	e.Value = json.RawMessage(raw)
	return &e, nil
}

// dbListData — visibilities filters the result; nil = every row.
func dbListData(db *sql.DB, pid string, playerID int64, visibilities []string) ([]DataEntry, error) {
	q := `SELECT ` + dataCols + ` FROM player_data WHERE project_id = ? AND player_id = ?`
	args := []any{pid, playerID}
	if len(visibilities) > 0 {
		marks := make([]string, len(visibilities))
		for i, v := range visibilities {
			marks[i] = "?"
			args = append(args, v)
		}
		q += ` AND visibility IN (` + strings.Join(marks, ",") + `)`
	}
	rows, err := db.Query(q+` ORDER BY key ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DataEntry{}
	for rows.Next() {
		e, err := scanData(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func dbGetData(db *sql.DB, pid string, playerID int64, key string) (*DataEntry, error) {
	e, err := scanData(db.QueryRow(`SELECT `+dataCols+` FROM player_data WHERE project_id = ? AND player_id = ? AND key = ?`, pid, playerID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

// dbSetData writes one key. expectVersion > 0 enforces optimistic
// concurrency: the row must currently be at that version (a missing
// row counts as a conflict too). visibility "" keeps the current one
// (private for a new row).
func dbSetData(db *sql.DB, pid string, playerID int64, key, value, visibility string, expectVersion int64) (*DataEntry, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var curVersion int64
	var curVis string
	err = tx.QueryRow(`SELECT version, visibility FROM player_data WHERE project_id = ? AND player_id = ? AND key = ?`,
		pid, playerID, key).Scan(&curVersion, &curVis)
	now := nowRFC()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if expectVersion > 0 {
			return nil, errVersionConflict
		}
		if visibility == "" {
			visibility = "private"
		}
		if _, err := tx.Exec(`
			INSERT INTO player_data(project_id, player_id, key, value, visibility, version, created_at, updated_at)
			VALUES(?,?,?,?,?,1,?,?)`, pid, playerID, key, value, visibility, now, now); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if expectVersion > 0 && expectVersion != curVersion {
			return nil, errVersionConflict
		}
		if visibility == "" {
			visibility = curVis
		}
		if _, err := tx.Exec(`
			UPDATE player_data SET value = ?, visibility = ?, version = version + 1, updated_at = ?
			WHERE project_id = ? AND player_id = ? AND key = ?`, value, visibility, now, pid, playerID, key); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetData(db, pid, playerID, key)
}

func dbDeleteData(db *sql.DB, pid string, playerID int64, key string) (bool, error) {
	res, err := db.Exec(`DELETE FROM player_data WHERE project_id = ? AND player_id = ? AND key = ?`, pid, playerID, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ─── stat definitions ────────────────────────────────────────────────

const statDefCols = `id, name, aggregation, client_writable, IFNULL(description,''), created_at`

func scanStatDef(sc rowScanner) (*StatDef, error) {
	var d StatDef
	var cw int
	if err := sc.Scan(&d.ID, &d.Name, &d.Aggregation, &cw, &d.Description, &d.CreatedAt); err != nil {
		return nil, err
	}
	d.ClientWritable = cw == 1
	return &d, nil
}

func dbUpsertStatDef(db *sql.DB, pid, name, aggregation string, clientWritable bool, description string) (*StatDef, error) {
	cw := 0
	if clientWritable {
		cw = 1
	}
	now := nowRFC()
	if _, err := db.Exec(`
		INSERT INTO stat_defs(project_id, name, aggregation, client_writable, description, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(project_id, name) DO UPDATE SET aggregation = excluded.aggregation,
			client_writable = excluded.client_writable, description = excluded.description, updated_at = excluded.updated_at`,
		pid, name, aggregation, cw, nullStr(description), now, now); err != nil {
		return nil, err
	}
	return dbGetStatDef(db, pid, name)
}

func dbGetStatDef(db *sql.DB, pid, name string) (*StatDef, error) {
	d, err := scanStatDef(db.QueryRow(`SELECT `+statDefCols+` FROM stat_defs WHERE project_id = ? AND name = ?`, pid, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func dbListStatDefs(db *sql.DB, pid string) ([]StatDef, error) {
	rows, err := db.Query(`SELECT `+statDefCols+` FROM stat_defs WHERE project_id = ? ORDER BY name ASC`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatDef{}
	for rows.Next() {
		d, err := scanStatDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// ─── player statistics ───────────────────────────────────────────────

func dbGetPlayerStats(db *sql.DB, pid string, playerID int64) ([]PlayerStat, error) {
	rows, err := db.Query(`SELECT stat, value, version, updated_at FROM player_stats WHERE project_id = ? AND player_id = ? ORDER BY stat ASC`, pid, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlayerStat{}
	for rows.Next() {
		var s PlayerStat
		if err := rows.Scan(&s.Stat, &s.Value, &s.Version, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func dbGetPlayerStat(db *sql.DB, pid string, playerID int64, stat string) (*PlayerStat, error) {
	var s PlayerStat
	err := db.QueryRow(`SELECT stat, value, version, updated_at FROM player_stats WHERE project_id = ? AND player_id = ? AND stat = ?`,
		pid, playerID, stat).Scan(&s.Stat, &s.Value, &s.Version, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func dbUpsertPlayerStat(db *sql.DB, pid string, playerID int64, stat string, value float64) (*PlayerStat, error) {
	if _, err := db.Exec(`
		INSERT INTO player_stats(project_id, player_id, stat, value, version, updated_at) VALUES(?,?,?,?,1,?)
		ON CONFLICT(player_id, stat) DO UPDATE SET value = excluded.value, version = player_stats.version + 1, updated_at = excluded.updated_at`,
		pid, playerID, stat, value, nowRFC()); err != nil {
		return nil, err
	}
	return dbGetPlayerStat(db, pid, playerID, stat)
}

// ─── leaderboards ────────────────────────────────────────────────────

const lbCols = `id, name, IFNULL(display_name,''), stat, sort, reset, season_days, current_period, IFNULL(period_started_at,''), created_at`

func scanLeaderboard(sc rowScanner) (*Leaderboard, error) {
	var l Leaderboard
	if err := sc.Scan(&l.ID, &l.Name, &l.DisplayName, &l.Stat, &l.Sort, &l.Reset, &l.SeasonDays,
		&l.CurrentPeriod, &l.PeriodStartedAt, &l.CreatedAt); err != nil {
		return nil, err
	}
	return &l, nil
}

func dbCreateLeaderboard(db *sql.DB, pid string, l Leaderboard) (*Leaderboard, error) {
	now := nowRFC()
	if _, err := db.Exec(`
		INSERT INTO leaderboards(project_id, name, display_name, stat, sort, reset, season_days, current_period, period_started_at, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		pid, l.Name, nullStr(l.DisplayName), l.Stat, l.Sort, l.Reset, l.SeasonDays, l.CurrentPeriod, nullStr(l.PeriodStartedAt), now, now); err != nil {
		return nil, err
	}
	return dbGetLeaderboard(db, pid, l.Name)
}

func dbGetLeaderboard(db *sql.DB, pid, name string) (*Leaderboard, error) {
	l, err := scanLeaderboard(db.QueryRow(`SELECT `+lbCols+` FROM leaderboards WHERE project_id = ? AND name = ?`, pid, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

func dbListLeaderboards(db *sql.DB, pid string) ([]Leaderboard, error) {
	return dbQueryLeaderboards(db, `SELECT `+lbCols+` FROM leaderboards WHERE project_id = ? ORDER BY name ASC`, pid)
}

func dbListLeaderboardsForStat(db *sql.DB, pid, stat string) ([]Leaderboard, error) {
	return dbQueryLeaderboards(db, `SELECT `+lbCols+` FROM leaderboards WHERE project_id = ? AND stat = ? ORDER BY name ASC`, pid, stat)
}

func dbQueryLeaderboards(db *sql.DB, q string, args ...any) ([]Leaderboard, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Leaderboard{}
	for rows.Next() {
		l, err := scanLeaderboard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func dbSetLeaderboardPeriod(db *sql.DB, pid string, id int64, period, startedAt string) error {
	_, err := db.Exec(`UPDATE leaderboards SET current_period = ?, period_started_at = ?, updated_at = ? WHERE project_id = ? AND id = ?`,
		period, startedAt, nowRFC(), pid, id)
	return err
}

func dbGetEntry(db *sql.DB, pid string, lbID int64, period string, playerID int64) (*LeaderboardEntry, error) {
	var e LeaderboardEntry
	err := db.QueryRow(`
		SELECT e.player_id, p.display_name, e.score, e.updated_at
		FROM leaderboard_entries e JOIN players p ON p.id = e.player_id
		WHERE e.project_id = ? AND e.leaderboard_id = ? AND e.period = ? AND e.player_id = ?`,
		pid, lbID, period, playerID).Scan(&e.PlayerID, &e.DisplayName, &e.Score, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func dbUpsertEntry(db *sql.DB, pid string, lbID int64, period string, playerID int64, score float64) error {
	_, err := db.Exec(`
		INSERT INTO leaderboard_entries(project_id, leaderboard_id, period, player_id, score, updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(leaderboard_id, period, player_id) DO UPDATE SET score = excluded.score, updated_at = excluded.updated_at`,
		pid, lbID, period, playerID, score, nowRFC())
	return err
}

func orderForSort(sort string) string {
	if sort == "asc" {
		return "e.score ASC, e.updated_at ASC, e.id ASC"
	}
	return "e.score DESC, e.updated_at ASC, e.id ASC"
}

func dbTopEntries(db *sql.DB, pid string, lbID int64, period, sort string, limit, offset int) ([]LeaderboardEntry, error) {
	rows, err := db.Query(`
		SELECT e.player_id, p.display_name, e.score, e.updated_at
		FROM leaderboard_entries e JOIN players p ON p.id = e.player_id
		WHERE e.project_id = ? AND e.leaderboard_id = ? AND e.period = ?
		ORDER BY `+orderForSort(sort)+` LIMIT ? OFFSET ?`,
		pid, lbID, period, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LeaderboardEntry{}
	for rows.Next() {
		var e LeaderboardEntry
		if err := rows.Scan(&e.PlayerID, &e.DisplayName, &e.Score, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Rank = int64(offset + len(out) + 1)
		out = append(out, e)
	}
	return out, rows.Err()
}

// dbEntryRank — 1 + the number of entries that sort ahead of the given
// score (ties broken by earlier update, matching dbTopEntries).
func dbEntryRank(db *sql.DB, pid string, lbID int64, period, sort string, score float64, updatedAt string) (int64, error) {
	op := ">"
	if sort == "asc" {
		op = "<"
	}
	var n int64
	err := db.QueryRow(`
		SELECT COUNT(*) FROM leaderboard_entries
		WHERE project_id = ? AND leaderboard_id = ? AND period = ?
		  AND (score `+op+` ? OR (score = ? AND updated_at < ?))`,
		pid, lbID, period, score, score, updatedAt).Scan(&n)
	return n + 1, err
}

func dbEntryCount(db *sql.DB, pid string, lbID int64, period string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM leaderboard_entries WHERE project_id = ? AND leaderboard_id = ? AND period = ?`,
		pid, lbID, period).Scan(&n)
	return n, err
}

func dbListPeriods(db *sql.DB, pid string, lbID int64, limit int) ([]string, error) {
	rows, err := db.Query(`
		SELECT period, MAX(updated_at) AS last FROM leaderboard_entries
		WHERE project_id = ? AND leaderboard_id = ? GROUP BY period ORDER BY last DESC LIMIT ?`, pid, lbID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p, last string
		if err := rows.Scan(&p, &last); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ─── achievements ────────────────────────────────────────────────────

const achCols = `id, key, name, IFNULL(description,''), IFNULL(stat,''), IFNULL(threshold,0), op, hidden, created_at`

func scanAchievementDef(sc rowScanner) (*AchievementDef, error) {
	var d AchievementDef
	var hidden int
	if err := sc.Scan(&d.ID, &d.Key, &d.Name, &d.Description, &d.Stat, &d.Threshold, &d.Op, &hidden, &d.CreatedAt); err != nil {
		return nil, err
	}
	d.Hidden = hidden == 1
	return &d, nil
}

func dbUpsertAchievementDef(db *sql.DB, pid string, d AchievementDef) (*AchievementDef, error) {
	hidden := 0
	if d.Hidden {
		hidden = 1
	}
	now := nowRFC()
	var threshold any
	if d.Stat != "" {
		threshold = d.Threshold
	}
	if _, err := db.Exec(`
		INSERT INTO achievement_defs(project_id, key, name, description, stat, threshold, op, hidden, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id, key) DO UPDATE SET name = excluded.name, description = excluded.description,
			stat = excluded.stat, threshold = excluded.threshold, op = excluded.op, hidden = excluded.hidden, updated_at = excluded.updated_at`,
		pid, d.Key, d.Name, nullStr(d.Description), nullStr(d.Stat), threshold, d.Op, hidden, now, now); err != nil {
		return nil, err
	}
	return dbGetAchievementDef(db, pid, d.Key)
}

func dbGetAchievementDef(db *sql.DB, pid, key string) (*AchievementDef, error) {
	d, err := scanAchievementDef(db.QueryRow(`SELECT `+achCols+` FROM achievement_defs WHERE project_id = ? AND key = ?`, pid, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func dbListAchievementDefs(db *sql.DB, pid string) ([]AchievementDef, error) {
	return dbQueryAchievementDefs(db, `SELECT `+achCols+` FROM achievement_defs WHERE project_id = ? ORDER BY key ASC`, pid)
}

func dbListAchievementDefsForStat(db *sql.DB, pid, stat string) ([]AchievementDef, error) {
	return dbQueryAchievementDefs(db, `SELECT `+achCols+` FROM achievement_defs WHERE project_id = ? AND stat = ? ORDER BY key ASC`, pid, stat)
}

func dbQueryAchievementDefs(db *sql.DB, q string, args ...any) ([]AchievementDef, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AchievementDef{}
	for rows.Next() {
		d, err := scanAchievementDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func dbPlayerAchievements(db *sql.DB, pid string, playerID int64) ([]PlayerAchievement, error) {
	rows, err := db.Query(`SELECT key, IFNULL(source,''), unlocked_at FROM player_achievements WHERE project_id = ? AND player_id = ? ORDER BY unlocked_at ASC`, pid, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlayerAchievement{}
	for rows.Next() {
		var a PlayerAchievement
		if err := rows.Scan(&a.Key, &a.Source, &a.UnlockedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func dbUnlockAchievement(db *sql.DB, pid string, playerID int64, key, source string) (bool, error) {
	res, err := db.Exec(`INSERT OR IGNORE INTO player_achievements(project_id, player_id, key, source, unlocked_at) VALUES(?,?,?,?,?)`,
		pid, playerID, key, nullStr(source), nowRFC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ─── tiny helpers ────────────────────────────────────────────────────

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
