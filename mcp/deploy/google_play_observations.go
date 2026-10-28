package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const googlePlayObservationTTL = 5 * time.Minute
const googlePlayTimeFormat = "2006-01-02T15:04:05.000000000Z"

type providerObservation struct {
	CheckedAt   string `json:"checked_at"`
	ExpiresAt   string `json:"expires_at"`
	Cached      bool   `json:"cached"`
	Stale       bool   `json:"stale"`
	NextRetryAt string `json:"next_retry_at,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type googlePlayPausedError struct {
	Until  time.Time
	Reason string
}

func (e *googlePlayPausedError) Error() string {
	return fmt.Sprintf("Google Play refresh paused until %s: %s", e.Until.UTC().Format(time.RFC3339), e.Reason)
}

// State is scoped to the install's database. File locks additionally coordinate
// old/new sidecars sharing that data directory during an upgrade, and release
// automatically on process death. A lock covers the entire edit, including AAB
// and image streams, rather than just the create_edit request.
type googlePlayCoordinator struct {
	db    *sql.DB
	dir   string
	now   func() time.Time
	mu    sync.Mutex
	edits map[string]func()
}

var googlePlayCoordinators sync.Map

func googlePlayCoordinatorForContext() *googlePlayCoordinator {
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return nil
	}
	db := globalCtx.AppDB()
	if cached, ok := googlePlayCoordinators.Load(db); ok {
		return cached.(*googlePlayCoordinator)
	}
	dir := globalCtx.DataDir()
	if dir == "" {
		var seq int
		var name, path string
		if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err == nil && path != "" {
			dir = filepath.Dir(path)
		}
	}
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "apteva-play", fmt.Sprintf("%d-%p", os.Getpid(), db))
	}
	c := &googlePlayCoordinator{db: db, dir: filepath.Join(dir, "google-play-locks"), now: time.Now, edits: map[string]func(){}}
	actual, _ := googlePlayCoordinators.LoadOrStore(db, c)
	return actual.(*googlePlayCoordinator)
}
func isGooglePlayIntegration(bound *sdk.BoundIntegration) bool {
	return bound != nil && (bound.AppSlug == "google-play-developer" || bound.AppSlug == "google-play")
}
func (c *googlePlayCoordinator) lock(key string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(c.dir, jsonDigest(key)+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || !time.Now().Before(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, &googlePlayPausedError{c.now().Add(15 * time.Second), "another Google Play publish or refresh is in progress"}
			}
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }) }, nil
}
func (c *googlePlayCoordinator) before(connection int64) error {
	var until, reason string
	err := c.db.QueryRow(`SELECT next_retry_at,reason FROM google_play_backoff WHERE connection_id=?`, connection).Scan(&until, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	next, err := time.Parse(time.RFC3339Nano, until)
	if err != nil {
		return fmt.Errorf("invalid persisted Google Play backoff: %w", err)
	}
	if c.now().Before(next) {
		return &googlePlayPausedError{next, reason}
	}
	return nil
}
func (c *googlePlayCoordinator) clearBackoff(connection int64) error {
	_, err := c.db.Exec(`DELETE FROM google_play_backoff WHERE connection_id=? AND next_retry_at<=?`, connection, c.now().UTC().Format(googlePlayTimeFormat))
	return err
}
func (c *googlePlayCoordinator) invalidate(connection int64, pkg string) error {
	_, err := c.db.Exec(`DELETE FROM google_play_observations WHERE (connection_id=? OR ?=0) AND package_name=?`, connection, connection, pkg)
	return err
}
func (c *googlePlayCoordinator) endEdit(connection int64, pkg, edit string) {
	key := fmt.Sprint(connection) + "/" + pkg + "/" + edit
	c.mu.Lock()
	unlock := c.edits[key]
	delete(c.edits, key)
	c.mu.Unlock()
	if unlock != nil {
		unlock()
	}
}

func googlePlayBackoffRequired(status int, body json.RawMessage) bool {
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		return true
	}
	if status != http.StatusForbidden {
		return false
	}
	// Permission-denied 403s must never be treated as quota errors.
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "reason" || k == "status" {
					switch strings.ToLower(fmt.Sprint(v)) {
					case "ratelimitexceeded", "userratelimitexceeded", "quotaexceeded", "dailylimitexceeded", "resource_exhausted", "rate_limit_exceeded", "quota_exceeded", "daily_limit_exceeded":
						return true
					}
				}
				if walk(v) {
					return true
				}
			}
		case []any:
			for _, v := range x {
				if walk(v) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}

func googleObservationErrorStatus(current string, err error) string {
	var paused *googlePlayPausedError
	if errors.As(err, &paused) {
		return current
	}
	return "failed"
}
func googleRetryDeadline(now time.Time, headers map[string]string, body json.RawMessage) time.Time {
	deadline := now
	for key, value := range headers {
		if !strings.EqualFold(key, "Retry-After") {
			continue
		}
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && seconds >= 0 && seconds < 365*24*3600 {
			candidate := now.Add(time.Duration(seconds * float64(time.Second)))
			if candidate.After(deadline) {
				deadline = candidate
			}
		} else if candidate, err := http.ParseTime(value); err == nil && candidate.After(deadline) {
			deadline = candidate
		}
	}
	var data any
	_ = json.Unmarshal(body, &data)
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "retryDelay" {
					if duration, err := time.ParseDuration(fmt.Sprint(v)); err == nil && duration > 0 && now.Add(duration).After(deadline) {
						deadline = now.Add(duration)
					}
				}
				walk(v)
			}
		case []any:
			for _, v := range x {
				walk(v)
			}
		}
	}
	walk(data)
	return deadline
}
func (c *googlePlayCoordinator) recordBackoff(connection int64, status int, headers map[string]string, body json.RawMessage) error {
	if !googlePlayBackoffRequired(status, body) {
		return nil
	}
	var attempts int
	err := c.db.QueryRow(`INSERT INTO google_play_backoff(connection_id,failures,next_retry_at,reason) VALUES (?,1,?,'provider quota exceeded') ON CONFLICT(connection_id) DO UPDATE SET failures=failures+1 RETURNING failures`, connection, c.now().Add(time.Minute).UTC().Format(googlePlayTimeFormat)).Scan(&attempts)
	if err != nil {
		return err
	}
	base := time.Minute
	if strings.Contains(strings.ToLower(string(body)), "dailylimitexceeded") {
		base = time.Hour
	}
	for i := 1; i < attempts && base < 6*time.Hour; i++ {
		base *= 2
	}
	if base > 6*time.Hour {
		base = 6 * time.Hour
	}
	now := c.now()
	// Stable connection-specific jitter avoids synchronized instance retries.
	reason := "provider quota exceeded"
	if status == http.StatusServiceUnavailable {
		reason = "provider temporarily unavailable"
	}
	next := now.Add(base + time.Duration(connection%11)*time.Second)
	if requested := googleRetryDeadline(now, headers, body); requested.After(next) {
		next = requested
	}
	_, err = c.db.Exec(`UPDATE google_play_backoff SET next_retry_at=MAX(next_retry_at,?),reason=? WHERE connection_id=?`, next.UTC().Format(googlePlayTimeFormat), reason, connection)
	return err
}

// This wrapper is used for all Google integration tools, including writes.
// It never automatically retries a mutation or an ambiguous commit.
func (c *googlePlayCoordinator) execute(bound *sdk.BoundIntegration, tool string, input map[string]any, run func(map[string]any) (json.RawMessage, error)) (json.RawMessage, error) {
	pkg, _ := input["packageName"].(string)
	edit, _ := input["editId"].(string)
	if tool == "delete_edit" || tool == "commit_edit" {
		defer c.endEdit(bound.ConnectionID, pkg, edit)
	}
	if err := c.before(bound.ConnectionID); err != nil {
		return nil, err
	}
	var unlock func()
	if tool == "create_edit" {
		wait := 30 * time.Second
		observing := input["_deploy_observation"] == true
		if observing {
			wait = 0
		}
		var err error
		releaseObservation := func() {}
		if !observing {
			releaseObservation, err = c.lock(fmt.Sprintf("observation/%d/%s", bound.ConnectionID, pkg), wait)
			if err != nil {
				return nil, err
			}
		}
		releaseEdit, err := c.lock(fmt.Sprintf("edit/%d/%s", bound.ConnectionID, pkg), wait)
		if err != nil {
			releaseObservation()
			return nil, err
		}
		unlock = func() { releaseEdit(); releaseObservation() }
		defer func() {
			if unlock != nil {
				unlock()
			}
		}()
		if err = c.before(bound.ConnectionID); err != nil {
			return nil, err
		}
		if input["_deploy_observation"] == true {
			if err = c.cloudUploadPause(bound.ConnectionID, pkg); err != nil {
				return nil, err
			}
			clean := make(map[string]any, len(input))
			for k, v := range input {
				if k != "_deploy_observation" {
					clean[k] = v
				}
			}
			input = clean
		}
	}
	data, err := run(input)
	if err != nil {
		var providerErr *integrationToolError
		if errors.As(err, &providerErr) {
			if saveErr := c.recordBackoff(bound.ConnectionID, providerErr.Status, providerErr.Headers, providerErr.Data); saveErr != nil {
				return nil, fmt.Errorf("%w (persist quota backoff: %v)", err, saveErr)
			}
		}
		return nil, err
	}
	if tool == "create_edit" {
		id := jsonStringAt(data, "id")
		if id == "" {
			return nil, errors.New("Google Play create_edit response missing id")
		}
		key := fmt.Sprint(bound.ConnectionID) + "/" + pkg + "/" + id
		c.mu.Lock()
		c.edits[key] = unlock
		c.mu.Unlock()
		unlock = nil
	}
	if tool == "commit_edit" {
		if err = c.invalidate(bound.ConnectionID, pkg); err != nil {
			return nil, err
		}
		if err = c.clearBackoff(bound.ConnectionID); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func (c *googlePlayCoordinator) cloudUploadPause(connection int64, pkg string) error {
	rows, err := c.db.Query(`SELECT b.target_config_json,b.build_backend_config_json FROM builds b JOIN deployments d ON d.id=b.deployment_id WHERE d.target_kind='android' AND b.status IN ('pending','running') AND b.build_backend!='local' AND b.external_job_id!=''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var targetJSON, backendJSON string
		if err = rows.Scan(&targetJSON, &backendJSON); err != nil {
			return err
		}
		var cfg struct {
			Package     string           `json:"package_name"`
			Connections map[string]int64 `json:"connections"`
		}
		var backend struct {
			Mode string `json:"artifact_mode"`
		}
		if json.Unmarshal([]byte(targetJSON), &cfg) != nil || json.Unmarshal([]byte(backendJSON), &backend) != nil {
			continue
		}
		if cfg.Package == pkg && backend.Mode == "store_upload" && (cfg.Connections["play_store"] == 0 || cfg.Connections["play_store"] == connection) {
			return &googlePlayPausedError{c.now().Add(30 * time.Second), "a cloud-managed Play upload is active"}
		}
	}
	return rows.Err()
}

