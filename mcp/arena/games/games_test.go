package games_test

import (
	"fmt"
	"testing"

	"github.com/apteva/apps/mcp/arena/engine"
	"github.com/apteva/apps/mcp/arena/games/holdem"
	_ "github.com/apteva/apps/mcp/arena/games/tictactoe"
)

func hv(t *testing.T, cards ...string) holdem.HandValue {
	t.Helper()
	cs := []holdem.Card{}
	for _, c := range cards {
		x, err := holdem.ParseCard(c)
		if err != nil {
			t.Fatal(err)
		}
		cs = append(cs, x)
	}
	v, _ := holdem.Best(cs)
	return v
}

func TestHandRanking(t *testing.T) {
	order := [][]string{
		{"2c", "4d", "6h", "8s", "Tc", "Jd", "Kh"}, // high card
		{"2c", "2d", "6h", "8s", "Tc", "Jd", "Kh"}, // pair
		{"2c", "2d", "6h", "6s", "Tc", "Jd", "Kh"}, // two pair
		{"2c", "2d", "2h", "8s", "Tc", "Jd", "Kh"}, // trips
		{"Ac", "2d", "3h", "4s", "5c", "Jd", "Kh"}, // wheel straight
		{"6c", "7d", "8h", "9s", "Tc", "Jd", "2h"}, // J-high straight
		{"2c", "4c", "6c", "8c", "Jc", "Jd", "Kh"}, // flush
		{"2c", "2d", "2h", "8s", "8c", "Jd", "Kh"}, // full house
		{"2c", "2d", "2h", "2s", "Tc", "Jd", "Kh"}, // quads
		{"9h", "Th", "Jh", "Qh", "Kh", "2c", "3d"}, // straight flush
	}
	var prev holdem.HandValue = -1
	for i, cs := range order {
		v := hv(t, cs...)
		if v <= prev {
			t.Fatalf("hand %d (%s) not stronger than previous", i, v.Name())
		}
		prev = v
	}
	if hv(t, "As", "Ad", "Kc", "Qh", "9s") <= hv(t, "As", "Ad", "Kc", "Qh", "8s") {
		t.Fatal("kicker ignored")
	}
	if hv(t, "As", "Kd", "Qc", "Jh", "9s") != hv(t, "Ac", "Kh", "Qd", "Js", "9h") {
		t.Fatal("suits should not matter for equal high cards")
	}
}

func playOut(t *testing.T, gameID string, seats int, opts map[string]any, seed string, level string) engine.State {
	t.Helper()
	g, _ := engine.Get(gameID)
	st, _, err := g.New([]byte(seed), seats, opts)
	if err != nil {
		t.Fatal(err)
	}
	rng := engine.RNG([]byte(seed), "test-bots", 0)
	for steps := 0; st.Result() == nil; steps++ {
		if steps > 20000 {
			t.Fatalf("%s seed %s did not finish", gameID, seed)
		}
		seat := st.ToAct()[0]
		a := st.BotAction(seat, level, rng)
		if _, err := st.Apply(seat, a); err != nil {
			t.Fatalf("bot made illegal move %+v: %v", a, err)
		}
	}
	return st
}

func TestTicTacToePerfectBotsDraw(t *testing.T) {
	for i := 0; i < 20; i++ {
		st := playOut(t, "tictactoe", 2, nil, fmt.Sprint(i), "perfect")
		if st.Result().Reason != "draw" {
			t.Fatalf("perfect play should draw, got %+v", st.Result())
		}
	}
}

func TestHoldemChipConservation(t *testing.T) {
	for seats := 2; seats <= 6; seats++ {
		for i := 0; i < 40; i++ {
			for _, level := range []string{"random", "basic"} {
				opts := map[string]any{"starting_stack": float64(500), "small_blind": float64(5), "big_blind": float64(10)}
				st := playOut(t, "holdem", seats, opts, fmt.Sprintf("s%d-%d-%s", seats, i, level), level)
				view := st.View(engine.Spectator).(map[string]any)
				total := 0
				for _, p := range view["players"].([]map[string]any) {
					total += p["stack"].(int)
				}
				if total != 500*seats {
					t.Fatalf("seats=%d seed=%d: chips not conserved: %d", seats, i, total)
				}
				r := st.Result()
				if len(r.Winners) != 1 || r.Ranks[r.Winners[0]] != 1 {
					t.Fatalf("expected one winner: %+v", r)
				}
			}
		}
	}
}

func TestHoldemHiddenCards(t *testing.T) {
	g, _ := engine.Get("holdem")
	st, evs, _ := g.New([]byte("x"), 3, nil)
	for _, e := range evs {
		if e.Type == "hole_cards" && e.VisibleTo(engine.Spectator) {
			t.Fatal("hole cards visible to spectator")
		}
	}
	v := st.View(1).(map[string]any)
	for _, p := range v["players"].([]map[string]any) {
		if _, ok := p["hole"]; ok && p["seat"] != 1 {
			t.Fatal("other seat's hole cards leaked")
		}
	}
	if _, ok := v["your_hole"]; !ok {
		t.Fatal("own hole cards missing")
	}
}

func TestHoldemMaxHands(t *testing.T) {
	st := playOut(t, "holdem", 4, map[string]any{"max_hands": float64(3)}, "mh", "basic")
	if st.View(engine.Spectator).(map[string]any)["hand"].(int) > 3 {
		t.Fatal("played past max_hands")
	}
}
