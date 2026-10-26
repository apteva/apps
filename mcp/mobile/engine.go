package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type deviceGate struct {
	mu        sync.Mutex
	file      *os.File
	sessionID string
}
type engine struct {
	db      *sql.DB
	drivers *drivers
	backend Backend
	dataDir string
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	gates   map[string]*deviceGate
	runs    map[string]context.CancelFunc
	wg      sync.WaitGroup
	opWG    sync.WaitGroup
}

func newEngine(db *sql.DB, dataDir string, d *drivers) (*engine, error) {
	if dataDir == "" {
		return nil, errors.New("Mobile requires a writable data directory")
	}
	if d.cfg.LockDir == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return nil, e
		}
		d.cfg.LockDir = filepath.Join(home, ".apteva", "mobile-locks")
	}
	if d.cfg.ArtifactDir == "" {
		d.cfg.ArtifactDir = filepath.Join(dataDir, "uploads")
	}
	for _, dir := range []string{d.cfg.LockDir, d.cfg.ArtifactDir, filepath.Join(dataDir, "evidence")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	if d.cfg.AndroidDriver != "" && d.cfg.AndroidDriver != "adb" && d.cfg.AndroidDriver != "appium" {
		return nil, errors.New("android_driver must be adb or appium")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &engine{db: db, drivers: d, backend: d, dataDir: dataDir, ctx: ctx, cancel: cancel, gates: map[string]*deviceGate{}, runs: map[string]context.CancelFunc{}}
	// Driver sessions may outlive the sidecar. Recover them under the same
	// host-wide hardware lock before permitting another session on that phone.
	if err := e.recover(); err != nil {
		cancel()
		return nil, err
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				e.reap()
			}
		}
	}()
	return e, nil
}
func (e *engine) gate(device string) *deviceGate {
	e.mu.Lock()
	defer e.mu.Unlock()
	g := e.gates[device]
	if g == nil {
		g = &deviceGate{}
		e.gates[device] = g
	}
	return g
}
func (e *engine) Shutdown() {
	e.mu.Lock()
	e.cancel()
	e.mu.Unlock()
	e.opWG.Wait()
	e.wg.Wait()
	sessions, err := e.allActive()
	if err != nil {
		return
	}
	for _, s := range sessions {
		g := e.gate(s.DeviceID)
		g.mu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if fresh, err := e.session(s.ProjectID, s.ID); err == nil {
			_ = e.finishLocked(ctx, fresh, g, "interrupted")
		}
		cancel()
		g.mu.Unlock()
	}
	// A terminating process must release its OS handles even if Appium cleanup
	// failed. The durable cleanup_failed row will be recovered by its successor.
	e.mu.Lock()
	for _, g := range e.gates {
		g.mu.Lock()
		unlockDevice(g.file)
		g.file = nil
		g.sessionID = ""
		g.mu.Unlock()
	}
	e.mu.Unlock()
}
func (e *engine) recover() error {
	sessions, err := e.allActive()
	if err != nil {
		return err
	}
	for _, s := range sessions {
		g := e.gate(s.DeviceID)
		g.mu.Lock()
		f, err := lockDevice(e.drivers.cfg.LockDir, e.lockIdentity(s.Device))
		if err == nil {
			g.file = f
			g.sessionID = s.ID
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = e.finishLocked(ctx, s, g, "interrupted")
			cancel()
		}
		g.mu.Unlock()
	}
	_, err = e.db.Exec(`UPDATE mobile_runs SET status='interrupted',failure_kind='infrastructure_error',error='Mobile runner restarted',finished_at=? WHERE status IN ('queued','running')`, time.Now().Unix())
	return err
}
func (e *engine) lockIdentity(d Device) string {
	// Local ADB and local Appium must contend for the same physical device.
	// A separately hosted Appium server must itself have one owning Mobile runner.
	return d.Platform + ":" + d.UDID
}
func (e *engine) reap() {
	sessions, err := e.allActive()
	if err != nil {
		return
	}
	for _, s := range sessions {
		if s.ExpiresAt > time.Now().Unix() && s.Status != "cleanup_failed" {
			continue
		}
		g := e.gate(s.DeviceID)
		if !g.mu.TryLock() {
			continue
		}
		ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		fresh, err := e.session(s.ProjectID, s.ID)
		if err == nil && (fresh.ExpiresAt <= time.Now().Unix() || fresh.Status == "cleanup_failed") {
			_ = e.finishLocked(ctx, fresh, g, "expired")
		}
		cancel()
		g.mu.Unlock()
	}
}
func (e *engine) finishLocked(ctx context.Context, s Session, g *deviceGate, status string) error {
	if s.Status != "active" && s.Status != "opening" && s.Status != "cleanup_failed" {
		return nil
	}
	if g.file == nil {
		f, err := lockDevice(e.drivers.cfg.LockDir, e.lockIdentity(s.Device))
		if err != nil {
			return err
		}
		g.file = f
		g.sessionID = s.ID
	}
	if g.sessionID != s.ID {
		return problem("device_unavailable", "device is owned by a different active lease")
	}
	if s.Status == "cleanup_failed" && s.Device.Driver == "appium" && s.ProviderID == "" {
		return problem("infrastructure_error", "Appium creation had an unknown outcome. Stop any orphaned Appium session, then call mobile_session(action=recover, confirm=true).")
	}
	if err := e.backend.Close(ctx, s); err != nil {
		_, dbErr := e.db.Exec(`UPDATE mobile_sessions SET status='cleanup_failed',error=? WHERE id=?`, "Device cleanup failed; retrying before reuse", s.ID)
		if dbErr != nil {
			return dbErr
		}
		return problem("infrastructure_error", "device cleanup failed; lease remains reserved until cleanup succeeds")
	}
	if _, err := e.db.Exec(`UPDATE mobile_sessions SET status=?,error='' WHERE id=?`, status, s.ID); err != nil {
		return err
	}
	unlockDevice(g.file)
	g.file = nil
	g.sessionID = ""
	return nil
}