func (c *googlePlayCoordinator) observe(bound *sdk.BoundIntegration, pkg, key string, load func() (json.RawMessage, error)) (json.RawMessage, *providerObservation, error) {
	unlock, lockErr := c.lock(fmt.Sprintf("observation/%d/%s", bound.ConnectionID, pkg), time.Second)
	if lockErr == nil {
		defer unlock()
	}
	var err error
	var data, checked string
	readErr := c.db.QueryRow(`SELECT payload_json,checked_at FROM google_play_observations WHERE connection_id=? AND package_name=? AND observation_key=?`, bound.ConnectionID, pkg, key).Scan(&data, &checked)
	if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		return nil, nil, readErr
	}
	at, parseErr := time.Parse(time.RFC3339Nano, checked)
	if readErr == nil && parseErr != nil {
		return nil, nil, parseErr
	}
	obs := &providerObservation{CheckedAt: checked, ExpiresAt: at.Add(googlePlayObservationTTL).UTC().Format(time.RFC3339Nano), Cached: readErr == nil}
	pause := lockErr
	if pause == nil {
		pause = c.before(bound.ConnectionID)
	}
	if pause == nil {
		pause = c.cloudUploadPause(bound.ConnectionID, pkg)
	}
	if pause == nil && readErr == nil && c.now().Before(at.Add(googlePlayObservationTTL)) {
		return json.RawMessage(data), obs, nil
	}
	if pause == nil {
		var fresh json.RawMessage
		fresh, err = load()
		if err == nil {
			err = c.before(bound.ConnectionID)
		}
		if err == nil {
			checked = c.now().UTC().Format(time.RFC3339Nano)
			_, err = c.db.Exec(`INSERT INTO google_play_observations(connection_id,package_name,observation_key,payload_json,checked_at) VALUES(?,?,?,?,?) ON CONFLICT(connection_id,package_name,observation_key) DO UPDATE SET payload_json=excluded.payload_json,checked_at=excluded.checked_at`, bound.ConnectionID, pkg, key, string(fresh), checked)
			if err != nil {
				return nil, nil, err
			}
			if err = c.clearBackoff(bound.ConnectionID); err != nil {
				return nil, nil, err
			}
			return fresh, &providerObservation{CheckedAt: checked, ExpiresAt: c.now().Add(googlePlayObservationTTL).UTC().Format(time.RFC3339Nano)}, nil
		}
		pause = err
		if backoff := c.before(bound.ConnectionID); backoff != nil {
			pause = backoff
		}
	}
	var paused *googlePlayPausedError
	if errors.As(pause, &paused) && readErr == nil {
		obs.Stale = true
		obs.NextRetryAt = paused.Until.UTC().Format(time.RFC3339Nano)
		obs.Reason = paused.Reason
		return json.RawMessage(data), obs, nil
	}
	return nil, nil, pause
}

