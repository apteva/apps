package main

// Storage layer — thin SQL over the v0.1 schema. Entity-resolution
// cache + normalized market rows + ground-truth cache. The signal
// tables exist in the schema but aren't written until v0.2.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ─── Entities (resolution cache) ───────────────────────────────────

type Entity struct {
	ID          int64             `json:"id"`
	Canonical   string            `json:"canonical"`
	Domain      string            `json:"domain"`
	ExternalIDs map[string]string `json:"external_ids"`
	Aliases     []string          `json:"aliases"`
}

func dbFindEntity(db *sql.DB, projectID, domain, canonical string) (*Entity, error) {
	row := db.QueryRow(`
		SELECT id, canonical, domain, external_ids, aliases
		FROM entities WHERE project_id = ? AND domain = ? AND canonical = ?`,
		projectID, domain, strings.TrimSpace(canonical))
	return scanEntity(row)
}

// dbSearchEntity does a loose match against canonical + aliases so a
// query of "Alcaraz" finds "Carlos Alcaraz". Cheap LIKE scan — the
// entities table stays small (only resolved entities land here).
func dbSearchEntity(db *sql.DB, projectID, domain, name string) (*Entity, error) {
	n := "%" + strings.ToLower(strings.TrimSpace(name)) + "%"
	q := `SELECT id, canonical, domain, external_ids, aliases
	      FROM entities WHERE project_id = ?`
	args := []any{projectID}
	if domain != "" {
		q += ` AND domain = ?`
		args = append(args, domain)
	}
	q += ` AND (LOWER(canonical) LIKE ? OR LOWER(aliases) LIKE ?) LIMIT 1`
	args = append(args, n, n)
	row := db.QueryRow(q, args...)
	e, err := scanEntity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func dbUpsertEntity(db *sql.DB, projectID string, e *Entity) (int64, error) {
	ext, _ := json.Marshal(e.ExternalIDs)
	al, _ := json.Marshal(e.Aliases)
	res, err := db.Exec(`
		INSERT INTO entities (project_id, canonical, domain, external_ids, aliases)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, domain, canonical) DO UPDATE SET
			external_ids = excluded.external_ids,
			aliases      = excluded.aliases,
			updated_at   = CURRENT_TIMESTAMP`,
		projectID, strings.TrimSpace(e.Canonical), e.Domain, string(ext), string(al))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanEntity(row *sql.Row) (*Entity, error) {
	var e Entity
	var ext, al string
	if err := row.Scan(&e.ID, &e.Canonical, &e.Domain, &ext, &al); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(ext), &e.ExternalIDs)
	_ = json.Unmarshal([]byte(al), &e.Aliases)
	if e.ExternalIDs == nil {
		e.ExternalIDs = map[string]string{}
	}
	return &e, nil
}

// ─── Markets (normalized venue rows) ───────────────────────────────

type Market struct {
	ID            int64    `json:"id"`
	Venue         string   `json:"venue"`
	ExternalID    string   `json:"external_id"`
	Question      string   `json:"question"`
	Category      string   `json:"category,omitempty"`
	YesPrice      *float64 `json:"yes_price,omitempty"`
	NoPrice       *float64 `json:"no_price,omitempty"`
	Volume        *float64 `json:"volume,omitempty"`
	OpenInterest  *float64 `json:"open_interest,omitempty"`
	CloseTime     string   `json:"close_time,omitempty"`
	Resolved      bool     `json:"resolved"`
	SyncedAt      string   `json:"synced_at,omitempty"`
}

func dbUpsertMarket(db *sql.DB, projectID string, m *Market) error {
	_, err := db.Exec(`
		INSERT INTO markets (project_id, venue, external_id, question, category,
		                     yes_price, no_price, volume, open_interest, close_time,
		                     resolved, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, venue, external_id) DO UPDATE SET
			question      = excluded.question,
			category      = excluded.category,
			yes_price     = excluded.yes_price,
			no_price      = excluded.no_price,
			volume        = excluded.volume,
			open_interest = excluded.open_interest,
			close_time    = excluded.close_time,
			resolved      = excluded.resolved,
			synced_at     = CURRENT_TIMESTAMP`,
		projectID, m.Venue, m.ExternalID, m.Question, nullStr(m.Category),
		fptr(m.YesPrice), fptr(m.NoPrice), fptr(m.Volume), fptr(m.OpenInterest),
		nullStr(m.CloseTime), boolInt(m.Resolved))
	return err
}

func dbGetMarket(db *sql.DB, projectID, venue, externalID string) (*Market, error) {
	row := db.QueryRow(`
		SELECT id, venue, external_id, question, COALESCE(category,''),
		       yes_price, no_price, volume, open_interest, COALESCE(close_time,''),
		       resolved, COALESCE(synced_at,'')
		FROM markets WHERE project_id = ? AND venue = ? AND external_id = ?`,
		projectID, venue, externalID)
	return scanMarket(row)
}

func scanMarket(row *sql.Row) (*Market, error) {
	var m Market
	var yes, no, vol, oi sql.NullFloat64
	var resolved int
	if err := row.Scan(&m.ID, &m.Venue, &m.ExternalID, &m.Question, &m.Category,
		&yes, &no, &vol, &oi, &m.CloseTime, &resolved, &m.SyncedAt); err != nil {
		return nil, err
	}
	if yes.Valid { v := yes.Float64; m.YesPrice = &v }
	if no.Valid  { v := no.Float64;  m.NoPrice = &v }
	if vol.Valid { v := vol.Float64; m.Volume = &v }
	if oi.Valid  { v := oi.Float64;  m.OpenInterest = &v }
	m.Resolved = resolved != 0
	return &m, nil
}

// ─── Ground-truth cache ────────────────────────────────────────────

func dbPutGroundTruth(db *sql.DB, projectID, source, key string, fairProb float64, raw []byte) error {
	_, err := db.Exec(`
		INSERT INTO ground_truth (project_id, source, key, fair_prob, raw_payload, computed_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id, source, key) DO UPDATE SET
			fair_prob = excluded.fair_prob,
			raw_payload = excluded.raw_payload,
			computed_at = CURRENT_TIMESTAMP`,
		projectID, source, key, fairProb, string(raw))
	return err
}

func dbGetGroundTruth(db *sql.DB, projectID, source, key string, maxAge time.Duration) (float64, bool) {
	row := db.QueryRow(`
		SELECT fair_prob, computed_at FROM ground_truth
		WHERE project_id = ? AND source = ? AND key = ?`, projectID, source, key)
	var p float64
	var at string
	if err := row.Scan(&p, &at); err != nil {
		return 0, false
	}
	if maxAge > 0 {
		if t, err := time.Parse("2006-01-02 15:04:05", at); err == nil {
			if time.Since(t) > maxAge {
				return 0, false // stale
			}
		}
	}
	return p, true
}

// ─── Helpers ───────────────────────────────────────────────────────

func fptr(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
