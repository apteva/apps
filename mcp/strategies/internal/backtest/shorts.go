package backtest

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// Shorts opts a run into cash-settled spot short selling. A nil or disabled
// value keeps the original long-only spot accounting byte for byte.
//
// Margin follows the Reg T shape: the requirement is a fraction of gross spot
// market value (longs plus |shorts|) plus any contract margin. Orders that grow
// |position| past InitialMargin are rejected; when equity falls below
// MaintenanceMargin the simulator submits reduce-only covers back to halfway
// between maintenance and initial margin, recorded as margin calls. Borrow accrues continuously on |short value|. Borrow
// availability (locates, recalls, hard-to-borrow spikes) is not modelled beyond
// the explicit Unshortable list.
type Shorts struct {
	Enabled           bool               `json:"enabled"`
	InitialMargin     float64            `json:"initial_margin"`
	MaintenanceMargin float64            `json:"maintenance_margin"`
	BorrowBpsAnnual   float64            `json:"borrow_bps_annual"`
	SymbolBorrowBps   map[string]float64 `json:"symbol_borrow_bps,omitempty"`
	Unshortable       []string           `json:"unshortable,omitempty"`
}

// Reg T defaults and a general-collateral borrow rate.
const (
	DefaultInitialMargin     = 0.5
	DefaultMaintenanceMargin = 0.3
	DefaultBorrowBpsAnnual   = 25
)

// DefaultShorts returns an enabled configuration with the documented defaults.
func DefaultShorts() *Shorts {
	return &Shorts{Enabled: true, InitialMargin: DefaultInitialMargin, MaintenanceMargin: DefaultMaintenanceMargin, BorrowBpsAnnual: DefaultBorrowBpsAnnual}
}

const marginCallTag = "margin_call"

var yearDuration = 365 * 24 * time.Hour

func validateShorts(s *Shorts) error {
	if s == nil || !s.Enabled {
		return nil
	}
	if !finite(s.InitialMargin) || s.InitialMargin <= 0 || s.InitialMargin > 1 || !finite(s.MaintenanceMargin) || s.MaintenanceMargin <= 0 || s.MaintenanceMargin > s.InitialMargin {
		return errors.New("shorts require 0 < maintenance_margin <= initial_margin <= 1")
	}
	if !finite(s.BorrowBpsAnnual) || s.BorrowBpsAnnual < 0 || s.BorrowBpsAnnual > 100000 {
		return errors.New("borrow_bps_annual must be between 0 and 100000")
	}
	for symbol, bps := range s.SymbolBorrowBps {
		if symbol == "" || !finite(bps) || bps < 0 || bps > 100000 {
			return errors.New("symbol_borrow_bps values must be between 0 and 100000")
		}
	}
	return nil
}

func (e *Engine) shortsEnabled() bool {
	return e.Config.Shorts != nil && e.Config.Shorts.Enabled
}

func (e *Engine) shortable(symbol string) bool {
	if !e.shortsEnabled() {
		return false
	}
	if _, contract := e.Config.Contracts[symbol]; contract {
		return false
	}
	for _, s := range e.Config.Shorts.Unshortable {
		if s == symbol {
			return false
		}
	}
	return true
}

func (e *Engine) borrowBps(symbol string) float64 {
	if bps, ok := e.Config.Shorts.SymbolBorrowBps[symbol]; ok {
		return bps
	}
	return e.Config.Shorts.BorrowBpsAnnual
}

func (e *Engine) mark(symbol string) float64 {
	pos := e.State.Positions[symbol]
	price := e.State.Quotes[symbol].Price
	if price <= 0 {
		price = pos.AvgCost
	}
	return price
}

// spotGross is the market value of spot longs plus |spot shorts|.
func (e *Engine) spotGross() float64 {
	gross := 0.0
	for _, symbol := range e.sortedPositions() {
		if _, contract := e.Config.Contracts[symbol]; contract {
			continue
		}
		gross += math.Abs(e.State.Positions[symbol].Qty) * e.mark(symbol)
	}
	return gross
}

