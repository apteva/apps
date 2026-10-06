// Package backtest implements a deterministic discrete-event simulator. It has
// no database, network, wall clock, or application dependencies.
package backtest

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const Version = "event-sim/1"

// Input.Data is deliberately generic. Built-in inputs are market.quote,
// market.bar.close and feature.<name>; additional types reach the strategy too.
// AvailableAt, not EventTime, determines when a strategy may observe an input.
type Input struct {
	ID          string             `json:"id"`
	Type        string             `json:"type"`
	Symbol      string             `json:"symbol,omitempty"`
	Source      string             `json:"source,omitempty"`
	EventTime   time.Time          `json:"event_time"`
	AvailableAt time.Time          `json:"available_at"`
	Data        map[string]float64 `json:"data"`
	Metadata    map[string]string  `json:"metadata,omitempty"`
}

type Costs struct {
	FeeBps      float64 `json:"fee_bps"`
	SlippageBps float64 `json:"slippage_bps"`
	SpreadBps   float64 `json:"spread_bps"`
	ImpactBps   float64 `json:"impact_bps"`
	QtyStep     float64 `json:"qty_step"`
	MinQty      float64 `json:"min_qty"`
	MinNotional float64 `json:"min_notional"`
}

type Config struct {
	NotifyFills           bool             `json:"notify_fills,omitempty"`
	Risk                  RiskLimits       `json:"risk"`
	Seed                  uint64           `json:"seed"`
	StartingCash          float64          `json:"starting_cash"`
	SubmissionLatencyMS   int64            `json:"submission_latency_ms"`
	CancellationLatencyMS int64            `json:"cancellation_latency_ms"`
	LatencyJitterMS       int64            `json:"latency_jitter_ms"`
	MaxFillQty            float64          `json:"max_fill_qty"`       // shared capacity per symbol per quote; zero is unlimited
	ParticipationRate     float64          `json:"participation_rate"` // zero disables volume limits
	Costs                 Costs            `json:"costs"`
	SymbolCosts           map[string]Costs `json:"symbol_costs,omitempty"`
	BenchmarkSymbol       string           `json:"benchmark_symbol,omitempty"`
}

type RiskLimits struct {
	MaxOrderPct         float64 `json:"max_order_pct"`
	MaxPositionPct      float64 `json:"max_position_pct"`
	MaxGrossExposurePct float64 `json:"max_gross_exposure_pct"`
	MaxDailyLossPct     float64 `json:"max_daily_loss_pct"`
	MaxDrawdownPct      float64 `json:"max_drawdown_pct"`
}

