package main

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

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
	case action == "cancel" && r.Method == http.MethodPost:
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
		agentID := body.AgentID
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
		symbols := cleanSymbols(body.Symbols)
		if len(symbols) == 0 {
			symbols = pf.Watchlist
		}
		if len(symbols) == 0 {
			symbols = []string{"SPY"}
		}
		startAt, endAt := defaultBacktestRange(body.StartAt, body.EndAt)
		steps := estimateBacktestSteps(startAt, endAt, body.Interval)
		if steps <= 0 {
			httpErr(w, 400, "date range must contain at least one step")
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			name = fmt.Sprintf("%s backtest", pf.Name)
		}
		interval := strings.TrimSpace(body.Interval)
		if interval == "" {
			interval = "1d"
		}
		startingCash := body.StartingCash
		if startingCash <= 0 {
			startingCash = pf.StartingCash
		}
		id, err := dbCreateBacktestRun(globalCtx.AppDB(), &BacktestRun{
			ProjectID:     projectID,
			PortfolioID:   pf.ID,
			SourceAgentID: agentID,
			Name:          name,
			Status:        "queued",
			Symbols:       symbols,
			StartAt:       startAt.Format("2006-01-02"),
			EndAt:         endAt.Format("2006-01-02"),
			Interval:      interval,
			StartingCash:  startingCash,
			FeeBps:        body.FeeBps,
			SlippageBps:   body.SlippageBps,
			TotalSteps:    steps,
			Summary:       map[string]any{"portfolio_name": pf.Name},
		})
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		run, _ := dbGetBacktestRun(globalCtx.AppDB(), projectID, id)
		_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), id, "created", "Backtest created", map[string]any{"symbols": symbols})
		emitBacktest("trading.backtest.created", id, map[string]any{"portfolio_id": pf.ID, "agent_id": agentID, "symbols": symbols})
		httpJSON(w, 201, map[string]any{"backtest": run})
	default:
		httpErr(w, 405, "GET or POST")
	}
}

func startBacktestRun(run *BacktestRun) (map[string]any, error) {
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

func stepBacktestRun(run *BacktestRun) (map[string]any, error) {
	if run == nil {
		return nil, errors.New("backtest run required")
	}
	if run.Status != "running" {
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
	prices := backtestMarks(run, step)
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
	status := "running"
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
	return map[string]any{"backtest": next}, nil
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
	return fmt.Sprintf("Backtest replay step %d/%d for environment portfolio %d. Current prices: %v. Inspect the portfolio, decide whether to trade, and journal your rationale.",
		step, run.TotalSteps, run.EnvironmentPortfolioID, prices)
}

func backtestMarks(run *BacktestRun, step int) []map[string]any {
	out := make([]map[string]any, 0, len(run.Symbols))
	for _, symbol := range run.Symbols {
		cls := inferAssetClass(symbol)
		base := backtestAnchor(symbol)
		wave := math.Sin(float64(step+hashSymbol(symbol)%17)/4.0) * 0.018
		trend := (float64((hashSymbol(symbol)%7)-3) * 0.0015) * float64(step)
		price := base * (1 + wave + trend)
		if cls == "polymarket" {
			if price < 0.01 {
				price = 0.01
			}
			if price > 0.99 {
				price = 0.99
			}
		}
		prev := base
		out = append(out, map[string]any{
			"symbol": symbol, "asset_class": cls, "price": round4(price), "prev_close": prev,
		})
	}
	return out
}

func backtestAnchor(symbol string) float64 {
	for _, seed := range mockUniverse {
		if strings.EqualFold(seed.symbol, symbol) {
			return seed.anchor
		}
	}
	switch inferAssetClass(symbol) {
	case "crypto":
		return 1000 + float64(hashSymbol(symbol)%90000)
	case "polymarket":
		return 0.5
	default:
		return 50 + float64(hashSymbol(symbol)%450)
	}
}

func hashSymbol(symbol string) int {
	sum := sha1.Sum([]byte(strings.ToUpper(symbol)))
	return int(binary.BigEndian.Uint32(sum[:4]))
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
	case "1h":
		return days * 6
	case "1w":
		return int(math.Ceil(float64(days) / 7))
	default:
		return days
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
