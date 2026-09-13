package backtest

import (
	"encoding/json"
	"testing"
	"time"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func quote(id string, second int, price, volume float64) Input {
	return Input{ID: id, Type: "market.quote", Symbol: "XYZ", EventTime: epoch.Add(time.Duration(second) * time.Second), AvailableAt: epoch.Add(time.Duration(second) * time.Second), Data: map[string]float64{"price": price, "volume": volume}}
}

func finish(t *testing.T, e *Engine) []Output {
	t.Helper()
	out := []Output{}
	for i := 0; !e.State.Finished; i++ {
		if i > 1000 {
			t.Fatal("simulation failed to terminate")
		}
		batch, err := e.Advance()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, batch...)
	}
	return out
}

func TestLatencyPartialFillsCostsAndCheckpointDeterminism(t *testing.T) {
	config := Config{Seed: 7, StartingCash: 1000, SubmissionLatencyMS: 1500, MaxFillQty: 2, Costs: Costs{FeeBps: 10, SlippageBps: 10}, BenchmarkSymbol: "XYZ"}
	inputs := []Input{quote("q0", 0, 100, 100), quote("q1", 1, 100, 100), quote("q2", 2, 100, 100), quote("q3", 3, 100, 100), quote("q4", 4, 110, 100)}
	strategy := func(s *State, in Input) ([]Command, error) {
		if in.ID == "q0" {
			return []Command{{Order: &Order{Symbol: "XYZ", Side: "buy", Qty: 5}}}, nil
		}
		return nil, nil
	}
	e, err := New(config, inputs, nil, strategy)
	if err != nil {
		t.Fatal(err)
	}
	out := finish(t, e)
	o := e.State.Orders[0]
	if o.FilledQty != 5 || o.Status != "filled" || !o.AcceptedAt.Equal(epoch.Add(1500*time.Millisecond)) {
		t.Fatalf("order=%+v", o)
	}
	var fills []Output
	for _, ev := range out {
		if ev.Type == "fill" {
			fills = append(fills, ev)
		}
	}
	if len(fills) != 3 || !fills[0].At.Equal(epoch.Add(2*time.Second)) {
		t.Fatalf("fills=%+v", fills)
	}
	if e.State.Fees <= 0 || e.Metrics()["benchmark_return_pct"] < 9.99 {
		t.Fatalf("metrics=%v", e.Metrics())
	}
	// Serializing a partial-fill checkpoint must not alter IDs, fees, event
	// ordering, or any byte of the subsequent result ledger.
	resumed, _ := New(config, inputs, nil, strategy)
	prefix := []Output{}
	for resumed.State.Now.Before(epoch.Add(2 * time.Second)) {
		batch, err := resumed.Advance()
		if err != nil {
			t.Fatal(err)
		}
		prefix = append(prefix, batch...)
	}
	raw, _ := json.Marshal(resumed.State)
	var checkpoint State
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	resumed, err = New(config, inputs, &checkpoint, strategy)
	if err != nil {
		t.Fatal(err)
	}
	got := append(prefix, finish(t, resumed)...)
	if Hash(out) != Hash(got) || Hash(e.State) != Hash(resumed.State) {
		t.Fatal("checkpoint replay diverged")
	}
}

func TestAvailabilityOrderingAndCancelLatency(t *testing.T) {
	news := Input{ID: "news", Type: "feature.sentiment", Symbol: "XYZ", EventTime: epoch, AvailableAt: epoch.Add(3 * time.Second), Data: map[string]float64{"score": 0.8}}
	inputs := []Input{quote("q4", 4, 100, 100), news, quote("q0", 0, 100, 100), quote("q1", 1, 100, 100), quote("q2", 2, 100, 100), quote("q3", 3, 100, 100)}
	seen := false
	strategy := func(s *State, in Input) ([]Command, error) {
		if in.AvailableAt.Before(news.AvailableAt) && s.Features["XYZ/sentiment"] != nil {
			t.Fatal("lookahead: unpublished sentiment visible")
		}
		if in.ID == "q0" {
			return []Command{{Order: &Order{Symbol: "XYZ", Side: "buy", Qty: 10}}}, nil
		}
		if in.ID == "q1" {
			return []Command{{CancelID: s.Orders[0].ID}}, nil
		}
		if in.ID == "q3" {
			seen = s.Features["XYZ/sentiment"]["score"] == 0.8
		}
		return nil, nil
	}
	e, err := New(Config{StartingCash: 10000, MaxFillQty: 1, CancellationLatencyMS: 1500}, inputs, nil, strategy)
	if err != nil {
		t.Fatal(err)
	}
	finish(t, e)
	o := e.State.Orders[0]
	if o.FilledQty != 3 || o.Status != "cancelled" || !o.ResolvedAt.Equal(epoch.Add(2500*time.Millisecond)) {
		t.Fatalf("order=%+v", o)
	}
	if !seen {
		t.Fatal("sentiment not visible on publication")
	}
}

