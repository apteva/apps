package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// New recursive indicators use a bounded, SMA-seeded history. The same window
// is requested live and sliced in replay, avoiding dependence on replay length.
// Legacy ema_N/rsi_N retain their original formulas for saved strategies.
type indicatorSpec struct {
	kind    string
	periods []int
	bars    int
}

func parseIndicator(name string) (indicatorSpec, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "price" {
		return indicatorSpec{kind: "price", bars: 1}, nil
	}
	if strings.HasPrefix(name, "feature:") {
		n, f, ok := strings.Cut(strings.TrimPrefix(name, "feature:"), ".")
		if !ok || n == "" || f == "" {
			return indicatorSpec{}, errors.New("feature indicator must be feature:name.field")
		}
		return indicatorSpec{kind: "feature", bars: 1}, nil
	}
	families := []string{"macd_signal", "macd_hist", "ema_sma", "rsi_wilder", "bb_upper", "bb_lower", "bb_width", "bb_percent_b", "zscore", "macd", "sma", "ema", "rsi", "return", "volatility"}
	for _, kind := range families {
		if name != kind && !strings.HasPrefix(name, kind+"_") {
			continue
		}
		defaults := []int{20}
		if kind == "rsi" || kind == "rsi_wilder" {
			defaults = []int{14}
		}
		if strings.HasPrefix(kind, "macd") {
			defaults = []int{12, 26, 9}
		}
		periods := defaults
		if name != kind {
			tokens := strings.Split(strings.TrimPrefix(name, kind+"_"), "_")
			if len(tokens) != len(defaults) {
				return indicatorSpec{}, fmt.Errorf("invalid parameters for %s", kind)
			}
			periods = make([]int, len(tokens))
			for i, s := range tokens {
				n, err := strconv.Atoi(s)
				if err != nil || n < 1 || n > 999 {
					return indicatorSpec{}, fmt.Errorf("invalid period in %q", name)
				}
				periods[i] = n
			}
		}
		n := periods[0]
		bars := n
		switch kind {
		case "rsi", "return", "volatility":
			bars = n + 1
		case "ema_sma":
			bars = 5 * n
		case "rsi_wilder":
			bars = 5*n + 1
		case "macd", "macd_signal", "macd_hist":
			if periods[0] >= periods[1] {
				return indicatorSpec{}, errors.New("MACD fast period must be less than slow period")
			}
			bars = 5*periods[1] + 5*periods[2]
		}
		if bars > 1000 {
			return indicatorSpec{}, fmt.Errorf("%s requires %d bars; maximum is 1000", name, bars)
		}
		return indicatorSpec{kind: kind, periods: periods, bars: bars}, nil
	}
	return indicatorSpec{}, fmt.Errorf("unsupported indicator %q", name)
}

func seededEMA(values []float64, n int) []float64 {
	if len(values) < n {
		return nil
	}
	out := make([]float64, 0, len(values)-n+1)
	v := avgFloat(values[:n])
	out = append(out, v)
	alpha := 2.0 / float64(n+1)
	for _, x := range values[n:] {
		v += alpha * (x - v)
		out = append(out, v)
	}
	return out
}
func wilderRSI(values []float64, n int) float64 {
	gain, loss := 0.0, 0.0
	for i := 1; i <= n; i++ {
		d := values[i] - values[i-1]
		gain += math.Max(d, 0)
		loss += math.Max(-d, 0)
	}
	gain /= float64(n)
	loss /= float64(n)
	for i := n + 1; i < len(values); i++ {
		d := values[i] - values[i-1]
		gain = (gain*float64(n-1) + math.Max(d, 0)) / float64(n)
		loss = (loss*float64(n-1) + math.Max(-d, 0)) / float64(n)
	}
	if gain == 0 && loss == 0 {
		return 50
	}
	if loss == 0 {
		return 100
	}
	return 100 - 100/(1+gain/loss)
}
func extendedIndicator(spec indicatorSpec, values []float64) (float64, error) {
	if len(values) < spec.bars {
		return 0, fmt.Errorf("need %d bars", spec.bars)
	}
	values = values[len(values)-spec.bars:]
	n := spec.periods[0]
	switch spec.kind {
	case "ema_sma":
		v := seededEMA(values, n)
		return v[len(v)-1], nil
	case "rsi_wilder":
		return wilderRSI(values, n), nil
	case "macd", "macd_signal", "macd_hist":
		fast, slow := seededEMA(values, n), seededEMA(values, spec.periods[1])
		line := make([]float64, len(slow))
		offset := len(fast) - len(slow)
		for i := range slow {
			line[i] = fast[i+offset] - slow[i]
		}
		last := line[len(line)-1]
		if spec.kind == "macd" {
			return last, nil
		}
		signal := seededEMA(line, spec.periods[2])
		v := signal[len(signal)-1]
		if spec.kind == "macd_hist" {
			return last - v, nil
		}
		return v, nil
	case "bb_upper", "bb_lower", "bb_width", "bb_percent_b", "zscore":
		mean, sd := avgFloat(values), stddevFloat(values)
		last := values[len(values)-1]
		switch spec.kind {
		case "bb_upper":
			return mean + 2*sd, nil
		case "bb_lower":
			return mean - 2*sd, nil
		case "bb_width":
			if mean == 0 {
				return 0, errors.New("zero Bollinger mean")
			}
			return 4 * sd / mean * 100, nil
		case "bb_percent_b":
			if sd == 0 {
				return 0.5, nil
			}
			return (last - (mean - 2*sd)) / (4 * sd), nil
		case "zscore":
			if sd == 0 {
				return 0, nil
			}
			return (last - mean) / sd, nil
		}
	}
	return 0, fmt.Errorf("unsupported extended indicator %s", spec.kind)
}

