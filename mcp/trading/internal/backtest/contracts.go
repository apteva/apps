package backtest

import (
	"errors"
	"math"
)

// Contract is an explicit cash-settled linear instrument. CurrencyRate converts
// settlement currency to account currency. No borrowing/financing is invented.
// Spot instruments retain their original cash-funded, long-only accounting.
type Contract struct {
	Multiplier     float64 `json:"multiplier"`
	CurrencyRate   float64 `json:"currency_rate"`
	MarginFraction float64 `json:"margin_fraction"`
}

func validateContracts(contracts map[string]Contract) error {
	for symbol, c := range contracts {
		if symbol == "" || !finite(c.Multiplier) || c.Multiplier <= 0 || !finite(c.CurrencyRate) || c.CurrencyRate <= 0 || !finite(c.MarginFraction) || c.MarginFraction <= 0 || c.MarginFraction > 1 {
			return errors.New("contracts require a symbol, positive multiplier/currency_rate, and margin_fraction in (0,1]")
		}
	}
	return nil
}

func (e *Engine) multiplier(symbol string) float64 {
	if c, ok := e.Config.Contracts[symbol]; ok {
		return c.Multiplier * c.CurrencyRate
	}
	return 1
}

func (e *Engine) marginUsed() float64 {
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

func (e *Engine) contractFill(o *Order, qty, price, fee float64) {
	s := e.State
	pos := s.Positions[o.Symbol]
	delta := qty
	if o.Side == "sell" {
		delta = -qty
	}
	if pos.Qty*delta < 0 {
		closing := math.Min(math.Abs(pos.Qty), qty)
		pnl := closing * (price - pos.AvgCost) * math.Copysign(1, pos.Qty) * e.multiplier(o.Symbol)
		s.RealizedPnL += pnl
		s.Cash += pnl
	}
	newQty := pos.Qty + delta
	if pos.Qty*delta >= 0 && newQty != 0 {
		pos.AvgCost = (math.Abs(pos.Qty)*pos.AvgCost + qty*price) / math.Abs(newQty)
	} else if pos.Qty*newQty < 0 {
		pos.AvgCost = price
	}
	pos.Qty = newQty
	s.Cash -= fee
	if math.Abs(pos.Qty) < 1e-12 {
		delete(s.Positions, o.Symbol)
	} else {
		s.Positions[o.Symbol] = pos
	}
}

// Equity is shared by rule sizing and simulator risk/accounting.
func Equity(config Config, state *State) float64 {
	return (&Engine{Config: config, State: state}).Metrics()["equity"]
}
