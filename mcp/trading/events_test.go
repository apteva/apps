package main

// Tier 1 tests for the app-event surface — every mutation site fires
// the right topic with a payload the UI can route. The SDK's
// AppCtx.SetEmitter lets us swap in an in-memory recorder; nothing
// goes over the wire.

import (
	"sync"
	"testing"
)

// recorder — in-memory implementation of sdk.Emitter used by every
// emit-call-site test. Captures (topic, data) tuples in order.
type recorder struct {
	mu sync.Mutex
	at []recorded
}

type recorded struct {
	Topic string
	Data  map[string]any
}

func (r *recorder) Emit(topic string, data any) {
	r.EmitWithProject(topic, "", data)
}

// EmitWithProject — satisfies sdk.Emitter. AppCtx.Emit dispatches via
// this method, so it's the one tests must implement; project id is
// ignored by the recorder.
func (r *recorder) EmitWithProject(topic, projectID string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, _ := data.(map[string]any)
	r.at = append(r.at, recorded{Topic: topic, Data: m})
}

func (r *recorder) topics() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.at))
	for i, e := range r.at {
		out[i] = e.Topic
	}
	return out
}

func (r *recorder) byTopic(topic string) []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []recorded{}
	for _, e := range r.at {
		if e.Topic == topic {
			out = append(out, e)
		}
	}
	return out
}

// installRecorder swaps the AppCtx emitter for the test's lifetime.
func installRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	if globalCtx == nil {
		t.Fatal("globalCtx not set — call newTestCtx first")
	}
	globalCtx.SetEmitter(r)
	t.Cleanup(func() { globalCtx.SetEmitter(nil) })
	return r
}

// ─── Mutation events ──────────────────────────────────────────────

func TestEvents_PortfolioCreateEmits(t *testing.T) {
	ctx := newTestCtx(t)
	r := installRecorder(t)
	out, err := (&App{}).toolPortfolioCreate(ctx, map[string]any{
		"name":            "EmitTest",
		"allowed_classes": []any{"crypto"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.byTopic("portfolio.created")) != 1 {
		t.Errorf("expected 1 portfolio.created event, got %d (topics=%v)",
			len(r.byTopic("portfolio.created")), r.topics())
	}
	got := r.byTopic("portfolio.created")[0].Data
	if got["name"] != "EmitTest" {
		t.Errorf("payload name=%v", got["name"])
	}
	_ = out
}

func TestEvents_OrderPlaceEmitsPlacedAndRationale(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	r := installRecorder(t)
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id),
		"symbol":       "AAPL", "side": "buy", "type": "limit",
		"qty": 1.0, "limit_price": 220.0,
		"rationale": "rationale long enough to satisfy the pre-trade pipeline check.",
	})
	if out.(map[string]any)["status"] != "working" {
		t.Fatal("expected working")
	}
	if len(r.byTopic("order.placed")) != 1 {
		t.Errorf("expected 1 order.placed, got %d", len(r.byTopic("order.placed")))
	}
	if len(r.byTopic("journal.appended")) != 1 {
		t.Errorf("expected 1 journal.appended (rationale), got %d", len(r.byTopic("journal.appended")))
	}
	op := r.byTopic("order.placed")[0].Data
	if op["status"] != "working" || op["symbol"] != "AAPL" {
		t.Errorf("order.placed payload wrong: %v", op)
	}
}

func TestEvents_OrderRejected_NoEventForPretradeRejection(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	r := installRecorder(t)
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id),
		"symbol":       "AAPL", "side": "buy", "type": "market", "qty": 1.0,
		"rationale":    "too short",
	})
	if out.(map[string]any)["status"] != "rejected" {
		t.Fatal("expected rejected")
	}
	// Pre-trade rejections don't persist orders — and don't emit. The
	// agent gets the rejection synchronously, the UI has no row to
	// invalidate. Engine-side rejections (insufficient_cash etc.) are
	// covered separately below.
	if len(r.byTopic("order.rejected")) != 0 {
		t.Errorf("did not expect order.rejected for pre-trade reject; got %d", len(r.byTopic("order.rejected")))
	}
	if len(r.byTopic("order.placed")) != 0 {
		t.Errorf("did not expect order.placed; got %d", len(r.byTopic("order.placed")))
	}
}

func TestEvents_OrderCancelEmits(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id),
		"symbol":       "AAPL", "side": "buy", "type": "limit",
		"qty": 1.0, "limit_price": 100.0,
		"rationale": "stays working at low limit so we can cancel cleanly.",
	})
	oid := out.(map[string]any)["order_id"].(string)
	r := installRecorder(t)
	if _, err := (&App{}).toolOrderCancel(ctx, map[string]any{"order_id": oid, "reason": "test"}); err != nil {
		t.Fatal(err)
	}
	if len(r.byTopic("order.cancelled")) != 1 {
		t.Errorf("expected 1 order.cancelled, got %d", len(r.byTopic("order.cancelled")))
	}
}

