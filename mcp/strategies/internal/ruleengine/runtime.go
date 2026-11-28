package ruleengine

import (
	"errors"
	"fmt"
	"math"
	"sort"

	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
)

// Strategy compiles a program into a simulator strategy. The same evaluator
// runs live through Evaluate.
func Strategy(p *Program, config sim.Config) (sim.Strategy, error) {
	if err := Validate(p); err != nil {
		return nil, err
	}
	if err := ValidateExecution(p, config); err != nil {
		return nil, err
	}
	return multiStrategy(p, config, false), nil
}

// actionSymbol is the symbol an order action trades: its own symbol, else the
// rule's current symbol (the program symbol in trading-rules/1).
func (c context) actionSymbol(a Action) string {
	if a.Symbol != "" {
		return a.Symbol
	}
	if c.symbol == "" && len(c.program.Symbols) == 1 {
		return c.program.Symbols[0]
	}
	return c.symbol
}

func (c context) action(rule string, a Action) ([]sim.Command, error) {
	s := c.state
	symbol := c.actionSymbol(a)
	switch a.Kind {
	case "set":
		v, e := c.eval(*a.Value, 0)
		if e != nil {
			return nil, e
		}
		c.runtime.Variables[a.Name] = v
		for k := range c.memo {
			delete(c.memo, k)
		}
		return nil, nil
	case "target":
		return c.target(rule, a)
	case "cancel", "flatten":
		if symbol == "" {
			// Outside for_each a symbol-less flatten or cancel covers the book.
			var out []sim.Command
			for _, each := range ProgramSymbols(c.program) {
				x := c
				x.symbol = each
				batch, err := x.action(rule, a)
				if err != nil {
					return nil, err
				}
				out = append(out, batch...)
			}
			return out, nil
		}
		var out []sim.Command
		for _, o := range s.Orders {
			if a.Kind == "flatten" && o.ReduceOnly && o.ParentID == "" {
				continue
			}
			if o.Symbol == symbol && (a.Group == "" || o.OCOGroup == a.Group) && (o.Status == "working" || o.Status == "submitted" || o.Status == "partially_filled") {
				out = append(out, sim.Command{CancelID: o.ID})
			}
		}
		if a.Kind == "flatten" {
			for _, o := range s.Orders {
				if o.Symbol == symbol && o.ReduceOnly && o.ParentID == "" && (o.Status == "working" || o.Status == "submitted" || o.Status == "partially_filled") {
					return out, nil
				}
			}
			qty := s.Positions[symbol].Qty
			if math.Abs(qty) > 1e-12 {
				side := "sell"
				if qty < 0 {
					side = "buy"
				}
				out = append(out, sim.Command{Order: &sim.Order{Symbol: symbol, Side: side, Type: "market", Qty: math.Abs(qty), ReduceOnly: true, TIF: "gtc", Tag: rule}})
			}
		}
		return out, nil
	case "enter":
		// Entry groups can contain opposing pending orders, but never silently
		// pyramid a held position or replace a different pending entry group.
		if math.Abs(s.Positions[symbol].Qty) > 1e-12 {
			return nil, ErrUnavailable
		}
		for _, o := range s.Orders {
			if o.Symbol == symbol && !o.ReduceOnly && (o.Status == "working" || o.Status == "submitted" || o.Status == "partially_filled") {
				return nil, ErrUnavailable
			}
		}
		price := s.Quotes[symbol].Price
		if a.Price != nil {
			v, e := c.eval(*a.Price, 0)
			if e != nil {
				return nil, e
			}
			price = v
		}
		if price <= 0 || !finite(price) {
			return nil, ErrUnavailable
		}
		protection := &sim.Protection{StopPct: a.StopPct, TargetPct: a.TargetPct, TrailActivationPct: a.TrailActivationPct, TrailDistancePct: a.TrailDistancePct}
		direction := 1.0
		if a.Side == "sell" {
			direction = -1
		}
		stop := price * (1 - direction*a.StopPct)
		if a.Stop != nil {
			v, e := c.eval(*a.Stop, 0)
			if e != nil {
				return nil, e
			}
			protection.StopPrice = v
			stop = v
		}
		if a.Target != nil {
			v, e := c.eval(*a.Target, 0)
			if e != nil {
				return nil, e
			}
			protection.TargetPrice = v
		}
		if (a.Stop != nil || a.StopPct > 0) && (stop <= 0 || direction*(price-stop) <= 0) {
			return nil, errors.New("stop must be on the losing side of entry")
		}
		if a.Target != nil && (protection.TargetPrice <= 0 || direction*(protection.TargetPrice-price) <= 0) {
			return nil, errors.New("target must be on the profitable side of entry")
		}
		mult := 1.0
		if contract, ok := c.config.Contracts[symbol]; ok {
			mult = contract.Multiplier * contract.CurrencyRate
		}
		qty := a.Sizing.Amount
		switch a.Sizing.Mode {
		case "fixed_risk":
			qty /= math.Abs(price-stop) * mult
		case "notional":
			qty /= price * mult
		case "equity_pct":
			qty = sim.Equity(c.config, s) * a.Sizing.Amount / (price * mult)
		}
		costs := c.config.Costs
		if v, ok := c.config.SymbolCosts[symbol]; ok {
			costs = v
		}
		if costs.QtyStep > 0 {
			qty = math.Floor(qty/costs.QtyStep+1e-10) * costs.QtyStep
		}
		if !finite(qty) || qty <= 0 || qty < costs.MinQty || qty*price*mult < costs.MinNotional {
			return nil, errors.New("sizing is below executable minimum")
		}
		typ := a.OrderType
		if typ == "" {
			typ = "market"
		}
		o := &sim.Order{Symbol: symbol, Side: a.Side, Type: typ, Qty: qty, TIF: "gtc", Tag: rule, OCOGroup: a.Group, Protection: protection}
		if typ == "stop" {
			o.StopPrice = price
		}
		if typ == "limit" {
			o.LimitPrice = price
		}
		return []sim.Command{{Order: o}}, nil
	}
	return nil, errors.New("unknown action")
}