func TestVolumeBudgetIsSharedAndIOCExpires(t *testing.T) {
	strategy := func(s *State, in Input) ([]Command, error) {
		if in.ID == "q0" {
			return []Command{{Order: &Order{Symbol: "XYZ", Side: "buy", Qty: 10, TIF: "ioc"}}, {Order: &Order{Symbol: "XYZ", Side: "buy", Qty: 10, TIF: "ioc"}}}, nil
		}
		return nil, nil
	}
	e, err := New(Config{StartingCash: 10000, ParticipationRate: 0.1}, []Input{quote("q0", 0, 100, 30)}, nil, strategy)
	if err != nil {
		t.Fatal(err)
	}
	finish(t, e)
	if e.State.Positions["XYZ"].Qty != 3 {
		t.Fatalf("volume consumed more than once: %+v", e.State.Positions)
	}
	for _, o := range e.State.Orders {
		if o.Status != "cancelled" {
			t.Fatalf("IOC remainder survives: %+v", o)
		}
	}
}

func TestLimitsStopsAndEndOfTapeExpiry(t *testing.T) {
	strategy := func(s *State, in Input) ([]Command, error) {
		if in.ID == "q0" {
			return []Command{{Order: &Order{Symbol: "XYZ", Side: "buy", Type: "limit", LimitPrice: 95, Qty: 1}}, {Order: &Order{Symbol: "XYZ", Side: "buy", Type: "stop", StopPrice: 105, Qty: 1}}, {Order: &Order{Symbol: "XYZ", Side: "buy", Type: "limit", LimitPrice: 1, Qty: 1}}}, nil
		}
		return nil, nil
	}
	e, err := New(Config{StartingCash: 1000}, []Input{quote("q0", 0, 100, 30), quote("q1", 1, 90, 30), quote("q2", 2, 110, 30)}, nil, strategy)
	if err != nil {
		t.Fatal(err)
	}
	finish(t, e)
	if e.State.Orders[0].AvgFillPrice != 90 || e.State.Orders[1].AvgFillPrice != 110 || e.State.Orders[2].Status != "expired" {
		t.Fatalf("orders=%+v", e.State.Orders)
	}
}

func TestRejectsInvalidInputs(t *testing.T) {
	bad := quote("q", 0, 100, 1)
	bad.AvailableAt = epoch.Add(-time.Second)
	if _, err := New(Config{StartingCash: 100}, []Input{bad}, nil, nil); err == nil {
		t.Fatal("accepted availability before event")
	}
	q := quote("q", 0, 100, 1)
	if _, err := New(Config{StartingCash: 100}, []Input{q, q}, nil, nil); err == nil {
		t.Fatal("accepted duplicate IDs")
	}
}

func TestFillTimeRiskLimitsPreventCombinedOverexposure(t *testing.T) {
	strategy := func(s *State, in Input) ([]Command, error) {
		if in.ID == "q0" {
			return []Command{{Order: &Order{Symbol: "XYZ", Side: "buy", Qty: 2}}, {Order: &Order{Symbol: "XYZ", Side: "buy", Qty: 2}}}, nil
		}
		return nil, nil
	}
	e, err := New(Config{StartingCash: 1000, Risk: RiskLimits{MaxPositionPct: 25}}, []Input{quote("q0", 0, 100, 100)}, nil, strategy)
	if err != nil {
		t.Fatal(err)
	}
	finish(t, e)
	if e.State.Positions["XYZ"].Qty != 2 || e.State.Orders[1].Status != "rejected" {
		t.Fatalf("combined orders exceeded risk limit: %+v", e.State)
	}
}

// Hourly OHLC cannot resolve millisecond fills. Keep the approximation explicit:
// zero latency may use the next bar's open at the close boundary; any positive
// latency waits for a genuinely fresh later quote, never the observed bar close.
func TestHourlyOpenQuoteLatencyResolution(t *testing.T) {
	inputs := []Input{quote("q0", 0, 100, 100), {ID: "closed", Type: "market.bar.close", Symbol: "XYZ", EventTime: epoch, AvailableAt: epoch.Add(time.Hour), Data: map[string]float64{"price": 105}}, quote("q1", 3600, 110, 100), quote("q2", 7200, 120, 100)}
	for _, ms := range []int64{0, 1, 250} {
		strategy := func(s *State, in Input) ([]Command, error) {
			if in.ID == "closed" {
				return []Command{{Order: &Order{Symbol: "XYZ", Side: "buy", Qty: 1}}}, nil
			}
			return nil, nil
		}
		e, err := New(Config{StartingCash: 1000, SubmissionLatencyMS: ms}, inputs, nil, strategy)
		if err != nil {
			t.Fatal(err)
		}
		outputs := finish(t, e)
		wantPrice, wantTime := 110.0, epoch.Add(time.Hour)
		if ms > 0 {
			wantPrice, wantTime = 120, epoch.Add(2*time.Hour)
		}
		if e.State.Orders[0].AvgFillPrice != wantPrice {
			t.Fatalf("latency %d price %v", ms, e.State.Orders[0].AvgFillPrice)
		}
		seen := false
		for _, out := range outputs {
			if out.Type == "fill" {
				seen = true
				if !out.At.Equal(wantTime) {
					t.Fatalf("latency %d fill at %s", ms, out.At)
				}
			}
		}
		if !seen {
			t.Fatal("missing fill")
		}
	}
}