func (e *Engine) sortedPositions() []string {
	symbols := make([]string, 0, len(e.State.Positions))
	for symbol := range e.State.Positions {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

// accrueBorrow charges borrow for the time elapsed since the last simulated
// timestamp, at the marks that held over that interval.
func (e *Engine) accrueBorrow(at time.Time) {
	s := e.State
	if !e.shortsEnabled() || s.Now.IsZero() || !at.After(s.Now) {
		return
	}
	elapsed := float64(at.Sub(s.Now)) / float64(yearDuration)
	total := 0.0
	for _, symbol := range e.sortedPositions() {
		pos := s.Positions[symbol]
		if pos.Qty >= 0 {
			continue
		}
		if _, contract := e.Config.Contracts[symbol]; contract {
			continue
		}
		total += -pos.Qty * e.mark(symbol) * e.borrowBps(symbol) / 10000 * elapsed
	}
	if total > 0 {
		s.Cash -= total
		s.BorrowCost += total
	}
}

// spotFill books a cash-settled signed spot fill: proceeds of a sale go to
// cash whether it closes a long or opens a short.
func (e *Engine) spotFill(o *Order, qty, price, fee float64) {
	s := e.State
	pos := s.Positions[o.Symbol]
	delta := qty
	if o.Side == "sell" {
		delta = -qty
	}
	if pos.Qty*delta < 0 {
		closing := math.Min(math.Abs(pos.Qty), qty)
		pnl := closing * (price - pos.AvgCost) * math.Copysign(1, pos.Qty)
		s.RealizedPnL += pnl
		if pos.Qty > 0 {
			s.RealizedLong += pnl
		} else {
			s.RealizedShort += pnl
		}
	}
	newQty := pos.Qty + delta
	if pos.Qty*delta >= 0 && newQty != 0 {
		pos.AvgCost = (math.Abs(pos.Qty)*pos.AvgCost + qty*price) / math.Abs(newQty)
	} else if pos.Qty*newQty < 0 {
		pos.AvgCost = price
	}
	pos.Qty = newQty
	s.Cash -= delta*price + fee
	if math.Abs(pos.Qty) < 1e-12 {
		delete(s.Positions, o.Symbol)
	} else {
		s.Positions[o.Symbol] = pos
	}
}

// spotMarginBreach reports whether a fill would leave the account short of
// initial margin.
func (e *Engine) spotMarginBreach(symbol string, newQty, price, fee, equity float64) bool {
	pos := e.State.Positions[symbol]
	before := math.Abs(pos.Qty) * e.mark(symbol)
	after := math.Abs(newQty) * price
	required := e.marginUsed() + (after-before)*e.Config.Shorts.InitialMargin
	return required+fee > equity+1e-9
}

func (e *Engine) marginCallActive() bool {
	for _, o := range e.State.Orders {
		if o.Tag == marginCallTag && active(o) {
			return true
		}
	}
	return false
}

// checkMaintenance submits reduce-only covers when equity is below the
// maintenance requirement. Shorts are covered first, pro rata, then longs, by
// just enough to restore maintenance at current marks.
func (e *Engine) checkMaintenance() {
	if !e.shortsEnabled() || e.marginCallActive() {
		return
	}
	s := e.State
	equity := e.Metrics()["equity"]
	maint := e.Config.Shorts.MaintenanceMargin
	contractMargin := e.contractMargin()
	gross := e.spotGross()
	required := gross*maint + contractMargin
	if gross <= 0 || equity >= required-1e-9 {
		return
	}
	// Covering a position at its mark leaves equity unchanged and lowers gross.
	// Restore to halfway between maintenance and initial margin, so borrow
	// accrual or the next tick does not re-trigger a call immediately.
	restore := (maint + e.Config.Shorts.InitialMargin) / 2
	target := math.Max(0, (equity-contractMargin)/restore)
	excess := gross - target
	shorts, longs := 0.0, 0.0
	for _, symbol := range e.sortedPositions() {
		if _, contract := e.Config.Contracts[symbol]; contract {
			continue
		}
		value := s.Positions[symbol].Qty * e.mark(symbol)
		if value < 0 {
			shorts -= value
		} else {
			longs += value
		}
	}
	shortFraction := math.Min(1, excess/math.Max(shorts, 1e-12))
	longFraction := 0.0
	if excess > shorts {
		longFraction = math.Min(1, (excess-shorts)/math.Max(longs, 1e-12))
	}
	s.MarginCalls++
	e.record("margin.call", map[string]any{"equity": equity, "required": required, "gross": gross, "maintenance_margin": maint, "cover_fraction": shortFraction, "long_sell_fraction": longFraction})
	for _, symbol := range e.sortedPositions() {
		if _, contract := e.Config.Contracts[symbol]; contract {
			continue
		}
		pos := s.Positions[symbol]
		fraction, side := shortFraction, "buy"
		if pos.Qty > 0 {
			fraction, side = longFraction, "sell"
		}
		qty := math.Abs(pos.Qty) * fraction
		if fraction >= 1-1e-9 {
			qty = math.Abs(pos.Qty)
		}
		if qty <= 1e-12 {
			continue
		}
		e.command(Command{Order: &Order{Symbol: symbol, Side: side, Type: "market", Qty: qty, ReduceOnly: true, Tag: marginCallTag, Reason: fmt.Sprintf("maintenance margin %.0f%%", maint*100)}})
	}
}

func (e *Engine) contractMargin() float64 {
	margin := 0.0
	for symbol, pos := range e.State.Positions {
		if c, ok := e.Config.Contracts[symbol]; ok {
			price := e.State.Quotes[symbol].Price
			if price <= 0 {
				price = pos.AvgCost
			}
			margin += math.Abs(pos.Qty) * price * c.Multiplier * c.CurrencyRate * c.MarginFraction
		}
	}
	return margin
}

// shortMetrics adds the long/short breakdown for short-enabled runs only, so
// long-only ledgers keep their original metric set.
func (e *Engine) shortMetrics(m map[string]float64) {
	if !e.shortsEnabled() {
		return
	}
	s := e.State
	long, short, longOpen, shortOpen := 0.0, 0.0, 0.0, 0.0
	for _, symbol := range e.sortedPositions() {
		if _, contract := e.Config.Contracts[symbol]; contract {
			continue
		}
		pos := s.Positions[symbol]
		price := e.mark(symbol)
		value := pos.Qty * price
		pnl := (price - pos.AvgCost) * pos.Qty
		if value < 0 {
			short -= value
			shortOpen += pnl
		} else {
			long += value
			longOpen += pnl
		}
	}
	equity := math.Max(m["equity"], 1e-12)
	m["long_exposure"] = long / equity * 100
	m["short_exposure"] = short / equity * 100
	m["net_exposure"] = (long - short) / equity * 100
	m["long_pnl"] = s.RealizedLong + longOpen
	m["short_pnl"] = s.RealizedShort + shortOpen
	m["long_return_pct"] = m["long_pnl"] / e.Config.StartingCash * 100
	m["short_return_pct"] = m["short_pnl"] / e.Config.StartingCash * 100
	m["borrow_cost"] = s.BorrowCost
	m["margin_calls"] = float64(s.MarginCalls)
	m["margin_requirement_pct"] = e.marginUsed() / equity * 100
	m["max_margin_used_pct"] = math.Max(s.MaxMarginPct, m["margin_requirement_pct"])
}