func conditionRequiredBars(c *StrategyCondition) int {
	if c == nil {
		return 1
	}
	n := max(indicatorRequiredBars(c.Indicator), indicatorRequiredBars(c.Compare))
	for _, group := range [][]StrategyCondition{c.All, c.Any} {
		for i := range group {
			n = max(n, conditionRequiredBars(&group[i]))
		}
	}
	if strings.EqualFold(strings.TrimSpace(c.Operator), "crosses_above") || strings.EqualFold(strings.TrimSpace(c.Operator), "crosses_below") {
		n++
	}
	return n
}
func validateCondition(c *StrategyCondition, universe map[string]bool, depth int) error {
	if c == nil {
		return nil
	}
	c.Operator = strings.ToLower(strings.TrimSpace(c.Operator))
	c.Indicator = strings.ToLower(strings.TrimSpace(c.Indicator))
	c.Compare = strings.ToLower(strings.TrimSpace(c.Compare))
	if depth > 8 {
		return errors.New("condition nesting exceeds 8")
	}
	if len(c.All) > 0 || len(c.Any) > 0 {
		if len(c.All) > 0 && len(c.Any) > 0 || c.Indicator != "" || c.Compare != "" || c.Operator != "" || c.Symbol != "" || c.Value != 0 {
			return errors.New("condition must contain either all, any, or a scalar comparison")
		}
		group := c.All
		if len(c.Any) > 0 {
			group = c.Any
		}
		for i := range group {
			if err := validateCondition(&group[i], universe, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if c.Indicator == "" {
		return errors.New("condition indicator required")
	}
	for _, metric := range []string{c.Indicator, c.Compare} {
		if _, err := parseIndicator(metric); err != nil {
			return err
		}
	}
	if c.Symbol != "" && !universe[strings.ToUpper(strings.TrimSpace(c.Symbol))] {
		return fmt.Errorf("condition symbol %s is outside the strategy universe", c.Symbol)
	}
	switch strings.ToLower(strings.TrimSpace(c.Operator)) {
	case ">", ">=", "<", "<=", "above", "below", "at_or_above", "at_or_below", "crosses_above", "crosses_below":
	default:
		return fmt.Errorf("unsupported operator %q", c.Operator)
	}
	if (c.Operator == "crosses_above" || c.Operator == "crosses_below") && (strings.HasPrefix(c.Indicator, "feature:") || strings.HasPrefix(c.Compare, "feature:")) {
		return errors.New("feature crossovers require a historical feature series; use scalar comparisons")
	}
	if conditionRequiredBars(c) > 1000 {
		return errors.New("condition requires more than 1000 bars")
	}
	return nil
}
func conditionSymbols(c *StrategyCondition) []string {
	if c == nil {
		return nil
	}
	out := []string{}
	if c.Symbol != "" {
		out = append(out, c.Symbol)
	}
	for _, g := range [][]StrategyCondition{c.All, c.Any} {
		for i := range g {
			out = append(out, conditionSymbols(&g[i])...)
		}
	}
	return out
}