const sessionColumns = "id,project_id,device_id,device_json,mode,status,provider_id,created_at,expires_at,error"

type scanner interface{ Scan(...any) error }

func scanSession(row scanner) (Session, error) {
	var s Session
	var device string
	err := row.Scan(&s.ID, &s.ProjectID, &s.DeviceID, &device, &s.Mode, &s.Status, &s.ProviderID, &s.CreatedAt, &s.ExpiresAt, &s.Error)
	if err == nil {
		err = json.Unmarshal([]byte(device), &s.Device)
	}
	return s, err
}
func (e *engine) session(pid, id string) (Session, error) {
	s, err := scanSession(e.db.QueryRow(`SELECT `+sessionColumns+` FROM mobile_sessions WHERE project_id=? AND id=?`, pid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, problem("not_found", "session not found")
	}
	return s, err
}
func (e *engine) allActive() ([]Session, error) {
	return e.querySessions(`SELECT ` + sessionColumns + ` FROM mobile_sessions WHERE status IN ('opening','active','cleanup_failed')`)
}
func (e *engine) querySessions(query string, args ...any) ([]Session, error) {
	rows, err := e.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func ttl(args map[string]any) (int, error) {
	n := integer(args, "ttl_seconds", 900)
	if n < 30 || n > 3600 {
		return 0, problem("invalid_argument", "ttl_seconds must be 30..3600")
	}
	return n, nil
}

func (e *engine) open(ctx context.Context, pid string, args map[string]any) (Session, error) {
	t, err := ttl(args)
	if err != nil {
		return Session{}, err
	}
	mode := str(args, "mode")
	if mode == "" {
		mode = "screen"
	}
	if mode != "screen" && mode != "structured" {
		return Session{}, problem("invalid_argument", "mode must be screen or structured")
	}
	devices, _, err := e.drivers.devices(ctx)
	if err != nil {
		return Session{}, err
	}
	var selected *Device
	for _, d := range devices {
		if str(args, "device_id") != "" {
			if d.ID != str(args, "device_id") {
				continue
			}
		} else {
			if str(args, "platform") != "" && d.Platform != str(args, "platform") {
				continue
			}
			if _, ok := args["physical"]; ok && d.Physical != boolean(args, "physical") {
				continue
			}
			if d.State != "ready" && d.State != "configured" {
				continue
			}
		}
		copy := d
		selected = &copy
		// With a selector, try each matching idle device instead of failing on
		// the first busy phone. A named device always fails explicitly.
		s, err := e.openDevice(ctx, pid, *selected, mode, t)
		if err == nil {
			return s, nil
		}
		if str(args, "device_id") != "" || errorCode(err) != "device_unavailable" {
			return Session{}, err
		}
	}
	if selected != nil {
		return Session{}, problem("device_unavailable", "all matching devices are busy")
	}
	return Session{}, problem("device_unavailable", "no matching device; inspect mobile_devices for setup diagnostics")
}
func (e *engine) openDevice(ctx context.Context, pid string, d Device, mode string, t int) (Session, error) {
	g := e.gate(d.ID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.file != nil {
		existing, err := e.sessionForDevice(d.ID)
		if err != nil {
			return Session{}, err
		}
		if existing.ExpiresAt <= time.Now().Unix() || existing.Status == "cleanup_failed" {
			if err := e.finishLocked(ctx, existing, g, "expired"); err != nil {
				return Session{}, err
			}
		} else {
			return Session{}, problem("device_unavailable", "device already has an exclusive session")
		}
	}
	f, err := lockDevice(e.drivers.cfg.LockDir, e.lockIdentity(d))
	if err != nil {
		return Session{}, err
	}
	s := Session{ID: id("ses_"), ProjectID: pid, DeviceID: d.ID, Device: d, Mode: mode, Status: "opening", CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + int64(t)}
	_, err = e.db.Exec(`INSERT INTO mobile_sessions (`+sessionColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?)`, s.ID, pid, d.ID, jsonText(d), mode, s.Status, "", s.CreatedAt, s.ExpiresAt, "")
	if err != nil {
		unlockDevice(f)
		return Session{}, problem("device_unavailable", "device already has a reserved lease; wait for cleanup or inspect session history")
	}
	g.file = f
	g.sessionID = s.ID
	provider, openErr := e.backend.Open(ctx, d, mode)
	if openErr != nil {
		// A timed-out Appium session creation has an unknown outcome: preserve
		// the reservation rather than allowing two controllers on the phone.
		status := "failed"
		if d.Driver == "appium" && (errors.Is(openErr, context.DeadlineExceeded) || errors.Is(openErr, context.Canceled) || errorCode(openErr) == "infrastructure_error") {
			status = "cleanup_failed"
		}
		if _, err := e.db.Exec(`UPDATE mobile_sessions SET status=?,error=? WHERE id=?`, status, openErr.Error(), s.ID); err != nil {
			return Session{}, err
		}
		if status == "failed" {
			unlockDevice(g.file)
			g.file = nil
			g.sessionID = ""
		}
		return Session{}, openErr
	}
	s.ProviderID = provider
	s.Status = "active"
	if _, err := e.db.Exec(`UPDATE mobile_sessions SET status='active',provider_id=? WHERE id=?`, provider, s.ID); err != nil {
		s.Status = "opening"
		_ = e.finishLocked(context.Background(), s, g, "failed")
		return Session{}, err
	}
	return s, nil
}
func (e *engine) sessionForDevice(device string) (Session, error) {
	return scanSession(e.db.QueryRow(`SELECT `+sessionColumns+` FROM mobile_sessions WHERE device_id=? AND status IN ('opening','active','cleanup_failed')`, device))
}
func (e *engine) withSession(ctx context.Context, pid, sid string, fn func(context.Context, Session) (any, error)) (any, error) {
	s, err := e.session(pid, sid)
	if err != nil {
		return nil, err
	}
	g := e.gate(s.DeviceID)
	g.mu.Lock()
	defer g.mu.Unlock()
	s, err = e.session(pid, sid)
	if err != nil {
		return nil, err
	}
	if s.Status != "active" {
		return nil, problem("session_inactive", "session is "+s.Status)
	}
	if g.file == nil || g.sessionID != s.ID {
		return nil, problem("session_inactive", "session is not owned by this runner process")
	}
	if s.ExpiresAt <= time.Now().Unix() {
		_ = e.finishLocked(ctx, s, g, "expired")
		return nil, problem("session_expired", "session expired; open a new session")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	value, err := fn(ctx, s)
	if err != nil && s.Device.Driver == "appium" && (ctx.Err() != nil || errorCode(err) == "infrastructure_error") {
		// A lost response does not establish that the remote command stopped.
		// End the driver session before permitting further control.
		_, _ = e.db.Exec(`UPDATE mobile_sessions SET status='cleanup_failed',error='Appium command outcome unknown; waiting for session cleanup' WHERE id=?`, s.ID)
	}
	return value, err
}

func (e *engine) Execute(ctx context.Context, pid, tool string, args map[string]any) (any, error) {
	if strings.TrimSpace(pid) == "" {
		return nil, problem("invalid_argument", "an authenticated project scope is required")
	}
	if len(pid) > 256 {
		return nil, problem("invalid_argument", "project id is too long")
	}
	e.mu.Lock()
	if e.ctx.Err() != nil {
		e.mu.Unlock()
		return nil, problem("infrastructure_error", "runner is shutting down")
	}
	e.opWG.Add(1)
	e.mu.Unlock()
	defer e.opWG.Done()
	switch tool {
	case "mobile_devices":
		devices, hints, err := e.drivers.devices(ctx)
		if err != nil {
			return nil, err
		}
		out := []Device{}
		for _, d := range devices {
			if str(args, "platform") != "" && d.Platform != str(args, "platform") {
				continue
			}
			if _, ok := args["physical"]; ok && d.Physical != boolean(args, "physical") {
				continue
			}
			if s, err := e.sessionForDevice(d.ID); err == nil {
				if s.ExpiresAt > time.Now().Unix() || s.Status == "cleanup_failed" {
					d.State = "busy"
				}
			}
			out = append(out, d)
		}
		return map[string]any{"devices": out, "diagnostics": hints}, nil
	case "mobile_session":
		switch str(args, "action") {
		case "open":
			s, err := e.open(ctx, pid, args)
			return map[string]any{"session": s}, err
		case "list":
			sessions, err := e.querySessions(`SELECT `+sessionColumns+` FROM mobile_sessions WHERE project_id=? ORDER BY created_at DESC LIMIT 100`, pid)
			return map[string]any{"sessions": sessions}, err
		case "get":
			s, err := e.session(pid, str(args, "session_id"))
			return map[string]any{"session": s}, err
		case "renew":
			return e.withSession(ctx, pid, str(args, "session_id"), func(ctx context.Context, s Session) (any, error) {
				t, err := ttl(args)
				if err != nil {
					return nil, err
				}
				s.ExpiresAt = time.Now().Unix() + int64(t)
				_, err = e.db.Exec(`UPDATE mobile_sessions SET expires_at=? WHERE id=?`, s.ExpiresAt, s.ID)
				return map[string]any{"session": s}, err
			})
		case "close":
			s, err := e.session(pid, str(args, "session_id"))
			if err != nil {
				return nil, err
			}
			g := e.gate(s.DeviceID)
			g.mu.Lock()
			defer g.mu.Unlock()
			s, err = e.session(pid, s.ID)
			if err != nil {
				return nil, err
			}
			if err = e.finishLocked(ctx, s, g, "closed"); err != nil {
				return nil, err
			}
			s, err = e.session(pid, s.ID)
			return map[string]any{"session": s}, err
		case "recover":
			if !boolean(args, "confirm") {
				return nil, problem("invalid_argument", "recover requires confirm=true after manually stopping the orphaned Appium session")
			}
			s, err := e.session(pid, str(args, "session_id"))
			if err != nil {
				return nil, err
			}
			g := e.gate(s.DeviceID)
			g.mu.Lock()
			defer g.mu.Unlock()
			s, err = e.session(pid, s.ID)
			if err != nil {
				return nil, err
			}
			if s.Status != "cleanup_failed" || s.ProviderID != "" {
				return nil, problem("invalid_argument", "recover only releases an unknown Appium creation outcome; use close for known sessions")
			}
			if g.file == nil {
				f, err := lockDevice(e.drivers.cfg.LockDir, e.lockIdentity(s.Device))
				if err != nil {
					return nil, err
				}
				g.file = f
				g.sessionID = s.ID
			}
			if g.sessionID != s.ID {
				return nil, problem("device_unavailable", "a different session owns this device")
			}
			if _, err = e.db.Exec(`UPDATE mobile_sessions SET status='closed',error='Operator confirmed orphan cleanup' WHERE id=?`, s.ID); err != nil {
				return nil, err
			}
			unlockDevice(g.file)
			g.file = nil
			g.sessionID = ""
			s, err = e.session(pid, s.ID)
			return map[string]any{"session": s}, err
		default:
			return nil, problem("invalid_argument", "session action must be open, list, get, renew, close or recover")
		}
	case "mobile_observe":
		return e.withSession(ctx, pid, str(args, "session_id"), func(ctx context.Context, s Session) (any, error) {
			return e.backend.Observe(ctx, s, boolean(args, "source"))
		})
	case "mobile_action":
		return e.withSession(ctx, pid, str(args, "session_id"), func(ctx context.Context, s Session) (any, error) {
			return e.backend.Action(ctx, s, str(args, "action"), args)
		})
	case "mobile_app":
		return e.withSession(ctx, pid, str(args, "session_id"), func(ctx context.Context, s Session) (any, error) {
			return e.backend.App(ctx, s, str(args, "action"), args)
		})
	case "mobile_stream":
		return e.withSession(ctx, pid, str(args, "session_id"), func(ctx context.Context, s Session) (any, error) {
			return map[string]any{"session_id": s.ID, "mode": "poll", "interval_ms": 1500, "screenshot_tool": "mobile_observe"}, nil
		})
	case "mobile_test_run":
		return e.startTest(pid, args)
	case "mobile_test_get":
		return e.testGet(pid, str(args, "run_id"))
	case "mobile_test_cancel":
		r, err := e.run(pid, str(args, "run_id"))
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		cancel := e.runs[r.ID]
		if cancel != nil {
			cancel()
		}
		e.mu.Unlock()
		return map[string]any{"run_id": r.ID, "cancellation_requested": cancel != nil, "status": r.Status}, nil
	default:
		return nil, problem("not_found", "unknown Mobile tool")
	}
}

const runColumns = "id,project_id,device_id,name,build_ref,status,failure_kind,session_id,created_at,finished_at,error,results_json"

func scanRun(row scanner) (TestRun, error) {
	var r TestRun
	var results string
	err := row.Scan(&r.ID, &r.ProjectID, &r.DeviceID, &r.Name, &r.BuildRef, &r.Status, &r.FailureKind, &r.SessionID, &r.CreatedAt, &r.FinishedAt, &r.Error, &results)
	if err == nil {
		err = json.Unmarshal([]byte(results), &r.Results)
	}
	return r, err
}
func (e *engine) run(pid, rid string) (TestRun, error) {
	r, err := scanRun(e.db.QueryRow(`SELECT `+runColumns+` FROM mobile_runs WHERE project_id=? AND id=?`, pid, rid))
	if errors.Is(err, sql.ErrNoRows) {
		return r, problem("not_found", "test run not found")
	}
	return r, err
}
func (e *engine) testGet(pid, rid string) (any, error) {
	if rid != "" {
		r, err := e.run(pid, rid)
		return map[string]any{"run": r}, err
	}
	rows, err := e.db.Query(`SELECT `+runColumns+` FROM mobile_runs WHERE project_id=? ORDER BY created_at DESC LIMIT 50`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []TestRun{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return map[string]any{"runs": runs}, rows.Err()
}
func (e *engine) startTest(pid string, args map[string]any) (any, error) {
	device, err := requireString(args, "device_id")
	if err != nil {
		return nil, err
	}
	var steps []Step
	if decodeMap(args["steps"], &steps) != nil || len(steps) == 0 || len(steps) > 50 {
		return nil, problem("invalid_argument", "steps must contain 1..50 steps")
	}
	for _, step := range steps {
		switch step.Action {
		case "navigate", "tap", "swipe", "text", "key", "tap_element", "context", "install", "launch", "stop", "screenshot", "assert_text", "wait_for_text":
		default:
			return nil, problem("invalid_argument", "unsupported test action: "+step.Action)
		}
		if len(step.Label) > 200 {
			return nil, problem("invalid_argument", "step label exceeds 200 characters")
		}
		if step.Action == "assert_text" || step.Action == "wait_for_text" {
			if _, err := requireString(step.Params, "text"); err != nil {
				return nil, err
			}
		}
	}
	mode := str(args, "mode")
	if mode != "" && mode != "screen" && mode != "structured" {
		return nil, problem("invalid_argument", "mode must be screen or structured")
	}
	name := str(args, "name")
	build := str(args, "build_ref")
	if len(name) > 200 || len(build) > 1024 {
		return nil, problem("invalid_argument", "name or build_ref too long")
	}
	r := TestRun{ID: id("run_"), ProjectID: pid, DeviceID: device, Name: name, BuildRef: build, Status: "queued", CreatedAt: time.Now().Unix(), Results: []StepResult{}}
	// Admission bounds goroutines and pending reservations on a shared runner.
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.runs) >= 8 {
		return nil, problem("device_unavailable", "runner test capacity reached; try again later")
	}
	if e.ctx.Err() != nil {
		return nil, errors.New("runner is shutting down")
	}
	_, err = e.db.Exec(`INSERT INTO mobile_runs (`+runColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, r.ID, pid, device, name, build, r.Status, "", "", r.CreatedAt, nil, "", "[]")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Minute)
	e.runs[r.ID] = cancel
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		defer func() { e.mu.Lock(); delete(e.runs, r.ID); e.mu.Unlock() }()
		e.executeTest(ctx, r, steps, mode)
	}()
	return map[string]any{"run_id": r.ID, "status": r.Status}, nil
}
func (e *engine) saveRun(r TestRun) error {
	_, err := e.db.Exec(`UPDATE mobile_runs SET status=?,failure_kind=?,session_id=?,finished_at=?,error=?,results_json=? WHERE id=?`, r.Status, r.FailureKind, r.SessionID, r.FinishedAt, r.Error, jsonText(r.Results), r.ID)
	return err
}
func (e *engine) executeTest(ctx context.Context, r TestRun, steps []Step, mode string) {
	finish := func(err error) {
		if r.SessionID != "" {
			// A passing report also requires successful session teardown. Cleanup
			// bypasses request admission so shutdown/cancellation can release a lease.
			if s, getErr := e.session(r.ProjectID, r.SessionID); getErr == nil {
				g := e.gate(s.DeviceID)
				g.mu.Lock()
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				fresh, getErr := e.session(r.ProjectID, r.SessionID)
				var cleanupErr error
				if getErr == nil {
					cleanupErr = e.finishLocked(cleanupCtx, fresh, g, "closed")
				} else {
					cleanupErr = getErr
				}
				cancel()
				g.mu.Unlock()
				if err == nil && cleanupErr != nil {
					err = cleanupErr
				}
			} else if err == nil {
				err = getErr
			}
		}
		if err == nil {
			r.Status = "passed"
		} else {
			r.Status = "failed"
			r.FailureKind = errorCode(err)
			r.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				r.Status = "cancelled"
				r.FailureKind = "cancelled"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				r.Status = "timed_out"
				r.FailureKind = "timeout"
			}
		}
		if e.ctx.Err() != nil {
			r.Status = "interrupted"
			r.FailureKind = "infrastructure_error"
			r.Error = "Mobile runner stopped"
		}
		now := time.Now().Unix()
		r.FinishedAt = &now
		_ = e.saveRun(r)
	}
	s, err := e.open(ctx, r.ProjectID, map[string]any{"device_id": r.DeviceID, "mode": mode, "ttl_seconds": 900})
	if err != nil {
		finish(err)
		return
	}
	r.SessionID = s.ID
	r.Status = "running"
	if err = e.saveRun(r); err != nil {
		finish(err)
		return
	}
	for i, step := range steps {
		result := StepResult{Index: i, Action: step.Action, Label: step.Label, Status: "passed"}
		if err = ctx.Err(); err == nil {
			err = e.testStep(ctx, r.ProjectID, s.ID, step)
		}
		if err != nil {
			result.Status = "failed"
			result.Error = err.Error()
		}
		// Evidence collection has its own short deadline so a cancellation can
		// still retain the last visible screen without delaying shutdown.
		captureCtx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
		v, captureErr := e.Execute(captureCtx, r.ProjectID, "mobile_observe", map[string]any{"session_id": s.ID})
		cancel()
		if captureErr == nil {
			var obs Observation
			if decodeMap(v, &obs) == nil {
				result.ArtifactID, captureErr = e.saveEvidence(r, obs)
			}
		}
		if captureErr != nil && err == nil {
			err = captureErr
			result.Status = "failed"
			result.Error = "Unable to capture step evidence"
		}
		r.Results = append(r.Results, result)
		if saveErr := e.saveRun(r); saveErr != nil && err == nil {
			err = saveErr
		}
		if err != nil {
			finish(err)
			return
		}
	}
	finish(nil)
}
func (e *engine) testStep(ctx context.Context, pid, sid string, step Step) error {
	args := map[string]any{}
	for k, v := range step.Params {
		args[k] = v
	}
	args["session_id"] = sid
	args["action"] = step.Action
	switch step.Action {
	case "assert_text", "wait_for_text":
		duration := time.Duration(integer(args, "timeout_ms", 10000)) * time.Millisecond
		if duration < 100*time.Millisecond || duration > 30*time.Second {
			return problem("invalid_argument", "timeout_ms must be 100..30000")
		}
		if step.Action == "assert_text" {
			duration = time.Millisecond
		}
		deadline := time.Now().Add(duration)
		for {
			v, err := e.Execute(ctx, pid, "mobile_observe", map[string]any{"session_id": sid, "source": true})
			if err != nil {
				return err
			}
			var obs Observation
			if err := decodeMap(v, &obs); err != nil {
				return err
			}
			if strings.Contains(obs.Source, str(args, "text")) {
				return nil
			}
			if time.Now().After(deadline) {
				return problem("assertion_failed", "expected text was not present in the UI source")
			}
			timer := time.NewTimer(400 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	case "screenshot":
		return nil
	case "install", "launch", "stop":
		_, err := e.Execute(ctx, pid, "mobile_app", args)
		return err
	default:
		_, err := e.Execute(ctx, pid, "mobile_action", args)
		return err
	}
}
func (e *engine) saveEvidence(r TestRun, obs Observation) (string, error) {
	b, err := base64.StdEncoding.DecodeString(obs.Image)
	if err != nil {
		return "", err
	}
	if _, err = observation(b, ""); err != nil {
		return "", err
	}
	aid := id("img_")
	p := filepath.Join(e.dataDir, "evidence", aid+".png")
	if err = os.WriteFile(p, b, 0600); err != nil {
		return "", err
	}
	_, err = e.db.Exec(`INSERT INTO mobile_artifacts(id,project_id,run_id,created_at) VALUES (?,?,?,?)`, aid, r.ProjectID, r.ID, time.Now().Unix())
	if err != nil {
		_ = os.Remove(p)
		return "", err
	}
	return aid, nil
}
func (e *engine) evidence(pid, aid string) ([]byte, error) {
	var found string
	if err := e.db.QueryRow(`SELECT id FROM mobile_artifacts WHERE id=? AND project_id=?`, aid, pid).Scan(&found); err != nil {
		return nil, problem("not_found", "artifact not found")
	}
	return os.ReadFile(filepath.Join(e.dataDir, "evidence", found+".png"))
}

// Keep textual test input out of reports. Labels are operator supplied and
// should not contain credentials. Screenshot/source access remains project scoped.