func googlePlayHTTPError(bound *sdk.BoundIntegration, tool string, status int, header http.Header, data json.RawMessage) error {
	headers := map[string]string{"Retry-After": header.Get("Retry-After")}
	err := &integrationToolError{Slug: bound.AppSlug, Tool: tool, Status: status, Data: data, Headers: headers}
	if c := googlePlayCoordinatorForContext(); c != nil {
		if saveErr := c.recordBackoff(bound.ConnectionID, status, headers, data); saveErr != nil {
			return fmt.Errorf("%w (persist quota backoff: %v)", err, saveErr)
		}
	}
	return err
}

func (a *App) observeGoogleStoreConfig(d *Deployment, doc StoreDocument, cfg *MobileStoreConfig) (map[string]any, error) {
	bound, err := selectedIntegration("play_store", d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	key := "listing/" + jsonDigest(map[string]any{"document": doc, "privacy_acknowledged": providerCommitValidated(cfg, "google_data_safety", doc.VersionName)})
	data, observation, err := googlePlayCoordinatorForContext().observe(bound, target.PackageName, key, func() (json.RawMessage, error) {
		value, err := a.observeGoogleStoreConfigUncached(d, doc, cfg)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)
	})
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err = json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	value["observation"] = observation
	if observation.Stale {
		if checks, ok := value["readiness"].(map[string]any); ok {
			for _, raw := range checks {
				if check, ok := raw.(map[string]any); ok && check["source"] == "provider" {
					check["status"] = "unverified"
					check["message"] = "Cached Google Play observation; " + observation.Reason + ". Next retry: " + observation.NextRetryAt
				}
			}
		}
	}
	return value, nil
}

func readAndroidDistribution(bound *sdk.BoundIntegration, target distributionTarget) (*mobileDistributionState, error) {
	data, observation, err := googlePlayCoordinatorForContext().observe(bound, target.PackageName, "testers/"+jsonDigest(target), func() (json.RawMessage, error) {
		value, err := readAndroidDistributionUncached(bound, target)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)
	})
	if err != nil {
		return nil, err
	}
	var value mobileDistributionState
	if err = json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	value.Observation = observation
	if observation.Stale {
		value.Synced = false
		value.TesterAccess = "sync_required"
	}
	return &value, nil
}