// Warmup exposes exact lower bounds for capture. Bounded Wilder/EMA seeds are
// deliberately consistent across historical and automated evaluation.
func RequiredBaseBars(p *Program) int {
	base, _ := duration(p.Timeframe)
	var need func(Expr, int) int
	need = func(e Expr, depth int) int {
		if depth > 24 {
			return 1000001
		}
		if e.Name != "" {
			if calc, ok := p.Calculations[e.Name]; ok {
				return need(calc, depth+1)
			}
		}
		frame := e.Timeframe
		if frame == "" {
			frame = p.Timeframe
		}
		d, _ := duration(frame)
		n := e.Period
		if n < 1 {
			n = 1
		}
		switch e.Metric {
		case "ema":
			n *= 5
		case "atr", "rsi":
			n = 5*n + 1
		case "return", "volatility", "correlation", "beta":
			n++
		}
		total := (n + e.Offset + 1) * int(d/base)
		if e.Input != nil {
			total = need(*e.Input, depth+1) + (n+e.Offset)*int(d/base)
		}
		for _, a := range e.Args {
			total = max(total, need(a, depth+1))
		}
		if e.Op == "crosses_above" || e.Op == "crosses_below" {
			total++
		}
		return total
	}
	total := 1
	for _, e := range p.Calculations {
		total = max(total, need(e, 0))
	}
	for _, r := range p.Rules {
		for _, e := range []*Expr{r.When, r.Reset} {
			if e != nil {
				total = max(total, need(*e, 0))
			}
		}
		for _, a := range r.Actions {
			for _, e := range []*Expr{a.Price, a.Stop, a.Target, a.Value, a.Weight} {
				if e != nil {
					total = max(total, need(*e, 0))
				}
			}
		}
	}
	return total
}

func OrderedCalculations(p *Program) []string {
	out := make([]string, 0, len(p.Calculations))
	for n := range p.Calculations {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ObserveOnly warms calculations without consuming triggers, setting variables,
// or emitting orders.
func ObserveOnly(p *Program) (sim.Strategy, error) {
	if err := Validate(p); err != nil {
		return nil, err
	}
	return multiStrategy(p, sim.Config{}, true), nil
}

// ValidateTape rejects incomplete or externally controlled inputs before execution.
func ValidateTape(program *Program, inputs []sim.Input) error {
	if len(inputs) == 0 {
		return errors.New("rule backtest requires a complete event tape")
	}
	if err := Validate(program); err != nil {
		return err
	}
	limits := historyLimits(program)
	series := map[string]*Runtime{}
	allowed := map[string]bool{}
	for _, symbol := range ProgramSymbols(program) {
		series[symbol] = newRuntime(program)
		series[symbol].limits = limits
		allowed[symbol] = true
	}
	quotes, bars := 0, 0
	for _, in := range inputs {
		if in.Type == "order.intent" || in.Type == "order.cancel_intent" {
			return errors.New("rule tapes cannot contain external order intents")
		}
		if len(in.Type) > 7 && in.Type[:7] == "market." {
			if !allowed[in.Symbol] || in.Source == "" {
				return errors.New("rule market events require the program symbol and an explicit data source")
			}
			if in.Type == "market.quote" {
				quotes++
			}
			if in.Type == "market.bar.close" {
				bars++
			}
		}
		if _, err := series[in.Symbol].observe(program, in.Symbol, in); in.Type == "market.bar.close" && err != nil {
			return fmt.Errorf("input %s: %w", in.ID, err)
		}
	}
	if bars == 0 || quotes == 0 {
		return errors.New("rule backtests require OHLC candles and executable quotes")
	}
	return nil
}
