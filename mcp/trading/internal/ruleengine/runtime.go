package ruleengine

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

type checkpointCache struct {
	mu      sync.Mutex
	runtime *Runtime
	digest  [32]byte
}

func (cache *checkpointCache) restore(p *Program, s *sim.State, limits map[string]int, location *time.Location) (*Runtime, error) {
	if cache.runtime != nil && sha256.Sum256(s.StrategyState) == cache.digest {
		return cache.runtime, nil
	}
	r := newRuntime(p)
	r.limits, r.location, r.cacheEnabled = limits, location, true
	if len(s.StrategyState) > 0 {
		if err := json.Unmarshal(s.StrategyState, r); err != nil {
			return nil, err
		}
		if r.Bars == nil || r.Aggregates == nil || r.Windows == nil || r.Variables == nil || r.Fired == nil {
			return nil, errors.New("invalid rule checkpoint")
		}
	}
	return r, nil
}
func (cache *checkpointCache) save(r *Runtime, s *sim.State) error {
	raw, err := r.marshalCheckpoint()
	if err != nil {
		return err
	}
	s.StrategyState = raw
	cache.runtime, cache.digest = r, sha256.Sum256(raw)
	return nil
}

func Strategy(p *Program, config sim.Config) (sim.Strategy, error) {
	if err := Validate(p); err != nil {
		return nil, err
	}
	if err := ValidateExecution(p, config); err != nil {
		return nil, err
	}
	limits := historyLimits(p)
	location, _ := time.LoadLocation(p.Timezone)
	cache := &checkpointCache{}
	return func(s *sim.State, in sim.Input) (retCommands []sim.Command, retErr error) {
		// Candle-only signals and resets cannot change between candle events.
		// Skip unrelated quotes without decoding and reserializing the checkpoint.
		if in.Type != "market.bar.close" {
			relevant := false
			for _, rule := range p.Rules {
				if rule.On == "any" || matches(p, rule, in, nil) || rule.Reset != nil && !historicalExpr(p, *rule.Reset, 0) {
					relevant = true
					break
				}
			}
			if !relevant {
				return nil, nil
			}
		}
		cache.mu.Lock()
		defer func() {
			if retErr != nil {
				cache.runtime = nil
			}
			cache.mu.Unlock()
		}()
		r, err := cache.restore(p, s, limits, location)
		if err != nil {
			return nil, err
		}
		closed, err := r.observe(p, in)
		if err != nil {
			return nil, err
		}
		c := context{program: p, runtime: r, state: s, config: config}
		var commands []sim.Command
		plannedEntry := false
		for _, rule := range p.Rules {
			// Reset is independent of entry trigger and open-position state.
			if rule.Reset != nil {
				v, e := c.eval(*rule.Reset, 0)
				if e == nil && v != 0 {
					delete(r.Fired, rule.ID)
				} else if e != nil && !errors.Is(e, ErrUnavailable) {
					return nil, e
				}
			}
			if !matches(p, rule, in, closed) {
				continue
			}
			frame := rule.Timeframe
			if frame == "" {
				frame = p.Timeframe
			}
			key := in.AvailableAt.Format("2006-01-02T15:04:05.999999999Z07:00")
			switch rule.Repeat {
			case "once_per_day":
				loc := r.timezone(p)
				key = s.Now.In(loc).Format("2006-01-02")
			case "until_reset":
				key = "latched"
			case "always":
				key = ""
			default:
				if h := r.Bars[frame]; len(h) > 0 {
					key = h[len(h)-1].At.String()
				}
			}
			if old, ok := r.Fired[rule.ID]; ok && key != "" && old == key {
				continue
			}
			if rule.When != nil {
				v, e := c.eval(*rule.When, 0)
				if errors.Is(e, ErrUnavailable) {
					commands = append(commands, sim.Command{Report: map[string]any{"rule": rule.ID, "status": "waiting_for_data"}})
					continue
				}
				if e != nil {
					return nil, e
				}
				if v == 0 {
					continue
				}
			}
			var batch []sim.Command
			vars := map[string]float64{}
			for name, v := range r.Variables {
				vars[name] = v
			}
			successful := true
			for _, action := range rule.Actions {
				var result []sim.Command
				var e error
				if action.Kind == "enter" && plannedEntry {
					e = ErrUnavailable
				} else {
					result, e = c.action(rule.ID, action)
				}
				if errors.Is(e, ErrUnavailable) {
					successful = false
					r.Variables = vars
					commands = append(commands, sim.Command{Report: map[string]any{"rule": rule.ID, "status": "waiting_for_data"}})
					break
				}
				if e != nil {
					return nil, fmt.Errorf("rule %s: %w", rule.ID, e)
				}
				batch = append(batch, result...)
			}
			if successful {
				for _, cmd := range batch {
					if cmd.Order != nil && !cmd.Order.ReduceOnly {
						plannedEntry = true
					}
				}
				r.Fired[rule.ID] = key
				commands = append(commands, batch...)
				commands = append(commands, sim.Command{Report: map[string]any{"rule": rule.ID, "status": "triggered", "event": in.ID}})
			}
		}
		return commands, cache.save(r, s)
	}, nil
}

