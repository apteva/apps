package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type IndicatorBar struct {
	T   int64   `json:"t"`
	O   float64 `json:"o,omitempty"`
	H   float64 `json:"h,omitempty"`
	L   float64 `json:"l,omitempty"`
	C   float64 `json:"c,omitempty"`
	V   float64 `json:"v,omitempty"`
	Yes float64 `json:"yes,omitempty"`
}

type IndicatorResult struct {
	Symbol     string         `json:"symbol"`
	Interval   string         `json:"interval"`
	Range      string         `json:"range"`
	AsOf       string         `json:"as_of,omitempty"`
	Source     string         `json:"source"`
	BarCount   int            `json:"bar_count"`
	Values     map[string]any `json:"values"`
	Summary    map[string]any `json:"summary,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	Indicators []string       `json:"indicators"`
}

type IndicatorSeriesPoint struct {
	T     int64   `json:"t"`
	Value float64 `json:"value"`
}

type IndicatorSeriesResult struct {
	Symbol    string                 `json:"symbol"`
	Interval  string                 `json:"interval"`
	Range     string                 `json:"range"`
	Indicator string                 `json:"indicator"`
	Source    string                 `json:"source"`
	Series    []IndicatorSeriesPoint `json:"series"`
	Meta      map[string]any         `json:"meta,omitempty"`
}

func indicatorPresets() map[string][]string {
	return map[string][]string{
		"trend":          {"sma_20", "ema_20", "ema_50", "macd_12_26_9"},
		"momentum":       {"rsi_14", "macd_12_26_9", "return_20"},
		"mean_reversion": {"rsi_14", "bbands_20_2", "zscore_20"},
		"volatility":     {"atr_14", "volatility_20", "bbands_20_2"},
		"breakout":       {"sma_20", "volume_sma_20", "bbands_20_2", "atr_14"},
		"risk":           {"atr_14", "volatility_20", "return_20"},
	}
}

func defaultIndicatorList() []string {
	return []string{"sma_20", "ema_20", "ema_50", "rsi_14", "macd_12_26_9", "bbands_20_2", "atr_14", "volatility_20", "return_20", "volume_sma_20"}
}

func (a *App) toolIndicators(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	symbol := strings.ToUpper(strArg(args, "symbol"))
	if symbol == "" {
		return nil, errors.New("symbol required")
	}
	interval := normalizeIndicatorInterval(strArg(args, "interval"))
	rng := normalizeIndicatorRange(strArg(args, "range"))
	indicators := parseIndicatorSelection(args)
	bars, source, err := a.indicatorBars(ctx, symbol, rng)
	if err != nil {
		return nil, err
	}
	return computeIndicators(symbol, interval, rng, source, bars, indicators)
}

func (a *App) toolIndicatorSeries(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	symbol := strings.ToUpper(strArg(args, "symbol"))
	if symbol == "" {
		return nil, errors.New("symbol required")
	}
	indicator := strings.ToLower(strings.TrimSpace(strArg(args, "indicator")))
	if indicator == "" {
		return nil, errors.New("indicator required")
	}
	interval := normalizeIndicatorInterval(strArg(args, "interval"))
	rng := normalizeIndicatorRange(strArg(args, "range"))
	bars, source, err := a.indicatorBars(ctx, symbol, rng)
	if err != nil {
		return nil, err
	}
	series, meta, err := computeIndicatorSeries(indicator, bars)
	if err != nil {
		return nil, err
	}
	return IndicatorSeriesResult{
		Symbol: symbol, Interval: interval, Range: rng, Indicator: indicator,
		Source: source, Series: series, Meta: meta,
	}, nil
}

func (a *App) toolIndicatorPresets(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return map[string]any{"presets": indicatorPresets(), "default": defaultIndicatorList()}, nil
}

func (a *App) indicatorBars(ctx *sdk.AppCtx, symbol, rng string) ([]IndicatorBar, string, error) {
	if ctx != nil && ctx.PlatformAPI() != nil {
		var out struct {
			Bars []IndicatorBar `json:"bars"`
		}
		err := ctx.PlatformAPI().CallAppResult("trading", "market_history", map[string]any{
			"symbol": symbol,
			"range":  rng,
		}, &out)
		if err == nil && len(out.Bars) > 0 {
			return cleanIndicatorBars(out.Bars), "trading.market_history", nil
		}
	}
	bars := syntheticIndicatorBars(symbol, rng)
	if len(bars) == 0 {
		return nil, "", fmt.Errorf("no bars available for %s", symbol)
	}
	return bars, "market-intel.synthetic", nil
}

func parseIndicatorSelection(args map[string]any) []string {
	seen := map[string]bool{}
	add := func(name string, out *[]string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		*out = append(*out, name)
	}
	out := []string{}
	if preset := strings.ToLower(strings.TrimSpace(strArg(args, "preset"))); preset != "" {
		for _, name := range indicatorPresets()[preset] {
			add(name, &out)
		}
	}
	switch v := args["indicators"].(type) {
	case []string:
		for _, name := range v {
			add(name, &out)
		}
	case []any:
		for _, item := range v {
			add(fmt.Sprint(item), &out)
		}
	case string:
		for _, name := range strings.Split(v, ",") {
			add(name, &out)
		}
	}
	if len(out) == 0 {
		out = defaultIndicatorList()
	}
	return out
}

func normalizeIndicatorInterval(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "5m", "15m", "1h", "4h", "1d", "1w":
		return v
	default:
		return "1d"
	}
}

func normalizeIndicatorRange(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	switch v {
	case "1D", "5D", "1M", "3M", "6M", "1Y", "ALL":
		return v
	default:
		return "3M"
	}
}

func cleanIndicatorBars(in []IndicatorBar) []IndicatorBar {
	out := make([]IndicatorBar, 0, len(in))
	for _, b := range in {
		if indicatorPrice(b) <= 0 || b.T <= 0 {
			continue
		}
		if b.C == 0 {
			b.C = indicatorPrice(b)
		}
		if b.O == 0 {
			b.O = b.C
		}
		if b.H == 0 {
			b.H = math.Max(b.O, b.C)
		}
		if b.L == 0 {
			b.L = math.Min(b.O, b.C)
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

func indicatorPrice(b IndicatorBar) float64 {
	if b.C > 0 {
		return b.C
	}
	if b.Yes > 0 {
		return b.Yes
	}
	return b.O
}

func computeIndicators(symbol, interval, rng, source string, bars []IndicatorBar, indicators []string) (IndicatorResult, error) {
	bars = cleanIndicatorBars(bars)
	if len(bars) < 2 {
		return IndicatorResult{}, errors.New("at least two bars are required")
	}
	values := map[string]any{}
	warnings := []string{}
	for _, name := range indicators {
		value, err := computeIndicatorValue(name, bars)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s", name, err.Error()))
			continue
		}
		values[name] = value
	}
	last := bars[len(bars)-1]
	result := IndicatorResult{
		Symbol: symbol, Interval: interval, Range: rng, Source: source,
		BarCount: len(bars), Values: values, Warnings: warnings, Indicators: indicators,
		AsOf: time.Unix(last.T, 0).UTC().Format(time.RFC3339),
	}
	result.Summary = summarizeIndicatorBias(values, bars)
	return result, nil
}

func computeIndicatorValue(name string, bars []IndicatorBar) (any, error) {
	switch {
	case name == "macd" || strings.HasPrefix(name, "macd_"):
		fast, slow, signal := parseThreeParams(name, "macd", 12, 26, 9)
		macd, sig, hist, err := latestMACD(bars, fast, slow, signal)
		if err != nil {
			return nil, err
		}
		return map[string]float64{"macd": round(macd), "signal": round(sig), "histogram": round(hist)}, nil
	case strings.HasPrefix(name, "bbands"):
		window, mult := parseWindowMultiplier(name, 20, 2)
		upper, middle, lower, err := latestBBands(bars, window, mult)
		if err != nil {
			return nil, err
		}
		return map[string]float64{"upper": round(upper), "middle": round(middle), "lower": round(lower)}, nil
	case strings.HasPrefix(name, "sma_"):
		window := parseWindow(name, "sma", 20)
		v, err := latestSMA(bars, window)
		if err != nil {
			return nil, err
		}
		return round(v), nil
	case strings.HasPrefix(name, "ema_"):
		window := parseWindow(name, "ema", 20)
		v, err := latestEMA(bars, window)
		if err != nil {
			return nil, err
		}
		return round(v), nil
	case strings.HasPrefix(name, "rsi_") || name == "rsi":
		window := parseWindow(name, "rsi", 14)
		v, err := latestRSI(bars, window)
		if err != nil {
			return nil, err
		}
		return round(v), nil
	case strings.HasPrefix(name, "atr_") || name == "atr":
		window := parseWindow(name, "atr", 14)
		v, err := latestATR(bars, window)
		if err != nil {
			return nil, err
		}
		return round(v), nil
	case strings.HasPrefix(name, "volatility_") || name == "volatility":
		window := parseWindow(name, "volatility", 20)
		v, err := latestVolatility(bars, window)
		if err != nil {
			return nil, err
		}
		return round(v), nil
	case strings.HasPrefix(name, "return_") || name == "return":
		window := parseWindow(name, "return", 20)
		v, err := latestReturn(bars, window)
		if err != nil {
			return nil, err
		}
		return round(v), nil
	case strings.HasPrefix(name, "volume_sma_"):
		window := parseWindow(name, "volume_sma", 20)
		v, err := latestVolumeSMA(bars, window)
		if err != nil {
			return nil, err
		}
		return round(v), nil
	case strings.HasPrefix(name, "zscore_") || name == "zscore":
		window := parseWindow(name, "zscore", 20)
		v, err := latestZScore(bars, window)
		if err != nil {
			return nil, err
		}
		return round(v), nil
	default:
		return nil, fmt.Errorf("unsupported indicator")
	}
}

func computeIndicatorSeries(name string, bars []IndicatorBar) ([]IndicatorSeriesPoint, map[string]any, error) {
	bars = cleanIndicatorBars(bars)
	series := []IndicatorSeriesPoint{}
	meta := map[string]any{}
	for i := range bars {
		if i < 1 {
			continue
		}
		value, err := computeScalarIndicatorForSeries(name, bars[:i+1])
		if err != nil {
			continue
		}
		series = append(series, IndicatorSeriesPoint{T: bars[i].T, Value: round(value)})
	}
	if len(series) == 0 {
		return nil, nil, fmt.Errorf("not enough bars for %s", name)
	}
	meta["points"] = len(series)
	return series, meta, nil
}

func computeScalarIndicatorForSeries(name string, bars []IndicatorBar) (float64, error) {
	value, err := computeIndicatorValue(name, bars)
	if err != nil {
		return 0, err
	}
	switch v := value.(type) {
	case float64:
		return v, nil
	case map[string]float64:
		if x, ok := v["histogram"]; ok {
			return x, nil
		}
		if x, ok := v["middle"]; ok {
			return x, nil
		}
	}
	return 0, fmt.Errorf("indicator %s is not scalar", name)
}

func parseWindow(name, prefix string, fallback int) int {
	trimmed := strings.TrimPrefix(name, prefix+"_")
	if trimmed == name {
		return fallback
	}
	if n, err := strconv.Atoi(trimmed); err == nil && n > 0 {
		return n
	}
	return fallback
}

func parseThreeParams(name, prefix string, a, b, c int) (int, int, int) {
	parts := strings.Split(strings.TrimPrefix(name, prefix+"_"), "_")
	if len(parts) != 3 {
		return a, b, c
	}
	out := []int{a, b, c}
	for i := range out {
		if n, err := strconv.Atoi(parts[i]); err == nil && n > 0 {
			out[i] = n
		}
	}
	return out[0], out[1], out[2]
}

func parseWindowMultiplier(name string, fallbackWindow int, fallbackMultiplier float64) (int, float64) {
	parts := strings.Split(strings.TrimPrefix(name, "bbands_"), "_")
	window := fallbackWindow
	multiplier := fallbackMultiplier
	if len(parts) >= 1 {
		if n, err := strconv.Atoi(parts[0]); err == nil && n > 0 {
			window = n
		}
	}
	if len(parts) >= 2 {
		if n, err := strconv.ParseFloat(parts[1], 64); err == nil && n > 0 {
			multiplier = n
		}
	}
	return window, multiplier
}

func closes(bars []IndicatorBar) []float64 {
	out := make([]float64, 0, len(bars))
	for _, b := range bars {
		out = append(out, indicatorPrice(b))
	}
	return out
}

func latestSMA(bars []IndicatorBar, window int) (float64, error) {
	values := closes(bars)
	if len(values) < window {
		return 0, fmt.Errorf("need %d bars", window)
	}
	return avg(values[len(values)-window:]), nil
}

func latestEMA(bars []IndicatorBar, window int) (float64, error) {
	values := closes(bars)
	if len(values) < window {
		return 0, fmt.Errorf("need %d bars", window)
	}
	return emaValues(values, window)[len(values)-1], nil
}

func emaValues(values []float64, window int) []float64 {
	out := make([]float64, len(values))
	alpha := 2.0 / float64(window+1)
	seed := values[0]
	for i, v := range values {
		if i == 0 {
			out[i] = seed
		} else {
			out[i] = (v * alpha) + (out[i-1] * (1 - alpha))
		}
	}
	return out
}

func latestRSI(bars []IndicatorBar, window int) (float64, error) {
	values := closes(bars)
	if len(values) <= window {
		return 0, fmt.Errorf("need %d bars", window+1)
	}
	var gains, losses float64
	start := len(values) - window
	for i := start; i < len(values); i++ {
		delta := values[i] - values[i-1]
		if delta >= 0 {
			gains += delta
		} else {
			losses -= delta
		}
	}
	avgGain := gains / float64(window)
	avgLoss := losses / float64(window)
	if avgLoss == 0 {
		return 100, nil
	}
	rs := avgGain / avgLoss
	return 100 - (100 / (1 + rs)), nil
}

func latestMACD(bars []IndicatorBar, fast, slow, signal int) (float64, float64, float64, error) {
	values := closes(bars)
	if len(values) < slow+signal {
		return 0, 0, 0, fmt.Errorf("need %d bars", slow+signal)
	}
	fastEMA := emaValues(values, fast)
	slowEMA := emaValues(values, slow)
	macdLine := make([]float64, len(values))
	for i := range values {
		macdLine[i] = fastEMA[i] - slowEMA[i]
	}
	signalLine := emaValues(macdLine, signal)
	macd := macdLine[len(macdLine)-1]
	sig := signalLine[len(signalLine)-1]
	return macd, sig, macd - sig, nil
}

func latestBBands(bars []IndicatorBar, window int, multiplier float64) (float64, float64, float64, error) {
	values := closes(bars)
	if len(values) < window {
		return 0, 0, 0, fmt.Errorf("need %d bars", window)
	}
	slice := values[len(values)-window:]
	mid := avg(slice)
	dev := stddev(slice)
	return mid + multiplier*dev, mid, mid - multiplier*dev, nil
}

func latestATR(bars []IndicatorBar, window int) (float64, error) {
	if len(bars) <= window {
		return 0, fmt.Errorf("need %d bars", window+1)
	}
	start := len(bars) - window
	values := []float64{}
	for i := start; i < len(bars); i++ {
		prevClose := indicatorPrice(bars[i-1])
		high := bars[i].H
		low := bars[i].L
		if high == 0 || low == 0 {
			close := indicatorPrice(bars[i])
			high, low = close, close
		}
		tr := math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose)))
		values = append(values, tr)
	}
	return avg(values), nil
}

func latestVolatility(bars []IndicatorBar, window int) (float64, error) {
	values := closes(bars)
	if len(values) <= window {
		return 0, fmt.Errorf("need %d bars", window+1)
	}
	returns := []float64{}
	for i := len(values) - window; i < len(values); i++ {
		if values[i-1] == 0 {
			continue
		}
		returns = append(returns, math.Log(values[i]/values[i-1]))
	}
	return stddev(returns), nil
}

func latestReturn(bars []IndicatorBar, window int) (float64, error) {
	values := closes(bars)
	if len(values) <= window {
		return 0, fmt.Errorf("need %d bars", window+1)
	}
	first := values[len(values)-1-window]
	if first == 0 {
		return 0, errors.New("zero starting price")
	}
	return (values[len(values)-1]/first - 1) * 100, nil
}

func latestVolumeSMA(bars []IndicatorBar, window int) (float64, error) {
	if len(bars) < window {
		return 0, fmt.Errorf("need %d bars", window)
	}
	values := []float64{}
	for _, b := range bars[len(bars)-window:] {
		values = append(values, b.V)
	}
	return avg(values), nil
}

func latestZScore(bars []IndicatorBar, window int) (float64, error) {
	values := closes(bars)
	if len(values) < window {
		return 0, fmt.Errorf("need %d bars", window)
	}
	slice := values[len(values)-window:]
	dev := stddev(slice)
	if dev == 0 {
		return 0, nil
	}
	return (values[len(values)-1] - avg(slice)) / dev, nil
}

func summarizeIndicatorBias(values map[string]any, bars []IndicatorBar) map[string]any {
	score := 0
	reasons := []string{}
	warnings := []string{}
	if ema20, ok := numericValue(values["ema_20"]); ok {
		if ema50, ok := numericValue(values["ema_50"]); ok {
			if ema20 > ema50 {
				score++
				reasons = append(reasons, "EMA20 above EMA50")
			} else if ema20 < ema50 {
				score--
				reasons = append(reasons, "EMA20 below EMA50")
			}
		}
	}
	if macd, ok := values["macd_12_26_9"].(map[string]float64); ok {
		if macd["histogram"] > 0 {
			score++
			reasons = append(reasons, "MACD histogram positive")
		} else if macd["histogram"] < 0 {
			score--
			reasons = append(reasons, "MACD histogram negative")
		}
	}
	if rsi, ok := numericValue(values["rsi_14"]); ok {
		if rsi > 70 {
			score--
			warnings = append(warnings, "RSI overbought")
		} else if rsi < 30 {
			score++
			warnings = append(warnings, "RSI oversold")
		}
	}
	bias := "neutral"
	if score >= 2 {
		bias = "bullish"
	} else if score <= -2 {
		bias = "bearish"
	}
	confidence := math.Min(0.9, 0.5+math.Abs(float64(score))*0.12)
	if len(bars) < 50 {
		warnings = append(warnings, "short history; long-window indicators may be missing")
	}
	return map[string]any{
		"bias":       bias,
		"confidence": round(confidence),
		"score":      score,
		"reasons":    reasons,
		"warnings":   warnings,
	}
}

func numericValue(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	default:
		return 0, false
	}
}

func syntheticIndicatorBars(symbol, rng string) []IndicatorBar {
	count := map[string]int{"1D": 96, "5D": 120, "1M": 90, "3M": 120, "6M": 180, "1Y": 260, "ALL": 360}[rng]
	if count == 0 {
		count = 120
	}
	anchor := syntheticAnchor(symbol)
	now := time.Now().UTC().Truncate(time.Hour)
	out := make([]IndicatorBar, 0, count)
	for i := 0; i < count; i++ {
		t := now.Add(-time.Duration(count-1-i) * time.Hour).Unix()
		wave := math.Sin(float64(i+syntheticHash(symbol)%31)/6.0) * 0.018
		trend := float64((syntheticHash(symbol)%9)-4) * 0.0007 * float64(i)
		close := anchor * (1 + wave + trend)
		if close <= 0 {
			close = anchor * 0.1
		}
		open := close * (1 + math.Sin(float64(i))*0.002)
		high := math.Max(open, close) * 1.004
		low := math.Min(open, close) * 0.996
		out = append(out, IndicatorBar{T: t, O: open, H: high, L: low, C: close, V: 100000 + float64((i%17)*2500)})
	}
	return out
}

func syntheticAnchor(symbol string) float64 {
	symbol = strings.ToUpper(symbol)
	if strings.HasPrefix(symbol, "POLY:") {
		return 0.5
	}
	if strings.HasSuffix(symbol, "-USD") {
		return 1000 + float64(syntheticHash(symbol)%90000)
	}
	return 50 + float64(syntheticHash(symbol)%450)
}

func syntheticHash(symbol string) int {
	sum := 0
	for _, r := range strings.ToUpper(symbol) {
		sum = (sum*31 + int(r)) & 0x7fffffff
	}
	return sum
}

func avg(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stddev(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	mean := avg(values)
	var sum float64
	for _, v := range values {
		d := v - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(values)))
}

func round(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10000) / 10000
}
