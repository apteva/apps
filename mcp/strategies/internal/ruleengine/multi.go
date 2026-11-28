package ruleengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
)

// EvaluateInput is the synthetic event a rule tape carries after
// every moment's closing candles. Rules triggered by bar.close run on it, once
// all symbols' candles for that moment are known and before its quotes.
const EvaluateInput = "rules.evaluate"

// EvaluationInputs adds one EvaluateInput per distinct closing-candle time of
// the program's symbols.
func EvaluationInputs(p *Program, inputs []sim.Input) []sim.Input {
	allowed := map[string]bool{}
	for _, symbol := range p.Symbols {
		allowed[symbol] = true
	}
	seen := map[int64]bool{}
	var times []time.Time
	for _, in := range inputs {
		if in.Type == EvaluateInput {
			return inputs
		}
		if in.Type == "market.bar.close" && allowed[in.Symbol] && !seen[in.AvailableAt.UnixNano()] {
			seen[in.AvailableAt.UnixNano()] = true
			times = append(times, in.AvailableAt.UTC())
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	out := append([]sim.Input(nil), inputs...)
	for _, at := range times {
		out = append(out, sim.Input{ID: "rules/evaluate/" + at.Format(time.RFC3339Nano), Type: EvaluateInput, Source: "rules", EventTime: at, AvailableAt: at, Data: map[string]float64{}})
	}
	return out
}

// multiCheckpoint is all a rule run serializes. Candles, aggregates
// and session windows are a pure function of the tape already processed, so
// they are rebuilt from State.Warmup and State.Tape on resume instead of being
// written into every step's checkpoint.
type multiCheckpoint struct {
	Warmup    int                `json:"warmup,omitempty"`
	Variables map[string]float64 `json:"variables"`
	Fired     map[string]string  `json:"fired"`
}

type multiRuntime struct {
	root   *Runtime
	series map[string]*Runtime
	closed map[string]bool // symbol/frame closed since the last evaluation
	next   int             // tape inputs observed
	warmup int
	saved  []byte
}

type multiCache struct {
	mu sync.Mutex
	rt *multiRuntime
}

func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || &a[0] == &b[0]
}

func (m *multiRuntime) observe(p *Program, in sim.Input) error {
	switch in.Type {
	case "market.bar.close":
		r := m.series[in.Symbol]
		if r == nil {
			return nil
		}
		closed, err := r.observe(p, in.Symbol, in)
		if err != nil {
			return err
		}
		for frame := range closed {
			m.closed[in.Symbol+"/"+frame] = true
		}
	case EvaluateInput:
		m.closed = map[string]bool{}
	}
	return nil
}

func newMultiRuntime(p *Program, limits map[string]int, location *time.Location) *multiRuntime {
	m := &multiRuntime{root: newRuntime(p), series: map[string]*Runtime{}, closed: map[string]bool{}}
	m.root.location = location
	for _, symbol := range p.Symbols {
		r := &Runtime{Bars: map[string][]Candle{}, Aggregates: map[string]aggregate{}, Windows: map[string]windowState{}, limits: limits, location: location}
		m.series[symbol] = r
	}
	return m
}

// restore returns the runtime as of every tape input before the current one,
// and whether the current input is itself the next tape input.
func (cache *multiCache) restore(p *Program, s *sim.State, in sim.Input, limits map[string]int, location *time.Location) (*multiRuntime, bool, error) {
	prefix := s.Cursor
	fromTape := prefix > 0 && prefix <= len(s.Tape) && s.Tape[prefix-1].ID == in.ID
	if fromTape {
		prefix--
	}
	m := cache.rt
	if m == nil || !sameBytes(m.saved, s.StrategyState) || m.next > prefix {
		m = newMultiRuntime(p, limits, location)
		if len(s.StrategyState) > 0 {
			var cp multiCheckpoint
			if err := json.Unmarshal(s.StrategyState, &cp); err != nil {
				return nil, false, err
			}
			if cp.Variables == nil || cp.Fired == nil {
				return nil, false, errors.New("invalid rule checkpoint")
			}
			m.root.Variables, m.root.Fired, m.warmup = cp.Variables, cp.Fired, cp.Warmup
		}
		if m.warmup > 0 {
			if len(s.Warmup) != m.warmup {
				return nil, false, errors.New("rule checkpoint requires its warmup inputs")
			}
			warm := append([]sim.Input(nil), s.Warmup...)
			sim.SortInputs(warm)
			for _, w := range warm {
				if err := m.observe(p, w); err != nil {
					return nil, false, fmt.Errorf("warmup input %s: %w", w.ID, err)
				}
			}
			m.closed = map[string]bool{}
		}
		m.saved = s.StrategyState
	}
	if prefix > len(s.Tape) {
		return nil, false, errors.New("rule checkpoint requires the input tape")
	}
	for ; m.next < prefix; m.next++ {
		if err := m.observe(p, s.Tape[m.next]); err != nil {
			return nil, false, fmt.Errorf("input %s: %w", s.Tape[m.next].ID, err)
		}
	}
	return m, fromTape, nil
}

func (cache *multiCache) save(m *multiRuntime, s *sim.State) error {
	raw, err := json.Marshal(multiCheckpoint{Warmup: m.warmup, Variables: m.root.Variables, Fired: m.root.Fired})
	if err != nil {
		return err
	}
	s.StrategyState = raw
	m.saved = raw
	cache.rt = m
	return nil
}

// multiStrategy runs a program. observeOnly warms candles for
// a validation warmup without triggering rules.
func multiStrategy(p *Program, config sim.Config, observeOnly bool) sim.Strategy {
	limits := historyLimits(p)
	location, _ := time.LoadLocation(p.Timezone)
	allowed := map[string]bool{}
	for _, symbol := range p.Symbols {
		allowed[symbol] = true
	}
	symbols := ProgramSymbols(p)
	cache := &multiCache{}
	return func(s *sim.State, in sim.Input) (retCommands []sim.Command, retErr error) {
		bar := in.Type == "market.bar.close" && allowed[in.Symbol]
		evaluate := in.Type == EvaluateInput
		if observeOnly && !bar {
			return nil, nil
		}
		if !bar && !evaluate {
			relevant := false
			for _, rule := range p.Rules {
				if rule.On != "bar.close" && len(matchMulti(p, rule, in, nil, symbols)) > 0 || rule.Reset != nil && !historicalExpr(p, *rule.Reset, 0) {
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
				cache.rt = nil
			}
			cache.mu.Unlock()
		}()
		m, fromTape, err := cache.restore(p, s, in, limits, location)
		if err != nil {
			return nil, err
		}
		closed := m.closed
		if err := m.observe(p, in); err != nil {
			return nil, err
		}
		if fromTape {
			m.next++
		}
		if observeOnly {
			// The warm engine's whole tape becomes the main run's State.Warmup.
			m.warmup = len(s.Tape)
			return nil, cache.save(m, s)
		}
		if bar {
			// Candles change derived state only; the checkpoint stays as is.
			cache.rt = m
			return nil, nil
		}
		if !evaluate {
			closed = nil
		}
		c := context{program: p, runtime: m.root, series: m.series, state: s, config: config, memo: map[*Expr]map[string]float64{}}
		commands, err := c.runRules(in, closed, symbols)
		if err != nil {
			return nil, err
		}
		return commands, cache.save(m, s)
	}
}

// matchMulti lists the symbols a rule runs for on this event. A rule without
// for_each runs once, reported as the single empty symbol.
func matchMulti(p *Program, r Rule, in sim.Input, closed map[string]bool, symbols []string) []string {
	var out []string
	inSet := false
	for _, symbol := range symbols {
		if symbol == in.Symbol {
			inSet = true
		}
	}
	switch r.On {
	case "bar.close":
		if in.Type != EvaluateInput {
			return nil
		}
		frame := r.Timeframe
		if frame == "" {
			frame = p.Timeframe
		}
		for _, symbol := range symbols {
			if closed[symbol+"/"+frame] {
				out = append(out, symbol)
			}
		}
	case "quote", "fill":
		kind := "market.quote"
		if r.On == "fill" {
			kind = "execution.fill"
		}
		if in.Type == kind && inSet {
			out = []string{in.Symbol}
		}
	case "clock":
		if in.Type == "clock" && (r.Schedule == "" || in.Metadata["schedule"] == r.Schedule) {
			out = symbols
		}
	case "any":
		out = symbols
		if inSet {
			out = []string{in.Symbol}
		}
	}
	if !r.ForEach && len(out) > 0 {
		return []string{""}
	}
	return out
}

func (c context) runRules(in sim.Input, closed map[string]bool, symbols []string) ([]sim.Command, error) {
	p, r, s := c.program, c.runtime, c.state
	var commands []sim.Command
	plannedEntry := map[string]bool{}
	for _, rule := range p.Rules {
		matched := matchMulti(p, rule, in, closed, symbols)
		if rule.Reset != nil {
			targets := []string{""}
			if rule.ForEach {
				targets = nil
				for _, symbol := range symbols {
					if _, ok := r.Fired[rule.ID+"/"+symbol]; ok {
						targets = append(targets, symbol)
					}
				}
			}
			for _, symbol := range targets {
				x := c
				x.symbol = symbol
				v, e := x.eval(*rule.Reset, 0)
				if e == nil && v != 0 {
					delete(r.Fired, firedKey(rule, symbol))
				} else if e != nil && !errors.Is(e, ErrUnavailable) {
					return nil, e
				}
			}
		}
		triggered, waiting := 0, 0
		for _, symbol := range matched {
			x := c
			x.symbol = symbol
			key := in.AvailableAt.Format("2006-01-02T15:04:05.999999999Z07:00")
			switch rule.Repeat {
			case "once_per_day":
				key = s.Now.In(r.timezone(p)).Format("2006-01-02")
			case "until_reset":
				key = "latched"
			case "always":
				key = ""
			default:
				frame := rule.Timeframe
				if frame == "" {
					frame = p.Timeframe
				}
				if series := c.series[symbol]; series != nil {
					if h := series.Bars[frame]; len(h) > 0 {
						key = h[len(h)-1].At.String()
					}
				}
			}
			fired := firedKey(rule, symbol)
			if old, ok := r.Fired[fired]; ok && key != "" && old == key {
				continue
			}
			if rule.When != nil {
				v, e := x.eval(*rule.When, 0)
				if errors.Is(e, ErrUnavailable) {
					waiting++
					continue
				}
				if e != nil {
					return nil, fmt.Errorf("rule %s %s: %w", rule.ID, symbol, e)
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
				if action.Kind == "enter" && plannedEntry[x.actionSymbol(action)] {
					e = ErrUnavailable
				} else {
					result, e = x.action(rule.ID, action)
				}
				if errors.Is(e, ErrUnavailable) {
					successful = false
					for name, v := range vars {
						r.Variables[name] = v
					}
					waiting++
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
						plannedEntry[cmd.Order.Symbol] = true
					}
				}
				r.Fired[fired] = key
				commands = append(commands, batch...)
				triggered++
			}
		}
		report := func(status string, n int) {
			fields := map[string]any{"rule": rule.ID, "status": status}
			if status == "triggered" {
				fields["event"] = in.ID
			}
			if rule.ForEach {
				fields["symbols"] = n
			}
			commands = append(commands, sim.Command{Report: fields})
		}
		if waiting > 0 {
			report("waiting_for_data", waiting)
		}
		if triggered > 0 {
			report("triggered", triggered)
		}
	}
	return commands, nil
}

func firedKey(rule Rule, symbol string) string {
	if symbol == "" {
		return rule.ID
	}
	return rule.ID + "/" + symbol
}

// target turns per-symbol signed scores into orders that move the book to the
// constructed weights. Symbols whose score or quote is unavailable keep their
// position this time.
func (c context) target(rule string, a Action) ([]sim.Command, error) {
	s, cfg := c.state, c.config
	construct := Construct{}
	if a.Construct != nil {
		construct = *a.Construct
	}
	if construct.Normalize == "" {
		construct.Normalize = "legs"
	}
	if construct.Gross == 0 {
		construct.Gross = 1
	}
	if construct.MaxGross == 0 {
		construct.MaxGross = 2
	}
	symbols := ProgramSymbols(c.program)
	weights := map[string]float64{}
	var used []string
	held, unshortable := 0, 0
	for _, symbol := range symbols {
		x := c
		x.symbol = symbol
		w, err := x.eval(*a.Weight, 0)
		if errors.Is(err, ErrUnavailable) || s.Quotes[symbol].Price <= 0 {
			held++
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("target weight for %s: %w", symbol, err)
		}
		if w < 0 && !Shortable(cfg, symbol) {
			if cfg.Shorts == nil || !cfg.Shorts.Enabled {
				return nil, fmt.Errorf("target gave %s a short weight but simulator shorts are disabled", symbol)
			}
			w = 0
			unshortable++
		}
		weights[symbol] = w
		used = append(used, symbol)
	}
	long, short := 0.0, 0.0
	for _, w := range weights {
		if w > 0 {
			long += w
		} else {
			short -= w
		}
	}
	switch construct.Normalize {
	case "legs":
		longBudget, shortBudget := (construct.Gross+construct.Net)/2, (construct.Gross-construct.Net)/2
		for symbol, w := range weights {
			if w > 0 {
				weights[symbol] = w / long * longBudget
			} else if w < 0 {
				weights[symbol] = w / short * shortBudget
			}
		}
	case "gross":
		if long+short > 0 {
			for symbol, w := range weights {
				weights[symbol] = w / (long + short) * construct.Gross
			}
		}
	}
	gross := 0.0
	for symbol, w := range weights {
		if construct.MaxPosition > 0 && math.Abs(w) > construct.MaxPosition {
			w = math.Copysign(construct.MaxPosition, w)
			weights[symbol] = w
		}
		gross += math.Abs(w)
	}
	if gross > construct.MaxGross {
		for symbol, w := range weights {
			weights[symbol] = w * construct.MaxGross / gross
		}
	}
	equity := sim.Equity(cfg, s)
	var commands, cancels, closes, sells, buys []sim.Command
	// This rule's orders still working from earlier rebalances, by symbol.
	working := map[string][]*sim.Order{}
	pending := map[string]float64{}
	for _, o := range s.Orders {
		if o.Tag == rule && (o.Status == "working" || o.Status == "submitted" || o.Status == "partially_filled") {
			working[o.Symbol] = append(working[o.Symbol], o)
			left := o.Qty - o.FilledQty
			if o.Side == "sell" {
				left = -left
			}
			pending[o.Symbol] += left
		}
	}
	net, finalGross, longs, shorts := 0.0, 0.0, 0, 0
	book := map[string]float64{}
	for _, symbol := range used {
		w := weights[symbol]
		if w != 0 {
			book[symbol] = w
		}
		net += w
		finalGross += math.Abs(w)
		if w > 0 {
			longs++
		} else if w < 0 {
			shorts++
		}
		price := s.Quotes[symbol].Price
		mult := 1.0
		if contract, ok := cfg.Contracts[symbol]; ok {
			mult = contract.Multiplier * contract.CurrencyRate
		}
		costs := cfg.Costs
		if v, ok := cfg.SymbolCosts[symbol]; ok {
			costs = v
		}
		round := func(q float64) float64 {
			if costs.QtyStep > 0 {
				return math.Floor(q/costs.QtyStep+1e-10) * costs.QtyStep
			}
			return q
		}
		want := 0.0
		if equity > 0 {
			want = equity * w / (price * mult)
		}
		order := func(side string, qty float64, reduce bool) *sim.Command {
			qty = round(qty)
			if qty <= 1e-12 || qty < costs.MinQty || qty*price*mult < costs.MinNotional {
				return nil
			}
			return &sim.Command{Order: &sim.Order{Symbol: symbol, Side: side, Type: "market", Qty: qty, ReduceOnly: reduce, TIF: "gtc", Tag: rule}}
		}
		side := func(q float64) string {
			if q < 0 {
				return "sell"
			}
			return "buy"
		}
		// plan returns the orders that move a position of current to want.
		plan := func(current float64) (c, sl, b []sim.Command) {
			if current != 0 && (want == 0 || math.Signbit(want) != math.Signbit(current)) {
				// Crossing or reaching zero: close, then open the other side.
				if cmd := order(side(-current), math.Abs(current), true); cmd != nil {
					c = append(c, *cmd)
				}
				if want != 0 {
					if cmd := order(side(want), math.Abs(want), false); cmd != nil {
						if want < 0 {
							sl = append(sl, *cmd)
						} else {
							b = append(b, *cmd)
						}
					}
				}
				return
			}
			delta := want - current
			if math.Abs(delta)*price*mult < construct.MinTradePct/100*equity {
				return
			}
			reduce := math.Abs(want) < math.Abs(current)
			cmd := order(side(delta), math.Abs(delta), reduce)
			switch {
			case cmd == nil:
			case reduce:
				c = append(c, *cmd)
			case delta < 0:
				sl = append(sl, *cmd)
			default:
				b = append(b, *cmd)
			}
			return
		}
		current := s.Positions[symbol].Qty
		c, sl, b := plan(current + pending[symbol])
		if len(c)+len(sl)+len(b) == 0 {
			continue // nothing to do, or the working orders already get there
		}
		// Working orders that get within a tenth of their size of the target
		// stay: replacing them on every small price move would starve them.
		if p := pending[symbol]; p != 0 && math.Abs(want-current-p) <= 0.1*math.Abs(p) {
			continue
		}
		if len(working[symbol]) > 0 {
			// Replace the stale working orders with a fresh plan.
			for _, o := range working[symbol] {
				cancels = append(cancels, sim.Command{CancelID: o.ID})
			}
			c, sl, b = plan(current)
		}
		closes, sells, buys = append(closes, c...), append(sells, sl...), append(buys, b...)
	}
	commands = append(commands, cancels...)
	// Covers and sales first so their proceeds fund new longs.
	commands = append(commands, closes...)
	commands = append(commands, sells...)
	commands = append(commands, buys...)
	commands = append(commands, sim.Command{Report: map[string]any{"rule": rule, "status": "rebalanced", "symbols_used": len(used), "held": held, "unshortable": unshortable, "longs": longs, "shorts": shorts, "gross": finalGross, "net": net, "orders": len(closes) + len(sells) + len(buys), "weights": book}})
	return commands, nil
}