func matches(p *Program, r Rule, in sim.Input, closed map[string]bool) bool {
	switch r.On {
	case "bar.close":
		frame := r.Timeframe
		if frame == "" {
			frame = p.Timeframe
		}
		return in.Symbol == p.Symbol && closed[frame]
	case "quote":
		return in.Symbol == p.Symbol && in.Type == "market.quote"
	case "fill":
		return in.Symbol == p.Symbol && in.Type == "execution.fill"
	case "clock":
		return in.Type == "clock" && (r.Schedule == "" || in.Metadata["schedule"] == r.Schedule)
	case "any":
		return true
	}
	return false
}

func (c context) action(rule string, a Action) ([]sim.Command, error) {
	s, p := c.state, c.program
	switch a.Kind {
	case "set":
		v, e := c.eval(*a.Value, 0)
		if e != nil {
			return nil, e
		}
		c.runtime.Variables[a.Name] = v
		return nil, nil
	case "cancel", "flatten":
		var out []sim.Command
		for _, o := range s.Orders {
			if a.Kind == "flatten" && o.ReduceOnly && o.ParentID == "" {
				continue
			}
			if o.Symbol == p.Symbol && (a.Group == "" || o.OCOGroup == a.Group) && (o.Status == "working" || o.Status == "submitted" || o.Status == "partially_filled") {
				out = append(out, sim.Command{CancelID: o.ID})
			}
		}
		if a.Kind == "flatten" {
			for _, o := range s.Orders {
				if o.Symbol == p.Symbol && o.ReduceOnly && o.ParentID == "" && (o.Status == "working" || o.Status == "submitted" || o.Status == "partially_filled") {
					return out, nil
				}
			}
			qty := s.Positions[p.Symbol].Qty
			if math.Abs(qty) > 1e-12 {
				side := "sell"
				if qty < 0 {
					side = "buy"
				}
				out = append(out, sim.Command{Order: &sim.Order{Symbol: p.Symbol, Side: side, Type: "market", Qty: math.Abs(qty), ReduceOnly: true, TIF: "gtc", Tag: rule}})
			}
		}
		return out, nil
	case "enter":
		// Entry groups can contain opposing pending orders, but never silently
		// pyramid a held position or replace a different pending entry group.
		if math.Abs(s.Positions[p.Symbol].Qty) > 1e-12 {
			return nil, ErrUnavailable
		}
		for _, o := range s.Orders {
			if o.Symbol == p.Symbol && !o.ReduceOnly && (o.Status == "working" || o.Status == "submitted" || o.Status == "partially_filled") {
				return nil, ErrUnavailable
			}
		}
		price := s.Quotes[p.Symbol].Price
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
		if contract, ok := c.config.Contracts[p.Symbol]; ok {
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
		if v, ok := c.config.SymbolCosts[p.Symbol]; ok {
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
		o := &sim.Order{Symbol: p.Symbol, Side: a.Side, Type: typ, Qty: qty, TIF: "gtc", Tag: rule, OCOGroup: a.Group, Protection: protection}
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
		case "return", "volatility":
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
			for _, e := range []*Expr{a.Price, a.Stop, a.Target, a.Value} {
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
// or emitting orders. This is also used to validate complete imported tapes.
func ObserveOnly(p *Program) (sim.Strategy, error) {
	if err := Validate(p); err != nil {
		return nil, err
	}
	limits := historyLimits(p)
	location, _ := time.LoadLocation(p.Timezone)
	cache := &checkpointCache{}
	return func(s *sim.State, in sim.Input) (retCommands []sim.Command, retErr error) {
		if in.Type != "market.bar.close" {
			return nil, nil
		}
		cache.mu.Lock()
		defer func() {
			if retErr != nil {
				cache.runtime = nil
			}
			cache.mu.Unlock()
		}()
		r, err := cache.restore(p, s, limits, location)
		if err != nil {
			return nil, err
		}
		if _, err := r.observe(p, in); err != nil {
			return nil, err
		}
		return nil, cache.save(r, s)
	}, nil
}

// ValidateTape rejects incomplete or externally controlled inputs before execution.
func ValidateTape(program *Program, inputs []sim.Input) error {
	if len(inputs) == 0 {
		return errors.New("rule backtest requires a complete event tape")
	}
	if err := Validate(program); err != nil {
		return err
	}
	runtime := newRuntime(program)
	runtime.limits = historyLimits(program)
	quotes, bars := 0, 0
	for _, in := range inputs {
		if in.Type == "order.intent" || in.Type == "order.cancel_intent" {
			return errors.New("rule tapes cannot contain external order intents")
		}
		if len(in.Type) > 7 && in.Type[:7] == "market." {
			if in.Symbol != program.Symbol || in.Source == "" {
				return errors.New("rule market events require the program symbol and an explicit data source")
			}
			if in.Type == "market.quote" {
				quotes++
			}
			if in.Type == "market.bar.close" {
				bars++
			}
		}
		if _, err := runtime.observe(program, in); err != nil {
			return fmt.Errorf("input %s: %w", in.ID, err)
		}
	}
	if bars == 0 || quotes == 0 {
		return errors.New("rule backtests require OHLC candles and executable quotes")
	}
	return nil
}
