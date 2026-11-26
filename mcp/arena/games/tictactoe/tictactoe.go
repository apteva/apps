// Package tictactoe is the smallest Arena game, used to prove the engine.
package tictactoe

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"

	"github.com/apteva/apps/mcp/arena/engine"
)

func init() { engine.Register(Game{}) }

type Game struct{}

func (Game) Info() engine.Info {
	return engine.Info{
		ID:       "tictactoe",
		Name:     "Tic-tac-toe",
		Summary:  "Classic 3x3 noughts and crosses. Seat 0 plays X and moves first.",
		Rules:    "Players alternate placing their mark in an empty cell. Three in a row (horizontal, vertical or diagonal) wins. A full board with no line is a draw.",
		MinSeats: 2, MaxSeats: 2,
		ActionHelp:  `{"type":"place","data":{"cell":0..8}} — cells are numbered row by row: 0 1 2 / 3 4 5 / 6 7 8.`,
		DefaultTurn: 60,
	}
}

type State struct {
	Board   [9]int          `json:"board"` // -1 empty, else seat
	Turn    int             `json:"turn"`
	Moves   int             `json:"moves"`
	Outcome *engine.Outcome `json:"result,omitempty"`
}

func (Game) New(_ []byte, seats int, _ map[string]any) (engine.State, []engine.Event, error) {
	if seats != 2 {
		return nil, nil, errors.New("tic-tac-toe needs exactly 2 seats")
	}
	s := &State{}
	for i := range s.Board {
		s.Board[i] = -1
	}
	return s, []engine.Event{{Type: "game_started", Data: map[string]any{"first": 0}}}, nil
}

func (Game) Restore(raw json.RawMessage) (engine.State, error) {
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *State) ToAct() []int {
	if s.Outcome != nil {
		return nil
	}
	return []int{s.Turn}
}

func mark(seat int) string {
	switch seat {
	case 0:
		return "X"
	case 1:
		return "O"
	}
	return "."
}

func (s *State) View(seat int) any {
	rows := make([]string, 3)
	for r := 0; r < 3; r++ {
		line := ""
		for c := 0; c < 3; c++ {
			line += mark(s.Board[r*3+c])
		}
		rows[r] = line
	}
	v := map[string]any{"board": s.Board, "rows": rows, "turn": s.Turn, "moves": s.Moves}
	if seat >= 0 {
		v["you"] = mark(seat)
	}
	if s.Outcome != nil {
		v["result"] = s.Outcome
	}
	return v
}

func (s *State) empty() []int {
	out := []int{}
	for i, v := range s.Board {
		if v < 0 {
			out = append(out, i)
		}
	}
	return out
}

func (s *State) Legal(seat int) []engine.ActionSpec {
	if s.Outcome != nil || seat != s.Turn {
		return nil
	}
	return []engine.ActionSpec{{Type: "place", Description: "Place your mark in an empty cell", Args: map[string]any{"cell": s.empty()}}}
}

var lines = [8][3]int{{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, {0, 3, 6}, {1, 4, 7}, {2, 5, 8}, {0, 4, 8}, {2, 4, 6}}

func winner(b [9]int) int {
	for _, l := range lines {
		if b[l[0]] >= 0 && b[l[0]] == b[l[1]] && b[l[1]] == b[l[2]] {
			return b[l[0]]
		}
	}
	return -1
}

func (s *State) Apply(seat int, a engine.Action) ([]engine.Event, error) {
	if s.Outcome != nil {
		return nil, engine.ErrGameOver
	}
	if seat != s.Turn {
		return nil, engine.ErrNotYourTurn
	}
	if a.Type != "place" {
		return nil, fmt.Errorf("unknown action %q (use place)", a.Type)
	}
	cell, ok := engine.IntArg(a.Data, "cell")
	if !ok || cell < 0 || cell > 8 {
		return nil, errors.New("cell must be an integer 0..8")
	}
	if s.Board[cell] >= 0 {
		return nil, fmt.Errorf("cell %d is taken", cell)
	}
	s.Board[cell] = seat
	s.Moves++
	evs := []engine.Event{{Type: "placed", Seat: engine.SeatPtr(seat), Data: map[string]any{"cell": cell, "mark": mark(seat)}}}
	if w := winner(s.Board); w >= 0 {
		scores := []float64{0, 0}
		scores[w] = 1
		ranks := []int{2, 2}
		ranks[w] = 1
		s.Outcome = &engine.Outcome{Winners: []int{w}, Ranks: ranks, Scores: scores, Reason: "three in a row"}
	} else if s.Moves == 9 {
		s.Outcome = &engine.Outcome{Winners: []int{}, Ranks: []int{1, 1}, Scores: []float64{0.5, 0.5}, Reason: "draw"}
	} else {
		s.Turn = 1 - s.Turn
	}
	if s.Outcome != nil {
		evs = append(evs, engine.Event{Type: "game_over", Data: map[string]any{"result": s.Outcome}})
	}
	return evs, nil
}

func (s *State) Result() *engine.Outcome { return s.Outcome }

func (s *State) TimeoutAction(seat int) engine.Action {
	e := s.empty()
	if len(e) == 0 {
		return engine.Action{Type: "place"}
	}
	return engine.Action{Type: "place", Data: map[string]any{"cell": e[0]}}
}

// BotAction: "random" plays any empty cell; anything else plays a perfect
// minimax game (the board is tiny).
func (s *State) BotAction(seat int, level string, rng *rand.Rand) engine.Action {
	e := s.empty()
	if level == "random" {
		return engine.Action{Type: "place", Data: map[string]any{"cell": e[rng.IntN(len(e))]}}
	}
	best, bestScore := []int{}, -2
	for _, c := range e {
		b := s.Board
		b[c] = seat
		sc := -negamax(b, 1-seat)
		if sc > bestScore {
			best, bestScore = []int{c}, sc
		} else if sc == bestScore {
			best = append(best, c)
		}
	}
	return engine.Action{Type: "place", Data: map[string]any{"cell": best[rng.IntN(len(best))]}}
}

// negamax returns the score from the perspective of `toMove`.
func negamax(b [9]int, toMove int) int {
	if w := winner(b); w >= 0 {
		if w == toMove {
			return 1
		}
		return -1
	}
	best, any := -2, false
	for i := range b {
		if b[i] >= 0 {
			continue
		}
		any = true
		b[i] = toMove
		if sc := -negamax(b, 1-toMove); sc > best {
			best = sc
		}
		b[i] = -1
	}
	if !any {
		return 0
	}
	return best
}