func TestEvents_FillEmitsThreeTopics(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Crypto", []string{"crypto"})
	out, _ := (&App{}).toolOrderPlace(ctx, map[string]any{
		"portfolio_id": float64(id),
		"symbol":       "BTC-USD", "side": "buy", "type": "market",
		"qty":          0.01,
		"rationale":    "starter — fill should produce three downstream events.",
	})
	if out.(map[string]any)["status"] != "working" {
		t.Fatal("expected working")
	}
	r := installRecorder(t)
	if err := markTick(nil, ctx); err != nil {
		t.Fatal(err)
	}
	for _, topic := range []string{"order.filled", "position.changed", "journal.appended"} {
		if len(r.byTopic(topic)) == 0 {
			t.Errorf("expected ≥ 1 %q event after tick, got 0 (topics=%v)", topic, r.topics())
		}
	}
}

func TestEvents_TickAlwaysFiresAtEndOfMarkTick(t *testing.T) {
	ctx := newTestCtx(t)
	r := installRecorder(t)
	if err := markTick(nil, ctx); err != nil {
		t.Fatal(err)
	}
	tick := r.byTopic("tick")
	if len(tick) != 1 {
		t.Fatalf("expected exactly 1 tick event per markTick, got %d", len(tick))
	}
	d := tick[0].Data
	if _, ok := d["providers"].(map[string]any); !ok {
		t.Errorf("tick event missing providers, got %v", d)
	}
	// First tick: every symbol that's in the universe should appear in
	// `marks` (no prior baseline → significantMarkDeltas sends them all).
	if marks, ok := d["marks"].([]*Mark); !ok || len(marks) == 0 {
		t.Errorf("first tick should ship full universe in marks; got %v", d["marks"])
	}
}

func TestEvents_JournalWriteEmits(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	r := installRecorder(t)
	if _, err := (&App{}).toolJournalWrite(ctx, map[string]any{
		"portfolio_id": float64(id), "kind": "thesis",
		"body": "first thesis — testing journal emit.",
	}); err != nil {
		t.Fatal(err)
	}
	if len(r.byTopic("journal.appended")) != 1 {
		t.Errorf("expected 1 journal.appended, got %d", len(r.byTopic("journal.appended")))
	}
}

func TestEvents_WatchlistAddRemoveEmit(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	r := installRecorder(t)
	app := &App{}
	_, _ = app.toolWatchlistAdd(ctx, map[string]any{"portfolio_id": float64(id), "symbol": "AAPL"})
	_, _ = app.toolWatchlistAdd(ctx, map[string]any{"portfolio_id": float64(id), "symbol": "AAPL"}) // dedupe → no event
	_, _ = app.toolWatchlistRemove(ctx, map[string]any{"portfolio_id": float64(id), "symbol": "AAPL"})
	if got := len(r.byTopic("watchlist.changed")); got != 2 {
		t.Errorf("expected 2 watchlist.changed events (1 add + 1 remove, dedupe skipped), got %d", got)
	}
}

func TestEvents_PortfolioPauseEmits(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "Eq", []string{"equity"})
	r := installRecorder(t)
	if _, err := (&App{}).toolPortfolioPause(ctx, map[string]any{
		"portfolio_id": float64(id), "reason": "self-test pause",
	}); err != nil {
		t.Fatal(err)
	}
	scs := r.byTopic("portfolio.status.changed")
	if len(scs) != 1 || scs[0].Data["status"] != "paused" {
		t.Errorf("expected 1 portfolio.status.changed{status=paused}, got %v", scs)
	}
	// Also one journal.appended because pause writes an alert row.
	if len(r.byTopic("journal.appended")) != 1 {
		t.Errorf("expected 1 journal.appended on pause, got %d", len(r.byTopic("journal.appended")))
	}
}

// ─── Tick payload — significant-deltas filter ─────────────────────

func TestEvents_TickDeltaFiltersUnchangedSymbols(t *testing.T) {
	ctx := newTestCtx(t)
	r := installRecorder(t)

	// First tick: full universe.
	_ = markTick(nil, ctx)
	first := r.byTopic("tick")
	if len(first) != 1 {
		t.Fatalf("first tick missing")
	}
	firstMarks, _ := first[0].Data["marks"].([]*Mark)
	if len(firstMarks) == 0 {
		t.Fatal("first tick should carry marks")
	}

	// Second tick: mock universe is deterministic but does drift each
	// invocation by a tiny amount. With our 0.1% threshold for crypto
	// and 0.5¢ for polymarket, several symbols will fall under the
	// threshold and be filtered out. We just assert the second tick's
	// marks count is ≤ first (filtering happened).
	_ = markTick(nil, ctx)
	second := r.byTopic("tick")
	if len(second) != 2 {
		t.Fatalf("expected 2 tick events, got %d", len(second))
	}
	secondMarks, _ := second[1].Data["marks"].([]*Mark)
	if len(secondMarks) > len(firstMarks) {
		t.Errorf("second-tick mark count (%d) larger than first (%d) — filter not applied?",
			len(secondMarks), len(firstMarks))
	}
}
