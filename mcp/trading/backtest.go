package main

import (
	"context"
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
			Name         string   `json:"name"`
			AgentID      int64    `json:"agent_id"`
			StrategyID   int64    `json:"strategy_id"`
			Symbols      []string `json:"symbols"`
			StartAt      string   `json:"start_at"`
			EndAt        string   `json:"end_at"`
			Interval     string   `json:"interval"`
			StartingCash float64  `json:"starting_cash"`
			FeeBps       float64  `json:"fee_bps"`
			SlippageBps  float64  `json:"slippage_bps"`
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
		startAt, endAt := defaultBacktestRange(body.StartAt, body.EndAt)
		marketBars, steps, marketSource, err := captureBacktestMarketBars(symbols, interval, startAt, endAt)
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
			ProjectID:       projectID,
			PortfolioID:     pf.ID,
			SourceAgentID:   agentID,
			StrategyID:      body.StrategyID,
			RunKind:         runKind,
			StrategyVersion: strategyVersion,
			Name:            name,
			Status:          "queued",
			Symbols:         symbols,
			StartAt:         startAt.Format("2006-01-02"),
			EndAt:           endAt.Format("2006-01-02"),
			Interval:        interval,
			StartingCash:    startingCash,
			FeeBps:          body.FeeBps,
			SlippageBps:     body.SlippageBps,
			TotalSteps:      steps,
			Summary: map[string]any{
				"portfolio_name": pf.Name,
				"market_source":  marketSource,
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
		if err != nil || run.Status != "running" {
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
			return
		}
		if run.CurrentStep >= run.TotalSteps {
			_ = dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "completed", "")
			_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "completed", "Backtest completed", run.Summary)
			emitBacktest("trading.backtest.completed", run.ID, map[string]any{"portfolio_id": run.PortfolioID})
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
		if next.Status != "running" || next.CurrentStep >= next.TotalSteps {
			continue
		}
		if !waitBacktestAgentSettled(ctx, next, stepStartedAt) {
			return
		}
	}
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
	if err := globalCtx.PlatformAPI().SendEvent(run.EnvironmentAgentID, prompt); err != nil {
		return nil, fmt.Errorf("send environment agent event: %w", err)
	}
	status := run.Status
	if status == "" {
		status = "running"
	}
	if step >= run.TotalSteps {
		status = "completed"
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
	if status == "completed" {
		_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "completed", "Backtest completed", summary)
		emitBacktest("trading.backtest.completed", run.ID, map[string]any{"portfolio_id": run.PortfolioID})
	}
	next, _ := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	_, _ = backtestPerformance(next)
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
	if len(perf.Orders) == 0 && perf.Current != nil && len(perf.Current.Orders) > 0 {
		perf.Orders = perf.Current.Orders
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
	perf.Metrics = backtestPerformanceMetrics(run, perf.Series, perf.Current)
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
	if sharpe, ok := backtestSharpeRatio(series, run.Interval); ok {
		metrics["sharpe_ratio"] = sharpe
	}
	return metrics
}

func backtestSharpeRatio(series []*BacktestSnapshot, interval string) (float64, bool) {
	returns := make([]float64, 0, len(series)-1)
	var prev float64
	for _, point := range series {
		if point == nil || point.Equity <= 0 {
			continue
		}
		if prev > 0 {
			returns = append(returns, point.Equity/prev-1)
		}
		prev = point.Equity
	}
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
	return mean / math.Sqrt(variance) * math.Sqrt(backtestPeriodsPerYear(interval)), true
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

func captureBacktestMarketBars(symbols []string, interval string, startAt, endAt time.Time) ([]*BacktestMarketBar, int, string, error) {
	symbols = cleanSymbols(symbols)
	if len(symbols) == 0 {
		return nil, 0, "", errors.New("at least one symbol required")
	}
	for _, symbol := range symbols {
		if inferAssetClass(symbol) != "crypto" {
			return nil, 0, "", fmt.Errorf("real backtest bars currently require crypto symbols via Binance; %s is %s", symbol, inferAssetClass(symbol))
		}
	}
	limit := estimateBacktestBarsLimit(startAt, endAt, interval)
	if limit <= 0 {
		return nil, 0, "", errors.New("date range must contain at least one market bar")
	}
	if limit > 1000 {
		return nil, 0, "", fmt.Errorf("backtest real market data is capped at 1000 bars per symbol for now; requested %d", limit)
	}
	source := "binance-public"
	provider := newBinancePublic()
	bySymbol := map[string]map[int64]Bar{}
	commonTimes := map[int64]bool{}
	for i, symbol := range symbols {
		bars, err := provider.BacktestBars(symbol, interval, startAt, inclusiveBacktestEnd(endAt, interval), limit)
		if err != nil {
			return nil, 0, source, fmt.Errorf("fetch %s bars: %w", symbol, err)
		}
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
	out := make([]*BacktestMarketBar, 0, len(times)*len(symbols))
	for step, t := range times {
		for _, symbol := range symbols {
			bar := bySymbol[symbol][t]
			out = append(out, &BacktestMarketBar{
				Step: step + 1, Symbol: symbol, AssetClass: inferAssetClass(symbol), T: t,
				O: nonZeroBarPrice(bar.O, bar.C), H: nonZeroBarPrice(bar.H, bar.C),
				L: nonZeroBarPrice(bar.L, bar.C), C: bar.C, V: bar.V, Source: source,
			})
		}
	}
	return out, len(times), source, nil
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

func defaultBacktestRange(start, end string) (time.Time, time.Time) {
	now := time.Now().UTC()
	endAt := parseDateOr(end, now)
	startAt := parseDateOr(start, endAt.AddDate(0, -3, 0))
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
	start := parseDateOr(run.StartAt, time.Now().UTC())
	if step < 1 {
		step = 1
	}
	switch strings.ToLower(strings.TrimSpace(run.Interval)) {
	case "5m", "15m", "1h", "4h":
		sessionStep := step - 1
		slots := backtestStepsPerSession(run.Interval)
		if slots < 1 {
			slots = 1
		}
		dayOffset := sessionStep / slots
		slot := sessionStep % slots
		sessionOpen := time.Date(start.Year(), start.Month(), start.Day(), 9, 30, 0, 0, time.UTC).AddDate(0, 0, dayOffset)
		return sessionOpen.Add(time.Duration(slot) * backtestIntervalDuration(run.Interval))
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
