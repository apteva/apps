package ruleengine

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
)

type Candle struct {
	At     time.Time `json:"at"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
}
type aggregate struct {
	Candle Candle    `json:"candle"`
	Next   time.Time `json:"next"`
	Count  int       `json:"count"`
}
type windowState struct {
	Day      string    `json:"day"`
	High     float64   `json:"high"`
	Low      float64   `json:"low"`
	Next     time.Time `json:"next"`
	Complete bool      `json:"complete"`
	Valid    bool      `json:"valid"`
}

// Runtime is one symbol's candle state, or a program's variables and fired
// triggers. It is derived from the tape and never serialized whole.
type Runtime struct {
	limits     map[string]int
	location   *time.Location
	Bars       map[string][]Candle    `json:"bars"`
	Aggregates map[string]aggregate   `json:"aggregates"`
	Windows    map[string]windowState `json:"windows"`
	Variables  map[string]float64     `json:"variables"`
	Fired      map[string]string      `json:"fired"`
}

func (r *Runtime) timezone(p *Program) *time.Location {
	if r.location == nil {
		r.location, _ = time.LoadLocation(p.Timezone)
	}
	return r.location
}

func newRuntime(p *Program) *Runtime {
	r := &Runtime{Bars: map[string][]Candle{}, Aggregates: map[string]aggregate{}, Windows: map[string]windowState{}, Variables: map[string]float64{}, Fired: map[string]string{}}
	for n, v := range p.Initial {
		r.Variables[n] = v
	}
	return r
}

func (r *Runtime) append(frame string, c Candle) {
	h := r.Bars[frame]
	if len(h) > 0 && !c.At.After(h[len(h)-1].At) {
		return
	}
	h = append(h, c)
	limit := r.limits[frame]
	if limit == 0 {
		limit = 10001
	}
	if len(h) > limit {
		h = h[len(h)-limit:]
	}
	r.Bars[frame] = h
}

// Retain only the observations that the declared expression graph can read.
// Aggregation and session extrema have separate state, so a minute program
// using a long hourly channel need not serialize thousands of minute candles.
func historyLimits(p *Program) map[string]int {
	out := map[string]int{}
	for frame := range frames(p) {
		out[frame] = 2
	}
	var visit func(Expr)
	visit = func(e Expr) {
		if e.Metric != "" {
			frame := e.Timeframe
			if frame == "" {
				frame = p.Timeframe
			}
			out[frame] = min(10001, max(out[frame], seriesBars(p, e, 0)+2))
		}
		if e.Input != nil {
			visit(*e.Input)
		}
		for _, arg := range e.Args {
			visit(arg)
		}
	}
	for _, expr := range p.Calculations {
		visit(expr)
	}
	for _, rule := range p.Rules {
		for _, expr := range []*Expr{rule.When, rule.Reset} {
			if expr != nil {
				visit(*expr)
			}
		}
		for _, action := range rule.Actions {
			for _, expr := range []*Expr{action.Price, action.Stop, action.Target, action.Value, action.Weight} {
				if expr != nil {
					visit(*expr)
				}
			}
		}
	}
	return out
}
func frames(p *Program) map[string]bool {
	out := map[string]bool{p.Timeframe: true}
	var visit func(Expr)
	visit = func(e Expr) {
		if e.Input != nil {
			visit(*e.Input)
		}
		if e.Timeframe != "" {
			out[e.Timeframe] = true
		}
		for _, a := range e.Args {
			visit(a)
		}
	}
	for _, e := range p.Calculations {
		visit(e)
	}
	for _, rule := range p.Rules {
		if rule.Timeframe != "" {
			out[rule.Timeframe] = true
		}
		for _, e := range []*Expr{rule.When, rule.Reset} {
			if e != nil {
				visit(*e)
			}
		}
		for _, a := range rule.Actions {
			for _, e := range []*Expr{a.Price, a.Stop, a.Target, a.Value, a.Weight} {
				if e != nil {
					visit(*e)
				}
			}
		}
	}
	return out
}
func (r *Runtime) observe(p *Program, symbol string, in sim.Input) (map[string]bool, error) {
	closed := map[string]bool{}
	if in.Type != "market.bar.close" || in.Symbol != symbol {
		return closed, nil
	}
	frame := in.Metadata["timeframe"]
	if frame == "" {
		frame = p.Timeframe
	}
	fd, frameErr := duration(frame)
	base, _ := duration(p.Timeframe)
	if frameErr != nil || fd < base || fd%base != 0 || !frames(p)[frame] {
		return nil, errors.New("undeclared or incompatible rule candle timeframe")
	}
	d, _ := duration(frame)
	c := Candle{At: in.EventTime, Open: in.Data["open"], High: in.Data["high"], Low: in.Data["low"], Close: in.Data["price"], Volume: in.Data["volume"]}
	closesAt := c.At.Add(d)
	// A session (US equities) can end a candle before its interval does.
	if t, err := time.Parse(time.RFC3339, in.Metadata[SessionCloseKey]); err == nil && t.After(c.At) && t.Before(closesAt) {
		closesAt = t
	}
	if c.Open <= 0 || c.Low <= 0 || c.High < math.Max(c.Open, c.Close) || c.Low > math.Min(c.Open, c.Close) || in.AvailableAt.Before(closesAt) {
		return nil, errors.New("rule candles require complete valid OHLC and availability at/after candle close")
	}
	h := r.Bars[frame]
	if len(h) > 0 && !c.At.After(h[len(h)-1].At) {
		return nil, errors.New("rule candles must arrive in strictly increasing event-time order")
	}
	r.append(frame, c)
	closed[frame] = true
	if frame != p.Timeframe {
		return closed, nil
	}
	for f := range frames(p) {
		if f == frame {
			continue
		}
		target, _ := duration(f)
		start := c.At.Truncate(target)
		a, exists := r.Aggregates[f]
		if !exists || !a.Candle.At.Equal(start) {
			a = aggregate{Candle: Candle{At: start, Open: c.Open, High: c.High, Low: c.Low}, Next: start}
		}
		if !c.At.Equal(a.Next) {
			a.Count = -100000
		}
		a.Candle.High = math.Max(a.Candle.High, c.High)
		a.Candle.Low = math.Min(a.Candle.Low, c.Low)
		a.Candle.Close = c.Close
		a.Candle.Volume += c.Volume
		a.Count++
		a.Next = c.At.Add(d)
		if a.Next.Equal(start.Add(target)) && a.Count == int(target/d) {
			r.append(f, a.Candle)
			closed[f] = true
		}
		r.Aggregates[f] = a
	}
	loc := r.timezone(p)
	local := c.At.In(loc)
	day := local.Format("2006-01-02")
	for name, w := range p.Windows {
		sm, _ := minute(w.Start)
		em, _ := minute(w.End)
		start := time.Date(local.Year(), local.Month(), local.Day(), sm/60, sm%60, 0, 0, loc)
		end := time.Date(local.Year(), local.Month(), local.Day(), em/60, em%60, 0, 0, loc)
		v := r.Windows[name]
		if v.Day != day {
			v = windowState{Day: day, Next: start, Valid: true}
		}
		if !c.At.Before(start) && c.At.Before(end) {
			if !c.At.Equal(v.Next) || c.At.Add(d).After(end) {
				v.Valid = false
			}
			if v.High == 0 {
				v.High = c.High
				v.Low = c.Low
			} else {
				v.High = math.Max(v.High, c.High)
				v.Low = math.Min(v.Low, c.Low)
			}
			v.Next = c.At.Add(d)
			v.Complete = v.Valid && v.Next.Equal(end)
		}
		r.Windows[name] = v
	}
	return closed, nil
}

type context struct {
	program *Program
	runtime *Runtime // variables and fired triggers
	series  map[string]*Runtime
	symbol  string
	memo    map[*Expr]map[string]float64
	state   *sim.State
	config  sim.Config
	lag     int
	cutoff  time.Time
}

// bars returns the candle state of the current symbol.
func (c context) bars() (*Runtime, error) {
	if c.symbol == "" && len(c.program.Symbols) == 1 {
		c.symbol = c.program.Symbols[0]
	}
	if c.symbol == "" {
		return nil, errors.New("metric needs a symbol outside for_each rules")
	}
	r := c.series[c.symbol]
	if r == nil {
		return nil, ErrUnavailable
	}
	return r, nil
}

// crossSection evaluates arg for every program symbol at this moment. A
// symbol whose value is unavailable is left out. Results are memoized for the
// current event; set actions clear the memo.
func (c context) crossSection(arg *Expr) (map[string]float64, []string, error) {
	values, ok := c.memo[arg]
	if !ok {
		values = map[string]float64{}
		for _, symbol := range ProgramSymbols(c.program) {
			x := c
			x.symbol = symbol
			v, err := x.eval(*arg, 0)
			if errors.Is(err, ErrUnavailable) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			values[symbol] = v
		}
		if c.memo != nil {
			c.memo[arg] = values
		}
	}
	symbols := make([]string, 0, len(values))
	for symbol := range values {
		symbols = append(symbols, symbol)
	}
	// Ascending by value; ties go to symbol order.
	sort.Slice(symbols, func(i, j int) bool {
		a, b := values[symbols[i]], values[symbols[j]]
		if a != b {
			return a < b
		}
		return symbols[i] < symbols[j]
	})
	return values, symbols, nil
}

func (c context) crossSectional(e Expr) (float64, error) {
	values, order, err := c.crossSection(&e.Args[0])
	if err != nil {
		return 0, err
	}
	if e.Op == "cs_count" {
		n := 0
		for _, v := range values {
			if v != 0 {
				n++
			}
		}
		return float64(n), nil
	}
	own, ok := values[c.symbol]
	if !ok {
		return 0, ErrUnavailable
	}
	n := len(order)
	index := 0
	for i, symbol := range order {
		if symbol == c.symbol {
			index = i
		}
	}
	switch e.Op {
	case "cs_rank":
		if n == 1 {
			return 0.5, nil
		}
		return float64(index) / float64(n-1), nil
	case "cs_zscore", "cs_demean":
		mean := 0.0
		for _, v := range values {
			mean += v
		}
		mean /= float64(n)
		if e.Op == "cs_demean" {
			return own - mean, nil
		}
		variance := 0.0
		for _, v := range values {
			variance += (v - mean) * (v - mean)
		}
		sd := math.Sqrt(variance / float64(n))
		if sd == 0 {
			return 0, nil
		}
		return (own - mean) / sd, nil
	case "top", "bottom":
		// One total order (value, then symbol), so top and bottom never share
		// a symbol while n leaves room for both.
		k := int(*e.Args[1].Value)
		if e.Op == "bottom" {
			return truth(index < k), nil
		}
		return truth(index >= n-k), nil
	case "quantile":
		q := *e.Args[1].Value
		return math.Min(q-1, math.Floor(float64(index)*q/float64(n))), nil
	}
	return 0, errors.New("unknown cross-sectional operator")
}

var ErrUnavailable = errors.New("calculation data unavailable")

func (c context) eval(e Expr, depth int) (float64, error) {
	if depth > 32 {
		return 0, errors.New("expression depth exceeded")
	}
	if e.Value != nil {
		return *e.Value, nil
	}
	if e.Name != "" {
		if calc, ok := c.program.Calculations[e.Name]; ok {
			return c.eval(calc, depth+1)
		}
		return c.runtime.Variables[e.Name], nil
	}
	if e.Metric != "" {
		return c.metric(e)
	}
	if csOp(e.Op) {
		return c.crossSectional(e)
	}
	if e.Op == "if" {
		cond, err := c.eval(e.Args[0], depth+1)
		if err != nil {
			return 0, err
		}
		if cond != 0 {
			return c.eval(e.Args[1], depth+1)
		}
		return c.eval(e.Args[2], depth+1)
	}
	if e.Op == "crosses_above" || e.Op == "crosses_below" {
		lhs, err := c.eval(e.Args[0], depth+1)
		if err != nil {
			return 0, err
		}
		rhs, err := c.eval(e.Args[1], depth+1)
		if err != nil {
			return 0, err
		}
		base, err := c.crossBars()
		if err != nil {
			return 0, err
		}
		baseHistory := base.Bars[c.program.Timeframe]
		if len(baseHistory) < 2 {
			return 0, ErrUnavailable
		}
		baseDuration, _ := duration(c.program.Timeframe)
		c.cutoff = baseHistory[len(baseHistory)-2].At.Add(baseDuration)
		pl, err := c.eval(e.Args[0], depth+1)
		if err != nil {
			return 0, err
		}
		pr, err := c.eval(e.Args[1], depth+1)
		if err != nil {
			return 0, err
		}
		if e.Op == "crosses_above" {
			return truth(pl <= pr && lhs > rhs), nil
		}
		return truth(pl >= pr && lhs < rhs), nil
	}
	a, err := c.eval(e.Args[0], depth+1)
	if err != nil {
		return 0, err
	}
	if e.Op == "not" {
		return truth(a == 0), nil
	}
	switch e.Op {
	case "abs":
		return math.Abs(a), nil
	case "sign":
		return math.Copysign(truth(a != 0), a), nil
	case "log":
		if a <= 0 {
			return 0, ErrUnavailable
		}
		return math.Log(a), nil
	case "sqrt":
		if a < 0 {
			return 0, ErrUnavailable
		}
		return math.Sqrt(a), nil
	case "exp":
		v := math.Exp(a)
		if !finite(v) {
			return 0, errors.New("nonfinite expression result")
		}
		return v, nil
	case "clamp":
		lo, err := c.eval(e.Args[1], depth+1)
		if err != nil {
			return 0, err
		}
		hi, err := c.eval(e.Args[2], depth+1)
		if err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, errors.New("clamp lower bound exceeds upper bound")
		}
		return math.Max(lo, math.Min(hi, a)), nil
	}
	// Short circuit guards (window_complete, flat, etc.) deliberately avoid
	// evaluating indicators with missing warmup on the inactive branch.
	if e.Op == "and" || e.Op == "or" {
		for _, arg := range e.Args[1:] {
			if e.Op == "and" && a == 0 {
				return 0, nil
			}
			if e.Op == "or" && a != 0 {
				return 1, nil
			}
			a, err = c.eval(arg, depth+1)
			if err != nil {
				return 0, err
			}
		}
		return truth(a != 0), nil
	}
	b, err := c.eval(e.Args[1], depth+1)
	if err != nil {
		return 0, err
	}
	var v float64
	switch e.Op {
	case "+":
		v = a + b
	case "-":
		v = a - b
	case "*":
		v = a * b
	case "/":
		if b == 0 {
			return 0, ErrUnavailable
		}
		v = a / b
	case "min":
		v = math.Min(a, b)
	case "max":
		v = math.Max(a, b)
	case ">":
		v = truth(a > b)
	case ">=":
		v = truth(a >= b)
	case "<":
		v = truth(a < b)
	case "<=":
		v = truth(a <= b)
	case "==":
		v = truth(a == b)
	case "!=":
		v = truth(a != b)
	default:
		return 0, errors.New("unknown operation")
	}
	if !finite(v) {
		return 0, errors.New("nonfinite expression result")
	}
	return v, nil
}
func truth(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
func (c context) metric(e Expr) (float64, error) {
	p, s := c.program, c.state
	if e.Symbol != "" {
		c.symbol = e.Symbol
	} else if c.symbol == "" && len(p.Symbols) == 1 {
		c.symbol = p.Symbols[0]
	}
	loc := c.runtime.timezone(p)
	local := s.Now.In(loc)
	switch e.Metric {
	case "minute", "weekday", "day_of_month", "month", "equity":
	default:
		if c.symbol == "" && e.Input == nil {
			return 0, errors.New("metric needs a symbol outside for_each rules")
		}
	}
	switch e.Metric {
	case "feature":
		fields, ok := s.Features[c.symbol+"/"+e.Feature]
		if !ok {
			return 0, ErrUnavailable
		}
		v, ok := fields[e.Field]
		if !ok {
			return 0, ErrUnavailable
		}
		return v, nil
	case "price":
		return s.Quotes[c.symbol].Price, nil
	case "bid":
		return s.Quotes[c.symbol].Bid, nil
	case "ask":
		return s.Quotes[c.symbol].Ask, nil
	case "position_qty":
		return s.Positions[c.symbol].Qty, nil
	case "minute":
		return float64(local.Hour()*60 + local.Minute()), nil
	case "weekday":
		return float64(local.Weekday()), nil
	case "day_of_month":
		return float64(local.Day()), nil
	case "month":
		return float64(local.Month()), nil
	case "equity":
		return sim.Equity(c.config, s), nil
	case "entry_count":
		count := 0
		for _, o := range s.Orders {
			if o.Symbol == c.symbol && !o.ReduceOnly && o.ParentID == "" && !o.FirstFillAt.IsZero() && o.FirstFillAt.In(loc).Format("2006-01-02") == local.Format("2006-01-02") {
				count++
			}
		}
		return float64(count), nil
	case "window_high", "window_low", "window_complete":
		r, err := c.bars()
		if err != nil {
			return 0, err
		}
		w := r.Windows[e.Window]
		if w.Day != local.Format("2006-01-02") {
			if e.Metric == "window_complete" {
				return 0, nil
			}
			return 0, ErrUnavailable
		}
		if e.Metric == "window_complete" {
			return truth(w.Complete), nil
		}
		if !w.Complete {
			return 0, ErrUnavailable
		}
		if e.Metric == "window_high" {
			return w.High, nil
		}
		return w.Low, nil
	}
	if e.Input != nil {
		n := e.Period
		count := n
		if e.Metric == "ema" {
			count = 5 * n
		}
		values := make([]float64, count)
		for i := 0; i < count; i++ {
			past := c
			past.lag += e.Offset + count - 1 - i
			v, err := past.eval(*e.Input, 0)
			if err != nil {
				return 0, err
			}
			values[i] = v
		}
		sum := 0.0
		for _, v := range values[:n] {
			sum += v
		}
		mean := sum / float64(n)
		if e.Metric == "ema" {
			for _, v := range values[n:] {
				mean += 2.0 / float64(n+1) * (v - mean)
			}
			return mean, nil
		}
		if e.Metric == "sma" {
			return mean, nil
		}
		variance := 0.0
		for _, v := range values {
			variance += (v - mean) * (v - mean)
		}
		return math.Sqrt(variance / float64(n)), nil
	}
	if e.Metric == "correlation" || e.Metric == "beta" {
		return c.comovement(e)
	}
	h, err := c.history(e)
	if err != nil {
		return 0, err
	}
	last := h[len(h)-1]
	switch e.Metric {
	case "open":
		return last.Open, nil
	case "high":
		return last.High, nil
	case "low":
		return last.Low, nil
	case "close":
		return last.Close, nil
	case "volume":
		return last.Volume, nil
	case "range":
		return last.High - last.Low, nil
	case "body":
		return last.Close - last.Open, nil
	case "close_fraction":
		if last.High == last.Low {
			return 0, ErrUnavailable
		}
		return (last.Close - last.Low) / (last.High - last.Low), nil
	}
	n := e.Period
	need := n
	if e.Metric == "atr" || e.Metric == "rsi" || e.Metric == "return" || e.Metric == "volatility" {
		need++
	}
	if len(h) < need {
		return 0, ErrUnavailable
	}
	switch e.Metric {
	case "highest_high", "lowest_low":
		v := h[len(h)-n].High
		if e.Metric == "lowest_low" {
			v = h[len(h)-n].Low
		}
		for _, bar := range h[len(h)-n:] {
			if e.Metric == "highest_high" {
				v = math.Max(v, bar.High)
			} else {
				v = math.Min(v, bar.Low)
			}
		}
		return v, nil
	case "stddev", "volatility":
		values := make([]float64, n)
		mean := 0.0
		for i := 0; i < n; i++ {
			v := h[len(h)-n+i].Close
			if e.Metric == "volatility" {
				v = math.Log(v / h[len(h)-n+i-1].Close)
			}
			values[i] = v
			mean += v / float64(n)
		}
		variance := 0.0
		for _, v := range values {
			variance += (v - mean) * (v - mean)
		}
		return math.Sqrt(variance / float64(n)), nil
	case "sma":
		v := 0.0
		for _, b := range h[len(h)-n:] {
			v += b.Close
		}
		return v / float64(n), nil
	case "return":
		return (last.Close/h[len(h)-n-1].Close - 1) * 100, nil
	case "ema":
		// Bounded SMA seed keeps replay/checkpoint behavior independent of tape length.
		warm := 5 * n
		if len(h) < warm {
			return 0, ErrUnavailable
		}
		h = h[len(h)-warm:]
		v := 0.0
		for _, b := range h[:n] {
			v += b.Close
		}
		v /= float64(n)
		for _, b := range h[n:] {
			v += 2.0 / float64(n+1) * (b.Close - v)
		}
		return v, nil
	case "atr", "rsi":
		// Fixed Wilder initialization: seed n true ranges/changes, then smooth
		// the remaining bounded 5n observations. No high/low is discarded.
		warm := 5*n + 1
		if len(h) < warm {
			return 0, ErrUnavailable
		}
		h = h[len(h)-warm:]
		a, b := 0.0, 0.0
		for i := 1; i < len(h); i++ {
			x, y := 0.0, 0.0
			if e.Metric == "atr" {
				x = math.Max(h[i].High-h[i].Low, math.Max(math.Abs(h[i].High-h[i-1].Close), math.Abs(h[i].Low-h[i-1].Close)))
			} else {
				delta := h[i].Close - h[i-1].Close
				x = math.Max(delta, 0)
				y = math.Max(-delta, 0)
			}
			if i <= n {
				a += x / float64(n)
				b += y / float64(n)
			} else {
				a = (a*float64(n-1) + x) / float64(n)
				b = (b*float64(n-1) + y) / float64(n)
			}
		}
		if e.Metric == "atr" {
			return a, nil
		}
		if a == 0 && b == 0 {
			return 50, nil
		}
		if b == 0 {
			return 100, nil
		}
		return 100 - 100/(1+a/b), nil
	}
	return 0, fmt.Errorf("unknown metric %s", e.Metric)
}

// history is the current symbol's candle series for e's frame, cut at the
// crossover cutoff and shifted by offset and lag. It is never empty.
func (c context) history(e Expr) ([]Candle, error) {
	r, err := c.bars()
	if err != nil {
		return nil, err
	}
	frame := e.Timeframe
	if frame == "" {
		frame = c.program.Timeframe
	}
	h := r.Bars[frame]
	if !c.cutoff.IsZero() {
		d, _ := duration(frame)
		end := len(h)
		for end > 0 && h[end-1].At.Add(d).After(c.cutoff) {
			end--
		}
		h = h[:end]
	}
	offset := e.Offset + c.lag
	if len(h) <= offset {
		return nil, ErrUnavailable
	}
	return h[:len(h)-offset], nil
}

// crossBars picks the series whose base candles define a crossover's previous
// moment: the current symbol, or outside for_each the first symbol with data.
func (c context) crossBars() (*Runtime, error) {
	if c.symbol != "" || len(c.program.Symbols) == 1 {
		return c.bars()
	}
	for _, symbol := range ProgramSymbols(c.program) {
		if r := c.series[symbol]; r != nil && len(r.Bars[c.program.Timeframe]) >= 2 {
			return r, nil
		}
	}
	return nil, ErrUnavailable
}

// comovement is the rolling correlation, or beta against e.Against, of
// close-to-close log returns over e.Period candles. Both series must share
// the same candle timestamps over the window.
func (c context) comovement(e Expr) (float64, error) {
	a, err := c.history(e)
	if err != nil {
		return 0, err
	}
	other := c
	other.symbol = e.Against
	b, err := other.history(e)
	if err != nil {
		return 0, err
	}
	n := e.Period
	if len(a) < n+1 || len(b) < n+1 {
		return 0, ErrUnavailable
	}
	a, b = a[len(a)-n-1:], b[len(b)-n-1:]
	x, y := make([]float64, n), make([]float64, n)
	mx, my := 0.0, 0.0
	for i := 0; i < n; i++ {
		if !a[i+1].At.Equal(b[i+1].At) || !a[i].At.Equal(b[i].At) {
			return 0, ErrUnavailable
		}
		x[i] = math.Log(a[i+1].Close / a[i].Close)
		y[i] = math.Log(b[i+1].Close / b[i].Close)
		mx += x[i] / float64(n)
		my += y[i] / float64(n)
	}
	cov, vx, vy := 0.0, 0.0, 0.0
	for i := 0; i < n; i++ {
		cov += (x[i] - mx) * (y[i] - my)
		vx += (x[i] - mx) * (x[i] - mx)
		vy += (y[i] - my) * (y[i] - my)
	}
	if vy == 0 || e.Metric == "correlation" && vx == 0 {
		return 0, ErrUnavailable
	}
	if e.Metric == "beta" {
		return cov / vy, nil
	}
	return cov / math.Sqrt(vx*vy), nil
}