type Order struct {
	ID           string    `json:"id"`
	Symbol       string    `json:"symbol"`
	Side         string    `json:"side"`
	Type         string    `json:"type"`
	Qty          float64   `json:"qty"`
	LimitPrice   float64   `json:"limit_price,omitempty"`
	StopPrice    float64   `json:"stop_price,omitempty"`
	TIF          string    `json:"tif"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	SubmittedAt  time.Time `json:"submitted_at"`
	AcceptedAt   time.Time `json:"accepted_at,omitempty"`
	ResolvedAt   time.Time `json:"resolved_at,omitempty"`
	Status       string    `json:"status"`
	FilledQty    float64   `json:"filled_qty"`
	AvgFillPrice float64   `json:"avg_fill_price"`
	Fees         float64   `json:"fees"`
	Triggered    bool      `json:"triggered,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

type Position struct {
	Qty     float64 `json:"qty"`
	AvgCost float64 `json:"avg_cost"`
}

type Quote struct {
	Price  float64   `json:"price"`
	Bid    float64   `json:"bid,omitempty"`
	Ask    float64   `json:"ask,omitempty"`
	Volume float64   `json:"volume,omitempty"`
	At     time.Time `json:"at"`
}

type Scheduled struct {
	At      time.Time `json:"at"`
	Type    string    `json:"type"`
	OrderID string    `json:"order_id"`
	Seq     uint64    `json:"seq"`
}

// State is a portable checkpoint. Pending contains only internally scheduled
// events; Cursor refers to the canonical immutable input tape.
type State struct {
	Day            string                        `json:"day"`
	DayStartEquity float64                       `json:"day_start_equity"`
	Cursor         int                           `json:"cursor"`
	Now            time.Time                     `json:"now"`
	Sequence       uint64                        `json:"sequence"`
	OrderSequence  uint64                        `json:"order_sequence"`
	Cash           float64                       `json:"cash"`
	RealizedPnL    float64                       `json:"realized_pnl"`
	Fees           float64                       `json:"fees"`
	Turnover       float64                       `json:"turnover"`
	Peak           float64                       `json:"peak"`
	MaxDrawdownPct float64                       `json:"max_drawdown_pct"`
	BenchmarkQty   float64                       `json:"benchmark_qty"`
	Positions      map[string]Position           `json:"positions"`
	Quotes         map[string]Quote              `json:"quotes"`
	Features       map[string]map[string]float64 `json:"features"`
	Orders         []*Order                      `json:"orders"`
	Pending        []Scheduled                   `json:"pending"`
	StrategyState  json.RawMessage               `json:"strategy_state,omitempty"`
	Finished       bool                          `json:"finished"`
}

type Output struct {
	Sequence uint64          `json:"sequence"`
	At       time.Time       `json:"at"`
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data"`
}

type Command struct {
	Report   map[string]any
	Order    *Order
	CancelID string
}

type Strategy func(*State, Input) ([]Command, error)

type Engine struct {
	Config   Config
	Inputs   []Input
	State    *State
	Strategy Strategy
	outputs  []Output
}

func Hash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func New(config Config, inputs []Input, state *State, strategy Strategy) (*Engine, error) {
	if !finite(config.StartingCash) || config.StartingCash <= 0 {
		return nil, errors.New("starting cash must be positive and finite")
	}
	if config.SubmissionLatencyMS < 0 || config.CancellationLatencyMS < 0 || config.LatencyJitterMS < 0 || config.SubmissionLatencyMS > 86400000 || config.CancellationLatencyMS > 86400000 || config.LatencyJitterMS > 86400000 {
		return nil, errors.New("latencies must be between zero and one day")
	}
	if !finite(config.MaxFillQty) || config.MaxFillQty < 0 || !finite(config.ParticipationRate) || config.ParticipationRate < 0 || config.ParticipationRate > 1 {
		return nil, errors.New("invalid fill capacity or participation rate")
	}
	for _, v := range []float64{config.Risk.MaxOrderPct, config.Risk.MaxPositionPct, config.Risk.MaxGrossExposurePct, config.Risk.MaxDailyLossPct, config.Risk.MaxDrawdownPct} {
		if !finite(v) || v < 0 {
			return nil, errors.New("risk limits must be finite and nonnegative")
		}
	}
	profiles := []Costs{config.Costs}
	for _, p := range config.SymbolCosts {
		profiles = append(profiles, p)
	}
	for _, p := range profiles {
		for _, v := range []float64{p.FeeBps, p.SlippageBps, p.SpreadBps, p.ImpactBps, p.QtyStep, p.MinQty, p.MinNotional} {
			if !finite(v) || v < 0 {
				return nil, errors.New("execution costs and constraints must be finite and nonnegative")
			}
		}
		if p.SlippageBps+p.SpreadBps/2+p.ImpactBps >= 10000 {
			return nil, errors.New("execution adjustment must be below 100%")
		}
	}
	tape := append([]Input(nil), inputs...)
	seen := map[string]bool{}
	quoteTimes := map[string]bool{}
	for i := range tape {
		in := &tape[i]
		if in.ID == "" || seen[in.ID] || in.Type == "" || in.EventTime.IsZero() || in.AvailableAt.Before(in.EventTime) {
			return nil, errors.New("inputs require unique IDs, a type, and availability at or after event time")
		}
		seen[in.ID] = true
		if in.Type == "market.quote" {
			key := in.Symbol + "/" + in.AvailableAt.UTC().Format(time.RFC3339Nano)
			if quoteTimes[key] {
				return nil, errors.New("multiple quote snapshots for one symbol at the same availability time; supply distinct timestamps or coalesce the snapshot")
			}
			quoteTimes[key] = true
		}
		data := map[string]float64{}
		for key, value := range in.Data {
			data[key] = value
		}
		in.Data = data
		metadata := map[string]string{}
		for key, value := range in.Metadata {
			metadata[key] = value
		}
		in.Metadata = metadata
		in.EventTime, in.AvailableAt = in.EventTime.UTC(), in.AvailableAt.UTC()
		for _, v := range in.Data {
			if !finite(v) {
				return nil, errors.New("input values must be finite")
			}
		}
		if strings.HasPrefix(in.Type, "market.") && (in.Symbol == "" || in.Data["price"] <= 0 || in.Data["volume"] < 0 || in.Data["bid"] < 0 || in.Data["ask"] < 0 || (in.Data["bid"] > 0 && in.Data["ask"] > 0 && in.Data["bid"] > in.Data["ask"])) {
			return nil, errors.New("market inputs require a symbol, positive price and nonnegative volume")
		}
	}
	// Closing information is published before the next opening quote at the
	// same timestamp. Features arriving then are visible to the closing signal.
	sort.Slice(tape, func(i, j int) bool {
		a, b := tape[i], tape[j]
		if !a.AvailableAt.Equal(b.AvailableAt) {
			return a.AvailableAt.Before(b.AvailableAt)
		}
		if inputPriority(a.Type) != inputPriority(b.Type) {
			return inputPriority(a.Type) < inputPriority(b.Type)
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.ID < b.ID
	})
	if len(tape) == 0 {
		return nil, errors.New("at least one input required")
	}
	if state == nil {
		state = &State{Cash: config.StartingCash, Peak: config.StartingCash, Positions: map[string]Position{}, Quotes: map[string]Quote{}, Features: map[string]map[string]float64{}, Orders: []*Order{}, Pending: []Scheduled{}}
	}
	if state.Cursor < 0 || state.Cursor > len(tape) || state.Positions == nil || state.Quotes == nil || state.Features == nil {
		return nil, errors.New("invalid checkpoint")
	}
	return &Engine{Config: config, Inputs: tape, State: state, Strategy: strategy}, nil
}

func inputPriority(kind string) int {
	if strings.HasPrefix(kind, "feature.") {
		return 0
	}
	if kind == "market.bar.close" {
		return 1
	}
	if kind == "market.quote" {
		return 3
	}
	return 2
}

func (e *Engine) record(kind string, v any) {
	e.State.Sequence++
	raw, _ := json.Marshal(v)
	e.outputs = append(e.outputs, Output{Sequence: e.State.Sequence, At: e.State.Now, Type: kind, Data: raw})
}

func (e *Engine) schedule(kind, id string, at time.Time) {
	e.State.Sequence++
	e.State.Pending = append(e.State.Pending, Scheduled{Type: kind, OrderID: id, At: at, Seq: e.State.Sequence})
	sort.Slice(e.State.Pending, func(i, j int) bool {
		a, b := e.State.Pending[i], e.State.Pending[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		return a.Seq < b.Seq
	})
}

func (e *Engine) latency(base int64, id string) time.Duration {
	jitter := int64(0)
	if e.Config.LatencyJitterMS > 0 {
		h := sha256.Sum256([]byte(fmt.Sprintf("%d/%s", e.Config.Seed, id)))
		v := uint64(0)
		for _, b := range h[:8] {
			v = v<<8 | uint64(b)
		}
		jitter = int64(v % uint64(e.Config.LatencyJitterMS+1))
	}
	return time.Duration(base+jitter) * time.Millisecond
}

func (e *Engine) command(c Command) {
	if c.Report != nil {
		e.record("strategy.decision", c.Report)
		return
	}
	if c.CancelID != "" {
		e.record("order.cancel_requested", map[string]string{"order_id": c.CancelID})
		e.schedule("cancel", c.CancelID, e.State.Now.Add(e.latency(e.Config.CancellationLatencyMS, "cancel/"+c.CancelID)))
		return
	}
	if c.Order == nil {
		return
	}
	o := *c.Order
	e.State.OrderSequence++
	o.ID = fmt.Sprintf("sim-%016x-%08d", e.Config.Seed, e.State.OrderSequence)
	o.SubmittedAt = e.State.Now
	o.Status = "submitted"
	o.FilledQty = 0
	o.AvgFillPrice = 0
	o.Fees = 0
	o.AcceptedAt = time.Time{}
	o.ResolvedAt = time.Time{}
	if o.Type == "" {
		o.Type = "market"
	}
	if o.TIF == "" {
		o.TIF = "gtc"
	}
	e.State.Orders = append(e.State.Orders, &o)
	e.record("order.submitted", o)
	if o.Symbol == "" || (o.Side != "buy" && o.Side != "sell") || !finite(o.Qty) || o.Qty <= 0 || (o.Type != "market" && o.Type != "limit" && o.Type != "stop") || (o.Type == "limit" && (!finite(o.LimitPrice) || o.LimitPrice <= 0)) || (o.Type == "stop" && (!finite(o.StopPrice) || o.StopPrice <= 0)) || (o.TIF != "gtc" && o.TIF != "ioc" && o.TIF != "day") || (o.TIF == "day" && o.ExpiresAt.IsZero()) {
		e.resolve(&o, "rejected", "invalid order")
		return
	}
	e.schedule("accept", o.ID, e.State.Now.Add(e.latency(e.Config.SubmissionLatencyMS, o.ID)))
	if !o.ExpiresAt.IsZero() {
		at := o.ExpiresAt
		if at.Before(e.State.Now) {
			at = e.State.Now
		}
		e.schedule("expire", o.ID, at)
	}
}

func active(o *Order) bool {
	return o.Status == "submitted" || o.Status == "working" || o.Status == "partially_filled"
}
func (e *Engine) resolve(o *Order, status, reason string) {
	o.Status = status
	o.Reason = reason
	o.ResolvedAt = e.State.Now
	e.record("order."+status, *o)
}

// Advance commits one simulated timestamp, irrespective of wall-clock speed.
// A caller must atomically persist State and returned outputs, or discard the
// engine instance on error. Fills require a fresh quote at this timestamp.
func (e *Engine) Advance() ([]Output, error) {
	e.outputs = nil
	s := e.State
	if s.Finished {
		return nil, nil
	}
	var at time.Time
	if s.Cursor < len(e.Inputs) {
		at = e.Inputs[s.Cursor].AvailableAt
	}
	if len(s.Pending) > 0 && (at.IsZero() || s.Pending[0].At.Before(at)) {
		at = s.Pending[0].At
	}
	// Do not invent liquidity or continue beyond the immutable input horizon.
	if at.IsZero() || at.After(e.Inputs[len(e.Inputs)-1].AvailableAt) {
		for _, o := range s.Orders {
			if active(o) {
				e.resolve(o, "expired", "end of input tape")
			}
		}
		s.Pending = nil
		s.Finished = true
		e.record("run.completed", e.Metrics())
		return e.outputs, nil
	}
	s.Now = at
	day := at.UTC().Format("2006-01-02")
	if day != s.Day {
		s.DayStartEquity = e.Metrics()["equity"]
		s.Day = day
	}
	fresh := map[string]bool{}
	for s.Cursor < len(e.Inputs) && e.Inputs[s.Cursor].AvailableAt.Equal(at) {
		in := e.Inputs[s.Cursor]
		s.Cursor++
		e.record("input", in)
		if strings.HasPrefix(in.Type, "feature.") {
			s.Features[in.Symbol+"/"+strings.TrimPrefix(in.Type, "feature.")] = in.Data
		}
		if in.Type == "market.quote" {
			s.Quotes[in.Symbol] = Quote{Price: in.Data["price"], Bid: in.Data["bid"], Ask: in.Data["ask"], Volume: in.Data["volume"], At: at}
			fresh[in.Symbol] = true
			if in.Symbol == e.Config.BenchmarkSymbol && s.BenchmarkQty == 0 {
				s.BenchmarkQty = e.Config.StartingCash / in.Data["price"]
			}
		} else if in.Type == "market.bar.close" {
			q := s.Quotes[in.Symbol]
			q.Price = in.Data["price"]
			s.Quotes[in.Symbol] = q
		}
		if e.Strategy != nil {
			commands, err := e.Strategy(s, in)
			if err != nil {
				return nil, err
			}
			for _, c := range commands {
				e.command(c)
			}
		}
	}
	for len(s.Pending) > 0 && !s.Pending[0].At.After(at) {
		x := s.Pending[0]
		s.Pending = s.Pending[1:]
		for _, o := range s.Orders {
			if o.ID != x.OrderID || !active(o) {
				continue
			}
			switch x.Type {
			case "accept":
				if o.Status == "submitted" {
					o.Status = "working"
					o.AcceptedAt = at
					e.record("order.accepted", *o)
				}
			case "cancel":
				e.resolve(o, "cancelled", "cancel acknowledged")
			case "expire":
				e.resolve(o, "expired", "time in force elapsed")
			}
		}
	}
	e.fill(fresh)
	// Agents can react to executions using the same deterministic callback.
	// New orders are acknowledged in a later Advance, never filled twice against
	// the quote that just supplied liquidity.
	if e.Config.NotifyFills && e.Strategy != nil {
		completed := append([]Output(nil), e.outputs...)
		for _, out := range completed {
			if out.Type != "fill" {
				continue
			}
			var fields map[string]any
			if err := json.Unmarshal(out.Data, &fields); err != nil {
				return nil, err
			}
			in := Input{ID: fmt.Sprintf("execution/%d", out.Sequence), Type: "execution.fill", Source: "simulator", EventTime: s.Now, AvailableAt: s.Now, Data: map[string]float64{}, Metadata: map[string]string{}}
			for key, value := range fields {
				switch v := value.(type) {
				case float64:
					in.Data[key] = v
				case string:
					in.Metadata[key] = v
				}
			}
			in.Symbol = in.Metadata["symbol"]
			e.record("execution.notification", in)
			commands, err := e.Strategy(s, in)
			if err != nil {
				return nil, err
			}
			for _, c := range commands {
				e.command(c)
			}
		}
	}
	metrics := e.Metrics()
	equity := metrics["equity"]
	if equity > s.Peak {
		s.Peak = equity
	}
	if s.Peak > 0 {
		s.MaxDrawdownPct = math.Min(s.MaxDrawdownPct, (equity/s.Peak-1)*100)
	}
	e.record("portfolio.snapshot", e.Metrics())
	return e.outputs, nil
}

func (e *Engine) costs(symbol string) Costs {
	if c, ok := e.Config.SymbolCosts[symbol]; ok {
		return c
	}
	return e.Config.Costs
}

// Decimal lot sizes rarely divide exactly in binary floats. Account for a few
// ULPs and a tiny fraction of a lot at the original order/position scale,
// including accumulated position error and cancellation error when
// subtracting prior fills. A fixed epsilon in lot units loses real lots on large
// orders and can leave an unfillable remainder blocking subsequent rebalances.
func floorFillQuantity(qty, step, scale float64) float64 {
	scale = math.Max(math.Abs(qty), math.Abs(scale))
	epsilon := math.Min(math.Max(16*(math.Nextafter(scale, math.Inf(1))-scale), step*1e-8), step*1e-6)
	nearest := math.Round(qty/step) * step
	if math.Abs(nearest-qty) <= epsilon {
		return nearest
	}
	return math.Floor(qty/step) * step
}

func (e *Engine) fill(fresh map[string]bool) {
	s := e.State
	orders := append([]*Order(nil), s.Orders...)
	// Sell proceeds are available to simultaneous buys, independent of symbol order.
	sort.SliceStable(orders, func(i, j int) bool { return orders[i].Side == "sell" && orders[j].Side != "sell" })
	used := map[string]float64{}
	for _, o := range orders {
		if (o.Status != "working" && o.Status != "partially_filled") || !fresh[o.Symbol] {
			continue
		}
		q := s.Quotes[o.Symbol]
		p := e.costs(o.Symbol)
		capacity := math.Inf(1)
		if e.Config.MaxFillQty > 0 {
			capacity = e.Config.MaxFillQty
		}
		if e.Config.ParticipationRate > 0 {
			capacity = math.Min(capacity, q.Volume*e.Config.ParticipationRate)
		}
		qty := math.Min(o.Qty-o.FilledQty, math.Max(0, capacity-used[o.Symbol]))
		price := q.Price
		buy := o.Side == "buy"
		if o.Type == "stop" && !o.Triggered {
			if (buy && price >= o.StopPrice) || (!buy && price <= o.StopPrice) {
				o.Triggered = true
			} else {
				continue
			}
		}
		if buy && q.Ask > 0 {
			price = q.Ask
		} else if !buy && q.Bid > 0 {
			price = q.Bid
		} else {
			sign := 1.0
			if !buy {
				sign = -1
			}
			price *= 1 + sign*p.SpreadBps/20000
		}
		touchPrice := price
		impact := 0.0
		if q.Volume > 0 {
			impact = p.ImpactBps * math.Min(1, qty/q.Volume)
		}
		sign := 1.0
		if !buy {
			sign = -1
		}
		price *= 1 + sign*(p.SlippageBps+impact)/10000
		if o.Type == "limit" && ((buy && price > o.LimitPrice) || (!buy && price < o.LimitPrice)) {
			if o.TIF == "ioc" {
				e.resolve(o, "cancelled", "IOC limit not marketable")
			}
			continue
		}
		if buy {
			qty = math.Min(qty, s.Cash/(price*(1+p.FeeBps/10000)))
		} else {
			qty = math.Min(qty, s.Positions[o.Symbol].Qty)
		}
		if p.QtyStep > 0 {
			qty = floorFillQuantity(qty, p.QtyStep, math.Max(o.Qty, s.Positions[o.Symbol].Qty))
		}
		if q.Volume > 0 {
			impact = p.ImpactBps * math.Min(1, qty/q.Volume)
		}
		price = touchPrice * (1 + sign*(p.SlippageBps+impact)/10000)
		if buy && qty*price*(1+p.FeeBps/10000) > s.Cash {
			qty = math.Nextafter(qty, 0)
			if p.QtyStep > 0 {
				qty = math.Floor(qty/p.QtyStep) * p.QtyStep
			}
		}
		if qty <= 1e-12 || qty < p.MinQty || qty*price < p.MinNotional {
			if o.TIF == "ioc" {
				e.resolve(o, "cancelled", "IOC has no executable quantity")
			}
			continue
		}
		if buy {
			m := e.Metrics()
			equity := m["equity"]
			risk := e.Config.Risk
			positionValue := s.Positions[o.Symbol].Qty * q.Price
			gross := m["exposure"] * equity / 100
			breach := ""
			if equity <= 0 {
				breach = "no equity"
			}
			if risk.MaxOrderPct > 0 && ((o.FilledQty*o.AvgFillPrice+qty*price)/equity*100) > risk.MaxOrderPct+1e-9 {
				breach = "maximum order exposure"
			}
			if risk.MaxPositionPct > 0 && ((positionValue+qty*price)/equity*100) > risk.MaxPositionPct+1e-9 {
				breach = "maximum position exposure"
			}
			if risk.MaxGrossExposurePct > 0 && ((gross+qty*price)/equity*100) > risk.MaxGrossExposurePct+1e-9 {
				breach = "maximum gross exposure"
			}
			if risk.MaxDrawdownPct > 0 && s.Peak > 0 && (1-equity/s.Peak)*100 > risk.MaxDrawdownPct {
				breach = "drawdown halt"
			}
			if risk.MaxDailyLossPct > 0 && s.DayStartEquity > 0 && (1-equity/s.DayStartEquity)*100 > risk.MaxDailyLossPct {
				breach = "daily loss halt"
			}
			if breach != "" {
				e.resolve(o, "rejected", breach)
				continue
			}
		}
		fee := qty * price * p.FeeBps / 10000
		pos := s.Positions[o.Symbol]
		if buy {
			pos.AvgCost = (pos.AvgCost*pos.Qty + price*qty) / (pos.Qty + qty)
			pos.Qty += qty
			s.Cash -= qty*price + fee
		} else {
			s.RealizedPnL += (price - pos.AvgCost) * qty
			pos.Qty -= qty
			s.Cash += qty*price - fee
		}
		if pos.Qty <= 1e-12 {
			delete(s.Positions, o.Symbol)
		} else {
			s.Positions[o.Symbol] = pos
		}
		o.AvgFillPrice = (o.AvgFillPrice*o.FilledQty + price*qty) / (o.FilledQty + qty)
		o.FilledQty += qty
		o.Fees += fee
		s.Fees += fee
		s.Turnover += qty * price
		used[o.Symbol] += qty
		o.Status = "partially_filled"
		if o.Qty-o.FilledQty <= 1e-9 {
			o.Status = "filled"
			o.ResolvedAt = s.Now
		}
		e.record("fill", map[string]any{"order_id": o.ID, "symbol": o.Symbol, "side": o.Side, "qty": qty, "price": price, "fee": fee, "reference_price": q.Price, "execution_cost": math.Abs(price-q.Price) * qty})
		e.record("order."+o.Status, *o)
		if o.TIF == "ioc" && active(o) {
			e.resolve(o, "cancelled", "IOC remainder")
		}
	}
}

func (e *Engine) Metrics() map[string]float64 {
	s := e.State
	equity := s.Cash
	open := 0.0
	exposure := 0.0
	symbols := make([]string, 0, len(s.Positions))
	for symbol := range s.Positions {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	for _, symbol := range symbols {
		p := s.Positions[symbol]
		price := s.Quotes[symbol].Price
		if price <= 0 {
			price = p.AvgCost
		}
		value := p.Qty * price
		equity += value
		exposure += value
		open += (price - p.AvgCost) * p.Qty
	}
	benchmark := e.Config.StartingCash
	if s.BenchmarkQty > 0 {
		benchmark = s.BenchmarkQty * s.Quotes[e.Config.BenchmarkSymbol].Price
	}
	ret := (equity/e.Config.StartingCash - 1) * 100
	br := (benchmark/e.Config.StartingCash - 1) * 100
	return map[string]float64{"equity": equity, "cash": s.Cash, "open_pnl": open, "realized_pnl": s.RealizedPnL, "total_pnl": equity - e.Config.StartingCash, "fees": s.Fees, "return_pct": ret, "benchmark_equity": benchmark, "benchmark_return_pct": br, "excess_return_pct": ret - br, "max_drawdown_pct": s.MaxDrawdownPct, "turnover_pct": s.Turnover / e.Config.StartingCash * 100, "exposure": exposure / math.Max(equity, 1e-12) * 100}
}
