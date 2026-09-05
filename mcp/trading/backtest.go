package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	backtestAgentQuietWindow      = 5 * time.Second
	backtestAgentMaxWait          = 90 * time.Second
	backtestTelemetryPollInterval = 2 * time.Second
	backtestTelemetryFallbackWait = 30 * time.Second
	defaultBacktestMaxMarketRows  = 500_000
	backtestHistoryFetchTimeout   = 5 * time.Minute
	backtestHistoryConcurrency    = 4
)

var (
	backtestRunnerMu      sync.Mutex
	backtestRunnerCancels = map[int64]*backtestRunner{}
)

type backtestRunner struct {
	cancel context.CancelFunc
}

func (a *App) handleHTTPBacktests(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/backtests")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		if r.Method != http.MethodGet {
			httpErr(w, 405, "GET only")
			return
		}
		runs, err := dbListBacktestRuns(globalCtx.AppDB(), pid, 0)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"backtests": runs})
		return
	}
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, 400, "backtest id must be integer")
		return
	}
	run, err := dbGetBacktestRun(globalCtx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, 404, "backtest not found")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		events, _ := dbListBacktestEvents(globalCtx.AppDB(), run.ID, 80)
		httpJSON(w, 200, map[string]any{"backtest": run, "events": events})
	case action == "start" && r.Method == http.MethodPost:
		out, err := startBacktestRun(run)
		if err != nil {
			_ = dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "failed", err.Error())
			_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "error", err.Error(), nil)
			emitBacktest("trading.backtest.failed", run.ID, map[string]any{"error": err.Error()})
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "step" && r.Method == http.MethodPost:
		out, err := stepBacktestRun(run)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "run" && r.Method == http.MethodPost:
		out, err := runBacktestToEnd(run)
		if err != nil {
			_ = dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "failed", err.Error())
			_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "error", err.Error(), nil)
			emitBacktest("trading.backtest.failed", run.ID, map[string]any{"error": err.Error()})
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "pause" && r.Method == http.MethodPost:
		out, err := pauseBacktestRun(run)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "cancel" && r.Method == http.MethodPost:
		stopBacktestRunner(run.ID)
		if run.EnvironmentID != "" && globalCtx.PlatformAPI() != nil {
			_ = globalCtx.PlatformAPI().DestroyEnvironment(run.EnvironmentID)
		}
		_ = dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "cancelled", "")
		_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "cancelled", "Backtest cancelled", nil)
		emitBacktest("trading.backtest.cancelled", run.ID, map[string]any{"portfolio_id": run.PortfolioID})
		httpJSON(w, 200, map[string]any{"status": "cancelled"})
	case action == "events" && r.Method == http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		events, err := dbListBacktestEvents(globalCtx.AppDB(), run.ID, limit)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"events": events})
	case action == "performance" && r.Method == http.MethodGet:
		perf, err := backtestPerformance(run)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"performance": perf})
	default:
		httpErr(w, 404, "no such backtest route")
	}
}

