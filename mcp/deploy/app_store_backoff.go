package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// App Store Connect limits requests per API key, and every app on the account
// shares that key. A 429 or timeout pauses the whole connection so background
// sync and manual tools stop spending calls until Apple's window resets.
const appStoreBackoffMax = time.Hour

// Per-release retry schedule after consecutive sync failures.
var releaseSyncSchedule = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

// Checks for an assigned build while Apple's answer stays the same. Any change
// in that answer goes back to the start of the schedule.
var releaseAwaitingAppleSchedule = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

// A processed build must be accepted into a TestFlight group within this window.
const testFlightAssignmentTimeout = 48 * time.Hour

var appStoreNow = time.Now

type appStorePausedError struct {
	Until  time.Time
	Reason string
}

func (e *appStorePausedError) Error() string {
	return fmt.Sprintf("App Store Connect calls paused until %s: %s", e.Until.UTC().Format(time.RFC3339), e.Reason)
}

func isAppStoreIntegration(bound *sdk.BoundIntegration) bool {
	return bound != nil && bound.AppSlug == "app-store-connect"
}

func executeAppStoreIntegration(bound *sdk.BoundIntegration, tool string, input map[string]any) (json.RawMessage, error) {
	db := globalCtx.AppDB()
	if db != nil {
		if err := appStoreBackoffActive(db, bound.ConnectionID); err != nil {
			return nil, err
		}
	}
	raw, err := executeIntegrationRaw(bound, tool, input)
	if db == nil {
		return raw, err
	}
	if err == nil {
		_, _ = db.Exec(`DELETE FROM app_store_backoff WHERE connection_id=?`, bound.ConnectionID)
		return raw, nil
	}
	if reason := appStoreBackoffReason(err); reason != "" {
		_ = recordAppStoreBackoff(db, bound.ConnectionID, reason, appStoreRetryAfter(err))
	}
	return nil, err
}

func appStoreBackoffActive(db *sql.DB, connection int64) error {
	var until, reason string
	err := db.QueryRow(`SELECT next_retry_at,reason FROM app_store_backoff WHERE connection_id=?`, connection).Scan(&until, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	next, err := time.Parse(time.RFC3339Nano, until)
	if err != nil {
		return nil
	}
	if appStoreNow().Before(next) {
		return &appStorePausedError{Until: next, Reason: reason}
	}
	return nil
}

// appStoreBackoffReason returns a non-empty reason when the error means the
// account is overloaded: rate limiting or a provider timeout.
func appStoreBackoffReason(err error) string {
	var toolErr *integrationToolError
	if errors.As(err, &toolErr) {
		if toolErr.Status == http.StatusTooManyRequests || strings.Contains(string(toolErr.Data), "RATE_LIMIT_EXCEEDED") {
			return "rate limited"
		}
		if toolErr.Status == 0 || toolErr.Status == http.StatusGatewayTimeout {
			if isTimeoutText(string(toolErr.Data)) || toolErr.Status == http.StatusGatewayTimeout {
				return "provider timeout"
			}
		}
		return ""
	}
	if isTimeoutText(err.Error()) {
		return "provider timeout"
	}
	return ""
}

func isTimeoutText(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "deadline exceeded") || strings.Contains(s, "timeout") || strings.Contains(s, "timed out")
}

func appStoreRetryAfter(err error) time.Duration {
	var toolErr *integrationToolError
	if !errors.As(err, &toolErr) {
		return 0
	}
	for key, value := range toolErr.Headers {
		if !strings.EqualFold(key, "Retry-After") {
			continue
		}
		if seconds, parseErr := strconv.Atoi(strings.TrimSpace(value)); parseErr == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		if at, parseErr := http.ParseTime(value); parseErr == nil {
			return time.Until(at)
		}
	}
	return 0
}

