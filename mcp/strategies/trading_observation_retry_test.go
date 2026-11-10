package main

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestFailedTradingSyncBacksOffWithoutConsumingStrategyCadence(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	globalEngine.provider = &recordingStrategyProvider{}
	platform := ctx.PlatformAPI().(*fixtureTrading)
	platform.snapshotErr = errors.New("Trading timed out")
	portfolioID := mustCreatePortfolio(t, ctx, "Retry portfolio", []string{"crypto"})
	strategyID := mustCreateFixedStrategy(t, ctx, "Daily BTC", "BTC-USD", .1, "1d")
	assignmentID, err := dbAssignStrategy(db, &StrategyAssignment{
		ProjectID: "test-proj", PortfolioID: portfolioID, StrategyID: strategyID,
		StrategyVersion: 1, ControlMode: "strategy", Cadence: "1d",
	})
	if err != nil {
		t.Fatal(err)
	}
	const lastCheck = "2026-07-12T00:00:00Z"
	if _, err := db.Exec(`
		UPDATE portfolio_strategy_assignments SET last_checked_at=?,last_evaluated_at=? WHERE id=?`,
		lastCheck, lastCheck, assignmentID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 15, 0, 0, 0, time.UTC)
	tick := func() {
		t.Helper()
		if orders, err := evaluateLiveStrategyAssignments(globalEngine, ctx, now); err != nil || orders != 0 {
			t.Fatalf("tick placed %d orders, err = %v", orders, err)
		}
	}
	checkFailure := func(wantFailures int, wantDelay time.Duration) {
		t.Helper()
		var failures int
		var delay int64
		var lastError string
		err := db.QueryRow(`
			SELECT failures,retry_at-failed_at,last_error FROM trading_observation_failures WHERE portfolio_id=?`,
			portfolioID).Scan(&failures, &delay, &lastError)
		if err != nil || failures != wantFailures || delay != wantDelay.Milliseconds() || lastError != platform.snapshotErr.Error() {
			t.Fatalf("failure state = (%d, %d ms, %q), err = %v", failures, delay, lastError, err)
		}
	}
	expireRetry := func() {
		t.Helper()
		if _, err := db.Exec(`UPDATE trading_observation_failures SET retry_at=0 WHERE portfolio_id=?`, portfolioID); err != nil {
			t.Fatal(err)
		}
	}

	tick()
	checkFailure(1, 30*time.Second)
	tick()
	_, err = (&App{}).toolOrderPlace(ctx, map[string]any{"portfolio_id": portfolioID, "strategy_id": strategyID})
	if !errors.Is(err, errTradingObservationDeferred) || len(platform.observedPortfolios) != 1 || len(platform.orderRequests) != 0 {
		t.Fatalf("blocked retry reached Trading: snapshots=%v orders=%v err=%v", platform.observedPortfolios, platform.orderRequests, err)
	}
	expireRetry()
	tick()
	checkFailure(2, time.Minute)
	assignment, err := dbActiveStrategyAssignment(db, "test-proj", portfolioID)
	if err != nil || assignment.LastCheckedAt != lastCheck || assignment.LastEvaluatedAt != lastCheck {
		t.Fatalf("failed sync advanced the strategy clock: %+v, %v", assignment, err)
	}
	portfolio, err := dbGetPortfolio(db, "test-proj", portfolioID)
	if err != nil || portfolio.Status != "active" {
		t.Fatalf("failed sync changed observed portfolio status: %+v, %v", portfolio, err)
	}

	expireRetry()
	platform.snapshotErr = nil
	tick()
	assignment, err = dbActiveStrategyAssignment(db, "test-proj", portfolioID)
	if err != nil || assignment.LastCheckedAt == lastCheck || assignment.LastMarketBarAt == "" {
		t.Fatalf("successful sync did not resume the strategy check: %+v, %v", assignment, err)
	}
	if err := db.QueryRow(`SELECT 1 FROM trading_observation_failures WHERE portfolio_id=?`, portfolioID).Scan(new(int)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("successful sync left retry state: %v", err)
	}
}

func TestOversizedLegacyTradingSnapshotBacksOffAndRecovers(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	platform := ctx.PlatformAPI().(*fixtureTrading)
	portfolioID := mustCreatePortfolio(t, ctx, "Oversized portfolio", []string{"crypto"})
	otherPortfolioID := mustCreatePortfolio(t, ctx, "Other portfolio", []string{"crypto"})
	platform.snapshotErr = fmt.Errorf("platform /api/apps/callback/apps/trading/call: http 502: app response too large\n")
	if err := syncTradingPortfolio(ctx, "test-proj", portfolioID); !errors.Is(err, platform.snapshotErr) {
		t.Fatalf("first sync error = %v", err)
	}
	var delay int64
	if err := db.QueryRow(`SELECT retry_at-failed_at FROM trading_observation_failures WHERE portfolio_id=?`, portfolioID).Scan(&delay); err != nil || delay != tradingRetryMax.Milliseconds() {
		t.Fatalf("oversized legacy response has incorrect retry delay: %d, %v", delay, err)
	}

	platform.snapshotErr = nil
	if err := syncTradingPortfolio(ctx, "test-proj", portfolioID); !errors.Is(err, errTradingObservationDeferred) || len(platform.observedPortfolios) != 1 {
		t.Fatalf("unchanged oversized request retried: snapshots=%v err=%v", platform.observedPortfolios, err)
	}
	if err := syncTradingPortfolio(ctx, "test-proj", otherPortfolioID); err != nil {
		t.Fatalf("unrelated snapshot was blocked: %v", err)
	}
	if _, err := db.Exec(`UPDATE trading_observation_failures SET retry_at=0 WHERE portfolio_id=?`, portfolioID); err != nil {
		t.Fatal(err)
	}
	if err := syncTradingPortfolio(ctx, "test-proj", portfolioID); err != nil {
		t.Fatalf("upgraded Trading was blocked after the retry deadline: %v", err)
	}
	if err := db.QueryRow(`SELECT 1 FROM trading_observation_failures WHERE portfolio_id=?`, portfolioID).Scan(new(int)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("upgraded Trading did not clear the previous failure: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO trading_observation_failures(project_id,portfolio_id,request_key,failures,failed_at,last_error)
		VALUES ('test-proj',?,'portfolio_research_snapshot/v1:legacy',1,0,'app response too large')`, portfolioID); err != nil {
		t.Fatal(err)
	}
	if err := syncTradingPortfolio(ctx, "test-proj", portfolioID); err != nil {
		t.Fatalf("paged protocol inherited a permanent failure from v1: %v", err)
	}
}
