package main

import (
	"testing"
	"time"
)

func testIndicatorBars(n int) []IndicatorBar {
	start := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)
	out := make([]IndicatorBar, 0, n)
	for i := 0; i < n; i++ {
		close := 100 + float64(i)*0.75 + float64(i%5)
		out = append(out, IndicatorBar{
			T: start.Add(time.Duration(i) * time.Hour).Unix(),
			O: close - 0.5,
			H: close + 1.2,
			L: close - 1.4,
			C: close,
			V: 1000 + float64(i*10),
		})
	}
	return out
}

func TestComputeIndicatorsCoreSet(t *testing.T) {
	res, err := computeIndicators("BTC-USD", "1h", "3M", "test", testIndicatorBars(80), []string{
		"sma_20", "ema_20", "ema_50", "rsi_14", "macd_12_26_9", "bbands_20_2", "atr_14", "volatility_20", "return_20", "volume_sma_20",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sma_20", "ema_20", "ema_50", "rsi_14", "macd_12_26_9", "bbands_20_2", "atr_14", "volatility_20", "return_20", "volume_sma_20"} {
		if _, ok := res.Values[key]; !ok {
			t.Fatalf("missing %s in values: %#v", key, res.Values)
		}
	}
	if res.Summary["bias"] == "" {
		t.Fatalf("missing summary: %#v", res.Summary)
	}
	if res.BarCount != 80 {
		t.Fatalf("bar_count=%d, want 80", res.BarCount)
	}
}

func TestIndicatorSeriesRSI(t *testing.T) {
	series, meta, err := computeIndicatorSeries("rsi_14", testIndicatorBars(40))
	if err != nil {
		t.Fatal(err)
	}
	if len(series) == 0 {
		t.Fatal("empty series")
	}
	if meta["points"] != len(series) {
		t.Fatalf("meta points=%v, series len=%d", meta["points"], len(series))
	}
	last := series[len(series)-1].Value
	if last <= 0 || last > 100 {
		t.Fatalf("last RSI=%v, want 0..100", last)
	}
}

func TestParseIndicatorSelectionPresetAndExplicit(t *testing.T) {
	got := parseIndicatorSelection(map[string]any{
		"preset":     "trend",
		"indicators": "rsi_14, atr_14",
	})
	seen := map[string]bool{}
	for _, name := range got {
		seen[name] = true
	}
	for _, want := range []string{"ema_20", "ema_50", "macd_12_26_9", "rsi_14", "atr_14"} {
		if !seen[want] {
			t.Fatalf("selection missing %s: %#v", want, got)
		}
	}
}