func recordAppStoreBackoff(db *sql.DB, connection int64, reason string, retryAfter time.Duration) error {
	var failures int
	if err := db.QueryRow(`INSERT INTO app_store_backoff(connection_id,failures,reason) VALUES (?,1,?)
		ON CONFLICT(connection_id) DO UPDATE SET failures=failures+1, reason=excluded.reason RETURNING failures`, connection, reason).Scan(&failures); err != nil {
		return err
	}
	delay := time.Minute
	for i := 1; i < failures && delay < appStoreBackoffMax; i++ {
		delay *= 2
	}
	if delay > appStoreBackoffMax {
		delay = appStoreBackoffMax
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	next := appStoreNow().Add(delay).UTC().Format(googlePlayTimeFormat)
	_, err := db.Exec(`UPDATE app_store_backoff SET next_retry_at=? WHERE connection_id=?`, next, connection)
	return err
}

// isFinalProviderError reports a provider answer that will never succeed for
// the same resource: a 404, or an integration error marked retryable:false.
func isFinalProviderError(err error) bool {
	var toolErr *integrationToolError
	if !errors.As(err, &toolErr) {
		return false
	}
	if toolErr.Status == http.StatusNotFound {
		return true
	}
	var body struct {
		Retryable *bool `json:"retryable"`
	}
	return json.Unmarshal(toolErr.Data, &body) == nil && body.Retryable != nil && !*body.Retryable
}

func releaseSyncDue(db *sql.DB, releaseID int64) bool {
	var next string
	if err := db.QueryRow(`SELECT next_sync_at FROM release_sync_state WHERE release_id=?`, releaseID).Scan(&next); err != nil {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, next)
	return err != nil || !appStoreNow().Before(at)
}

func recordReleaseSyncFailure(db *sql.DB, releaseID int64, syncErr error) {
	var failures int
	if err := db.QueryRow(`INSERT INTO release_sync_state(release_id,failures,last_error) VALUES (?,1,?)
		ON CONFLICT(release_id) DO UPDATE SET failures=failures+1, last_error=excluded.last_error RETURNING failures`, releaseID, truncateString(syncErr.Error(), 800)).Scan(&failures); err != nil {
		return
	}
	delay := releaseSyncSchedule[len(releaseSyncSchedule)-1]
	if failures <= len(releaseSyncSchedule) {
		delay = releaseSyncSchedule[failures-1]
	}
	_, _ = db.Exec(`UPDATE release_sync_state SET next_sync_at=? WHERE release_id=?`, appStoreNow().Add(delay).UTC().Format(googlePlayTimeFormat), releaseID)
}

func deferReleaseSync(db *sql.DB, releaseID int64, until time.Time) {
	_, _ = db.Exec(`INSERT INTO release_sync_state(release_id,next_sync_at) VALUES (?,?)
		ON CONFLICT(release_id) DO UPDATE SET next_sync_at=excluded.next_sync_at`, releaseID, until.UTC().Format(googlePlayTimeFormat))
}

// recordReleaseSyncSuccess resets failures. A build that is assigned and still
// waiting on Apple keeps a growing interval; anything else is due again next run.
func recordReleaseSyncSuccess(db *sql.DB, releaseID int64) {
	if rel, err := dbGetRelease(db, releaseID); err == nil && rel != nil && releaseAwaitingApple(rel) {
		recordReleaseAwaitingApple(db, releaseID, rel.ExternalStatus)
		return
	}
	_, _ = db.Exec(`DELETE FROM release_sync_state WHERE release_id=?`, releaseID)
}

func releaseAwaitingApple(rel *Release) bool {
	if rel.Provider != "app_store_connect" || rel.Status != "starting" || (rel.Channel != "internal" && rel.Channel != "external") {
		return false
	}
	var meta mobileReleaseMeta
	return json.Unmarshal([]byte(rel.ReleaseMetaJSON), &meta) == nil && meta.BetaGroupAssigned != ""
}

func recordReleaseAwaitingApple(db *sql.DB, releaseID int64, status string) {
	var checks int
	var observed string
	if err := db.QueryRow(`SELECT idle_checks,observed_status FROM release_sync_state WHERE release_id=?`, releaseID).Scan(&checks, &observed); err != nil || observed != status {
		checks = 0
	}
	delay := releaseAwaitingAppleSchedule[len(releaseAwaitingAppleSchedule)-1]
	if checks < len(releaseAwaitingAppleSchedule) {
		delay = releaseAwaitingAppleSchedule[checks]
	}
	_, _ = db.Exec(`INSERT INTO release_sync_state(release_id,failures,next_sync_at,last_error,idle_checks,observed_status) VALUES (?,0,?,'',?,?)
		ON CONFLICT(release_id) DO UPDATE SET failures=0, next_sync_at=excluded.next_sync_at, last_error='', idle_checks=excluded.idle_checks, observed_status=excluded.observed_status`,
		releaseID, appStoreNow().Add(delay).UTC().Format(googlePlayTimeFormat), checks+1, status)
}

// dbStopSupersededAppStoreReleases stops older TestFlight releases that are
// still starting once a newer release exists for the same target and channel.
func dbStopSupersededAppStoreReleases(db *sql.DB) error {
	_, err := db.Exec(`UPDATE releases SET status='stopped', external_status='superseded', stopped_at=?,
		error=CASE WHEN error='' THEN 'Superseded by a newer release' ELSE error END
		WHERE provider='app_store_connect' AND status='starting' AND channel IN ('internal','external')
		AND EXISTS (SELECT 1 FROM releases n WHERE n.provider='app_store_connect' AND n.id>releases.id
			AND n.deployment_id=releases.deployment_id AND COALESCE(n.environment_id,0)=COALESCE(releases.environment_id,0)
			AND n.channel=releases.channel AND n.status IN ('starting','live'))`, nowUTC())
	return err
}

// Beta groups rarely change; cache lookups so each sync does not re-read them.
const appleBetaGroupCacheTTL = 30 * time.Minute

type cachedAppleBetaGroup struct {
	group   appleBetaGroup
	expires time.Time
}

// Keyed by the install database (holding it, so a new database never reuses
// an old entry), then by connection, app, channel and requested group.
var appleBetaGroupCaches sync.Map

func appleBetaGroupCache() *sync.Map {
	var db *sql.DB
	if globalCtx != nil {
		db = globalCtx.AppDB()
	}
	cache, _ := appleBetaGroupCaches.LoadOrStore(db, &sync.Map{})
	return cache.(*sync.Map)
}

func appleBetaGroupCacheKey(bound *sdk.BoundIntegration, target distributionTarget) string {
	return fmt.Sprintf("%d/%s/%s/%s/%s", bound.ConnectionID, target.AppID, target.Channel, target.BetaGroupID, strings.ToLower(strings.TrimSpace(target.BetaGroupName)))
}

func (a *App) cachedIOSBetaGroup(bound *sdk.BoundIntegration, target distributionTarget) (appleBetaGroup, error) {
	cache, key := appleBetaGroupCache(), appleBetaGroupCacheKey(bound, target)
	if cached, ok := cache.Load(key); ok {
		entry := cached.(cachedAppleBetaGroup)
		if appStoreNow().Before(entry.expires) {
			return entry.group, nil
		}
		cache.Delete(key)
	}
	group, err := a.findIOSBetaGroup(bound, target, true)
	if err == nil && group.ID != "" {
		cache.Store(key, cachedAppleBetaGroup{group: group, expires: appStoreNow().Add(appleBetaGroupCacheTTL)})
	}
	return group, err
}

func forgetIOSBetaGroup(bound *sdk.BoundIntegration, target distributionTarget) {
	appleBetaGroupCache().Delete(appleBetaGroupCacheKey(bound, target))
}
