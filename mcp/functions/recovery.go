package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// A kernel lock, rather than a PID or expiring heartbeat, proves ownership.
// Locks live next to the database, so moving the artifact cache has no effect.
// After a crash (or a restore on another host), locks are released by the OS.
type runtimeOwner struct {
	id, dir string
	lock    *os.File
}

func openRuntimeOwner(ctx context.Context, db *sql.DB, fallback string) (*runtimeOwner, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(fallback, "runtime-locks")
	for rows.Next() {
		var seq int
		var name, file string
		if err = rows.Scan(&seq, &name, &file); err != nil {
			rows.Close()
			return nil, err
		}
		if name == "main" && file != "" {
			dir = file + ".runtime-locks"
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	o := &runtimeOwner{id: uuid.NewString(), dir: dir}
	o.lock, err = lockOwner(dir, o.id)
	if err != nil {
		return nil, err
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO function_runtime_owners(id) VALUES(?)", o.id); err != nil {
		o.close()
		return nil, err
	}
	return o, nil
}
func lockOwner(dir, id string) (*os.File, error) {
	// IDs from restored databases are untrusted path components.
	if _, err := uuid.Parse(id); err != nil {
		return nil, fmt.Errorf("invalid runtime owner: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, id), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (o *runtimeOwner) close() {
	if o != nil && o.lock != nil {
		o.lock.Close()
		o.lock = nil
	}
}

func (o *runtimeOwner) recover(ctx context.Context, db *sql.DB) (int64, error) {
	rows, err := db.QueryContext(ctx, "SELECT id FROM function_runtime_owners WHERE id!=?", o.id)
	if err != nil {
		return 0, err
	}
	var owners []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		owners = append(owners, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, id := range owners {
		lock, e := lockOwner(o.dir, id)
		if errors.Is(e, unix.EWOULDBLOCK) || errors.Is(e, unix.EAGAIN) {
			continue
		}
		if e != nil {
			return total, e
		}
		n, e := recoverOwner(ctx, db, id)
		lock.Close()
		if e != nil {
			return total, e
		}
		// Unique owner IDs are never reused. Only remove after the DB row is gone.
		_ = os.Remove(filepath.Join(o.dir, id))
		total += n
	}
	return total, nil
}
func recoverOwner(ctx context.Context, db *sql.DB, owner string) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var total int64
	for _, s := range []struct{ kind, query string }{
		{"invocation", `UPDATE function_invocations SET status='error',error='Invocation interrupted by restart',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id IN (SELECT id FROM function_active_work WHERE owner=? AND kind='invocation') AND status='running'`},
		{"build", `UPDATE function_versions SET build_status='failed',build_log='Build interrupted by restart' WHERE id IN (SELECT id FROM function_active_work WHERE owner=? AND kind='build') AND build_status IN ('pending','building')`},
	} {
		r, e := tx.ExecContext(ctx, s.query, owner)
		if e != nil {
			return total, e
		}
		n, _ := r.RowsAffected()
		total += n
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM function_active_work WHERE owner=?", owner); err != nil {
		return total, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM function_runtime_owners WHERE id=?", owner); err != nil {
		return total, err
	}
	return total, tx.Commit()
}

func (p *pool) beginWork() (func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.draining {
		return nil, errors.New("functions runtime is draining; retry on the active instance")
	}
	p.workWG.Add(1)
	p.activeWork++
	return func() { p.mu.Lock(); p.activeWork--; p.mu.Unlock(); p.workWG.Done() }, nil
}
func (a *App) handleRuntimeDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		httpErr(w, 405, "POST required")
		return
	}
	token := os.Getenv("APTEVA_APP_DRAIN_TOKEN")
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(r.Header.Get("X-Apteva-Drain-Token"))) != 1 {
		httpErr(w, 403, "platform drain credential required")
		return
	}
	p := currentPool()
	if p == nil {
		httpErr(w, 503, "not mounted")
		return
	}
	p.mu.Lock()
	p.draining = true
	n := p.activeWork
	p.mu.Unlock()
	httpJSON(w, map[string]any{"draining": true, "active_work": n, "drained": n == 0})
}

// Legacy rows have no provable owner. Snapshot high-water IDs at mount, then
// wait beyond the old runtime's maximum invocation/build lifetime before
// touching them. Persist cursors and scan in small, paced batches off startup.
// New owned work is always excluded, including work from overlapping runtimes.
func (p *pool) initLegacyRecovery(ctx context.Context) error {
	for _, s := range []struct{ kind, table string }{{"invocation", "function_invocations"}, {"build", "function_versions"}} {
		if _, err := p.ctx.AppDB().ExecContext(ctx, `INSERT OR IGNORE INTO function_recovery_progress(kind,cursor,ceiling) SELECT ?,0,COALESCE(MAX(id),0) FROM `+s.table, s.kind); err != nil {
			return err
		}
	}
	return nil
}
func (p *pool) legacyRecoveryLoop() {
	defer p.maintenanceWG.Done()
	timer := time.NewTimer(time.Duration(maxTimeoutMS)*time.Millisecond + time.Minute)
	defer timer.Stop()
	select {
	case <-p.life.Done():
		return
	case <-timer.C:
	}
	for _, kind := range []string{"invocation", "build"} {
		for {
			done, err := p.recoverLegacyBatch(p.life, kind)
			if err != nil {
				p.ctx.Logger().Warn("legacy recovery", "kind", kind, "err", err)
				return
			}
			if done {
				break
			}
			timer.Reset(100 * time.Millisecond)
			select {
			case <-p.life.Done():
				return
			case <-timer.C:
			}
		}
	}
}
func (p *pool) recoverLegacyBatch(ctx context.Context, kind string) (bool, error) {
	table, status, update := "function_invocations", "status='running'", "status='error',error='Invocation interrupted by restart'"
	if kind == "build" {
		table, status, update = "function_versions", "build_status IN ('pending','building')", "build_status='failed',build_log='Build interrupted by restart'"
	}
	tx, err := p.ctx.AppDB().BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var cursor, ceiling int64
	if err = tx.QueryRowContext(ctx, "SELECT cursor,ceiling FROM function_recovery_progress WHERE kind=?", kind).Scan(&cursor, &ceiling); err != nil {
		return false, err
	}
	var next sql.NullInt64
	if err = tx.QueryRowContext(ctx, "SELECT MAX(id) FROM (SELECT id FROM "+table+" WHERE id>? AND id<=? ORDER BY id LIMIT 128)", cursor, ceiling).Scan(&next); err != nil {
		return false, err
	}
	if !next.Valid {
		return true, nil
	}
	_, err = tx.ExecContext(ctx, "UPDATE "+table+" SET "+update+" WHERE id>? AND id<=? AND "+status+" AND NOT EXISTS (SELECT 1 FROM function_active_work WHERE kind=? AND id="+table+".id)", cursor, next.Int64, kind)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE function_recovery_progress SET cursor=? WHERE kind=?", next.Int64, kind); err != nil {
		return false, err
	}
	return next.Int64 >= ceiling, tx.Commit()
}

// Runtime lifecycle diagnostics are available without initiating a drain.
type startupStep struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
}

func (a *App) handleRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		httpErr(w, 405, "GET required")
		return
	}
	p := currentPool()
	if p == nil {
		httpErr(w, 503, "not mounted")
		return
	}
	p.mu.Lock()
	draining, n := p.draining, p.activeWork
	p.mu.Unlock()
	httpJSON(w, map[string]any{"draining": draining, "active_work": n, "startup_steps": p.startupSteps, "recovered_work": p.recoveredWork, "legacy_recovery": "background, bounded batches after a six-minute safety grace"})
}

// OLD may still be alive when NEW mounts and die later. Revisit only the small
// owner/active-work tables so that these late crashes do not leave running rows.
func (p *pool) recoverAbandonedWork() {
	n, err := p.owner.recover(p.life, p.ctx.AppDB())
	if err != nil && p.life.Err() == nil {
		p.ctx.Logger().Warn("recover abandoned work", "err", err)
	}
	if n > 0 {
		p.ctx.Logger().Info("recovered abandoned work after handover", "rows", n)
	}
}
