// Package ruleengine evaluates portable trading programs against timestamped
// events. It has no network, database, broker, or wall-clock dependencies.
package ruleengine

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
)

const Version = "trading-rules/1"

// Expr is a bounded expression tree, never executable source code. Booleans are
// represented as 0/1, allowing calculated comparisons and nested all/any rules.
type Expr struct {
	Input     *Expr    `json:"input,omitempty"`
	Feature   string   `json:"feature,omitempty"`
	Field     string   `json:"field,omitempty"`
	Value     *float64 `json:"value,omitempty"`
	Metric    string   `json:"metric,omitempty"`
	Name      string   `json:"name,omitempty"`
	Timeframe string   `json:"timeframe,omitempty"`
	Period    int      `json:"period,omitempty"`
	Offset    int      `json:"offset,omitempty"`
	Window    string   `json:"window,omitempty"`
	Op        string   `json:"op,omitempty"`
	Args      []Expr   `json:"args,omitempty"`
}

type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type Program struct {
	Schedules    map[string]string  `json:"schedules,omitempty"`
	Version      string             `json:"version"`
	Symbol       string             `json:"symbol"`
	Timeframe    string             `json:"timeframe"`
	Timezone     string             `json:"timezone"`
	Windows      map[string]Window  `json:"windows,omitempty"`
	Calculations map[string]Expr    `json:"calculations,omitempty"`
	Initial      map[string]float64 `json:"initial,omitempty"`
	Rules        []Rule             `json:"rules"`
}

type Rule struct {
	Schedule  string   `json:"schedule,omitempty"`
	ID        string   `json:"id"`
	On        string   `json:"on"` // bar.close, quote, clock, fill, any
	Timeframe string   `json:"timeframe,omitempty"`
	When      *Expr    `json:"when,omitempty"`
	Reset     *Expr    `json:"reset,omitempty"`  // rearm an until_reset rule
	Repeat    string   `json:"repeat,omitempty"` // once_per_bar (default), once_per_day, until_reset, always
	Actions   []Action `json:"actions"`
}

type Action struct {
	Kind               string  `json:"kind"` // enter, flatten, cancel, set
	Name               string  `json:"name,omitempty"`
	Value              *Expr   `json:"value,omitempty"`
	Side               string  `json:"side,omitempty"`
	OrderType          string  `json:"order_type,omitempty"`
	Price              *Expr   `json:"price,omitempty"`
	Stop               *Expr   `json:"stop,omitempty"`
	Target             *Expr   `json:"target,omitempty"`
	StopPct            float64 `json:"stop_pct,omitempty"`
	TargetPct          float64 `json:"target_pct,omitempty"`
	TrailActivationPct float64 `json:"trail_activation_pct,omitempty"`
	TrailDistancePct   float64 `json:"trail_distance_pct,omitempty"`
	Sizing             Sizing  `json:"sizing,omitempty"`
	Group              string  `json:"group,omitempty"`
}

type Sizing struct {
	Mode   string  `json:"mode"` // fixed_risk, quantity, notional, equity_pct
	Amount float64 `json:"amount"`
}

