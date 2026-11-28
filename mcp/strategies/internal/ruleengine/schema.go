// Package ruleengine evaluates portable trading programs against timestamped
// events. It has no network, database, broker, or wall-clock dependencies.
package ruleengine

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
)

// Version is the one program format. A program trades a list of symbols;
// expressions may name a symbol, rules may run once per symbol, and
// cross-sectional operators and the target action work across the list.
const Version = "trading-rules/1"

// MaxSymbols bounds a program's universe.
const MaxSymbols = 500

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
	Symbol    string   `json:"symbol,omitempty"`  // read this symbol
	Against   string   `json:"against,omitempty"` // correlation/beta reference symbol
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
	Symbols      []string           `json:"symbols"`
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
	ForEach   bool     `json:"for_each,omitempty"`
	Actions   []Action `json:"actions"`
}

type Action struct {
	Kind               string     `json:"kind"` // enter, flatten, cancel, set, target
	Symbol             string     `json:"symbol,omitempty"`
	Weight             *Expr      `json:"weight,omitempty"`
	Construct          *Construct `json:"construct,omitempty"`
	Name               string     `json:"name,omitempty"`
	Value              *Expr      `json:"value,omitempty"`
	Side               string     `json:"side,omitempty"`
	OrderType          string     `json:"order_type,omitempty"`
	Price              *Expr      `json:"price,omitempty"`
	Stop               *Expr      `json:"stop,omitempty"`
	Target             *Expr      `json:"target,omitempty"`
	StopPct            float64    `json:"stop_pct,omitempty"`
	TargetPct          float64    `json:"target_pct,omitempty"`
	TrailActivationPct float64    `json:"trail_activation_pct,omitempty"`
	TrailDistancePct   float64    `json:"trail_distance_pct,omitempty"`
	Sizing             Sizing     `json:"sizing,omitempty"`
	Group              string     `json:"group,omitempty"`
}

