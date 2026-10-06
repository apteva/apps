package main

import (
	"encoding/json"
	sim "github.com/apteva/apps/mcp/trading/internal/backtest"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func indicatorNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Fatalf("got %.12f want %.12f", got, want)
	}
}
func TestIndicatorReferenceFormulas(t *testing.T) {
	// Wilder's published 14-period example: initial RSI and next smoothed RSI.
	v := []float64{44.34, 44.09, 44.15, 43.61, 44.33, 44.83, 45.10, 45.42, 45.84, 46.08, 45.89, 46.03, 45.61, 46.28, 46.28, 46.00}
	indicatorNear(t, wilderRSI(v[:15], 14), 70.46413502109705)
	indicatorNear(t, wilderRSI(v, 14), 66.24961855355505)
	ema := seededEMA([]float64{1, 2, 3, 4, 5}, 3)
	indicatorNear(t, ema[0], 2)
	indicatorNear(t, ema[2], 4)
	market := strategyMarket{history: map[string][]float64{"A": {1, 2, 3, 4, 5}}, prices: map[string]float64{"A": 5}}
	for name, want := range map[string]float64{"sma_5": 3, "bb_upper_5": 3 + 2*math.Sqrt(2), "bb_lower_5": 3 - 2*math.Sqrt(2), "zscore_5": math.Sqrt(2), "bb_percent_b_5": 0.5 + 1/(2*math.Sqrt(2)), "bb_width_5": 4 * math.Sqrt(2) / 3 * 100} {
		got, err := strategyMetric("A", name, market)
		if err != nil {
			t.Fatal(err)
		}
		indicatorNear(t, got, want)
	}
	// Linear sequence has EMA lag (N-1)/2 under SMA initialization. MACD12/26=7.
	line := make([]float64, 175)
	flat := make([]float64, 175)
	for i := range line {
		line[i] = float64(i + 1)
		flat[i] = 42
	}
	market.history["A"] = line
	for name, want := range map[string]float64{"macd_12_26_9": 7, "macd_signal_12_26_9": 7, "macd_hist_12_26_9": 0} {
		got, err := strategyMetric("A", name, market)
		if err != nil {
			t.Fatal(err)
		}
		indicatorNear(t, got, want)
	}
	market.history["A"] = flat
	for name, want := range map[string]float64{"rsi_wilder_14": 50, "bb_percent_b_20": 0.5, "zscore_20": 0} {
		got, err := strategyMetric("A", name, market)
		if err != nil {
			t.Fatal(err)
		}
		indicatorNear(t, got, want)
	}
}
func TestIndicatorValidationRejectsMalformedMetrics(t *testing.T) {
	for _, name := range []string{"ema_20junk", "rsi_0", "macd_26_12_9", "macd_12_26", "bb_lower_-1", "rsi_wilder_200", "feature:sentiment.", "atr_14", "nonsense"} {
		if _, err := parseIndicator(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, p := range strategyPresets([]string{"BTC-USD", "ETH-USD"}) {
		raw, _ := json.Marshal(p.Definition)
		var def map[string]any
		json.Unmarshal(raw, &def)
		if _, _, err := validateStrategyDefinition(def); err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
	}
}
func TestIndicatorCompoundRankAndCrossovers(t *testing.T) {
	market := strategyMarket{history: map[string][]float64{"A": {10, 9, 8, 12}, "B": {10, 11, 12, 8}}, prices: map[string]float64{"A": 12, "B": 8}}
	c := StrategyCondition{Indicator: "price", Operator: "crosses_above", Compare: "sma_3"}
	ok, _, err := evalStrategyCondition(c, &StrategyDefinition{Universe: []string{"A"}}, market)
	if err != nil || !ok {
		t.Fatalf("cross: %v %v", ok, err)
	}
	ok, _, err = evalStrategyCondition(c, &StrategyDefinition{Universe: []string{"B"}}, market)
	if err != nil || ok {
		t.Fatalf("wrong cross: %v %v", ok, err)
	}
	filter := &StrategyCondition{All: []StrategyCondition{{Indicator: "price", Operator: ">", Compare: "sma_3"}, {Any: []StrategyCondition{{Indicator: "return_1", Operator: ">", Value: 0}, {Indicator: "price", Operator: ">", Value: 100}}}}}
	allocations, _, err := evalStrategyRank(StrategyRank{Symbols: []string{"A", "B"}, By: "return_1", Top: 2, Budget: 0.6, Where: filter}, market)
	if err != nil || len(allocations) != 1 || allocations[0].Symbol != "A" {
		t.Fatalf("rank: %+v %v", allocations, err)
	}
	indicatorNear(t, allocations[0].Weight, 0.6)
	allocations, _, err = evalStrategyRank(StrategyRank{Symbols: []string{"A", "B"}, By: "return_1", Top: 1, Direction: "asc"}, market)
	if err != nil || allocations[0].Symbol != "B" {
		t.Fatalf("ascending: %+v %v", allocations, err)
	}
	c.Indicator = "feature:sentiment.score"
	if err := validateCondition(&c, map[string]bool{"A": true}, 0); err == nil {
		t.Fatal("feature crossover must not use latest feature as previous feature")
	}
}
func TestIndicatorLiveReplayHistoryParity(t *testing.T) {
	values := make([]float64, 1500)
	for i := range values {
		values[i] = 100 + math.Sin(float64(i)/7)*10 + float64(i)/30
	}
	for _, name := range []string{"ema_sma_20", "rsi_wilder_14", "macd_hist_12_26_9", "bb_lower_20"} {
		spec, _ := parseIndicator(name)
		full := strategyMarket{history: map[string][]float64{"A": values}}
		bounded := strategyMarket{history: map[string][]float64{"A": values[len(values)-spec.bars:]}}
		x, err := strategyMetric("A", name, full)
		if err != nil {
			t.Fatal(err)
		}
		y, err := strategyMetric("A", name, bounded)
		if err != nil {
			t.Fatal(err)
		}
		indicatorNear(t, x, y)
		bounded.history["A"] = bounded.history["A"][1:]
		if _, err := strategyMetric("A", name, bounded); err == nil {
			t.Fatal("accepted incomplete warmup", name)
		}
	}
}
func TestIndicatorExecutionResolutionNotes(t *testing.T) {
	spec := simulationSpec{Interval: "1h", Config: sim.Config{SubmissionLatencyMS: 1}}
	tape := []sim.Input{{Type: "market.quote", Metadata: map[string]string{"liquidity_model": "previous_completed_bar_volume"}}}
	if !strings.Contains(simulationExecutionNotes(spec, tape), "entire replay interval") {
		t.Fatal("missing coarse latency limitation")
	}
	spec.Config.SubmissionLatencyMS = 0
	if !strings.Contains(simulationExecutionNotes(spec, tape), "idealized next-open") {
		t.Fatal("missing next-open assumption")
	}
}
func TestIndicatorReplayCheckpointAndAvailability(t *testing.T) {
	def := map[string]any{"universe": []string{"BTC-USD"}, "cadence": "1h", "rules": []any{map[string]any{"when": map[string]any{"all": []any{map[string]any{"indicator": "macd_hist_2_3_2", "operator": ">=", "value": 0}, map[string]any{"indicator": "feature:sentiment.score", "operator": ">", "value": 0}}}, "allocate": []any{map[string]any{"symbol": "BTC-USD", "weight": 0.5}}}}}
	spec := simulationSpec{Strategy: def, Symbols: []string{"BTC-USD"}, Interval: "1h", Config: sim.Config{StartingCash: 10000, BenchmarkSymbol: "BTC-USD"}}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tape := []sim.Input{}
	for i := 0; i < 40; i++ {
		at := start.Add(time.Duration(i) * time.Hour)
		tape = append(tape, sim.Input{ID: fmtIndicatorID(i, "close"), Type: "market.bar.close", Symbol: "BTC-USD", EventTime: at, AvailableAt: at.Add(time.Hour), Data: map[string]float64{"price": 100 + float64(i), "step": float64(i + 1)}}, sim.Input{ID: fmtIndicatorID(i, "open"), Type: "market.quote", Symbol: "BTC-USD", EventTime: at, AvailableAt: at, Data: map[string]float64{"price": 100 + float64(i)}})
	}
	featureTime := start.Add(30 * time.Hour)
	tape = append(tape, sim.Input{ID: "sentiment", Type: "feature.sentiment", Symbol: "BTC-USD", EventTime: start, AvailableAt: featureTime, Data: map[string]float64{"score": 1}})
	run := func(checkpoint bool) (string, int) {
		e, err := sim.New(spec.Config, tape, nil, simulationStrategy(spec))
		if err != nil {
			t.Fatal(err)
		}
		outputs := []sim.Output{}
		steps := 0
		orders := 0
		for !e.State.Finished {
			o, err := e.Advance()
			if err != nil {
				t.Fatal(err)
			}
			outputs = append(outputs, o...)
			for _, out := range o {
				if out.Type == "order.submitted" {
					orders++
					if out.At.Before(featureTime) {
						t.Fatal("lookahead before sentiment availability")
					}
				}
			}
			steps++
			if checkpoint && steps == 20 {
				raw, _ := json.Marshal(e.State)
				var state sim.State
				json.Unmarshal(raw, &state)
				e, err = sim.New(spec.Config, tape, &state, simulationStrategy(spec))
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		return sim.Hash(outputs), orders
	}
	a, n := run(false)
	b, _ := run(true)
	if a != b || n == 0 {
		t.Fatalf("checkpoint mismatch or no signal: %s %s %d", a, b, n)
	}
}
func fmtIndicatorID(i int, s string) string { return time.Unix(int64(i), 0).Format(time.RFC3339) + s }

func TestIndicatorCatalogHTTPAndMCPAgree(t *testing.T) {
	app := &App{}
	response := httptest.NewRecorder()
	app.handleHTTPStrategies(response, httptest.NewRequest("GET", "/strategies/catalog?project_id=test-proj&symbols=BTC-USD,ETH-USD", nil))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var httpCatalog map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &httpCatalog); err != nil {
		t.Fatal(err)
	}
	mcpCatalog, err := app.toolStrategyCatalog(nil, map[string]any{"symbols": []any{"BTC-USD", "ETH-USD"}})
	if err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(mcpCatalog)
	var normalized map[string]any
	json.Unmarshal(serialized, &normalized)
	if sim.Hash(httpCatalog) != sim.Hash(normalized) {
		t.Fatal("HTTP/MCP catalog drift")
	}
	for _, entry := range httpCatalog["presets"].([]any) {
		p := entry.(map[string]any)
		def := p["definition"].(map[string]any)
		if _, _, err := validateStrategyDefinition(def); err != nil {
			t.Fatal(err)
		}
	}
}
func TestIndicatorNestedWarmupLiveProvider(t *testing.T) {
	ctx := newTestCtx(t)
	provider := &recordingStrategyProvider{}
	globalEngine.provider = provider
	def := strategyPresets([]string{"BTC-USD", "ETH-USD"})[0].Definition
	market, err := liveStrategyMarket(ctx, &def)
	if err != nil {
		t.Fatal(err)
	}
	interval, limit, _, fallback := provider.calls()
	if limit != 500 || interval != "1h" || fallback {
		t.Fatalf("history request: %s %d %v", interval, limit, fallback)
	}
	raw, _ := json.Marshal(def)
	var d map[string]any
	json.Unmarshal(raw, &d)
	evaluation, err := evaluateStrategy(&Strategy{Definition: d}, market)
	if err != nil || len(evaluation.Warnings) != 0 || len(evaluation.TargetAllocations) != 2 {
		t.Fatalf("live evaluation: %+v %v", evaluation, err)
	}
}
