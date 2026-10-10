package ruleengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
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
type Runtime struct {
	limits       map[string]int
	location     *time.Location
	cacheEnabled bool
	encodedBars  map[string]json.RawMessage
	Bars         map[string][]Candle    `json:"bars"`
	Aggregates   map[string]aggregate   `json:"aggregates"`
	Windows      map[string]windowState `json:"windows"`
	Variables    map[string]float64     `json:"variables"`
	Fired        map[string]string      `json:"fired"`
}

// Compiled evaluators own their runtime and invalidate a frame on append.
// Preserve the public checkpoint format while reusing unchanged higher-frame
// JSON during minute signals. Ordinary Runtime values use uncached encoding.
func (r *Runtime) MarshalJSON() ([]byte, error) {
	return r.marshalCheckpoint()
}

func (r *Runtime) marshalCheckpoint() ([]byte, error) {
	if !r.cacheEnabled {
		type plain Runtime
		return json.Marshal((*plain)(r))
	}
	if r.encodedBars == nil {
		r.encodedBars = map[string]json.RawMessage{}
	}
	for frame, bars := range r.Bars {
		if _, ok := r.encodedBars[frame]; !ok {
			raw, err := json.Marshal(bars)
			if err != nil {
				return nil, err
			}
			r.encodedBars[frame] = raw
		}
	}
	rest, err := json.Marshal(struct {
		Aggregates map[string]aggregate   `json:"aggregates"`
		Windows    map[string]windowState `json:"windows"`
		Variables  map[string]float64     `json:"variables"`
		Fired      map[string]string      `json:"fired"`
	}{r.Aggregates, r.Windows, r.Variables, r.Fired})
	if err != nil {
		return nil, err
	}
	frames := make([]string, 0, len(r.Bars))
	size := len(rest) + 16
	for frame := range r.Bars {
		frames = append(frames, frame)
		size += len(r.encodedBars[frame]) + len(frame) + 4
	}
	sort.Strings(frames)
	// Every reused fragment was produced by encoding/json. Concatenate those
	// trusted fragments directly instead of validating all hourly history again.
	out := make([]byte, 0, size)
	out = append(out, `{"bars":{`...)
	for i, frame := range frames {
		if i > 0 {
			out = append(out, ',')
		}
		key, _ := json.Marshal(frame)
		out = append(out, key...)
		out = append(out, ':')
		out = append(out, r.encodedBars[frame]...)
	}
	out = append(out, '}', ',')
	out = append(out, rest[1:]...)
	return out, nil
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
	delete(r.encodedBars, frame)
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
			for _, expr := range []*Expr{action.Price, action.Stop, action.Target, action.Value} {
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
			for _, e := range []*Expr{a.Price, a.Stop, a.Target, a.Value} {
				if e != nil {
					visit(*e)
				}
			}
		}
	}
	return out
}
func (r *Runtime) observe(p *Program, in sim.Input) (map[string]bool, error) {
	closed := map[string]bool{}
	if in.Type != "market.bar.close" || in.Symbol != p.Symbol {
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
	if c.Open <= 0 || c.Low <= 0 || c.High < math.Max(c.Open, c.Close) || c.Low > math.Min(c.Open, c.Close) || in.AvailableAt.Before(c.At.Add(d)) {
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
	runtime *Runtime
	state   *sim.State
	config  sim.Config
	lag     int
	cutoff  time.Time
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
	if e.Op == "crosses_above" || e.Op == "crosses_below" {
		lhs, err := c.eval(e.Args[0], depth+1)
		if err != nil {
			return 0, err
		}
		rhs, err := c.eval(e.Args[1], depth+1)
		if err != nil {
			return 0, err
		}
		baseHistory := c.runtime.Bars[c.program.Timeframe]
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
	if e.Op == "abs" {
		return math.Abs(a), nil
	}
	// Short circuit guards (window_complete, flat, etc.) deliberately avoid
	// evaluating indicators with missing warmup on the inactive branch.
	if e.Op == "and" && a == 0 {
		return 0, nil
	}
	if e.Op == "or" && a != 0 {
		return 1, nil
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
	case "and":
		v = truth(a != 0 && b != 0)
	case "or":
		v = truth(a != 0 || b != 0)
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
	p, r, s := c.program, c.runtime, c.state
	loc := r.timezone(p)
	local := s.Now.In(loc)
	switch e.Metric {
	case "feature":
		fields, ok := s.Features[p.Symbol+"/"+e.Feature]
		if !ok {
			return 0, ErrUnavailable
		}
		v, ok := fields[e.Field]
		if !ok {
			return 0, ErrUnavailable
		}
		return v, nil
	case "price":
		return s.Quotes[p.Symbol].Price, nil
	case "bid":
		return s.Quotes[p.Symbol].Bid, nil
	case "ask":
		return s.Quotes[p.Symbol].Ask, nil
	case "position_qty":
		return s.Positions[p.Symbol].Qty, nil
	case "minute":
		return float64(local.Hour()*60 + local.Minute()), nil
	case "weekday":
		return float64(local.Weekday()), nil
	case "equity":
		return sim.Equity(c.config, s), nil
	case "entry_count":
		count := 0
		for _, o := range s.Orders {
			if o.Symbol == p.Symbol && !o.ReduceOnly && o.ParentID == "" && !o.FirstFillAt.IsZero() && o.FirstFillAt.In(loc).Format("2006-01-02") == local.Format("2006-01-02") {
				count++
			}
		}
		return float64(count), nil
	case "window_high", "window_low", "window_complete":
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
	frame := e.Timeframe
	if frame == "" {
		frame = p.Timeframe
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
		return 0, ErrUnavailable
	}
	h = h[:len(h)-offset]
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
