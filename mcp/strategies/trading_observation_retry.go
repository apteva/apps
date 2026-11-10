package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	tradingSnapshotTool    = "portfolio_research_snapshot"
	tradingSnapshotVersion = 1
	tradingRetryBase       = 30 * time.Second
	tradingRetryMax        = 15 * time.Minute
)

var errTradingObservationDeferred = errors.New("Trading observation is stale")

func syncTradingPortfolio(ctx *sdk.AppCtx, pid string, id int64) error {
	tradingObservationMu.Lock()
	defer tradingObservationMu.Unlock()
	return syncTradingPortfolioLocked(ctx, pid, id)
}

// syncTradingPortfolioLocked requires tradingObservationMu.
func syncTradingPortfolioLocked(ctx *sdk.AppCtx, pid string, id int64) error {
	args := map[string]any{"_project_id": pid, "portfolio_id": id}
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	// A different tool, request, or protocol version gets a fresh attempt.
	requestKey := fmt.Sprintf("%s/v%d:%s", tradingSnapshotTool, tradingSnapshotVersion, raw)
	db := ctx.AppDB()
	var previousKey, lastError string
	var failures int
	var retryAt sql.NullInt64
	err = db.QueryRow(`
		SELECT request_key, failures, retry_at, last_error
		FROM trading_observation_failures WHERE project_id=? AND portfolio_id=?`, pid, id).
		Scan(&previousKey, &failures, &retryAt, &lastError)
	hasFailure := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load Trading observation retry state: %w", err)
	}
	if previousKey != requestKey {
		failures = 0
	} else {
		if !retryAt.Valid {
			return fmt.Errorf("%w: snapshot exceeds the response limit until the request or protocol changes", errTradingObservationDeferred)
		}
		deadline := time.UnixMilli(retryAt.Int64)
		if time.Now().Before(deadline) {
			return fmt.Errorf("%w: retry after %s: %s", errTradingObservationDeferred, deadline.UTC().Format(time.RFC3339), lastError)
		}
	}

	if err = refreshTradingPortfolio(ctx, pid, id, args); err != nil {
		failures++
		failedAt := time.Now()
		var nextRetry any
		if !tradingSnapshotTooLarge(err) {
			delay := min(tradingRetryBase<<min(failures-1, 5), tradingRetryMax)
			nextRetry = failedAt.Add(delay).UnixMilli()
		}
		if _, saveErr := db.Exec(`
			INSERT INTO trading_observation_failures(project_id,portfolio_id,request_key,failures,failed_at,retry_at,last_error)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(project_id,portfolio_id) DO UPDATE SET
				request_key=excluded.request_key, failures=excluded.failures, failed_at=excluded.failed_at,
				retry_at=excluded.retry_at, last_error=excluded.last_error`,
			pid, id, requestKey, failures, failedAt.UnixMilli(), nextRetry, strings.TrimSpace(err.Error())); saveErr != nil {
			return errors.Join(err, fmt.Errorf("save Trading observation retry state: %w", saveErr))
		}
		return err
	}
	if hasFailure {
		if _, err = db.Exec(`DELETE FROM trading_observation_failures WHERE project_id=? AND portfolio_id=?`, pid, id); err != nil {
			return fmt.Errorf("clear Trading observation retry state: %w", err)
		}
	}
	return nil
}

func tradingSnapshotTooLarge(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		message := strings.TrimSpace(err.Error())
		// The SDK returns a plain error for its own limit and wraps the
		// gateway's response as an HTTP status and body.
		if message == "app response too large" ||
			(strings.HasPrefix(message, "platform ") && strings.HasSuffix(message, ": http 502: app response too large")) {
			return true
		}
	}
	return false
}