func (a *App) handleHTTPPortfolioBacktests(w http.ResponseWriter, r *http.Request, pf *Portfolio, projectID string) {
	switch r.Method {
	case http.MethodGet:
		runs, err := dbListBacktestRuns(globalCtx.AppDB(), projectID, pf.ID)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"backtests": runs})
	case http.MethodPost:
		var body struct {
			Name           string   `json:"name"`
			AgentID        int64    `json:"agent_id"`
			StrategyID     int64    `json:"strategy_id"`
			Symbols        []string `json:"symbols"`
			StartAt        string   `json:"start_at"`
			EndAt          string   `json:"end_at"`
			Interval       string   `json:"interval"`
			StartingCash   float64  `json:"starting_cash"`
			FeeBps         float64  `json:"fee_bps"`
			SlippageBps    float64  `json:"slippage_bps"`
			AdjustmentMode string   `json:"adjustment_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		symbols := cleanSymbols(body.Symbols)
		var strategy *Strategy
		runKind := "agent"
		strategyVersion := 0
		agentID := body.AgentID
		if body.StrategyID > 0 {
			runKind = "strategy"
			var err error
			strategy, err = dbGetStrategy(globalCtx.AppDB(), projectID, body.StrategyID)
			if err != nil {
				httpErr(w, 400, fmt.Sprintf("strategy %d not found", body.StrategyID))
				return
			}
			def, _, err := validateStrategyDefinition(strategy.Definition)
			if err != nil {
				httpErr(w, 400, err.Error())
				return
			}
			strategyVersion = strategy.Version
			if len(symbols) == 0 {
				symbols = def.Universe
			}
		} else {
			if agentID == 0 {
				agentID = portfolioAgentIDInt(pf.AgentID)
			}
			if agentID == 0 {
				httpErr(w, 400, "agent_id required or bind an agent to this portfolio")
				return
			}
			if globalCtx.PlatformAPI() != nil {
				agent, err := globalCtx.PlatformAPI().GetAgent(agentID)
				if err != nil {
					httpErr(w, 400, fmt.Sprintf("agent %d not found", agentID))
					return
				}
				if agent.ProjectID != "" && agent.ProjectID != projectID {
					httpErr(w, 400, "agent belongs to a different project")
					return
				}
			}
		}
		if len(symbols) == 0 {
			symbols = pf.Watchlist
		}
		if len(symbols) == 0 {
			symbols = []string{"SPY"}
		}
		interval, err := normalizeBacktestInterval(body.Interval)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		startAt, endAt, err := resolveBacktestRange(body.StartAt, body.EndAt)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		adjustmentMode, err := normalizeBacktestAdjustmentMode(body.AdjustmentMode)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		marketBars, steps, marketSource, err := captureBacktestMarketBarsAdjusted(r.Context(), symbols, interval, startAt, endAt, adjustmentMode)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		if steps <= 0 {
			httpErr(w, 400, "market data range must contain at least one step")
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			if strategy != nil {
				name = fmt.Sprintf("%s strategy backtest", strategy.Name)
			} else {
				name = fmt.Sprintf("%s backtest", pf.Name)
			}
		}
		startingCash := body.StartingCash
		if startingCash <= 0 {
			startingCash = pf.StartingCash
		}
		id, err := dbCreateBacktestRun(globalCtx.AppDB(), &BacktestRun{
			ProjectID:         projectID,
			PortfolioID:       pf.ID,
			SourceAgentID:     agentID,
			StrategyID:        body.StrategyID,
			RunKind:           runKind,
			StrategyVersion:   strategyVersion,
			Name:              name,
			Status:            "queued",
			Symbols:           symbols,
			StartAt:           startAt.Format("2006-01-02"),
			EndAt:             endAt.Format("2006-01-02"),
			Interval:          interval,
			StartingCash:      startingCash,
			FeeBps:            body.FeeBps,
			SlippageBps:       body.SlippageBps,
			AdjustmentMode:    adjustmentMode,
			ReferenceManifest: referenceManifest(globalCtx.AppDB(), symbols, startAt.Format("2006-01-02"), endAt.Format("2006-01-02"), adjustmentMode),
			TotalSteps:        steps,
			Summary: map[string]any{
				"portfolio_name":   pf.Name,
				"market_source":    marketSource,
				"market_data":      summarizeBacktestMarketCapture(marketBars, symbols, startAt, endAt),
				"dataset_sha256":   backtestMarketBarsChecksum(marketBars),
				"dataset_rows":     len(marketBars),
				"price_adjustment": adjustmentMode,
				"execution_model": map[string]any{
					"fee_bps": body.FeeBps, "slippage_bps": body.SlippageBps,
					"fill_model": "next_replay_mark", "lookahead": "disabled",
				},
				"strategy_name": func() string {
					if strategy != nil {
						return strategy.Name
					}
					return ""
				}(),
			},
		})
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		if err := dbReplaceBacktestMarketBars(globalCtx.AppDB(), id, marketBars); err != nil {
			_ = dbSetBacktestStatus(globalCtx.AppDB(), id, "failed", err.Error())
			httpErr(w, 500, err.Error())
			return
		}
		run, _ := dbGetBacktestRun(globalCtx.AppDB(), projectID, id)
		_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), id, "created", "Backtest created", map[string]any{"symbols": symbols, "run_kind": runKind, "strategy_id": body.StrategyID, "market_source": marketSource, "bars": len(marketBars)})
		emitBacktest("trading.backtest.created", id, map[string]any{"portfolio_id": pf.ID, "agent_id": agentID, "strategy_id": body.StrategyID, "symbols": symbols, "run_kind": runKind})
		httpJSON(w, 201, map[string]any{"backtest": run})
	default:
		httpErr(w, 405, "GET or POST")
	}
}

func startBacktestRun(run *BacktestRun) (map[string]any, error) {
	if run != nil && run.RunKind == "strategy" {
		return startStrategyBacktestRun(run)
	}
	if run.Status != "queued" && run.Status != "failed" {
		return map[string]any{"backtest": run}, nil
	}
	api := globalCtx.PlatformAPI()
	if api == nil {
		return nil, errors.New("platform API unavailable")
	}
	identity, err := api.WhoAmI()
	if err != nil {
		return nil, fmt.Errorf("whoami: %w", err)
	}
	env, err := api.CreateEnvironment(sdk.EnvironmentCreateRequest{
		ID:            fmt.Sprintf("trading-bt-%d-%d", run.ID, time.Now().UnixNano()),
		ProjectID:     run.ProjectID,
		AppInstallIDs: []int64{identity.InstallID},
		Mode:          sdk.EnvironmentModeBlock,
	})
	if err != nil {
		return nil, fmt.Errorf("create environment: %w", err)
	}

	pf, err := dbGetPortfolio(globalCtx.AppDB(), run.ProjectID, run.PortfolioID)
	if err != nil {
		return nil, err
	}
	var created struct {
		PortfolioID int64 `json:"portfolio_id"`
	}
	if err := api.CallEnvironmentAppResult(env.ID, "trading", "portfolio_create", map[string]any{
		"_project_id":     run.ProjectID,
		"name":            pf.Name + " backtest",
		"mandate":         pf.Mandate,
		"allowed_classes": pf.AllowedClasses,
		"starting_cash":   run.StartingCash,
		"mode":            "paper",
		"source_override": "backtest",
		"fee_bps":         run.FeeBps,
		"slippage_bps":    run.SlippageBps,
	}, &created); err != nil {
		_ = api.DestroyEnvironment(env.ID)
		return nil, fmt.Errorf("seed environment portfolio: %w", err)
	}
	for _, symbol := range run.Symbols {
		var ignored map[string]any
		_ = api.CallEnvironmentAppResult(env.ID, "trading", "watchlist_add", map[string]any{
			"_project_id":  run.ProjectID,
			"portfolio_id": created.PortfolioID,
			"symbol":       symbol,
		}, &ignored)
	}

	run.EnvironmentPortfolioID = created.PortfolioID
	agent, err := api.SpawnEnvironmentAgent(env.ID, sdk.EnvironmentAgentSpawnRequest{
		SourceAgentID: run.SourceAgentID,
		Alias:         fmt.Sprintf("backtest-%d", run.ID),
		Directive:     backtestDirective(run),
	})
	if err != nil {
		_ = api.DestroyEnvironment(env.ID)
		return nil, fmt.Errorf("spawn environment agent: %w", err)
	}
	if err := dbUpdateBacktestEnvironment(globalCtx.AppDB(), run.ID, env.ID, agent.AgentID, created.PortfolioID); err != nil {
		return nil, err
	}
	_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "started", "Environment created and agent spawned", map[string]any{
		"environment_id": env.ID, "environment_agent_id": agent.AgentID, "environment_portfolio_id": created.PortfolioID,
	})
	emitBacktest("trading.backtest.started", run.ID, map[string]any{
		"portfolio_id": run.PortfolioID, "environment_id": env.ID, "environment_agent_id": agent.AgentID,
	})
	next, _ := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	if _, err := stepBacktestRun(next); err != nil {
		return nil, err
	}
	next, _ = dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	return map[string]any{"backtest": next}, nil
}

func runBacktestToEnd(run *BacktestRun) (map[string]any, error) {
	if run != nil && run.RunKind == "strategy" {
		return runStrategyBacktestToEnd(run)
	}
	if run == nil {
		return nil, errors.New("backtest run required")
	}
	waitBeforeFirstStep := false
	waitSince := time.Now()
	switch run.Status {
	case "queued", "failed":
		waitSince = time.Now()
		if _, err := startBacktestRun(run); err != nil {
			return nil, err
		}
		waitBeforeFirstStep = true
	case "paused":
		if err := dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "running", ""); err != nil {
			return nil, err
		}
		_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "resumed", "Continuous run resumed", nil)
		emitBacktest("trading.backtest.resumed", run.ID, map[string]any{"portfolio_id": run.PortfolioID})
	case "running":
	default:
		return nil, fmt.Errorf("backtest is %s; cannot run", run.Status)
	}
	next, err := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	if err != nil {
		return nil, err
	}
	started := startBacktestRunner(next, waitBeforeFirstStep, waitSince)
	if started {
		_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "run", "Continuous run started", map[string]any{
			"mode":                  "agent_paced",
			"quiet_seconds":         int(backtestAgentQuietWindow.Seconds()),
			"max_wait_seconds":      int(backtestAgentMaxWait.Seconds()),
			"wait_before_first":     waitBeforeFirstStep,
			"fallback_wait_seconds": int(backtestTelemetryFallbackWait.Seconds()),
		})
		emitBacktest("trading.backtest.run", run.ID, map[string]any{"portfolio_id": run.PortfolioID})
	}
	next, _ = dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	return map[string]any{"backtest": next, "status": "running", "runner_started": started}, nil
}

func pauseBacktestRun(run *BacktestRun) (map[string]any, error) {
	if run == nil {
		return nil, errors.New("backtest run required")
	}
	stopBacktestRunner(run.ID)
	if run.Status == "running" {
		if err := dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "paused", ""); err != nil {
			return nil, err
		}
		_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "paused", "Continuous run paused", nil)
		emitBacktest("trading.backtest.paused", run.ID, map[string]any{"portfolio_id": run.PortfolioID})
	}
	next, _ := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	return map[string]any{"backtest": next, "status": "paused"}, nil
}

func startBacktestRunner(run *BacktestRun, waitBeforeFirstStep bool, waitSince time.Time) bool {
	if run == nil || run.ID == 0 {
		return false
	}
	backtestRunnerMu.Lock()
	if _, ok := backtestRunnerCancels[run.ID]; ok {
		backtestRunnerMu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	worker := &backtestRunner{cancel: cancel}
	backtestRunnerCancels[run.ID] = worker
	backtestRunnerMu.Unlock()

	go func(projectID string, runID int64, waitBeforeFirst bool, firstWaitSince time.Time) {
		defer func() {
			backtestRunnerMu.Lock()
			if cur := backtestRunnerCancels[runID]; cur == worker {
				delete(backtestRunnerCancels, runID)
			}
			backtestRunnerMu.Unlock()
		}()
		backtestRunnerLoop(ctx, projectID, runID, waitBeforeFirst, firstWaitSince)
	}(run.ProjectID, run.ID, waitBeforeFirstStep, waitSince)
	return true
}

func stopBacktestRunner(runID int64) {
	backtestRunnerMu.Lock()
	worker := backtestRunnerCancels[runID]
	delete(backtestRunnerCancels, runID)
	backtestRunnerMu.Unlock()
	if worker != nil && worker.cancel != nil {
		worker.cancel()
	}
}

func backtestRunnerLoop(ctx context.Context, projectID string, runID int64, waitBeforeFirstStep bool, firstWaitSince time.Time) {
	if waitBeforeFirstStep {
		run, err := dbGetBacktestRun(globalCtx.AppDB(), projectID, runID)
		if err != nil || (run.Status != "running" && !(run.Status == "paused" && run.CurrentStep >= run.TotalSteps)) {
			return
		}
		if !waitBacktestAgentSettled(ctx, run, firstWaitSince) {
			return
		}
	}
	for {
		run, err := dbGetBacktestRun(globalCtx.AppDB(), projectID, runID)
		if err != nil {
			return
		}
		if run.Status != "running" {
			if run.CurrentStep >= run.TotalSteps && run.Status == "paused" {
				finalizeAgentBacktest(run)
			}
			return
		}
		if run.CurrentStep >= run.TotalSteps {
			finalizeAgentBacktest(run)
			return
		}
		stepStartedAt := time.Now()
		if _, err := stepBacktestRun(run); err != nil {
			_ = dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "failed", err.Error())
			_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "error", err.Error(), nil)
			emitBacktest("trading.backtest.failed", run.ID, map[string]any{"error": err.Error()})
			return
		}
		next, err := dbGetBacktestRun(globalCtx.AppDB(), projectID, runID)
		if err != nil {
			return
		}
		if next.Status != "running" {
			return
		}
		if !waitBacktestAgentSettled(ctx, next, stepStartedAt) {
			return
		}
		if next.CurrentStep >= next.TotalSteps {
			finalizeAgentBacktest(next)
			return
		}
	}
}

func finalizeAgentBacktest(run *BacktestRun) {
	if run == nil || run.Status == "completed" || run.Status == "cancelled" || run.Status == "failed" {
		return
	}
	if run.Summary == nil {
		run.Summary = map[string]any{}
	}
	// Pull the environment portfolio after the final quiet window so the last
	// agent decision and its fill are included in the terminal snapshot.
	if perf, err := backtestPerformance(run); err == nil && perf != nil && perf.Current != nil {
		run.Summary["final_equity"] = perf.Current.Equity
		run.Summary["last_step"] = run.CurrentStep
	}
	_ = dbAdvanceBacktestStep(globalCtx.AppDB(), run.ID, run.CurrentStep, run.Summary, "completed")
	_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "completed", "Backtest completed", run.Summary)
	emitBacktest("trading.backtest.completed", run.ID, map[string]any{"portfolio_id": run.PortfolioID})
}

func waitBacktestFallback(ctx context.Context) bool {
	timer := time.NewTimer(backtestTelemetryFallbackWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type backtestTelemetryEvent struct {
	ID       string          `json:"id"`
	AgentID  int64           `json:"instance_id"`
	ThreadID string          `json:"thread_id"`
	Type     string          `json:"type"`
	Time     time.Time       `json:"time"`
	Data     json.RawMessage `json:"data"`
}

func waitBacktestAgentSettled(ctx context.Context, run *BacktestRun, since time.Time) bool {
	if run == nil || run.EnvironmentAgentID == 0 {
		return waitBacktestFallback(ctx)
	}
	if since.IsZero() {
		since = time.Now()
	}

	ticker := time.NewTicker(backtestTelemetryPollInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(backtestAgentMaxWait)
	defer deadline.Stop()

	seenActivity := false
	lastActivityAt := time.Time{}
	failures := 0

	check := func() (settled bool, telemetryOK bool) {
		events, err := fetchBacktestTelemetry(ctx, run.EnvironmentAgentID, since, 120)
		if err != nil {
			return false, false
		}
		for _, ev := range events {
			if ev.Time.Before(since) {
				continue
			}
			seenActivity = true
			if ev.Time.After(lastActivityAt) {
				lastActivityAt = ev.Time
			}
		}
		if !seenActivity {
			return false, true
		}
		return time.Since(lastActivityAt) >= backtestAgentQuietWindow, true
	}

	for {
		settled, ok := check()
		if settled {
			return true
		}
		if !ok {
			failures++
			if failures >= 3 {
				return waitBacktestFallback(ctx)
			}
		} else {
			failures = 0
		}

		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return true
		case <-ticker.C:
		}
	}
}

func fetchBacktestTelemetry(ctx context.Context, agentID int64, since time.Time, limit int) ([]backtestTelemetryEvent, error) {
	if agentID == 0 {
		return nil, errors.New("agent_id required")
	}
	if limit <= 0 {
		limit = 120
	}
	baseURL := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:5280"
	}
	u, err := url.Parse(baseURL + "/api/telemetry")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("agent_id", strconv.FormatInt(agentID, 10))
	q.Set("limit", strconv.Itoa(limit))
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	u.RawQuery = q.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if installID := strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID")); installID != "" {
		req.Header.Set("X-Apteva-App-Install-ID", installID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("telemetry http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var events []backtestTelemetryEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}
	if events == nil {
		events = []backtestTelemetryEvent{}
	}
	return events, nil
}

func stepBacktestRun(run *BacktestRun) (map[string]any, error) {
	if run != nil && run.RunKind == "strategy" {
		return stepStrategyBacktestRun(run)
	}
	if run == nil {
		return nil, errors.New("backtest run required")
	}
	if run.Status != "running" && run.Status != "paused" {
		return nil, fmt.Errorf("backtest is %s, not running", run.Status)
	}
	if run.EnvironmentID == "" || run.EnvironmentAgentID == 0 || run.EnvironmentPortfolioID == 0 {
		return nil, errors.New("backtest environment is not ready")
	}
	step := run.CurrentStep + 1
	if step > run.TotalSteps {
		_ = dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "completed", "")
		return map[string]any{"status": "completed", "backtest": run}, nil
	}
	prices, err := backtestMarks(run, step)
	if err != nil {
		return nil, err
	}
	var ignored map[string]any
	if err := globalCtx.PlatformAPI().CallEnvironmentAppResult(run.EnvironmentID, "trading", "backtest_market_step", map[string]any{
		"_project_id":  run.ProjectID,
		"portfolio_id": run.EnvironmentPortfolioID,
		"run_id":       run.ID,
		"step":         step,
		"prices":       prices,
	}, &ignored); err != nil {
		return nil, fmt.Errorf("update environment marks: %w", err)
	}
	prompt := backtestStepPrompt(run, step, prices)
	eventSentAt := time.Now().UTC()
	if err := globalCtx.PlatformAPI().SendEvent(run.EnvironmentAgentID, prompt); err != nil {
		return nil, fmt.Errorf("send environment agent event: %w", err)
	}
	status := run.Status
	if status == "" {
		status = "running"
	}
	summary := map[string]any{
		"last_step": step,
		"prices":    prices,
	}
	if err := dbAdvanceBacktestStep(globalCtx.AppDB(), run.ID, step, summary, status); err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("Step %d/%d replayed", step, run.TotalSteps)
	_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "step", msg, map[string]any{"step": step, "prices": prices})
	emitBacktest("trading.backtest.tick", run.ID, map[string]any{
		"portfolio_id": run.PortfolioID, "step": step, "total_steps": run.TotalSteps, "prices": prices,
	})
	next, _ := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	_, _ = backtestPerformance(next)
	if step >= run.TotalSteps {
		startBacktestRunner(next, true, eventSentAt)
	}
	return map[string]any{"backtest": next}, nil
}

type BacktestPerformance struct {
	Current   *BacktestSnapshot   `json:"current,omitempty"`
	Series    []*BacktestSnapshot `json:"series"`
	Portfolio *Portfolio          `json:"portfolio,omitempty"`
	Positions []*Position         `json:"positions"`
	Orders    []*Order            `json:"orders"`
	Entries   []*JournalEntry     `json:"entries"`
	Metrics   map[string]float64  `json:"metrics"`
	Error     string              `json:"error,omitempty"`
}

func backtestPerformance(run *BacktestRun) (*BacktestPerformance, error) {
	if run == nil {
		return nil, errors.New("backtest run required")
	}
	perf := &BacktestPerformance{
		Series:    []*BacktestSnapshot{},
		Positions: []*Position{},
		Orders:    []*Order{},
		Entries:   []*JournalEntry{},
		Metrics:   map[string]float64{},
	}

	if run.EnvironmentID != "" && run.EnvironmentPortfolioID > 0 && globalCtx.PlatformAPI() != nil {
		current, portfolio, positions, orders, entries, err := fetchBacktestEnvironmentPerformance(run)
		if err != nil {
			perf.Error = err.Error()
		} else if current != nil {
			perf.Current = current
			perf.Portfolio = portfolio
			perf.Positions = positions
			perf.Orders = orders
			perf.Entries = entries
			_ = dbUpsertBacktestSnapshot(globalCtx.AppDB(), current)
		}
	}

	series, err := dbListBacktestSnapshots(globalCtx.AppDB(), run.ID)
	if err != nil {
		return nil, err
	}
	perf.Series = backtestSeriesWithBaseline(run, series)
	if perf.Current == nil && len(perf.Series) > 0 {
		perf.Current = perf.Series[len(perf.Series)-1]
	}
	if len(perf.Positions) == 0 && perf.Current != nil && len(perf.Current.Positions) > 0 {
		perf.Positions = perf.Current.Positions
	}
	if len(perf.Orders) == 0 {
		seenOrders := map[string]bool{}
		for _, snapshot := range perf.Series {
			for _, order := range snapshot.Orders {
				if order != nil && !seenOrders[order.ID] {
					seenOrders[order.ID] = true
					perf.Orders = append(perf.Orders, order)
				}
			}
		}
	}
	if perf.Positions == nil {
		perf.Positions = []*Position{}
	}
	if perf.Orders == nil {
		perf.Orders = []*Order{}
	}
	if perf.Entries == nil {
		perf.Entries = []*JournalEntry{}
	}
	perf.Metrics = backtestPerformanceMetricsWithOrders(run, perf.Series, perf.Current, perf.Orders)
	return perf, nil
}

func fetchBacktestEnvironmentPerformance(run *BacktestRun) (*BacktestSnapshot, *Portfolio, []*Position, []*Order, []*JournalEntry, error) {
	api := globalCtx.PlatformAPI()
	args := map[string]any{"_project_id": run.ProjectID, "portfolio_id": run.EnvironmentPortfolioID}

	var pfResp struct {
		Portfolio *Portfolio `json:"portfolio"`
	}
	if err := api.CallEnvironmentAppResult(run.EnvironmentID, "trading", "portfolio_get", args, &pfResp); err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("environment portfolio: %w", err)
	}
	var acct struct {
		Equity      float64 `json:"equity"`
		Cash        float64 `json:"cash"`
		BuyingPower float64 `json:"buying_power"`
		OpenPnL     float64 `json:"open_pnl"`
		OpenPnLPct  float64 `json:"open_pnl_pct"`
	}
	if err := api.CallEnvironmentAppResult(run.EnvironmentID, "trading", "account_summary", args, &acct); err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("environment account: %w", err)
	}
	var posResp struct {
		Positions []*Position `json:"positions"`
	}
	if err := api.CallEnvironmentAppResult(run.EnvironmentID, "trading", "positions_list", args, &posResp); err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("environment positions: %w", err)
	}
	var ordResp struct {
		Orders []*Order `json:"orders"`
	}
	orderArgs := map[string]any{"_project_id": run.ProjectID, "portfolio_id": run.EnvironmentPortfolioID, "status": "all", "limit": 100}
	if err := api.CallEnvironmentAppResult(run.EnvironmentID, "trading", "orders_list", orderArgs, &ordResp); err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("environment orders: %w", err)
	}
	var journalResp struct {
		Entries []*JournalEntry `json:"entries"`
	}
	journalArgs := map[string]any{"_project_id": run.ProjectID, "portfolio_id": run.EnvironmentPortfolioID, "limit": 20}
	_ = api.CallEnvironmentAppResult(run.EnvironmentID, "trading", "journal_read", journalArgs, &journalResp)
	if posResp.Positions == nil {
		posResp.Positions = []*Position{}
	}
	if ordResp.Orders == nil {
		ordResp.Orders = []*Order{}
	}
	if journalResp.Entries == nil {
		journalResp.Entries = []*JournalEntry{}
	}
	if pfResp.Portfolio == nil {
		pfResp.Portfolio = &Portfolio{ID: run.EnvironmentPortfolioID}
	}
	if acct.Equity == 0 {
		acct.Equity = pfResp.Portfolio.Equity
	}
	if acct.Cash == 0 {
		acct.Cash = pfResp.Portfolio.Cash
	}
	if acct.BuyingPower == 0 {
		acct.BuyingPower = acct.Cash
	}
	prices := backtestSummaryPrices(run)
	equity, openPnL, openPnLPct, realized, exposure := valueBacktestPositions(acct.Cash, posResp.Positions, prices)
	if equity == 0 && acct.Equity > 0 {
		equity = acct.Equity
		openPnL = acct.OpenPnL
		openPnLPct = acct.OpenPnLPct
	}
	snap := &BacktestSnapshot{
		RunID:       run.ID,
		Step:        run.CurrentStep,
		Equity:      equity,
		Cash:        acct.Cash,
		BuyingPower: acct.BuyingPower,
		OpenPnL:     openPnL,
		OpenPnLPct:  openPnLPct,
		RealizedPnL: realized,
		Exposure:    exposure,
		Positions:   posResp.Positions,
		Orders:      ordResp.Orders,
		Prices:      prices,
	}
	return snap, pfResp.Portfolio, posResp.Positions, ordResp.Orders, journalResp.Entries, nil
}

func valueBacktestPositions(cash float64, positions []*Position, prices []map[string]any) (equity, openPnL, openPnLPct, realizedPnL, exposure float64) {
	priceBySymbol := map[string]float64{}
	for _, row := range prices {
		symbol := strings.ToUpper(strings.TrimSpace(fmt.Sprint(row["symbol"])))
		price := anyFloat(row["price"])
		if symbol != "" && price > 0 {
			priceBySymbol[symbol] = price
		}
	}
	grossMarketValue := 0.0
	costBasis := 0.0
	for _, p := range positions {
		if p == nil {
			continue
		}
		if price := priceBySymbol[strings.ToUpper(strings.TrimSpace(p.Symbol))]; price > 0 {
			p.MarketPrice = price
		}
		if p.MarketPrice <= 0 {
			p.MarketPrice = p.AvgCost
		}
		p.MarketValue = p.Qty * p.MarketPrice
		p.UnrealizedPnL = (p.MarketPrice - p.AvgCost) * p.Qty
		if p.AvgCost > 0 {
			p.UnrealizedPnLPct = (p.MarketPrice/p.AvgCost - 1) * 100
		}
		grossMarketValue += p.MarketValue
		openPnL += p.UnrealizedPnL
		realizedPnL += p.RealizedPnL
		exposure += math.Abs(p.MarketValue)
		costBasis += math.Abs(p.Qty * p.AvgCost)
	}
	equity = cash + grossMarketValue
	if costBasis > 0 {
		openPnLPct = openPnL / costBasis * 100
	}
	if equity > 0 {
		exposure = exposure / equity * 100
		for _, p := range positions {
			if p != nil {
				p.WeightPct = p.MarketValue / equity * 100
			}
		}
	}
	return equity, openPnL, openPnLPct, realizedPnL, exposure
}

func backtestSeriesWithBaseline(run *BacktestRun, snapshots []*BacktestSnapshot) []*BacktestSnapshot {
	base := &BacktestSnapshot{
		RunID:       run.ID,
		Step:        0,
		Equity:      run.StartingCash,
		Cash:        run.StartingCash,
		BuyingPower: run.StartingCash,
		Prices:      []map[string]any{},
		Positions:   []*Position{},
		Orders:      []*Order{},
	}
	out := []*BacktestSnapshot{base}
	for _, s := range snapshots {
		if s.Step == 0 {
			out[0] = s
			continue
		}
		out = append(out, s)
	}
	return out
}

func backtestPerformanceMetrics(run *BacktestRun, series []*BacktestSnapshot, current *BacktestSnapshot) map[string]float64 {
	return backtestPerformanceMetricsWithOrders(run, series, current, nil)
}

func backtestPerformanceMetricsWithOrders(run *BacktestRun, series []*BacktestSnapshot, current *BacktestSnapshot, orders []*Order) map[string]float64 {
	metrics := map[string]float64{
		"starting_cash": run.StartingCash,
		"current_step":  float64(run.CurrentStep),
		"total_steps":   float64(run.TotalSteps),
	}
	if current != nil {
		metrics["equity"] = current.Equity
		metrics["cash"] = current.Cash
		metrics["buying_power"] = current.BuyingPower
		metrics["open_pnl"] = current.OpenPnL
		metrics["open_pnl_pct"] = current.OpenPnLPct
		metrics["realized_pnl"] = current.RealizedPnL
		metrics["exposure"] = current.Exposure
		if run.StartingCash > 0 {
			metrics["total_pnl"] = current.Equity - run.StartingCash
			metrics["return_pct"] = (current.Equity/run.StartingCash - 1) * 100
		}
	}
	peak := 0.0
	maxDD := 0.0
	for _, point := range series {
		if point.Equity > peak {
			peak = point.Equity
		}
		if peak > 0 {
			dd := (point.Equity/peak - 1) * 100
			if dd < maxDD {
				maxDD = dd
			}
		}
	}
	metrics["max_drawdown_pct"] = maxDD
	periods := backtestPeriodsPerYearForRun(run)
	if sharpe, ok := backtestSharpeRatioWithPeriods(series, periods); ok {
		metrics["sharpe_ratio"] = sharpe
	}
	returns := backtestEquityReturns(series)
	if len(returns) > 1 {
		mean, volatility, downside := returnMoments(returns)
		metrics["annualized_return_pct"] = mean * periods * 100
		metrics["annualized_volatility_pct"] = volatility * math.Sqrt(periods) * 100
		if downside > 0 {
			metrics["sortino_ratio"] = mean / downside * math.Sqrt(periods)
		}
	}
	if current != nil && run.StartingCash > 0 && current.Equity > 0 && len(returns) > 0 {
		years := float64(len(returns)) / periods
		if years > 0 {
			cagr := math.Pow(current.Equity/run.StartingCash, 1/years) - 1
			metrics["cagr_pct"] = cagr * 100
			if maxDD < 0 {
				metrics["calmar_ratio"] = cagr / math.Abs(maxDD/100)
			}
		}
	}
	metrics["orders"] = float64(len(orders))
	turnover := 0.0
	for _, order := range orders {
		if order != nil && order.FilledQty > 0 && order.AvgFillPrice > 0 {
			turnover += order.FilledQty * order.AvgFillPrice
		}
	}
	if run.StartingCash > 0 {
		metrics["turnover_pct"] = turnover / run.StartingCash * 100
	}
	return metrics
}

func backtestEquityReturns(series []*BacktestSnapshot) []float64 {
	returns := make([]float64, 0, len(series)-1)
	prev := 0.0
	for _, point := range series {
		if point == nil || point.Equity <= 0 {
			continue
		}
		if prev > 0 {
			returns = append(returns, point.Equity/prev-1)
		}
		prev = point.Equity
	}
	return returns
}

func returnMoments(returns []float64) (mean, volatility, downside float64) {
	for _, value := range returns {
		mean += value
	}
	mean /= float64(len(returns))
	for _, value := range returns {
		delta := value - mean
		volatility += delta * delta
		if value < 0 {
			downside += value * value
		}
	}
	volatility = math.Sqrt(volatility / float64(len(returns)-1))
	downside = math.Sqrt(downside / float64(len(returns)))
	return
}

func backtestSharpeRatio(series []*BacktestSnapshot, interval string) (float64, bool) {
	return backtestSharpeRatioWithPeriods(series, backtestPeriodsPerYear(interval))
}

func backtestSharpeRatioWithPeriods(series []*BacktestSnapshot, periods float64) (float64, bool) {
	returns := backtestEquityReturns(series)
	if len(returns) < 2 {
		return 0, false
	}
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))
	variance := 0.0
	for _, r := range returns {
		d := r - mean
		variance += d * d
	}
	variance /= float64(len(returns) - 1)
	if variance <= 0 {
		return 0, false
	}
	return mean / math.Sqrt(variance) * math.Sqrt(periods), true
}

func backtestPeriodsPerYearForRun(run *BacktestRun) float64 {
	cryptoOnly := len(run.Symbols) > 0
	for _, symbol := range run.Symbols {
		if inferAssetClass(symbol) != "crypto" {
			cryptoOnly = false
			break
		}
	}
	if cryptoOnly {
		return backtestPeriodsPerYear(run.Interval)
	}
	switch strings.ToLower(strings.TrimSpace(run.Interval)) {
	case "5m":
		return 252 * 78
	case "15m":
		return 252 * 26
	case "1h":
		return 252 * 6.5
	case "4h":
		return 252 * 2
	case "1w":
		return 52
	default:
		return 252
	}
}

func backtestPeriodsPerYear(interval string) float64 {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "5m":
		return 365 * 24 * 12
	case "15m":
		return 365 * 24 * 4
	case "1h":
		return 365 * 24
	case "4h":
		return 365 * 6
	case "1w":
		return 365.0 / 7.0
	default:
		return 365
	}
}

func backtestSummaryPrices(run *BacktestRun) []map[string]any {
	raw, ok := run.Summary["prices"].([]any)
	if !ok {
		if typed, ok := run.Summary["prices"].([]map[string]any); ok {
			return typed
		}
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if row, ok := item.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

func (a *App) toolBacktestMarketStep(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID := int64Arg(args, "portfolio_id", 0)
	step := intArg(args, "step", 0)
	raw, _ := args["prices"].([]any)
	updated := []map[string]any{}
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		symbol := strings.ToUpper(strings.TrimSpace(fmt.Sprint(row["symbol"])))
		if symbol == "" {
			continue
		}
		price := anyFloat(row["price"])
		if price <= 0 {
			continue
		}
		cls := strings.TrimSpace(fmt.Sprint(row["asset_class"]))
		if cls == "" {
			cls = inferAssetClass(symbol)
		}
		prev := anyFloat(row["prev_close"])
		if prev <= 0 {
			prev = price
		}
		mark := &Mark{
			Symbol:     symbol,
			AssetClass: cls,
			Price:      price,
			PrevClose:  &prev,
			MarkedAt:   time.Now().UTC().Format(time.RFC3339),
		}
		if cls == "polymarket" {
			no := 1 - price
			mark.NoPrice = &no
		}
		if err := dbUpsertMark(ctx.AppDB(), mark); err != nil {
			return nil, err
		}
		updated = append(updated, map[string]any{"symbol": symbol, "price": price, "asset_class": cls})
	}
	if portfolioID > 0 {
		_, _ = dbInsertJournal(ctx.AppDB(), pid, portfolioID, "note",
			fmt.Sprintf("Backtest market step %d loaded %d mark(s).", step, len(updated)),
			map[string]any{"source": "backtest_market_step", "step": step, "prices": updated})
	}
	emit("trading.backtest.market_step", map[string]any{"portfolio_id": portfolioID, "step": step, "prices": updated})
	return map[string]any{"updated": len(updated), "prices": updated}, nil
}

func backtestDirective(run *BacktestRun) string {
	return fmt.Sprintf(`You are running inside an isolated trading backtest.
Only use the trading MCP tools in this environment. Evaluate the portfolio one replay step at a time.
At each replay message, inspect portfolio %d, review prices/positions/orders, and place paper orders only when the mandate justifies it.
Always write a short journal note explaining the decision, including when you choose to do nothing.`, run.EnvironmentPortfolioID)
}

func backtestStepPrompt(run *BacktestRun, step int, prices []map[string]any) string {
	replayAt := backtestReplayTime(run, step)
	return fmt.Sprintf("Backtest replay step %d/%d for environment portfolio %d. Replay interval: %s. Replay time: %s. Current prices: %v. Inspect the portfolio, decide whether to trade, and journal your rationale.",
		step, run.TotalSteps, run.EnvironmentPortfolioID, run.Interval, replayAt.Format(time.RFC3339), prices)
}

func backtestMarks(run *BacktestRun, step int) ([]map[string]any, error) {
	if run == nil {
		return nil, errors.New("backtest run required")
	}
	if step <= 0 {
		return nil, fmt.Errorf("backtest step must be positive, got %d", step)
	}
	return dbBacktestMarketMarks(globalCtx.AppDB(), run.ID, step, run.Symbols)
}

func captureBacktestMarketBars(parent context.Context, symbols []string, interval string, startAt, endAt time.Time) ([]*BacktestMarketBar, int, string, error) {
	return captureBacktestMarketBarsAdjusted(parent, symbols, interval, startAt, endAt, "provider_adjusted")
}

func captureBacktestMarketBarsAdjusted(parent context.Context, symbols []string, interval string, startAt, endAt time.Time, adjustmentMode string) ([]*BacktestMarketBar, int, string, error) {
	symbols = cleanSymbols(symbols)
	if len(symbols) == 0 {
		return nil, 0, "", errors.New("at least one symbol required")
	}
	if endAt.Before(startAt) {
		return nil, 0, "", errors.New("backtest end date must be on or after start date")
	}
	provider, source, err := backtestBarsProviderForSymbols(symbols)
	if err != nil {
		return nil, 0, "", err
	}
	adjustmentMode, err = normalizeBacktestAdjustmentMode(adjustmentMode)
	if err != nil {
		return nil, 0, "", err
	}
	if _, ok := provider.(historicalAdjustedBacktestBarsProvider); !ok && adjustmentMode != "provider_adjusted" {
		return nil, 0, "", fmt.Errorf("market source %s cannot guarantee adjustment mode %s", source, adjustmentMode)
	}
	limit := estimateBacktestBarsLimit(startAt, endAt, interval)
	if limit <= 0 {
		return nil, 0, "", errors.New("date range must contain at least one market bar")
	}
	estimatedRows := estimateBacktestMarketRows(source, startAt, endAt, interval, len(symbols))
	if maxRows := configuredBacktestMaxMarketRows(); maxRows > 0 && estimatedRows > maxRows {
		return nil, 0, "", fmt.Errorf(
			"backtest would load approximately %d market rows, above this install's %d-row safety budget; shorten the range, reduce the universe, use a coarser interval, or raise backtest_max_market_rows",
			estimatedRows, maxRows)
	}

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, backtestHistoryFetchTimeout)
	defer cancel()
	type fetchResult struct {
		symbol string
		bars   []Bar
		err    error
	}
	results := make(chan fetchResult, len(symbols))
	sem := make(chan struct{}, backtestHistoryConcurrency)
	var wg sync.WaitGroup
	for _, symbol := range symbols {
		symbol := symbol
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- fetchResult{symbol: symbol, err: ctx.Err()}
				return
			}
			var bars []Bar
			var err error
			if adjusted, ok := provider.(historicalAdjustedBacktestBarsProvider); ok {
				bars, err = adjusted.BacktestBarsContextAdjustment(ctx, symbol, interval, startAt, inclusiveBacktestEnd(endAt, interval), limit, adjustmentMode)
			} else {
				bars, err = provider.BacktestBarsContext(ctx, symbol, interval, startAt, inclusiveBacktestEnd(endAt, interval), limit)
			}
			if err == nil {
				bars, err = validateBacktestHistory(symbol, source, interval, startAt, endAt, bars, time.Now().UTC())
			}
			results <- fetchResult{symbol: symbol, bars: bars, err: err}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	fetched := make(map[string][]Bar, len(symbols))
	actualRows := 0
	for result := range results {
		if result.err != nil {
			cancel()
			return nil, 0, source, fmt.Errorf("fetch %s bars: %w", result.symbol, result.err)
		}
		actualRows += len(result.bars)
		if maxRows := configuredBacktestMaxMarketRows(); maxRows > 0 && actualRows > maxRows {
			cancel()
			return nil, 0, source, fmt.Errorf("backtest fetched more than the %d-row market-data safety budget", maxRows)
		}
		fetched[result.symbol] = result.bars
	}

	bySymbol := map[string]map[int64]Bar{}
	commonTimes := map[int64]bool{}
	for i, symbol := range symbols {
		bars := fetched[symbol]
		if len(bars) == 0 {
			return nil, 0, source, fmt.Errorf("fetch %s bars: no bars returned", symbol)
		}
		rows := map[int64]Bar{}
		for _, bar := range bars {
			if bar.C <= 0 {
				continue
			}
			rows[bar.T] = bar
		}
		if len(rows) == 0 {
			return nil, 0, source, fmt.Errorf("fetch %s bars: no priced bars returned", symbol)
		}
		bySymbol[symbol] = rows
		if i == 0 {
			for t := range rows {
				commonTimes[t] = true
			}
			continue
		}
		for t := range commonTimes {
			if _, ok := rows[t]; !ok {
				delete(commonTimes, t)
			}
		}
	}
	times := make([]int64, 0, len(commonTimes))
	for t := range commonTimes {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if len(times) == 0 {
		return nil, 0, source, errors.New("market bars have no common timestamps across symbols")
	}
	minRows := 0
	for _, rows := range bySymbol {
		if minRows == 0 || len(rows) < minRows {
			minRows = len(rows)
		}
	}
	if minRows > 0 && len(times)*100 < minRows*95 {
		return nil, 0, source, fmt.Errorf(
			"market histories are not sufficiently aligned across symbols: only %d of at least %d timestamps are common",
			len(times), minRows)
	}
	out := make([]*BacktestMarketBar, 0, len(times)*len(symbols))
	for step, t := range times {
		for _, symbol := range symbols {
			bar := bySymbol[symbol][t]
			out = append(out, &BacktestMarketBar{
				Step: step + 1, Symbol: symbol, AssetClass: inferAssetClass(symbol), T: t,
				O: nonZeroBarPrice(bar.O, bar.C), H: nonZeroBarPrice(bar.H, bar.C),
				L: nonZeroBarPrice(bar.L, bar.C), C: bar.C, V: bar.V, Source: source,
				VolumeUnit: historicalVolumeUnit(source, inferAssetClass(symbol)), TimestampKind: "exchange",
			})
		}
	}
	return out, len(times), source, nil
}

func normalizeBacktestAdjustmentMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "provider_adjusted":
		return "provider_adjusted", nil
	case "raw":
		return "raw", nil
	case "split_adjusted":
		return "split_adjusted", nil
	case "price_return":
		return "price_return", nil
	case "total_return":
		return "total_return", nil
	default:
		return "", fmt.Errorf("adjustment_mode must be provider_adjusted|raw|split_adjusted|price_return|total_return")
	}
}

func backtestMarketBarsChecksum(bars []*BacktestMarketBar) string {
	h := sha256.New()
	encoder := json.NewEncoder(h)
	encoder.SetEscapeHTML(false)
	for _, bar := range bars {
		_ = encoder.Encode(bar)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

type historicalBacktestBarsProvider interface {
	BacktestBarsContext(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]Bar, error)
}

type historicalAdjustedBacktestBarsProvider interface {
	BacktestBarsContextAdjustment(ctx context.Context, symbol, interval string, start, end time.Time, limit int, adjustmentMode string) ([]Bar, error)
}

func backtestBarsProviderForSymbols(symbols []string) (historicalBacktestBarsProvider, string, error) {
	group := ""
	for _, symbol := range symbols {
		class := inferAssetClass(symbol)
		next := ""
		switch class {
		case "crypto":
			next = "crypto"
		case "equity", "etf":
			next = "stocks"
		default:
			return nil, "", fmt.Errorf("real backtest bars are not available for %s (%s)", symbol, class)
		}
		if group != "" && group != next {
			return nil, "", errors.New("real backtests require a single market calendar; do not mix crypto with equity/ETF symbols")
		}
		group = next
	}
	switch group {
	case "crypto":
		return newBinancePublic(), "binance-public", nil
	case "stocks":
		if globalEngine != nil {
			if live, ok := globalEngine.provider.(*liveProvider); ok && live.equity != nil && live.equity.available() {
				return live.equity, alpacaMarketDataSlug, nil
			}
		}
		return newYahooPublic(), "yahoo-finance", nil
	default:
		return nil, "", errors.New("no supported backtest symbols")
	}
}

func estimateBacktestBarsLimit(startAt, endAt time.Time, interval string) int {
	if endAt.Before(startAt) {
		return 0
	}
	duration := backtestIntervalDuration(interval)
	if duration <= 0 {
		duration = 24 * time.Hour
	}
	end := inclusiveBacktestEnd(endAt, interval)
	return int(end.Sub(startAt)/duration) + 1
}

func estimateBacktestMarketRows(source string, startAt, endAt time.Time, interval string, symbols int) int {
	if symbols <= 0 {
		return 0
	}
	perSymbol := estimateBacktestBarsLimit(startAt, endAt, interval)
	if source == "yahoo-finance" {
		perSymbol = estimateBacktestSteps(startAt, endAt, interval)
	}
	if perSymbol > math.MaxInt/symbols {
		return math.MaxInt
	}
	return perSymbol * symbols
}

func configuredBacktestMaxMarketRows() int {
	if globalCtx == nil {
		return defaultBacktestMaxMarketRows
	}
	raw := strings.TrimSpace(globalCtx.Config().Get("backtest_max_market_rows"))
	if raw == "" {
		return defaultBacktestMaxMarketRows
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultBacktestMaxMarketRows
	}
	return value
}

func validateBacktestHistory(symbol, source, interval string, startAt, endAt time.Time, bars []Bar, now time.Time) ([]Bar, error) {
	duration := backtestIntervalDuration(interval)
	if duration <= 0 {
		return nil, fmt.Errorf("unsupported backtest interval %q", interval)
	}
	normalized, err := normalizeBars(symbol, source, bars)
	if err != nil {
		return nil, err
	}
	if source == "yahoo-finance" {
		normalized = completedYahooStrategyBars(normalized, interval, now)
	} else {
		completed := normalized[:0]
		for _, bar := range normalized {
			if !time.Unix(bar.T, 0).UTC().Add(duration).After(now) {
				completed = append(completed, bar)
			}
		}
		normalized = completed
	}
	if len(normalized) == 0 {
		return nil, errors.New("provider returned no completed priced bars")
	}

	first := time.Unix(normalized[0].T, 0).UTC()
	last := time.Unix(normalized[len(normalized)-1].T, 0).UTC()
	startTolerance := duration
	endTolerance := duration
	if source == "yahoo-finance" {
		startTolerance = 7 * 24 * time.Hour
		endTolerance = 7 * 24 * time.Hour
		if interval == "1w" {
			startTolerance = 14 * 24 * time.Hour
			endTolerance = 14 * 24 * time.Hour
		}
	}
	if first.After(startAt.UTC().Add(startTolerance)) {
		return nil, fmt.Errorf(
			"history begins at %s, later than requested start %s; this provider does not cover the full range",
			first.Format(time.RFC3339), startAt.UTC().Format(time.RFC3339))
	}
	targetEnd := inclusiveBacktestEnd(endAt, interval)
	if targetEnd.After(now) {
		targetEnd = strategyClosedCandleBoundary(now, interval)
	}
	if last.Before(targetEnd.Add(-endTolerance)) {
		return nil, fmt.Errorf(
			"history ends at %s, earlier than requested end %s; this provider returned incomplete data",
			last.Format(time.RFC3339), targetEnd.UTC().Format(time.RFC3339))
	}
	if source == "binance-public" {
		for i := 1; i < len(normalized); i++ {
			previous := time.Unix(normalized[i-1].T, 0).UTC()
			current := time.Unix(normalized[i].T, 0).UTC()
			if current.Sub(previous) > duration+time.Second {
				return nil, fmt.Errorf(
					"history has a gap between %s and %s",
					previous.Format(time.RFC3339), current.Format(time.RFC3339))
			}
		}
	}
	return normalized, nil
}

func summarizeBacktestMarketCapture(bars []*BacktestMarketBar, symbols []string, requestedStart, requestedEnd time.Time) map[string]any {
	summary := map[string]any{
		"requested_start_at": requestedStart.UTC().Format(time.RFC3339),
		"requested_end_at":   requestedEnd.UTC().Format(time.RFC3339),
		"row_count":          len(bars),
		"symbol_count":       len(cleanSymbols(symbols)),
	}
	counts := map[string]int{}
	var first, last int64
	maxStep := 0
	for _, bar := range bars {
		if bar == nil {
			continue
		}
		counts[bar.Symbol]++
		if first == 0 || bar.T < first {
			first = bar.T
		}
		if bar.T > last {
			last = bar.T
		}
		if bar.Step > maxStep {
			maxStep = bar.Step
		}
	}
	summary["bars_per_symbol"] = counts
	summary["step_count"] = maxStep
	if first > 0 {
		summary["actual_start_at"] = time.Unix(first, 0).UTC().Format(time.RFC3339)
	}
	if last > 0 {
		summary["actual_end_at"] = time.Unix(last, 0).UTC().Format(time.RFC3339)
	}
	return summary
}

func inclusiveBacktestEnd(endAt time.Time, interval string) time.Time {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "5m", "15m", "1h", "4h":
		return time.Date(endAt.Year(), endAt.Month(), endAt.Day(), 23, 59, 59, 0, time.UTC)
	default:
		return endAt
	}
}

func nonZeroBarPrice(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func resolveBacktestRange(start, end string) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	endAt := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if strings.TrimSpace(end) != "" {
		parsed, err := time.Parse("2006-01-02", strings.TrimSpace(end))
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid backtest end date %q; use YYYY-MM-DD", end)
		}
		endAt = parsed
	}
	startAt := endAt.AddDate(0, -3, 0)
	if strings.TrimSpace(start) != "" {
		parsed, err := time.Parse("2006-01-02", strings.TrimSpace(start))
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid backtest start date %q; use YYYY-MM-DD", start)
		}
		startAt = parsed
	}
	if endAt.Before(startAt) {
		return time.Time{}, time.Time{}, errors.New("backtest end date must be on or after start date")
	}
	return startAt, endAt, nil
}

func defaultBacktestRange(start, end string) (time.Time, time.Time) {
	startAt, endAt, err := resolveBacktestRange(start, end)
	if err != nil {
		now := time.Now().UTC()
		return now.AddDate(0, -3, 0), now
	}
	return startAt, endAt
}

func parseDateOr(value string, fallback time.Time) time.Time {
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(value)); err == nil {
		return t
	}
	return fallback
}

func estimateBacktestSteps(start, end time.Time, interval string) int {
	if end.Before(start) {
		return 0
	}
	days := int(end.Sub(start).Hours()/24) + 1
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "5m", "15m", "1h", "4h":
		return days * backtestStepsPerSession(interval)
	case "1w":
		return int(math.Ceil(float64(days) / 7))
	default:
		return days
	}
}

func normalizeBacktestInterval(interval string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(interval))
	if v == "" {
		v = "1d"
	}
	switch v {
	case "5m", "15m", "1h", "4h", "1d", "1w":
		return v, nil
	default:
		return "", fmt.Errorf("unsupported backtest interval %q; use 5m, 15m, 1h, 4h, 1d, or 1w", interval)
	}
}

func backtestStepsPerSession(interval string) int {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "5m":
		return 78 // 6.5h US-style regular session.
	case "15m":
		return 26
	case "1h":
		return 7
	case "4h":
		return 2
	default:
		return 1
	}
}

func backtestReplayTime(run *BacktestRun, step int) time.Time {
	if run != nil && globalCtx != nil && globalCtx.AppDB() != nil {
		if replayAt, ok := dbBacktestMarketStepTime(globalCtx.AppDB(), run.ID, step); ok {
			return replayAt
		}
	}
	start := parseDateOr(run.StartAt, time.Now().UTC())
	if step < 1 {
		step = 1
	}
	switch strings.ToLower(strings.TrimSpace(run.Interval)) {
	case "5m", "15m", "1h", "4h":
		return start.Add(time.Duration(step-1) * backtestIntervalDuration(run.Interval))
	case "1w":
		return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, (step-1)*7)
	default:
		return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, step-1)
	}
}

func backtestIntervalDuration(interval string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "4h":
		return 4 * time.Hour
	case "1w":
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func cleanSymbols(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, symbol := range in {
		s := strings.ToUpper(strings.TrimSpace(symbol))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func portfolioAgentIDInt(ref string) int64 {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		ref = ref[i+1:]
	}
	n, _ := strconv.ParseInt(ref, 10, 64)
	return n
}

func anyFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return n
	default:
		return 0
	}
}

func emitBacktest(topic string, runID int64, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["backtest_id"] = runID
	emit(topic, data)
}
