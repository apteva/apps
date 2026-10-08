package history

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/apteva/apps/mcp/instances/internal/monitor"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const DefaultBudget = int64(512 << 20)

type Status struct {
	BudgetEvictions  int64  `json:"budget_evictions"`
	Enabled          bool   `json:"enabled"`
	State            string `json:"state"`
	Version          string `json:"version,omitempty"`
	Error            string `json:"error,omitempty"`
	LastSeen         int64  `json:"last_seen,omitempty"`
	LastPoint        int64  `json:"last_point,omitempty"`
	DetailEvicted    int64  `json:"detail_evicted"`
	BudgetBytes      int64  `json:"budget_bytes"`
	StorageBytes     int64  `json:"storage_bytes"`
	SampleIntervalMS int    `json:"sample_interval_ms"`
}
type Store struct {
	DB     *sql.DB
	mu     sync.Mutex
	budget int64
}

func OpenStore(path string, budget int64) (*Store, error) {
	if budget < 4<<20 {
		return nil, errors.New("monitoring budget must be at least 4MiB")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, budget: budget}
	// Reserve 1/8 of the budget for the rollback journal. Small transactions and
	// incremental vacuum bound both the live file and journal; no unbounded WAL.
	for _, stmt := range []string{
		"PRAGMA busy_timeout=5000", "PRAGMA page_size=4096", "PRAGMA auto_vacuum=INCREMENTAL", "PRAGMA journal_mode=TRUNCATE", "PRAGMA journal_size_limit=0",
		fmt.Sprintf("PRAGMA max_page_count=%d", budget*7/8/4096),
		`CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value INTEGER NOT NULL)`,
		`INSERT OR IGNORE INTO metadata(key,value) VALUES('budget_evictions',0)`,
		`CREATE TABLE IF NOT EXISTS hosts(id INTEGER PRIMARY KEY,enabled INTEGER NOT NULL DEFAULT 1,state TEXT NOT NULL DEFAULT 'pending',version TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',last_seen INTEGER NOT NULL DEFAULT 0,last_point INTEGER NOT NULL DEFAULT 0,evicted INTEGER NOT NULL DEFAULT 0,latest TEXT)`,
		`CREATE TABLE IF NOT EXISTS points(host INTEGER NOT NULL,step INTEGER NOT NULL,ts INTEGER NOT NULL,data TEXT NOT NULL,PRIMARY KEY(host,step,ts)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS points_age ON points(step,ts)`,
		`CREATE TABLE IF NOT EXISTS incidents(host INTEGER NOT NULL,id TEXT NOT NULL,start INTEGER NOT NULL,updated INTEGER NOT NULL,data TEXT NOT NULL,detail BLOB,PRIMARY KEY(host,id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS incidents_age ON incidents(updated)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) EnsureHost(id int64) error {
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO hosts(id) VALUES(?)`, id)
	return err
}
func (s *Store) Status(id int64) (Status, error) {
	out := Status{Enabled: true, State: "pending", BudgetBytes: s.budget, SampleIntervalMS: 250}
	err := s.DB.QueryRow(`SELECT enabled,state,version,error,last_seen,last_point,evicted FROM hosts WHERE id=?`, id).Scan(&out.Enabled, &out.State, &out.Version, &out.Error, &out.LastSeen, &out.LastPoint, &out.DetailEvicted)
	if err == sql.ErrNoRows {
		err = nil
	}
	var pages, size int64
	s.DB.QueryRow("PRAGMA page_count").Scan(&pages)
	s.DB.QueryRow("PRAGMA page_size").Scan(&size)
	out.StorageBytes = pages * size
	s.DB.QueryRow("SELECT value FROM metadata WHERE key='budget_evictions'").Scan(&out.BudgetEvictions)
	if out.Enabled && out.LastSeen > 0 && time.Now().UnixMilli()-out.LastSeen > 15000 {
		out.State = "stale"
	}
	if !out.Enabled {
		out.State = "disabled"
	}
	return out, err
}
func (s *Store) SetState(id int64, state, version, message string) error {
	if err := s.EnsureHost(id); err != nil {
		return err
	}
	_, err := s.DB.Exec(`UPDATE hosts SET state=?,version=?,error=? WHERE id=?`, state, version, message, id)
	return err
}
func (s *Store) SetEnabled(id int64, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.EnsureHost(id); err != nil {
		return err
	}
	state := "pending"
	if !enabled {
		state = "disabled"
	}
	_, err := s.DB.Exec(`UPDATE hosts SET enabled=?,state=?,error='' WHERE id=?`, enabled, state, id)
	return err
}
func (s *Store) Latest(id int64) (*monitor.Metrics, error) {
	var text sql.NullString
	if err := s.DB.QueryRow(`SELECT latest FROM hosts WHERE id=?`, id).Scan(&text); err != nil {
		return nil, err
	}
	if !text.Valid {
		return nil, nil
	}
	var out monitor.Metrics
	if err := json.Unmarshal([]byte(text.String), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (s *Store) Ingest(id int64, b monitor.Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(b.Points) > 120 || len(b.Incidents) > 50 {
		return errors.New("collector batch exceeds limits")
	}
	if b.Version != monitor.CollectorVersion {
		return fmt.Errorf("collector version mismatch: %s", b.Version)
	}
	if err := monitor.ValidateMetrics(b.Latest); err != nil {
		return err
	}
	if err := s.EnsureHost(id); err != nil {
		return err
	}
	// Prune before writing, including when budget exhaustion interrupted a prior
	// import. Checkpoint advancement happens only in the successful transaction.
	if err := s.pruneLocked(time.Now()); err != nil {
		return err
	}
	err := s.ingestLocked(id, b)
	for retry := 0; err != nil && strings.Contains(strings.ToLower(err.Error()), "full") && retry < 3; retry++ {
		// Reclaim old incident detail first; preserve its summary and rollup peaks.
		result, evictErr := s.DB.Exec(`UPDATE incidents SET detail=NULL WHERE (host,id) IN (SELECT host,id FROM incidents WHERE detail IS NOT NULL ORDER BY updated LIMIT 16)`)
		if evictErr != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			result, evictErr = s.DB.Exec(`DELETE FROM points WHERE (host,step,ts) IN (SELECT host,step,ts FROM points ORDER BY CASE step WHEN 1000 THEN 0 ELSE 1 END,ts LIMIT 1000)`)
			if evictErr != nil {
				return err
			}
			n, _ = result.RowsAffected()
		}
		if n == 0 {
			return err
		}
		s.DB.Exec(`UPDATE metadata SET value=value+? WHERE key='budget_evictions'`, n)
		s.DB.Exec("PRAGMA incremental_vacuum(1000)")
		err = s.ingestLocked(id, b)
	}

	return err
}
func (s *Store) ingestLocked(id int64, b monitor.Batch) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var enabled bool
	var last int64
	if err := tx.QueryRow(`SELECT enabled,last_point FROM hosts WHERE id=?`, id).Scan(&enabled, &last); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	now := time.Now()
	newLast := last
	for _, p := range b.Points {
		if p.Time <= last {
			continue
		}
		if p.Step != 1000 || p.Time%1000 != 0 || p.ObservedMS < 1 || p.ObservedMS > 1000 || p.Samples < 1 || p.Time > now.Add(5*time.Minute).UnixMilli() {
			return errors.New("invalid collector point or host clock")
		}
		// Advance past expired spool entries too, otherwise reconnect repeatedly
		// imports the same old batch. Older readings may still fit coarser tiers.
		if p.Time > newLast {
			newLast = p.Time
		}
		for _, tier := range monitor.Tiers {
			if p.Time < now.Add(-tier.Retention).UnixMilli()/tier.Step*tier.Step {
				continue
			}
			ts := p.Time / tier.Step * tier.Step
			agg := monitor.Point{Time: ts, Step: tier.Step}
			var data string
			err := tx.QueryRow(`SELECT data FROM points WHERE host=? AND step=? AND ts=?`, id, tier.Step, ts).Scan(&data)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil {
				if err := json.Unmarshal([]byte(data), &agg); err != nil {
					return err
				}
			}
			agg.Merge(p)
			encoded, err := json.Marshal(agg)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO points(host,step,ts,data) VALUES(?,?,?,?) ON CONFLICT(host,step,ts) DO UPDATE SET data=excluded.data`, id, tier.Step, ts, string(encoded)); err != nil {
				return err
			}
		}
		if p.Time > newLast {
			newLast = p.Time
		}
	}
	for _, ev := range b.Incidents {
		if len(ev.Recordings) > 3000 || ev.Start > now.Add(5*time.Minute).UnixMilli() || ev.Start < now.Add(-30*24*time.Hour).UnixMilli() {
			continue
		}
		var detail []byte
		if len(ev.Recordings) > 0 {
			detail, err = monitor.Encode(ev.Recordings)
			if err != nil {
				return err
			}
		}
		ev.Recordings = nil
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO incidents(host,id,start,updated,data,detail) VALUES(?,?,?,?,?,?) ON CONFLICT(host,id) DO UPDATE SET updated=excluded.updated,data=excluded.data,detail=COALESCE(excluded.detail,incidents.detail) WHERE excluded.updated>incidents.updated`, id, ev.ID, ev.Start, ev.Updated, string(data), detail); err != nil {
			return err
		}
	}
	latest, err := json.Marshal(b.Latest)
	if err != nil {
		return err
	}
	sampleTime, _ := time.Parse(time.RFC3339Nano, b.Latest.Timestamp)
	if _, err := tx.Exec(`UPDATE hosts SET state='running',version=?,error='',latest=?,last_seen=?,last_point=? WHERE id=?`, b.Version, string(latest), sampleTime.UnixMilli(), newLast, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked(now)
}
func (s *Store) pruneLocked(now time.Time) error {
	for _, tier := range monitor.Tiers {
		for {
			result, err := s.DB.Exec(`DELETE FROM points WHERE (host,step,ts) IN (SELECT host,step,ts FROM points WHERE step=? AND ts<? LIMIT 1000)`, tier.Step, now.Add(-tier.Retention).UnixMilli()/tier.Step*tier.Step)
			if err != nil {
				return err
			}
			n, _ := result.RowsAffected()
			if n == 0 {
				break
			}
		}
	}
	for {
		result, err := s.DB.Exec(`DELETE FROM incidents WHERE (host,id) IN (SELECT host,id FROM incidents WHERE updated<? LIMIT 16)`, now.Add(-30*24*time.Hour).UnixMilli())
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			break
		}
	}
	// Limit incident metadata as well as bytes: a noisy machine cannot create
	// unbounded rows even when each incident has only a small summary.
	for {
		result, err := s.DB.Exec(`DELETE FROM incidents WHERE (host,id) IN (SELECT host,id FROM (SELECT host,id,ROW_NUMBER() OVER(PARTITION BY host ORDER BY start DESC) AS n FROM incidents) WHERE n>200 LIMIT 16)`)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			break
		}
	}
	rows, err := s.DB.Query(`SELECT host,id,length(detail) FROM incidents WHERE detail IS NOT NULL ORDER BY updated DESC,host`)
	if err != nil {
		return err
	}
	type key struct {
		host int64
		id   string
	}
	evict := []key{}
	sizes := map[int64]int64{}
	total := int64(0)
	for rows.Next() {
		var h, n int64
		var id string
		if err := rows.Scan(&h, &id, &n); err != nil {
			rows.Close()
			return err
		}
		if sizes[h]+n > monitor.IncidentBudgetBytes || total+n > s.budget/4 {
			evict = append(evict, key{h, id})
		} else {
			sizes[h] += n
			total += n
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, k := range evict {
		if _, err := s.DB.Exec(`UPDATE incidents SET detail=NULL WHERE host=? AND id=?`, k.host, k.id); err != nil {
			return err
		}
		s.DB.Exec(`UPDATE hosts SET evicted=evicted+1 WHERE id=?`, k.host)
	}
	// Size reclamation avoids a large retained file after expiry. Work is bounded
	// per pass; allocation itself is hard-limited by SQLite max_page_count.
	_, err = s.DB.Exec("PRAGMA incremental_vacuum(128)")
	return err
}

type History struct {
	Points     []monitor.Point `json:"points"`
	Resolution string          `json:"resolution"`
	From       int64           `json:"from"`
	To         int64           `json:"to"`
	Oldest     int64           `json:"oldest,omitempty"`
	MaxPoints  int             `json:"max_points"`
	Status     Status          `json:"monitoring"`
}

func (s *Store) History(id int64, from, to int64, resolution string, max int) (History, error) {
	out := History{Points: []monitor.Point{}, From: from, To: to, MaxPoints: max}
	if to <= from || to-from > int64(366*24*time.Hour/time.Millisecond) || max < 1 || max > 3600 {
		return out, errors.New("invalid history range or max_points (1–3600)")
	}
	index := -1
	for i, t := range monitor.Tiers {
		if resolution != "" && resolution != "auto" {
			if t.Name == resolution {
				index = i
				break
			}
		} else if to-from <= int64(t.Retention/time.Millisecond) && int((to+t.Step-1)/t.Step-from/t.Step) <= max {
			index = i
			break
		}
	}
	if index < 0 {
		return out, errors.New("unsupported resolution or range too large for max_points")
	}
	tier := monitor.Tiers[index]
	if int((to+tier.Step-1)/tier.Step-from/tier.Step) > max {
		return out, errors.New("requested resolution exceeds max_points; use auto or a larger interval")
	}
	out.Resolution = tier.Name
	out.Status, _ = s.Status(id)
	rows, err := s.DB.Query(`SELECT data FROM points WHERE host=? AND step=? AND ts>=? AND ts<? ORDER BY ts LIMIT ?`, id, tier.Step, from/tier.Step*tier.Step, to, max)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return out, err
		}
		var p monitor.Point
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return out, err
		}
		out.Points = append(out.Points, p)
	}
	err = rows.Err()
	rows.Close()
	var oldest sql.NullInt64
	s.DB.QueryRow(`SELECT MIN(ts) FROM points WHERE host=? AND step=?`, id, tier.Step).Scan(&oldest)
	if oldest.Valid {
		out.Oldest = oldest.Int64
	}
	return out, err
}
func (s *Store) ListIncidents(id int64, limit int) ([]monitor.Incident, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("limit must be 1–200")
	}
	rows, err := s.DB.Query(`SELECT data,detail IS NULL FROM incidents WHERE host=? ORDER BY start DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []monitor.Incident{}
	for rows.Next() {
		var data string
		var expired bool
		if err := rows.Scan(&data, &expired); err != nil {
			return nil, err
		}
		var ev monitor.Incident
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil, err
		}
		ev.DetailExpired = expired || ev.DetailExpired
		out = append(out, ev)
	}
	return out, rows.Err()
}
func (s *Store) Incident(id int64, event string) (*monitor.Incident, error) {
	var data string
	var detail []byte
	if err := s.DB.QueryRow(`SELECT data,detail FROM incidents WHERE host=? AND id=?`, id, event).Scan(&data, &detail); err != nil {
		return nil, err
	}
	var ev monitor.Incident
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return nil, err
	}
	if len(detail) > 0 {
		if err := monitor.Decode(detail, &ev.Recordings); err != nil {
			return nil, err
		}
	} else {
		ev.DetailExpired = true
	}
	return &ev, nil
}
func (s *Store) RemoveHost(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, table := range []string{"points", "incidents"} {
		if _, err := s.DB.Exec("DELETE FROM "+table+" WHERE host=?", id); err != nil {
			return err
		}
	}
	_, err := s.DB.Exec(`DELETE FROM hosts WHERE id=?`, id)
	return err
}

// The in-process local sampler is known to have stopped on a sidecar restart.
func (s *Store) CloseOpenIncidents(id int64, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.DB.Query(`SELECT id,data FROM incidents WHERE host=?`, id)
	if err != nil {
		return err
	}
	events := []monitor.Incident{}
	for rows.Next() {
		var key, data string
		if err := rows.Scan(&key, &data); err != nil {
			rows.Close()
			return err
		}
		var ev monitor.Incident
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			rows.Close()
			return err
		}
		if ev.End == 0 {
			ev.End = ev.Updated
			ev.Updated = now
			events = append(events, ev)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := s.DB.Exec(`UPDATE incidents SET updated=?,data=? WHERE host=? AND id=?`, now, string(data), id, ev.ID); err != nil {
			return err
		}
	}
	return nil
}
