package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
	rules "github.com/apteva/apps/mcp/strategies/internal/ruleengine"
)

// StrategyDefinition is a rule program. Universe and Cadence mirror the
// program's symbols and base timeframe for the scheduling, capture and
// governance code that reads them.
type StrategyDefinition struct {
	Program  *rules.Program `json:"program"`
	Universe []string       `json:"universe"`
	Cadence  string         `json:"cadence"`
}

func parseStrategyDefinition(raw map[string]any) (*StrategyDefinition, error) {
	if raw == nil {
		return nil, errors.New("definition required")
	}
	for key := range raw {
		switch key {
		case "program", "universe", "cadence":
		default:
			return nil, fmt.Errorf("unknown definition field %q: a strategy is a rule program in definition.program", key)
		}
	}
	if raw["program"] == nil {
		return nil, errors.New("definition.program required")
	}
	programJSON, err := json.Marshal(raw["program"])
	if err != nil {
		return nil, err
	}
	var def StrategyDefinition
	decoder := json.NewDecoder(bytes.NewReader(programJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&def.Program); err != nil {
		return nil, fmt.Errorf("program: %w", err)
	}
	if err := rules.Validate(def.Program); err != nil {
		return nil, err
	}
	def.Universe = append([]string(nil), def.Program.Symbols...)
	def.Cadence = def.Program.Timeframe
	return &def, nil
}

func validateStrategyDefinition(raw map[string]any) (*StrategyDefinition, []string, error) {
	def, err := parseStrategyDefinition(raw)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	if err := liveSupport(def); err != nil {
		warnings = append(warnings, "Backtest only: "+err.Error())
	}
	return def, warnings, nil
}

// liveSupport reports what keeps a program from running on a Trading
// portfolio today. Live evaluation happens once per newly closed bar from the
// portfolio's state, so it supports bar-close rules, set, flatten, target and
// market entries. Shorts, protective orders and features need Trading support.
func liveSupport(def *StrategyDefinition) error {
	p := def.Program
	var visit func(rules.Expr, int) error
	visit = func(e rules.Expr, depth int) error {
		if depth > 32 {
			return nil
		}
		if e.Metric == "feature" {
			return errors.New("feature metrics have no live source yet")
		}
		if e.Name != "" {
			if calc, ok := p.Calculations[e.Name]; ok {
				return visit(calc, depth+1)
			}
		}
		if e.Input != nil {
			if err := visit(*e.Input, depth+1); err != nil {
				return err
			}
		}
		for _, a := range e.Args {
			if err := visit(a, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range p.Rules {
		if r.On != "bar.close" {
			return fmt.Errorf("rule %s triggers on %s; live runs evaluate closed bars only", r.ID, r.On)
		}
		for _, e := range []*rules.Expr{r.When, r.Reset} {
			if e != nil {
				if err := visit(*e, 0); err != nil {
					return err
				}
			}
		}
		for _, a := range r.Actions {
			for _, e := range []*rules.Expr{a.Price, a.Stop, a.Target, a.Value, a.Weight} {
				if e != nil {
					if err := visit(*e, 0); err != nil {
						return err
					}
				}
			}
			if a.Kind == "target" {
				c := rules.Construct{}
				if a.Construct != nil {
					c = *a.Construct
				}
				gross := c.Gross
				if gross == 0 {
					gross = 1
				}
				if (c.Normalize == "" || c.Normalize == "legs") && gross > c.Net {
					return fmt.Errorf("rule %s builds a short leg; live short selling needs Trading support", r.ID)
				}
			}
			if a.Kind != "enter" {
				continue
			}
			if a.Side == "sell" {
				return fmt.Errorf("rule %s enters short; live short selling needs Trading support", r.ID)
			}
			if a.Stop != nil || a.Target != nil || a.StopPct > 0 || a.TargetPct > 0 || a.TrailDistancePct > 0 {
				return fmt.Errorf("rule %s uses protective stops or targets; live protective orders need Trading support", r.ID)
			}
			if a.OrderType != "" && a.OrderType != "market" {
				return fmt.Errorf("rule %s places %s entries; live entries are market orders", r.ID, a.OrderType)
			}
		}
	}
	return nil
}

// barCloseAt is when a bar's close is known: the session close for US
// equities, otherwise the end of the interval.
func barCloseAt(def *StrategyDefinition, interval string, at time.Time) time.Time {
	return simulationBarClose(def, interval, at)
}

// liveRuleTape loads each symbol's recent completed bars and turns them into
// the same candle and evaluation events a backtest tape carries.
func liveRuleTape(def *StrategyDefinition, now time.Time) ([]sim.Input, time.Time, error) {
	if globalEngine == nil || globalEngine.provider == nil {
		return nil, time.Time{}, errors.New("live strategy market provider not ready")
	}
	interval := def.Cadence
	limit := min(1000, rules.RequiredBaseBars(def.Program)+2)
	type result struct {
		symbol string
		bars   []Bar
		err    error
	}
	results := make(chan result, len(def.Universe))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, symbol := range def.Universe {
		symbol := symbol
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			bars, err := loadLiveStrategyBars(globalEngine.provider, symbol, interval, limit)
			<-sem
			results <- result{symbol, bars, err}
		}()
	}
	wg.Wait()
	close(results)
	var tape []sim.Input
	latest := time.Time{}
	for r := range results {
		if r.err != nil {
			return nil, time.Time{}, fmt.Errorf("strategy history unavailable for %s: %w", r.symbol, r.err)
		}
		for _, bar := range r.bars {
			if bar.T <= 0 || bar.C <= 0 {
				continue
			}
			at := time.Unix(bar.T, 0).UTC()
			closeAt := barCloseAt(def, interval, at)
			if closeAt.After(now) {
				continue // still forming
			}
			o, h, l := bar.O, bar.H, bar.L
			if o <= 0 {
				o = bar.C
			}
			h = math.Max(h, math.Max(o, bar.C))
			if l <= 0 {
				l = math.Min(o, bar.C)
			}
			l = math.Min(l, math.Min(o, bar.C))
			tape = append(tape, sim.Input{ID: fmt.Sprintf("live/%s/%d", r.symbol, bar.T), Type: "market.bar.close", Symbol: r.symbol, Source: "trading", EventTime: at, AvailableAt: closeAt, Data: map[string]float64{"price": bar.C, "open": o, "high": h, "low": l, "volume": bar.V}, Metadata: barCloseMetadata(interval, at, closeAt)})
			if closeAt.After(latest) {
				latest = closeAt
			}
		}
	}
	if len(tape) == 0 {
		return nil, time.Time{}, errors.New("strategy has no completed market bars")
	}
	tape = rules.EvaluationInputs(def.Program, tape)
	sim.SortInputs(tape)
	return tape, latest, nil
}

// liveRuleResult is one live evaluation: target weights for the rebalance
// path, entry and flatten orders, and the program's new checkpoint.
type liveRuleResult struct {
	BarAt      time.Time
	Targets    *StrategyEvaluation
	Orders     []*sim.Order
	Reports    []map[string]any
	Checkpoint []byte
}

// evaluateRulesLive runs the program's latest closed moment with the
// portfolio's cash, positions and marks as account state. Earlier moments are
// replayed for history only and trigger nothing.
func evaluateRulesLive(def *StrategyDefinition, state *TradingState, checkpoint []byte, now time.Time) (*liveRuleResult, error) {
	tape, latest, err := liveRuleTape(def, now)
	if err != nil {
		return nil, err
	}
	return evaluateRulesOnTape(def, state, checkpoint, tape, latest)
}

func evaluateRulesOnTape(def *StrategyDefinition, state *TradingState, checkpoint []byte, tape []sim.Input, latest time.Time) (*liveRuleResult, error) {
	last := tape[len(tape)-1]
	if last.Type != rules.EvaluateInput {
		return nil, errors.New("live tape must end with an evaluation event")
	}
	config := sim.Config{StartingCash: math.Max(state.Equity, 1), Costs: sim.Costs{QtyStep: 0.0001}, SymbolCosts: map[string]sim.Costs{}}
	for symbol, profile := range state.Profiles {
		config.SymbolCosts[symbol] = sim.Costs{QtyStep: math.Max(profile.QtyStep, 1e-8), MinQty: profile.MinQty, MinNotional: profile.MinNotional}
	}
	s := &sim.State{Cash: state.Portfolio.Cash, Positions: map[string]sim.Position{}, Quotes: map[string]sim.Quote{}, Features: map[string]map[string]float64{}, Orders: []*sim.Order{}, Now: last.AvailableAt, Cursor: len(tape), Tape: tape, StrategyState: checkpoint}
	for _, p := range state.Positions {
		symbol := strings.ToUpper(p.Symbol)
		pos := s.Positions[symbol]
		pos.AvgCost = (pos.AvgCost*pos.Qty + p.AvgCost*p.Qty) / math.Max(pos.Qty+p.Qty, 1e-12)
		pos.Qty += p.Qty
		s.Positions[symbol] = pos
	}
	for _, symbol := range def.Universe {
		if mark := state.Mark(symbol); mark != nil && mark.Price > 0 {
			q := sim.Quote{Price: mark.Price, At: last.AvailableAt}
			if mark.BidPrice != nil {
				q.Bid = *mark.BidPrice
			}
			if mark.AskPrice != nil {
				q.Ask = *mark.AskPrice
			}
			s.Quotes[symbol] = q
		}
	}
	// Without a Trading mark, price at the latest closed bar.
	for i := len(tape) - 1; i >= 0; i-- {
		in := tape[i]
		if _, ok := s.Quotes[in.Symbol]; in.Symbol != "" && !ok && in.Type == "market.bar.close" && in.Data["price"] > 0 {
			s.Quotes[in.Symbol] = sim.Quote{Price: in.Data["price"], At: in.AvailableAt}
		}
	}
	for symbol, pos := range s.Positions {
		if _, ok := s.Quotes[symbol]; !ok && pos.AvgCost > 0 {
			s.Quotes[symbol] = sim.Quote{Price: pos.AvgCost, At: last.AvailableAt}
		}
	}
	strategy, err := rules.Strategy(def.Program, config)
	if err != nil {
		return nil, err
	}
	commands, err := strategy(s, last)
	if err != nil {
		if strings.Contains(err.Error(), "shorts are disabled") {
			return nil, fmt.Errorf("%w (live short selling needs Trading support)", err)
		}
		return nil, err
	}
	out := &liveRuleResult{BarAt: latest, Checkpoint: s.StrategyState}
	targetRules := map[string]bool{}
	for _, c := range commands {
		if c.Report == nil || c.Report["status"] != "rebalanced" {
			continue
		}
		rule := fmt.Sprint(c.Report["rule"])
		targetRules[rule] = true
		eval := &StrategyEvaluation{AsOf: latest.Format(time.RFC3339), TargetAllocations: []StrategyAllocation{}}
		weights, _ := c.Report["weights"].(map[string]float64)
		symbols := make([]string, 0, len(weights))
		for symbol := range weights {
			symbols = append(symbols, symbol)
		}
		sort.Strings(symbols)
		for _, symbol := range symbols {
			if w := weights[symbol]; w > 0 {
				eval.TargetAllocations = append(eval.TargetAllocations, StrategyAllocation{Symbol: symbol, Weight: w})
			}
		}
		held, _ := c.Report["held"].(int)
		eval.Decisions = []string{fmt.Sprintf("rule %s: %d long targets, gross %.3f", rule, len(eval.TargetAllocations), c.Report["gross"])}
		if held > 0 {
			eval.Warnings = append(eval.Warnings, fmt.Sprintf("%d symbols without data keep their position", held))
		}
		out.Targets = eval
	}
	for _, c := range commands {
		switch {
		case c.Report != nil:
			out.Reports = append(out.Reports, c.Report)
		case c.Order != nil && !targetRules[c.Order.Tag]:
			out.Orders = append(out.Orders, c.Order)
		}
	}
	return out, nil
}

func dbRuleAssignmentState(db *sql.DB, assignmentID int64) ([]byte, error) {
	var raw string
	err := db.QueryRow(`SELECT state_json FROM strategy_rule_state WHERE assignment_id=?`, assignmentID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

func dbSaveRuleAssignmentState(db *sql.DB, assignmentID int64, state []byte) error {
	_, err := db.Exec(`INSERT INTO strategy_rule_state(assignment_id,state_json) VALUES(?,?) ON CONFLICT(assignment_id) DO UPDATE SET state_json=excluded.state_json,updated_at=CURRENT_TIMESTAMP`, assignmentID, string(state))
	return err
}

// submitRuleOrders places a live evaluation's entry and flatten orders as
// market orders, each checked against the revision the previous one returned.
func submitRuleOrders(app *sdk.AppCtx, state *TradingState, strategy *Strategy, assignment *StrategyAssignment, orders []*sim.Order) ([]*Order, error) {
	pf := state.Portfolio
	created := []*Order{}
	revision := state.Revision
	for _, o := range orders {
		qty := o.Qty
		if o.ReduceOnly {
			held := 0.0
			for _, p := range state.Positions {
				if strings.EqualFold(p.Symbol, o.Symbol) {
					held += p.Qty
				}
			}
			qty = math.Min(qty, held)
		}
		qty = floorStrategyOrderQuantity(qty, state.Profile(o.Symbol).QtyStep)
		if qty <= 0 {
			continue
		}
		out, err := (&App{}).submitStrategyOrder(app, state, map[string]any{
			"_project_id": pf.ProjectID, "portfolio_id": pf.ID, "symbol": o.Symbol,
			"side": o.Side, "type": "market", "qty": qty, "tif": "day",
			"rationale":   fmt.Sprintf("Strategy %s assignment #%d rule %s", strategy.Name, assignment.ID, o.Tag),
			"strategy_id": strategy.ID, "assignment_id": assignment.ID, "if_revision": revision,
		})
		if err != nil {
			return created, err
		}
		if fmt.Sprint(out["status"]) == "rejected" {
			return created, fmt.Errorf("strategy order rejected: %s", fmt.Sprint(out["detail"]))
		}
		orderID := strings.TrimSpace(fmt.Sprint(out["order_id"]))
		if orderID == "" || orderID == "<nil>" {
			return created, errors.New("strategy order returned no order_id")
		}
		created = append(created, &Order{ID: orderID, PortfolioID: pf.ID, Symbol: o.Symbol, Side: o.Side, Qty: qty, Status: fmt.Sprint(out["status"])})
		next, ok := out["revision"].(float64)
		if !ok {
			break
		}
		revision = int64(next)
	}
	return created, nil
}

// strategyInterval is the bar interval a strategy backtests on: its program's
// base timeframe.
func strategyInterval(def *StrategyDefinition, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" && requested != def.Cadence {
		return "", fmt.Errorf("strategy runs on its program timeframe %s, not %s", def.Cadence, requested)
	}
	return normalizeBacktestInterval(def.Cadence)
}

// hasMarketInputs reports whether supplied inputs carry their own market tape.
func hasMarketInputs(inputs []sim.Input) bool {
	for _, in := range inputs {
		if strings.HasPrefix(in.Type, "market.") {
			return true
		}
	}
	return false
}

// strategyHistoryInterval is the bar interval a strategy's program reads.
func strategyHistoryInterval(def *StrategyDefinition) string {
	if def == nil || def.Cadence == "" {
		return "1d"
	}
	return def.Cadence
}
