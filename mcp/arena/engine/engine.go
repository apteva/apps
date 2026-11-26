// Package engine defines the rules contract every Arena game implements.
//
// A game is a deterministic state machine: given the same seed, seat count,
// options and ordered action log it always reaches the same state. The match
// runtime stores the seed + action log and a JSON snapshot of the state, so
// any match can be replayed or audited.
//
// Hidden information is enforced by View: callers only ever receive the view
// for their own seat (or the spectator view, seat = Spectator).
package engine

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"sort"
)

// Spectator is the seat index used for public (no private info) views.
const Spectator = -1

// Info describes a game for listings and agent onboarding.
type Info struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Summary     string         `json:"summary"`
	Rules       string         `json:"rules"`
	MinSeats    int            `json:"min_seats"`
	MaxSeats    int            `json:"max_seats"`
	Options     map[string]any `json:"options,omitempty"` // option name -> description/default
	ActionHelp  string         `json:"action_help"`
	DefaultTurn int            `json:"default_turn_seconds"`
}

// Action is a move submitted by a seat. Type is game specific ("place",
// "fold", "raise"...); Data carries arguments.
type Action struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// ActionSpec tells a seat what it may legally do right now.
type ActionSpec struct {
	Type        string         `json:"type"`
	Description string         `json:"description,omitempty"`
	Args        map[string]any `json:"args,omitempty"` // e.g. {"cell": [0,2,5]} or {"min": 20, "max": 400}
}

// Event is something that happened as a result of an action. PrivateTo,
// when non-empty, limits who can see it (e.g. dealt hole cards).
type Event struct {
	Type      string         `json:"type"`
	Seat      *int           `json:"seat,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	PrivateTo []int          `json:"private_to,omitempty"`
}

// VisibleTo reports whether seat may see the event.
func (e Event) VisibleTo(seat int) bool {
	if len(e.PrivateTo) == 0 {
		return true
	}
	for _, s := range e.PrivateTo {
		if s == seat {
			return true
		}
	}
	return false
}

// Outcome is the terminal result. Scores are game specific (chips for
// poker, 1/0.5/0 for board games); Ranks are 1-based placings.
type Outcome struct {
	Winners []int     `json:"winners"`
	Ranks   []int     `json:"ranks"`
	Scores  []float64 `json:"scores"`
	Reason  string    `json:"reason"`
}

// State is a live, mutable, JSON-serialisable game state.
type State interface {
	// ToAct returns the seats that may act now (usually one).
	ToAct() []int
	// View returns what seat may see. Spectator gets public info only.
	View(seat int) any
	// Legal lists legal actions for seat (empty when not its turn).
	Legal(seat int) []ActionSpec
	// Apply validates and applies a. It must not mutate state on error.
	Apply(seat int, a Action) ([]Event, error)
	// Result is nil until the game is over.
	Result() *Outcome
	// TimeoutAction is the move applied when seat's clock runs out.
	TimeoutAction(seat int) Action
	// BotAction picks a move for a built-in bot at the given level.
	BotAction(seat int, level string, rng *rand.Rand) Action
}

// Game creates and restores states.
type Game interface {
	Info() Info
	New(seed []byte, seats int, opts map[string]any) (State, []Event, error)
	Restore(raw json.RawMessage) (State, error)
}

var registry = map[string]Game{}

// Register adds a game to the catalog. Called from game packages' init.
func Register(g Game) { registry[g.Info().ID] = g }

// Get returns a registered game.
func Get(id string) (Game, bool) { g, ok := registry[id]; return g, ok }

// List returns all games sorted by id.
func List() []Info {
	out := make([]Info, 0, len(registry))
	for _, g := range registry {
		out = append(out, g.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RNG returns a deterministic generator derived from seed and a label, so
// games can draw independent streams (e.g. one shuffle per hand).
func RNG(seed []byte, label string, n uint64) *rand.Rand {
	h := sha256.New()
	h.Write(seed)
	h.Write([]byte(label))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	h.Write(b[:])
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return rand.New(rand.NewChaCha8(key))
}

// ErrNotYourTurn is returned when a seat acts out of turn.
var ErrNotYourTurn = errors.New("not your turn")

// ErrGameOver is returned when acting on a finished game.
var ErrGameOver = errors.New("game is over")

// IntArg reads an integer argument from action data.
func IntArg(d map[string]any, key string) (int, bool) {
	switch x := d[key].(type) {
	case float64:
		return int(x), x == float64(int(x))
	case int:
		return x, true
	case int64:
		return int(x), true
	case json.Number:
		n, err := x.Int64()
		return int(n), err == nil
	}
	return 0, false
}

// SeatPtr is a helper for Event.Seat.
func SeatPtr(s int) *int { return &s }
