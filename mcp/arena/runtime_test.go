package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apteva/apps/mcp/arena/engine"
	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "arena.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	mig, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type recNotifier struct {
	mu    sync.Mutex
	kinds []string
	turns []int64
}

func (r *recNotifier) MatchChanged(m *Match, kind string) {
	r.mu.Lock()
	r.kinds = append(r.kinds, kind)
	r.mu.Unlock()
}
func (r *recNotifier) AgentTurn(m *Match, s Seat) {
	r.mu.Lock()
	r.turns = append(r.turns, s.AgentID)
	r.mu.Unlock()
}

func newRT(t *testing.T, db *sql.DB) (*Runtime, *recNotifier) {
	n := &recNotifier{}
	r := NewRuntime(db, n)
	r.BotDelay = time.Millisecond
	return r, n
}

const pid = "p1"

func waitTurn(t *testing.T, r *Runtime, id int64, seat int) *SeatState {
	t.Helper()
	st, _, err := r.Wait(context.Background(), pid, id, seat, 0, true, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAgentVsPerfectBotTicTacToe(t *testing.T) {
	r, n := newRT(t, testDB(t))
	res, err := r.Create(CreateRequest{ProjectID: pid, Game: "tictactoe", Seats: []string{"me", "bot:perfect"}, CallerAgent: 7})
	if err != nil {
		t.Fatal(err)
	}
	if res.Match.Status != "live" || res.You == nil || *res.You != 0 {
		t.Fatalf("expected live match with me at seat 0: %+v", res)
	}
	id := res.Match.ID
	seat, err := r.ResolveSeat(pid, id, 7, "")
	if err != nil || seat != 0 {
		t.Fatalf("resolve: %v %d", err, seat)
	}
	if _, err := r.ResolveSeat(pid, id, 8, ""); err != ErrNotSeated {
		t.Fatalf("other agent should not be seated: %v", err)
	}
	for i := 0; i < 5; i++ {
		st := waitTurn(t, r, id, 0)
		if st.Match.Status != "live" {
			break
		}
		cells := st.Legal[0].Args["cell"].([]int)
		if _, err := r.Act(pid, id, 0, engine.Action{Type: "place", Data: map[string]any{"cell": float64(cells[0])}}); err != nil {
			t.Fatal(err)
		}
	}
	st := waitTurn(t, r, id, 0)
	if st.Match.Status != "finished" {
		t.Fatalf("expected finished, got %s", st.Match.Status)
	}
	if st.Match.Result.Ranks[1] != 1 {
		t.Fatalf("perfect bot should never lose: %+v", st.Match.Result)
	}
	seed, _ := hex.DecodeString(st.Match.Seed)
	h := sha256.Sum256(seed)
	if hex.EncodeToString(h[:]) != st.Match.SeedHash {
		t.Fatal("revealed seed does not match committed hash")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !strings.Contains(strings.Join(n.kinds, ","), "finished") || len(n.turns) == 0 {
		t.Fatalf("notifications missing: %v turns=%v", n.kinds, n.turns)
	}
}

func TestHoldemJoinHiddenInfoAndTimeout(t *testing.T) {
	db := testDB(t)
	r, _ := newRT(t, db)
	zero := 0
	res, err := r.Create(CreateRequest{ProjectID: pid, Game: "holdem", Seats: []string{"me", "open", "human:Ann"}, CallerAgent: 1, TurnSeconds: &zero})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Match.ID
	if res.Match.Status != "waiting" || res.Tokens[2] == "" {
		t.Fatalf("expected waiting match with token for seat 2: %+v", res)
	}
	if _, err := r.Act(pid, id, 0, engine.Action{Type: "call"}); err != ErrNotStarted {
		t.Fatalf("expected not started, got %v", err)
	}
	j, err := r.Join(JoinRequest{ProjectID: pid, MatchID: id, CallerAgent: 2})
	if err != nil || j.Seat != 1 || j.Match.Status != "live" {
		t.Fatalf("join: %v %+v", err, j)
	}
	if _, err := r.Join(JoinRequest{ProjectID: pid, MatchID: id, CallerAgent: 3}); err == nil {
		t.Fatal("third agent should not be able to join a full match")
	}
	annSeat, err := r.ResolveSeat(pid, id, 0, res.Tokens[2])
	if err != nil || annSeat != 2 {
		t.Fatalf("token resolve: %v", err)
	}
	if _, err := r.ResolveSeat(pid, id, 0, "seat_bogus"); err == nil {
		t.Fatal("bogus token accepted")
	}
	// Spectator never sees hole cards, in state or events.
	spec, _ := r.State(pid, id, engine.Spectator, 50)
	for _, e := range spec.Recent {
		if e.Type == "hole_cards" {
			t.Fatal("spectator saw hole cards event")
		}
	}
	mine, _ := r.State(pid, id, 0, 50)
	holes := 0
	for _, e := range mine.Recent {
		if e.Type == "hole_cards" {
			holes++
			if *e.Seat != 0 {
				t.Fatal("seat 0 saw another seat's hole cards")
			}
		}
	}
	if holes != 1 {
		t.Fatalf("seat 0 should see exactly its own hole cards, saw %d", holes)
	}
	// Out-of-turn move is rejected.
	toAct := mine.Match.ToAct[0]
	other := (toAct + 1) % 3
	if _, err := r.Act(pid, id, other, engine.Action{Type: "fold"}); err == nil {
		t.Fatal("out-of-turn move accepted")
	}
	// Persistence: a fresh runtime on the same DB restores identical state.
	r2, _ := newRT(t, db)
	st2, err := r2.State(pid, id, toAct, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.YourTurn || st2.Match.Version != mine.Match.Version {
		t.Fatalf("restored state mismatch: %+v", st2.Match)
	}
	// Wrong project cannot see the match.
	if _, err := r.State("other", id, 0, 0); err != ErrNotFound {
		t.Fatalf("expected not found across projects, got %v", err)
	}
}

func TestTurnClockPlaysDefaultMove(t *testing.T) {
	r, _ := newRT(t, testDB(t))
	one := 1
	res, err := r.Create(CreateRequest{ProjectID: pid, Game: "tictactoe", Seats: []string{"me", "bot:random"}, CallerAgent: 5, TurnSeconds: &one})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Match.ID
	// Do nothing: the clock should place for seat 0, then the bot replies.
	st, _, err := r.Wait(context.Background(), pid, id, 0, 2, false, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if st.Match.Version < 2 {
		t.Fatalf("expected auto move, version=%d", st.Match.Version)
	}
	evs, _ := r.Events(pid, id, 0, 0, 100)
	found := false
	for _, e := range evs {
		if e.Type == "timeout" {
			found = true
		}
	}
	if !found {
		t.Fatal("timeout event not recorded")
	}
}

func TestBotsOnlyHoldemFinishes(t *testing.T) {
	r, n := newRT(t, testDB(t))
	res, err := r.Create(CreateRequest{ProjectID: pid, Game: "holdem", Seats: []string{"bot:basic", "bot:random", "bot:basic"},
		Options: map[string]any{"starting_stack": float64(200), "small_blind": float64(10), "big_blind": float64(20)}})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := r.Wait(context.Background(), pid, res.Match.ID, engine.Spectator, 0, true, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if st.Match.Status != "finished" {
		t.Fatalf("bot match did not finish: %s v%d", st.Match.Status, st.Match.Version)
	}
	ms, _ := r.List(pid, ListFilter{Status: "finished"})
	if len(ms) != 1 {
		t.Fatalf("list finished: %d", len(ms))
	}
	_ = n
}

func TestChatAndCancel(t *testing.T) {
	r, _ := newRT(t, testDB(t))
	res, _ := r.Create(CreateRequest{ProjectID: pid, Game: "tictactoe", Seats: []string{"me", "open"}, CallerAgent: 9})
	id := res.Match.ID
	if err := r.Chat(pid, id, 0, "good luck"); err != nil {
		t.Fatal(err)
	}
	st, _ := r.State(pid, id, engine.Spectator, 10)
	if len(st.Chat) != 1 || st.Chat[0].Data["text"] != "good luck" {
		t.Fatalf("chat missing: %+v", st.Chat)
	}
	m, err := r.Cancel(pid, id)
	if err != nil || m.Status != "cancelled" || m.Seed == "" {
		t.Fatalf("cancel: %v %+v", err, m)
	}
	if _, err := r.Join(JoinRequest{ProjectID: pid, MatchID: id, CallerAgent: 10}); err == nil {
		t.Fatal("joined a cancelled match")
	}
}