// Construct turns raw signed target scores into an executable book.
type Construct struct {
	Normalize   string  `json:"normalize,omitempty"` // legs (default), gross, none
	Gross       float64 `json:"gross,omitempty"`     // default 1
	Net         float64 `json:"net,omitempty"`       // legs only; default 0
	MaxPosition float64 `json:"max_position,omitempty"`
	MaxGross    float64 `json:"max_gross,omitempty"`     // default and ceiling 2
	MinTradePct float64 `json:"min_trade_pct,omitempty"` // percent of equity
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

// ProgramSymbols lists the symbols a program trades, sorted.
func ProgramSymbols(p *Program) []string {
	out := append([]string(nil), p.Symbols...)
	sort.Strings(out)
	return out
}

func csOp(op string) bool {
	switch op {
	case "cs_rank", "cs_zscore", "cs_demean", "cs_count", "top", "bottom", "quantile":
		return true
	}
	return false
}

func Validate(p *Program) error {
	if p == nil || p.Version != Version {
		return errors.New("program requires version trading-rules/1")
	}
	if len(p.Symbols) == 0 || len(p.Symbols) > MaxSymbols {
		return fmt.Errorf("program requires 1–%d symbols", MaxSymbols)
	}
	universe := map[string]bool{}
	for _, symbol := range p.Symbols {
		if strings.TrimSpace(symbol) == "" || symbol != strings.TrimSpace(symbol) || universe[symbol] {
			return errors.New("symbols must be unique and non-blank")
		}
		universe[symbol] = true
	}
	single := len(p.Symbols) == 1
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
		if e.Metric == "" && (e.Period != 0 || e.Offset != 0 || e.Timeframe != "" || e.Window != "" || e.Input != nil || e.Feature != "" || e.Field != "" || e.Symbol != "" || e.Against != "") {
			return errors.New("metric parameters require a metric expression")
		}
		if e.Symbol != "" && !universe[e.Symbol] {
			return errors.New("expression symbol must be one of the program's symbols")
		}
		if (e.Metric == "correlation" || e.Metric == "beta") != (e.Against != "") {
			return errors.New("correlation and beta require against; against is only for them")
		}
		if e.Against != "" && (!universe[e.Against] || e.Period < 2) {
			return errors.New("correlation/beta require an against symbol in the program and period >= 2")
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
			case "open", "high", "low", "close", "volume", "range", "body", "close_fraction", "sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return", "feature", "price", "bid", "ask", "position_qty", "entry_count", "minute", "weekday", "day_of_month", "month", "window_high", "window_low", "window_complete", "equity", "correlation", "beta":
			default:
				return fmt.Errorf("unsupported metric %q", e.Metric)
			}
			if strings.HasPrefix(e.Metric, "window_") {
				if _, ok := p.Windows[e.Window]; !ok {
					return errors.New("window metric requires a declared window")
				}
			}
			switch e.Metric {
			case "sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return", "correlation", "beta":
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
			n := 2
			switch e.Op {
			case "not", "abs", "sign", "log", "exp", "sqrt", "cs_rank", "cs_zscore", "cs_demean", "cs_count":
				n = 1
			case "clamp", "if":
				n = 3
			case "+", "-", "*", "/", "min", "max", ">", ">=", "<", "<=", "==", "!=", "and", "or", "crosses_above", "crosses_below", "top", "bottom", "quantile":
			default:
				return fmt.Errorf("unsupported operator %q", e.Op)
			}
			if e.Op == "top" || e.Op == "bottom" || e.Op == "quantile" {
				low := 1.0
				if e.Op == "quantile" {
					low = 2
				}
				if len(e.Args) != 2 || e.Args[1].Value == nil || *e.Args[1].Value < low || *e.Args[1].Value != math.Trunc(*e.Args[1].Value) || *e.Args[1].Value > MaxSymbols {
					return fmt.Errorf("%s requires a whole-number literal count", e.Op)
				}
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
					if csOp(x.Op) {
						return false
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
			if e.Op == "and" || e.Op == "or" {
				if len(e.Args) < 2 || len(e.Args) > 64 {
					return fmt.Errorf("%s requires 2–64 operands", e.Op)
				}
			} else if len(e.Args) != n {
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
	targets, entering := 0, false
	for _, r := range p.Rules {
		if r.ID == "" || ids[r.ID] || len(r.Actions) == 0 || len(r.Actions) > 32 {
			return errors.New("rules require unique IDs and 1–32 actions")
		}
		ids[r.ID] = true
		// Outside for_each a rule of a multi-symbol program has no implicit
		// symbol, so every symbol-dependent metric must name one.
		bound := single || r.ForEach
		if !bound {
			for _, expr := range []*Expr{r.When, r.Reset} {
				if expr != nil && !symbolBound(p, *expr, 0) {
					return fmt.Errorf("rule %s: outside for_each, metrics must name a symbol", r.ID)
				}
			}
		}
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
			for _, expr := range []*Expr{a.Price, a.Stop, a.Target, a.Value, a.Weight} {
				if expr != nil {
					if e := check(*expr, 0, map[string]bool{}); e != nil {
						return e
					}
				}
			}
			if a.Symbol != "" && (!universe[a.Symbol] || (a.Kind != "enter" && a.Kind != "flatten" && a.Kind != "cancel")) {
				return errors.New("action symbol must be a program symbol on enter, flatten or cancel")
			}
			if a.Kind != "target" && (a.Weight != nil || a.Construct != nil) {
				return errors.New("weight and construct belong to target actions")
			}
			if !bound && a.Kind != "target" {
				for _, expr := range []*Expr{a.Price, a.Stop, a.Target, a.Value} {
					if expr != nil && !symbolBound(p, *expr, 0) {
						return fmt.Errorf("rule %s: outside for_each, metrics must name a symbol", r.ID)
					}
				}
				if a.Kind == "enter" && a.Symbol == "" {
					return fmt.Errorf("rule %s: outside for_each, enter requires a symbol", r.ID)
				}
			}
			switch a.Kind {
			case "set":
				if _, ok := p.Initial[a.Name]; !ok || a.Value == nil {
					return errors.New("set requires a declared variable and value")
				}
			case "flatten", "cancel":
			case "target":
				targets++
				if r.ForEach || a.Weight == nil {
					return errors.New("target requires a rule without for_each and a weight expression")
				}
				if err := validateConstruct(a.Construct); err != nil {
					return err
				}
			case "enter":
				entering = true
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
	if targets > 1 || targets == 1 && entering {
		return errors.New("a program holds at most one target action, and target and enter cannot both manage its symbols")
	}
	if RequiredBaseBars(p) > 1000000 {
		return errors.New("calculation warmup exceeds one million base candles")
	}
	return nil
}

func validateConstruct(c *Construct) error {
	if c == nil {
		return nil
	}
	switch c.Normalize {
	case "", "legs", "gross", "none":
	default:
		return errors.New("construct.normalize must be legs, gross or none")
	}
	for _, v := range []float64{c.Gross, c.Net, c.MaxPosition, c.MaxGross, c.MinTradePct} {
		if !finite(v) {
			return errors.New("construct values must be finite")
		}
	}
	if c.Gross < 0 || c.Gross > 2 || c.MaxGross < 0 || c.MaxGross > 2 || c.MaxPosition < 0 || c.MaxPosition > 2 || c.MinTradePct < 0 || c.MinTradePct >= 100 {
		return errors.New("construct: gross, max_gross and max_position in [0,2]; min_trade_pct in [0,100)")
	}
	gross := c.Gross
	if gross == 0 {
		gross = 1
	}
	if math.Abs(c.Net) > gross {
		return errors.New("construct: |net| cannot exceed gross")
	}
	return nil
}

// symbolBound reports whether an expression can be evaluated with no current
// symbol: every symbol-dependent metric names its symbol.
func symbolBound(p *Program, e Expr, depth int) bool {
	if depth > 24 {
		return false
	}
	if e.Name != "" {
		if calc, ok := p.Calculations[e.Name]; ok {
			return symbolBound(p, calc, depth+1)
		}
		return true
	}
	if e.Metric != "" {
		switch e.Metric {
		case "minute", "weekday", "day_of_month", "month", "equity":
			return true
		}
		if e.Symbol == "" && e.Input != nil {
			return symbolBound(p, *e.Input, depth+1)
		}
		return e.Symbol != ""
	}
	if e.Op == "cs_count" {
		return true
	}
	if csOp(e.Op) {
		return false
	}
	for _, a := range e.Args {
		if !symbolBound(p, a, depth+1) {
			return false
		}
	}
	return true
}

// Shortable reports whether the simulator may hold symbol short: a declared
// linear contract, or spot with shorts enabled and the symbol not excluded.
func Shortable(c sim.Config, symbol string) bool {
	if _, ok := c.Contracts[symbol]; ok {
		return true
	}
	if c.Shorts == nil || !c.Shorts.Enabled {
		return false
	}
	for _, s := range c.Shorts.Unshortable {
		if s == symbol {
			return false
		}
	}
	return true
}

// Config must agree with the program's symbols. It deliberately does not
// invent a contract specification for a ticker or silently enable short spot
// sales: spot shorts need Config.Shorts.
func ValidateExecution(p *Program, c sim.Config) error {
	for _, r := range p.Rules {
		for _, a := range r.Actions {
			if a.Kind == "enter" && a.Side == "sell" {
				symbols := []string{a.Symbol}
				if a.Symbol == "" {
					symbols = ProgramSymbols(p)
				}
				for _, symbol := range symbols {
					if !Shortable(c, symbol) {
						return fmt.Errorf("short entries on %s require an explicit simulated linear contract or simulator shorts", symbol)
					}
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
	if csOp(e.Op) {
		return false
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
	case "return", "volatility", "correlation", "beta":
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

// SessionCloseKey is bar metadata naming a session close earlier than the
// candle interval's end (RFC 3339); the candle is complete from then.
const SessionCloseKey = "session_close"
