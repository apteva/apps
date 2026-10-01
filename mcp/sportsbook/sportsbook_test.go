package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func testApp(t *testing.T) *App {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, file := range []string{"migrations/001_init.sql", "migrations/002_catalog.sql"} {
		raw, _ := embedded.ReadFile(file)
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return &App{db: db, now: func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }}
}
func seed(t *testing.T, a *App) (bankroll, market string, quote int64, prediction string) {
	t.Helper()
	if _, err := a.loadDemo("p1", "test"); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT id FROM bankrolls WHERE project_id='p1'").Scan(&bankroll); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT m.id FROM markets m JOIN events e ON e.project_id=m.project_id AND e.id=m.event_id WHERE e.project_id='p1' AND e.status='scheduled' AND e.sport='football' ORDER BY e.starts_at LIMIT 1").Scan(&market); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT id FROM odds_observations WHERE project_id='p1' AND market_id=? AND selection='home' ORDER BY id DESC LIMIT 1", market).Scan(&quote); err != nil {
		t.Fatal(err)
	}
	out, err := a.predict("p1", "test", market)
	if err != nil {
		t.Fatal(err)
	}
	prediction = out.(map[string]any)["id"].(string)
	return
}
func proposal(t *testing.T, a *App, bankroll string, quote int64, prediction string, stake int64) string {
	t.Helper()
	out, err := a.propose("p1", "test", map[string]any{"bankroll_id": bankroll, "quote_id": quote, "prediction_id": prediction, "stake_minor": stake, "rationale": "Historical results support this paper simulation"})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["id"].(string)
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *appError
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("want error %s, got %v", want, err)
	}
}
func assertBalanced(t *testing.T, a *App) {
	t.Helper()
	var n int
	err := a.db.QueryRow("SELECT COUNT(*) FROM (SELECT transaction_id,SUM(amount_minor) s FROM ledger_entries GROUP BY project_id,transaction_id HAVING s<>0)").Scan(&n)
	if err != nil || n != 0 {
		t.Fatalf("unbalanced transactions: %d %v", n, err)
	}
}
func TestManifestAndToolParity(t *testing.T) {
	a := testApp(t)
	m := a.Manifest()
	if err := sdk.ValidateManifest(&m); err != nil {
		t.Fatal(err)
	}
	if len(m.Requires.Integrations) != 4 {
		t.Fatal("four roles required")
	}
	for _, d := range m.Requires.Integrations {
		if d.Mode != "multiple" {
			t.Fatalf("single role %s", d.Role)
		}
	}
	if len(a.MCPTools()) != len(m.Provides.MCPTools) {
		t.Fatal("tool parity")
	}
	for _, tool := range a.MCPTools() {
		if permission(tool.Name) == "" {
			t.Fatalf("missing permission %s", tool.Name)
		}
	}
}
func TestPaperLifecycleAndBalancedLedger(t *testing.T) {
	for _, outcome := range []string{"won", "lost", "void"} {
		t.Run(outcome, func(t *testing.T) {
			a := testApp(t)
			bankroll, market, q, p := seed(t, a)
			_ = market
			id := proposal(t, a, bankroll, q, p, 1000)
			bet, err := a.accept("p1", "test", id)
			if err != nil {
				t.Fatal(err)
			}
			betID := bet.(map[string]any)["id"].(string)
			cash, locked, err := balances(a.db, "p1", bankroll)
			if err != nil || cash != 99000 || locked != 1000 {
				t.Fatalf("reservation %d %d %v", cash, locked, err)
			}
			var price int64
			a.db.QueryRow("SELECT odds_micros FROM odds_observations WHERE id=?", q).Scan(&price)
			returned := int64(0)
			if outcome == "won" {
				returned, _ = payout(1000, price)
			} else if outcome == "void" {
				returned = 1000
			}
			if _, err = a.settle("p1", "test", map[string]any{"bet_id": betID, "outcome": outcome, "note": "Manual simulation settlement"}); err != nil {
				t.Fatal(err)
			}
			cash, locked, err = balances(a.db, "p1", bankroll)
			if err != nil || cash != 99000+returned || locked != 0 {
				t.Fatalf("settlement %d %d %v", cash, locked, err)
			}
			assertBalanced(t, a)
			var count int
			a.db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&count)
			if _, err = a.accept("p1", "test", id); err != nil {
				t.Fatal(err)
			}
			if _, err = a.settle("p1", "test", map[string]any{"bet_id": betID, "outcome": outcome, "note": "Retry the same settlement"}); err != nil {
				t.Fatal(err)
			}
			var after int
			a.db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&after)
			if after != count {
				t.Fatal("retry duplicated ledger")
			}
			different := "lost"
			if outcome == "lost" {
				different = "won"
			}
			_, err = a.settle("p1", "test", map[string]any{"bet_id": betID, "outcome": different, "note": "Attempt changed settlement"})
			code(t, err, "already_settled")
		})
	}
}
func TestConcurrentAcceptIsIdempotent(t *testing.T) {
	a := testApp(t)
	b, _, q, p := seed(t, a)
	id := proposal(t, a, b, q, p, 1000)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := []string{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := a.accept("p1", "test", id)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids = append(ids, out.(map[string]any)["id"].(string))
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, v := range ids {
		if v != ids[0] {
			t.Fatal("duplicate acceptance")
		}
	}
	cash, locked, _ := balances(a.db, "p1", b)
	if cash != 99000 || locked != 1000 {
		t.Fatal("duplicate reservation")
	}
	assertBalanced(t, a)
}
func TestLargeBankrollRiskAllowance(t *testing.T) {
	// Maximum exact JSON integer, at the largest supported exposure policy.
	if got := riskAllowance(9007199254740991, 5000); got != 4503599627370495 {
		t.Fatalf("large bankroll risk allowance: %d", got)
	}
}

func TestSettlementRangeFailureIsAtomic(t *testing.T) {
	a := testApp(t)
	b, _, q, p := seed(t, a)
	accepted, err := a.accept("p1", "test", proposal(t, a, b, q, p, 1000))
	if err != nil {
		t.Fatal(err)
	}
	bet := accepted.(map[string]any)["id"].(string)
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	topUp := maxMoneyMinor - 100000
	if err = ledger(tx, "p1", b, "range-test", map[string]int64{"cash": topUp, "equity": -topUp}, a.clock()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = a.settle("p1", "test", map[string]any{"bet_id": bet, "outcome": "won", "note": "Range boundary simulation"})
	code(t, err, "accounting_range")
	cash, locked, err := balances(a.db, "p1", b)
	if err != nil || cash != maxMoneyMinor-1000 || locked != 1000 {
		t.Fatal("rejected settlement changed funds")
	}
	var status string
	if err = a.db.QueryRow("SELECT status FROM bets WHERE id=?", bet).Scan(&status); err != nil || status != "open" {
		t.Fatal("rejected settlement changed bet")
	}
	assertBalanced(t, a)
}

func TestRiskRecheckedAtAcceptance(t *testing.T) {
	a := testApp(t)
	b, _, q, p := seed(t, a)
	ids := []string{}
	for i := 0; i < 6; i++ {
		ids = append(ids, proposal(t, a, b, q, p, 2000))
	}
	for i := 0; i < 5; i++ {
		if _, err := a.accept("p1", "test", ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	_, err := a.accept("p1", "test", ids[5])
	code(t, err, "exposure_limit")
	cash, locked, _ := balances(a.db, "p1", b)
	if cash != 90000 || locked != 10000 {
		t.Fatal("failed acceptance changed funds")
	}
	_, err = a.propose("p1", "test", map[string]any{"bankroll_id": b, "quote_id": q, "prediction_id": p, "stake_minor": 2001, "rationale": "This stake exceeds the risk limit"})
	code(t, err, "stake_limit")
}
func TestQuoteExpiryAndSupersession(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		a := testApp(t)
		b, _, q, p := seed(t, a)
		id := proposal(t, a, b, q, p, 1000)
		old := a.clock()
		a.now = func() time.Time { return time.Unix(old+quoteTTL, 0) }
		_, err := a.accept("p1", "test", id)
		code(t, err, "proposal_expired")
	})
	t.Run("replacement", func(t *testing.T) {
		a := testApp(t)
		b, _, q, p := seed(t, a)
		id := proposal(t, a, b, q, p, 1000)
		if _, err := a.loadDemo("p1", "test"); err != nil {
			t.Fatal(err)
		}
		_, err := a.accept("p1", "test", id)
		code(t, err, "quote_changed")
		_, err = a.propose("p1", "test", map[string]any{"bankroll_id": b, "quote_id": q, "prediction_id": p, "stake_minor": 1000, "rationale": "Old quote cannot be reused"})
		code(t, err, "quote_unavailable")
	})
}
func TestProjectAndExampleIsolation(t *testing.T) {
	a := testApp(t)
	b, m, q, p := seed(t, a)
	out, err := a.workspace("p2", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.(map[string]any)["events"].([]map[string]any)) != 0 {
		t.Fatal("project leak")
	}
	out, err = a.workspace("p1", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.(map[string]any)["events"].([]map[string]any)) != 0 {
		t.Fatal("example leak")
	}
	_, err = a.predict("p2", "test", m)
	code(t, err, "not_found")
	_, err = a.propose("p2", "test", map[string]any{"bankroll_id": b, "quote_id": q, "prediction_id": p, "stake_minor": 1000, "rationale": "Cross-project proposal attempt"})
	code(t, err, "quote_unavailable")
	live, err := a.createBankroll("p1", "test", map[string]any{"name": "Connected data", "currency": "EUR", "initial_minor": int64(100000)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.propose("p1", "test", map[string]any{"bankroll_id": live.(map[string]any)["id"], "quote_id": q, "prediction_id": p, "stake_minor": 1000, "rationale": "Mixing example and connected data"})
	code(t, err, "data_mode_mismatch")
}
func TestExperimentalModelSnapshotsAndNoFutureLeak(t *testing.T) {
	a := testApp(t)
	_, m, _, p := seed(t, a)
	var model, raw string
	if err := a.db.QueryRow("SELECT model,features FROM predictions WHERE project_id='p1' AND id=?", p).Scan(&model, &raw); err != nil {
		t.Fatal(err)
	}
	if model != "elo-v2-experimental" {
		t.Fatalf("model=%s", model)
	}
	var features map[string]any
	json.Unmarshal([]byte(raw), &features)
	if features["calibrated"] != false {
		t.Fatal("unvalidated model misrepresented")
	}
	for _, v := range features["training_events"].([]any) {
		if int64(v.(map[string]any)["starts_at"].(float64)) >= a.clock() {
			t.Fatal("future leak")
		}
	}
	if _, err := a.predict("p1", "test", m); err != nil {
		t.Fatal(err)
	}
	var count int
	a.db.QueryRow("SELECT COUNT(*) FROM predictions").Scan(&count)
	if count != 2 {
		t.Fatal("prediction not immutable")
	}
}
func TestBookmakerBaselineAndIncompleteMarkets(t *testing.T) {
	a := testApp(t)
	_, m, _, _ := seed(t, a)
	if _, err := a.db.Exec("UPDATE events SET home_score=NULL WHERE status='finished'"); err != nil {
		t.Fatal(err)
	}
	out, err := a.predict("p1", "test", m)
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["model"] != "bookmaker-baseline-v1" {
		t.Fatal("must label baseline")
	}
	probs := out.(map[string]any)["probabilities"].(map[string]float64)
	sum := 0.0
	for _, p := range probs {
		sum += p
	}
	if math.Abs(sum-1) > 1e-12 {
		t.Fatal("not normalized")
	}
	if _, err = a.db.Exec("UPDATE quote_heads SET snapshot_id=''"); err != nil {
		t.Fatal(err)
	}
	_, err = a.predict("p1", "test", m)
	code(t, err, "insufficient_evidence")
}
func TestAuthorizationAndLiveExecution(t *testing.T) {
	code(t, authorize(context.Background(), "proposal_accept", "p1"), "caller_required")
	denied := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "p1", DefaultEffect: "deny", Grants: []sdk.Grant{{Effect: "allow", Permission: "sportsbook.read", Resource: "*"}}})
	if err := authorize(denied, "workspace_get", "p1"); err != nil {
		t.Fatal(err)
	}
	code(t, authorize(denied, "proposal_accept", "p1"), "permission_denied")
	code(t, authorize(denied, "workspace_get", "p2"), "project_mismatch")
	a := testApp(t)
	_, err := a.perform(context.Background(), nil, "p1", "test", "bet_submit", nil)
	code(t, err, "live_execution_unavailable")
}
func TestIntegerMoneyValidationAndPayout(t *testing.T) {
	for _, v := range []any{1.5, math.NaN(), math.Inf(1), "100", float64(1e20)} {
		if intArg(map[string]any{"n": v}, "n") != 0 {
			t.Fatalf("accepted %v", v)
		}
	}
	got, err := payout(101, 1550000)
	if err != nil || got != 157 {
		t.Fatalf("fixed precision payout %d %v", got, err)
	}
	_, err = payout(math.MaxInt64, 1000000000)
	code(t, err, "payout_overflow")
}
func TestEventClosureRejectsAcceptance(t *testing.T) {
	a := testApp(t)
	b, m, q, p := seed(t, a)
	id := proposal(t, a, b, q, p, 1000)
	a.db.Exec("UPDATE events SET status='live' WHERE id=(SELECT event_id FROM markets WHERE id=?)", m)
	_, err := a.accept("p1", "test", id)
	code(t, err, "event_closed")
}
func TestDemoReloadPreservesAcceptedBets(t *testing.T) {
	a := testApp(t)
	b, _, q, p := seed(t, a)
	id := proposal(t, a, b, q, p, 1000)
	if _, err := a.accept("p1", "test", id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.loadDemo("p1", "test"); err != nil {
		t.Fatal(err)
	}
	var n int
	a.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
	if n != 54 {
		t.Fatalf("duplicate demo %d", n)
	}
	cash, locked, _ := balances(a.db, "p1", b)
	if cash != 99000 || locked != 1000 {
		t.Fatal("reload reset bankroll")
	}
}
