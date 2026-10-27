package backtest

import (
	"fmt"
	"math"
)

// Protection is frozen with the entry command. Percent values are fractions
// (0.005 means 0.5%); absolute prices permit signal-candle and indicator stops.
type Protection struct {
	StopPrice          float64 `json:"stop_price,omitempty"`
	StopPct            float64 `json:"stop_pct,omitempty"`
	TargetPrice        float64 `json:"target_price,omitempty"`
	TargetPct          float64 `json:"target_pct,omitempty"`
	TrailActivationPct float64 `json:"trail_activation_pct,omitempty"`
	TrailDistancePct   float64 `json:"trail_distance_pct,omitempty"`
}

func validProtection(p *Protection) bool {
	if p == nil {
		return true
	}
	for _, x := range []float64{p.StopPrice, p.StopPct, p.TargetPrice, p.TargetPct, p.TrailActivationPct, p.TrailDistancePct} {
		if !finite(x) || x < 0 {
			return false
		}
	}
	return p.StopPct < 1 && p.TargetPct < 1 && p.TrailDistancePct < 1 && !(p.StopPrice > 0 && p.StopPct > 0) && !(p.TargetPrice > 0 && p.TargetPct > 0) && (p.TrailActivationPct == 0 || p.TrailDistancePct > 0)
}

func (e *Engine) protectFill(entry *Order, qty, price float64) {
	p := entry.Protection
	if p == nil || entry.ReduceOnly {
		return
	}
	direction, side := 1.0, "sell"
	if entry.Side == "sell" {
		direction, side = -1, "buy"
	}
	stop, target := p.StopPrice, p.TargetPrice
	if p.StopPct > 0 {
		stop = price * (1 - direction*p.StopPct)
	}
	if p.TargetPct > 0 {
		target = price * (1 + direction*p.TargetPct)
	}
	group := fmt.Sprintf("protect/%s/%d", entry.ID, e.State.Sequence)
	for _, typ := range []string{"stop", "limit"} {
		if typ == "stop" && stop <= 0 && p.TrailDistancePct <= 0 || typ == "limit" && target <= 0 {
			continue
		}
		// A dormant trail has a valid but untriggerable initial boundary.
		if typ == "stop" && stop <= 0 {
			if direction > 0 {
				stop = math.SmallestNonzeroFloat64
			} else {
				stop = math.MaxFloat64
			}
		}
		e.command(Command{Order: &Order{Symbol: entry.Symbol, Side: side, Type: typ, Qty: qty, TIF: "gtc", StopPrice: stop, LimitPrice: target, ReduceOnly: true, OCOGroup: group, Tag: entry.Tag, ParentID: entry.ID}})
		child := e.State.Orders[len(e.State.Orders)-1]
		if child.Status == "rejected" {
			continue
		}
		child.Status = "working"
		child.AcceptedAt = e.State.Now
		if typ == "stop" {
			child.TrailEntry = price
			child.TrailBest = price
			child.TrailActivationPct = p.TrailActivationPct
			child.TrailDistancePct = p.TrailDistancePct
		}
		e.record("order.accepted", *child)
	}
}

func (e *Engine) updateTrail(o *Order, price float64) {
	if o.TrailDistancePct <= 0 || o.Triggered {
		return
	}
	oldStop := o.StopPrice
	long := o.Side == "sell"
	if long {
		o.TrailBest = math.Max(o.TrailBest, price)
	} else {
		o.TrailBest = math.Min(o.TrailBest, price)
	}
	profit := (o.TrailBest/o.TrailEntry - 1)
	if !long {
		profit = 1 - o.TrailBest/o.TrailEntry
	}
	if profit+1e-12 < o.TrailActivationPct {
		return
	}
	stop := o.TrailBest * (1 - o.TrailDistancePct)
	if long {
		o.StopPrice = math.Max(o.StopPrice, stop)
	} else {
		stop = o.TrailBest * (1 + o.TrailDistancePct)
		o.StopPrice = math.Min(o.StopPrice, stop)
	}
	if o.StopPrice != oldStop {
		e.record("order.trailing_updated", map[string]any{"order_id": o.ID, "old_stop": oldStop, "stop_price": o.StopPrice, "best_price": o.TrailBest})
	}
}

func (e *Engine) resolveOCO(o *Order, qty float64) {
	if o.ParentID != "" {
		for _, parent := range e.State.Orders {
			if parent.ID == o.ParentID && active(parent) {
				e.resolve(parent, "cancelled", "protective exit filled")
			}
		}
	}
	if o.OCOGroup == "" {
		return
	}
	for _, other := range e.State.Orders {
		if other.ID == o.ID || other.OCOGroup != o.OCOGroup || !active(other) {
			continue
		}
		if o.ReduceOnly && other.ReduceOnly {
			other.Qty -= qty
			if other.Qty-other.FilledQty > 1e-9 {
				continue
			}
		}
		e.resolve(other, "cancelled", "OCO peer filled")
	}
}

// Arithmetic-derived boundaries can differ by a few ULPs (100*1.1 vs 110).
// This absorbs representation error, never a market tick or execution cost.
func priceAtOrAbove(a, b float64) bool {
	return a >= b || b-a <= 4*(math.Nextafter(math.Max(math.Abs(a), math.Abs(b)), math.Inf(1))-math.Max(math.Abs(a), math.Abs(b)))
}
func priceAtOrBelow(a, b float64) bool { return priceAtOrAbove(b, a) }