func Number(v float64) Expr                  { return Expr{Value: &v} }
func Operation(op string, args ...Expr) Expr { return Expr{Op: op, Args: args} }
func duration(frame string) (time.Duration, error) {
	switch frame {
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "4h":
		return 4 * time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unsupported rule timeframe %q", frame)
}
func minute(text string) (int, error) {
	t, err := time.Parse("15:04", text)
	if err != nil {
		return 0, fmt.Errorf("invalid session time %q", text)
	}
	return t.Hour()*60 + t.Minute(), nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func Validate(p *Program) error {
	if p == nil || p.Version != Version || strings.TrimSpace(p.Symbol) == "" {
		return errors.New("program requires trading-rules/1 version and symbol")
	}
	base, err := duration(p.Timeframe)
	if err != nil {
		return err
	}
	if _, err = time.LoadLocation(p.Timezone); err != nil || p.Timezone == "" {
		return errors.New("program requires a valid IANA timezone")
	}
	if len(p.Rules) == 0 || len(p.Rules) > 128 || len(p.Calculations) > 128 || len(p.Windows) > 32 {
		return errors.New("program limits: 1–128 rules, 128 calculations, 32 windows")
	}
	if len(p.Schedules) > 32 {
		return errors.New("maximum 32 schedules")
	}
	for name, clock := range p.Schedules {
		if _, err := minute(clock); name == "" || err != nil {
			return errors.New("schedules require names and valid HH:MM times")
		}
	}
	for name, w := range p.Windows {
		start, e1 := minute(w.Start)
		end, e2 := minute(w.End)
		if name == "" || e1 != nil || e2 != nil || start >= end {
			return errors.New("windows require names and start < end within one local day")
		}
	}
	for name, v := range p.Initial {
		if name == "" || !finite(v) {
			return errors.New("initial variables must be named and finite")
		}
	}
	var check func(Expr, int, map[string]bool) error
	check = func(e Expr, depth int, path map[string]bool) error {
		if depth > 24 {
			return errors.New("expression nesting/cycle exceeds 24")
		}
		choices := 0
		if e.Value != nil {
			choices++
		}
		if e.Metric != "" {
			choices++
		}
		if e.Name != "" {
			choices++
		}
		if e.Op != "" {
			choices++
		}
		if choices != 1 {
			return errors.New("expression requires exactly one of value, metric, name, op")
		}
		if e.Period < 0 || e.Period > 2000 || e.Offset < 0 || e.Offset > 2000 {
			return errors.New("period/offset must be between 0 and 2000")
		}
		if e.Metric == "" && (e.Period != 0 || e.Offset != 0 || e.Timeframe != "" || e.Window != "" || e.Input != nil || e.Feature != "" || e.Field != "") {
			return errors.New("metric parameters require a metric expression")
		}
		if e.Value != nil && !finite(*e.Value) {
			return errors.New("literal must be finite")
		}
		if e.Name != "" {
			if calc, ok := p.Calculations[e.Name]; ok {
				if path[e.Name] {
					return fmt.Errorf("calculation cycle at %s", e.Name)
				}
				next := map[string]bool{}
				for k, v := range path {
					next[k] = v
				}
				next[e.Name] = true
				return check(calc, depth+1, next)
			}
			if _, ok := p.Initial[e.Name]; !ok {
				return fmt.Errorf("unknown calculation/variable %q", e.Name)
			}
		}
		if e.Timeframe != "" {
			d, err := duration(e.Timeframe)
			if err != nil || d < base || d%base != 0 {
				return errors.New("expression timeframe must be a multiple of the base timeframe")
			}
		}
		if e.Metric != "" {
			switch e.Metric {
			case "open", "high", "low", "close", "volume", "range", "body", "close_fraction", "sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return", "feature", "price", "bid", "ask", "position_qty", "entry_count", "minute", "weekday", "window_high", "window_low", "window_complete", "equity":
			default:
				return fmt.Errorf("unsupported metric %q", e.Metric)
			}
			if strings.HasPrefix(e.Metric, "window_") {
				if _, ok := p.Windows[e.Window]; !ok {
					return errors.New("window metric requires a declared window")
				}
			}
			switch e.Metric {
			case "sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return":
				if e.Period < 1 {
					return errors.New("rolling indicators require period >= 1")
				}
			}
		}
		if e.Metric == "feature" && (e.Feature == "" || e.Field == "") {
			return errors.New("feature metric requires feature and field")
		}
		if e.Input != nil {
			if e.Metric != "sma" && e.Metric != "ema" && e.Metric != "stddev" {
				return errors.New("series input is supported by sma, ema and stddev")
			}
			frame := e.Timeframe
			if frame == "" {
				frame = p.Timeframe
			}
			if !sameSeriesFrame(p, *e.Input, frame, 0) {
				return errors.New("nested series inputs must use the same explicit timeframe")
			}
			if seriesBars(p, e, 0) > 10001 {
				return errors.New("nested indicator history exceeds 10001 retained candles")
			}
			if !historicalExpr(p, *e.Input, 0) {
				return errors.New("rolling series input must be a historical candle expression")
			}
			if err := check(*e.Input, depth+1, path); err != nil {
				return err
			}
		}
		if e.Metric != "" && seriesBars(p, e, 0) > 10001 {
			return errors.New("indicator history exceeds 10001 retained candles")
		}
		if e.Op != "" {
			unary := false
			switch e.Op {
			case "not", "abs":
				unary = true
			case "+", "-", "*", "/", "min", "max", ">", ">=", "<", "<=", "==", "!=", "and", "or", "crosses_above", "crosses_below":
			default:
				return fmt.Errorf("unsupported operator %q", e.Op)
			}
			if e.Op == "crosses_above" || e.Op == "crosses_below" {
				var candleOnly func(Expr, int) bool
				candleOnly = func(x Expr, n int) bool {
					if n > 24 {
						return false
					}
					if x.Input != nil && !candleOnly(*x.Input, n+1) {
						return false
					}
					if x.Value != nil {
						return true
					}
					if x.Name != "" {
						calc, ok := p.Calculations[x.Name]
						return ok && candleOnly(calc, n+1)
					}
					if x.Metric != "" {
						switch x.Metric {
						case "open", "high", "low", "close", "volume", "range", "body", "close_fraction", "sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return":
							return true
						default:
							return false
						}
					}
					for _, arg := range x.Args {
						if !candleOnly(arg, n+1) {
							return false
						}
					}
					return true
				}
				for _, arg := range e.Args {
					if !candleOnly(arg, 0) {
						return errors.New("crossovers require candle expressions; account, clock and mutable variables have no historical series")
					}
				}
			}
			n := 2
			if unary {
				n = 1
			}
			if len(e.Args) != n {
				return fmt.Errorf("%s requires %d operands", e.Op, n)
			}
		} else if len(e.Args) > 0 {
			return errors.New("operands require an operator")
		}
		for _, a := range e.Args {
			if err := check(a, depth+1, path); err != nil {
				return err
			}
		}
		return nil
	}
	for name, calc := range p.Calculations {
		if _, ok := p.Initial[name]; ok {
			return errors.New("calculation and variable names must be distinct")
		}
		if err := check(calc, 0, map[string]bool{name: true}); err != nil {
			return fmt.Errorf("calculation %s: %w", name, err)
		}
	}
	ids := map[string]bool{}
	for _, r := range p.Rules {
		if r.ID == "" || ids[r.ID] || len(r.Actions) == 0 || len(r.Actions) > 32 {
			return errors.New("rules require unique IDs and 1–32 actions")
		}
		ids[r.ID] = true
		if r.Schedule != "" {
			if r.On != "clock" {
				return errors.New("schedule triggers require on=clock")
			}
			if _, ok := p.Schedules[r.Schedule]; !ok {
				return errors.New("unknown schedule")
			}
		}
		switch r.On {
		case "bar.close", "quote", "clock", "fill", "any":
		default:
			return fmt.Errorf("invalid trigger %q", r.On)
		}
		if r.Timeframe != "" {
			d, e := duration(r.Timeframe)
			if e != nil || d < base || d%base != 0 {
				return errors.New("rule timeframe must be a multiple of base timeframe")
			}
		}
		switch r.Repeat {
		case "", "once_per_bar", "once_per_day", "until_reset", "always":
		default:
			return errors.New("invalid repeat policy")
		}
		if r.Repeat == "until_reset" && r.Reset == nil {
			return errors.New("until_reset requires a reset condition")
		}
		for _, expr := range []*Expr{r.When, r.Reset} {
			if expr != nil {
				if e := check(*expr, 0, map[string]bool{}); e != nil {
					return e
				}
			}
		}
		var entries []Action
		for _, a := range r.Actions {
			for _, expr := range []*Expr{a.Price, a.Stop, a.Target, a.Value} {
				if expr != nil {
					if e := check(*expr, 0, map[string]bool{}); e != nil {
						return e
					}
				}
			}
			switch a.Kind {
			case "set":
				if _, ok := p.Initial[a.Name]; !ok || a.Value == nil {
					return errors.New("set requires a declared variable and value")
				}
			case "flatten", "cancel":
			case "enter":
				entries = append(entries, a)
				if a.Side != "buy" && a.Side != "sell" {
					return errors.New("entry side must be buy or sell")
				}
				switch a.OrderType {
				case "", "market", "limit", "stop":
				default:
					return errors.New("unsupported order type")
				}
				if a.OrderType != "" && a.OrderType != "market" && a.Price == nil {
					return errors.New("pending entry requires price")
				}
				if !finite(a.Sizing.Amount) || a.Sizing.Amount <= 0 {
					return errors.New("positive sizing amount required")
				}
				switch a.Sizing.Mode {
				case "fixed_risk":
					if a.Stop == nil && a.StopPct <= 0 {
						return errors.New("fixed_risk requires a stop")
					}
				case "quantity", "notional":
				case "equity_pct":
					if a.Sizing.Amount > 1 {
						return errors.New("equity_pct amount must be <= 1")
					}
				default:
					return errors.New("unknown sizing mode")
				}
				for _, v := range []float64{a.StopPct, a.TargetPct, a.TrailActivationPct, a.TrailDistancePct} {
					if !finite(v) || v < 0 || v >= 1 {
						return errors.New("protection fractions must be in [0,1)")
					}
				}
				if a.Stop != nil && a.StopPct > 0 || a.Target != nil && a.TargetPct > 0 || a.TrailActivationPct > 0 && a.TrailDistancePct == 0 {
					return errors.New("conflicting or incomplete protection")
				}
			default:
				return fmt.Errorf("unknown action %q", a.Kind)
			}
		}
		if len(entries) > 1 && (len(entries) != 2 || entries[0].Group == "" || entries[0].Group != entries[1].Group || entries[0].Side == entries[1].Side) {
			return errors.New("multiple entries in one rule require one opposing OCO pair")
		}
	}
	if RequiredBaseBars(p) > 1000000 {
		return errors.New("calculation warmup exceeds one million base candles")
	}
	return nil
}

// Config must agree with the program's symbol. It deliberately does not invent
// a contract specification for a ticker or silently enable short spot sales.
func ValidateExecution(p *Program, c sim.Config) error {
	for _, r := range p.Rules {
		for _, a := range r.Actions {
			if a.Kind == "enter" && a.Side == "sell" {
				if _, ok := c.Contracts[p.Symbol]; !ok {
					return errors.New("short entries require an explicit simulated linear contract")
				}
			}
		}
	}
	return nil
}

// Mutable features/account state cannot be retroactively treated as a series.
func historicalExpr(p *Program, e Expr, depth int) bool {
	if depth > 24 {
		return false
	}
	if e.Value != nil {
		return true
	}
	if e.Name != "" {
		v, ok := p.Calculations[e.Name]
		return ok && historicalExpr(p, v, depth+1)
	}
	if e.Metric != "" {
		switch e.Metric {
		case "open", "high", "low", "close", "volume", "range", "body", "close_fraction", "sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return":
		default:
			return false
		}
	}
	if e.Input != nil && !historicalExpr(p, *e.Input, depth+1) {
		return false
	}
	for _, a := range e.Args {
		if !historicalExpr(p, a, depth+1) {
			return false
		}
	}
	return true
}

func sameSeriesFrame(p *Program, e Expr, frame string, depth int) bool {
	if depth > 24 {
		return false
	}
	if e.Name != "" {
		v, ok := p.Calculations[e.Name]
		return ok && sameSeriesFrame(p, v, frame, depth+1)
	}
	if e.Metric != "" {
		f := e.Timeframe
		if f == "" {
			f = p.Timeframe
		}
		if f != frame {
			return false
		}
	}
	if e.Input != nil && !sameSeriesFrame(p, *e.Input, frame, depth+1) {
		return false
	}
	for _, a := range e.Args {
		if !sameSeriesFrame(p, a, frame, depth+1) {
			return false
		}
	}
	return true
}
func seriesBars(p *Program, e Expr, depth int) int {
	if depth > 24 {
		return 10002
	}
	if e.Name != "" {
		if calc, ok := p.Calculations[e.Name]; ok {
			return seriesBars(p, calc, depth+1)
		}
	}
	n := max(e.Period, 1)
	switch e.Metric {
	case "ema":
		n *= 5
	case "atr", "rsi":
		n = 5*n + 1
	case "return", "volatility":
		n++
	}
	n += e.Offset
	if e.Input != nil {
		n += seriesBars(p, *e.Input, depth+1) - 1
	}
	for _, a := range e.Args {
		n = max(n, seriesBars(p, a, depth+1))
	}
	return n
}
