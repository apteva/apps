// Package holdem implements no-limit Texas Hold'em with play-money chips.
//
// A match is a sequence of hands that runs until one player holds all the
// chips or max_hands is reached. Side pots, heads-up blind order, split pots
// and odd-chip distribution are handled. Every hand's deck is drawn from a
// deterministic stream derived from the match seed, so a revealed seed lets
// anyone verify the deals.
//
// Simplification: an all-in raise smaller than a full raise does not reopen
// betting formally, but players who already acted may still re-raise.
package holdem

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/apteva/apps/mcp/arena/engine"
)

func init() { engine.Register(Game{}) }

type Game struct{}

func (Game) Info() engine.Info {
	return engine.Info{
		ID:       "holdem",
		Name:     "No-limit Texas Hold'em",
		Summary:  "Play-money no-limit Hold'em for 2-9 seats. Runs hand after hand until one player has every chip or max_hands is reached.",
		Rules:    "Each player gets two private hole cards; five community cards are dealt (flop 3, turn 1, river 1) with a betting round before the flop and after each deal. Make the best five-card hand from your two cards and the board. Blinds are forced bets; the button rotates each hand. Chips are play money only.",
		MinSeats: 2, MaxSeats: 9,
		Options: map[string]any{
			"starting_stack": "chips per player (default 1000)",
			"small_blind":    "default 10",
			"big_blind":      "default 20",
			"max_hands":      "stop after this many hands; 0 = play until one player remains (default 0)",
		},
		ActionHelp:  `fold | check | call | raise {"to": total_bet_this_street} | all_in. "bet" is accepted as an alias of raise when nobody has bet. Amounts are the TOTAL you will have in front of you this street, not the increment.`,
		DefaultTurn: 45,
	}
}

type Player struct {
	Stack     int    `json:"stack"`
	Bet       int    `json:"bet"`       // this street
	Committed int    `json:"committed"` // this hand
	Folded    bool   `json:"folded"`
	AllIn     bool   `json:"all_in"`
	Out       bool   `json:"out"` // busted
	Acted     bool   `json:"acted"`
	Hole      []Card `json:"hole,omitempty"`
	BustHand  int    `json:"bust_hand,omitempty"`
}

type HandSummary struct {
	Hand     int              `json:"hand"`
	Board    []string         `json:"board"`
	Shown    map[int][]string `json:"shown,omitempty"`
	Hands    map[int]string   `json:"hands,omitempty"`
	Pots     []PotResult      `json:"pots"`
	Showdown bool             `json:"showdown"`
}

type PotResult struct {
	Amount  int    `json:"amount"`
	Winners []int  `json:"winners"`
	Hand    string `json:"hand,omitempty"`
}

type State struct {
	Seed       []byte          `json:"seed"`
	Start      int             `json:"start"`
	SB         int             `json:"sb"`
	BB         int             `json:"bb"`
	MaxHands   int             `json:"max_hands"`
	Players    []*Player       `json:"players"`
	HandNo     int             `json:"hand_no"`
	Dealer     int             `json:"dealer"`
	Deck       []Card          `json:"deck"`
	Board      []Card          `json:"board"`
	Street     string          `json:"street"` // preflop|flop|turn|river
	ToActSeat  int             `json:"to_act"`
	CurrentBet int             `json:"current_bet"`
	MinRaise   int             `json:"min_raise"`
	LastHand   *HandSummary    `json:"last_hand,omitempty"`
	Outcome    *engine.Outcome `json:"result,omitempty"`
}

func optInt(opts map[string]any, key string, def int) int {
	if v, ok := engine.IntArg(opts, key); ok && v > 0 {
		return v
	}
	if _, present := opts[key]; present {
		if v, ok := engine.IntArg(opts, key); ok && v == 0 {
			return 0
		}
	}
	return def
}

func (Game) New(seed []byte, seats int, opts map[string]any) (engine.State, []engine.Event, error) {
	if seats < 2 || seats > 9 {
		return nil, nil, errors.New("hold'em needs 2-9 seats")
	}
	s := &State{
		Seed:     seed,
		Start:    optInt(opts, "starting_stack", 1000),
		SB:       optInt(opts, "small_blind", 10),
		BB:       optInt(opts, "big_blind", 20),
		MaxHands: optInt(opts, "max_hands", 0),
		Dealer:   -1,
	}
	if s.SB <= 0 || s.BB < s.SB || s.Start < s.BB {
		return nil, nil, errors.New("need 0 < small_blind <= big_blind <= starting_stack")
	}
	for i := 0; i < seats; i++ {
		s.Players = append(s.Players, &Player{Stack: s.Start})
	}
	evs := []engine.Event{{Type: "game_started", Data: map[string]any{"starting_stack": s.Start, "small_blind": s.SB, "big_blind": s.BB}}}
	evs = append(evs, s.startHand()...)
	return s, evs, nil
}

func (Game) Restore(raw json.RawMessage) (engine.State, error) {
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// --- seat iteration -------------------------------------------------------

func (s *State) n() int { return len(s.Players) }

// next returns the next seat after from satisfying ok, or -1.
func (s *State) next(from int, ok func(*Player) bool) int {
	for i := 1; i <= s.n(); i++ {
		j := (from + i + s.n()) % s.n()
		if ok(s.Players[j]) {
			return j
		}
	}
	return -1
}

func inHand(p *Player) bool { return !p.Out && !p.Folded }
func canAct(p *Player) bool { return !p.Out && !p.Folded && !p.AllIn }
func notOut(p *Player) bool { return !p.Out }

func (s *State) count(ok func(*Player) bool) int {
	c := 0
	for _, p := range s.Players {
		if ok(p) {
			c++
		}
	}
	return c
}

func (s *State) pot() int {
	t := 0
	for _, p := range s.Players {
		t += p.Committed
	}
	return t
}

// --- hand lifecycle -------------------------------------------------------

func (s *State) startHand() []engine.Event {
	if s.count(notOut) < 2 || (s.MaxHands > 0 && s.HandNo >= s.MaxHands) {
		return s.finish()
	}
	s.HandNo++
	for _, p := range s.Players {
		p.Bet, p.Committed, p.Folded, p.AllIn, p.Acted, p.Hole = 0, 0, false, false, false, nil
	}
	s.Dealer = s.next(s.Dealer, notOut)
	s.Deck = newDeck()
	rng := engine.RNG(s.Seed, "holdem-deck", uint64(s.HandNo))
	rng.Shuffle(len(s.Deck), func(i, j int) { s.Deck[i], s.Deck[j] = s.Deck[j], s.Deck[i] })
	s.Board = nil
	s.Street = "preflop"

	var sbSeat, bbSeat int
	if s.count(notOut) == 2 { // heads-up: button posts the small blind
		sbSeat = s.Dealer
	} else {
		sbSeat = s.next(s.Dealer, notOut)
	}
	bbSeat = s.next(sbSeat, notOut)
	s.post(sbSeat, s.SB)
	s.post(bbSeat, s.BB)
	s.CurrentBet = s.BB
	s.MinRaise = s.BB

	evs := []engine.Event{{Type: "hand_started", Data: map[string]any{
		"hand": s.HandNo, "dealer": s.Dealer, "small_blind_seat": sbSeat, "big_blind_seat": bbSeat,
		"small_blind": s.SB, "big_blind": s.BB, "stacks": s.stacks(),
	}}}
	for i, p := range s.Players {
		if p.Out {
			continue
		}
		p.Hole = []Card{s.draw(), s.draw()}
		evs = append(evs, engine.Event{Type: "hole_cards", Seat: engine.SeatPtr(i), Data: map[string]any{"cards": cardStrings(p.Hole)}, PrivateTo: []int{i}})
	}
	s.ToActSeat = s.next(bbSeat, canAct)
	// Everyone all-in from the blinds (tiny stacks): run it out.
	if s.roundComplete() {
		evs = append(evs, s.advance()...)
	}
	return evs
}

func (s *State) post(seat, amount int) {
	p := s.Players[seat]
	if amount >= p.Stack {
		amount = p.Stack
		p.AllIn = true
	}
	p.Stack -= amount
	p.Bet += amount
	p.Committed += amount
}

func (s *State) draw() Card {
	c := s.Deck[0]
	s.Deck = s.Deck[1:]
	return c
}

func (s *State) stacks() []int {
	out := make([]int, s.n())
	for i, p := range s.Players {
		out[i] = p.Stack
	}
	return out
}

// roundComplete: every player who can still act has acted and matched.
func (s *State) roundComplete() bool {
	for _, p := range s.Players {
		if canAct(p) && (!p.Acted || p.Bet < s.CurrentBet) {
			// A lone player facing no further action does not need to act.
			if s.count(canAct) == 1 && p.Bet >= s.CurrentBet {
				continue
			}
			return false
		}
	}
	return true
}

// advance moves to the next street, runs out the board, or goes to showdown.
func (s *State) advance() []engine.Event {
	var evs []engine.Event
	for {
		for _, p := range s.Players {
			p.Bet, p.Acted = 0, false
		}
		s.CurrentBet, s.MinRaise = 0, s.BB
		switch s.Street {
		case "preflop":
			s.Street = "flop"
			s.draw() // burn
			s.Board = append(s.Board, s.draw(), s.draw(), s.draw())
		case "flop":
			s.Street = "turn"
			s.draw()
			s.Board = append(s.Board, s.draw())
		case "turn":
			s.Street = "river"
			s.draw()
			s.Board = append(s.Board, s.draw())
		case "river":
			return append(evs, s.showdown()...)
		}
		evs = append(evs, engine.Event{Type: "board", Data: map[string]any{"street": s.Street, "board": cardStrings(s.Board), "pot": s.pot()}})
		if s.count(canAct) >= 2 {
			s.ToActSeat = s.next(s.Dealer, canAct)
			return evs
		}
		// 0 or 1 players can act: nothing to bet, keep dealing.
	}
}

func (s *State) showdown() []engine.Event {
	sum := &HandSummary{Hand: s.HandNo, Board: cardStrings(s.Board), Shown: map[int][]string{}, Hands: map[int]string{}, Showdown: true}
	values := map[int]HandValue{}
	for i, p := range s.Players {
		if inHand(p) {
			v, _ := Best(append(append([]Card{}, p.Hole...), s.Board...))
			values[i] = v
			sum.Shown[i] = cardStrings(p.Hole)
			sum.Hands[i] = v.Name()
		}
	}
	// Build pots by commitment level.
	levels := []int{}
	seen := map[int]bool{}
	for _, p := range s.Players {
		if inHand(p) && !seen[p.Committed] && p.Committed > 0 {
			seen[p.Committed] = true
			levels = append(levels, p.Committed)
		}
	}
	sort.Ints(levels)
	prev := 0
	for li, lvl := range levels {
		amount := 0
		for _, p := range s.Players {
			amount += min(p.Committed, lvl) - min(p.Committed, prev)
		}
		// Chips committed above the highest live level (folded overbets) go to the last pot.
		if li == len(levels)-1 {
			for _, p := range s.Players {
				if p.Committed > lvl {
					amount += p.Committed - lvl
				}
			}
		}
		prev = lvl
		var best HandValue = -1
		winners := []int{}
		for i, p := range s.Players {
			if !inHand(p) || p.Committed < lvl {
				continue
			}
			if v := values[i]; v > best {
				best, winners = v, []int{i}
			} else if v == best {
				winners = append(winners, i)
			}
		}
		s.award(amount, winners)
		sum.Pots = append(sum.Pots, PotResult{Amount: amount, Winners: winners, Hand: best.Name()})
	}
	return s.endHand(sum)
}

// award splits amount among winners; odd chips go to the first winner(s)
// left of the dealer.
func (s *State) award(amount int, winners []int) {
	if len(winners) == 0 {
		return
	}
	sort.Slice(winners, func(i, j int) bool {
		return (winners[i]-s.Dealer-1+s.n())%s.n() < (winners[j]-s.Dealer-1+s.n())%s.n()
	})
	share, rem := amount/len(winners), amount%len(winners)
	for k, w := range winners {
		s.Players[w].Stack += share
		if k < rem {
			s.Players[w].Stack++
		}
	}
}

func (s *State) endHand(sum *HandSummary) []engine.Event {
	s.LastHand = sum
	evs := []engine.Event{{Type: "hand_result", Data: map[string]any{"summary": sum, "stacks": s.stacks()}}}
	for i, p := range s.Players {
		if !p.Out && p.Stack == 0 {
			p.Out = true
			p.BustHand = s.HandNo
			evs = append(evs, engine.Event{Type: "busted", Seat: engine.SeatPtr(i)})
		}
	}
	for _, p := range s.Players {
		p.Committed, p.Bet = 0, 0
	}
	return append(evs, s.startHand()...)
}

func (s *State) finish() []engine.Event {
	n := s.n()
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa, pb := s.Players[order[a]], s.Players[order[b]]
		if pa.Stack != pb.Stack {
			return pa.Stack > pb.Stack
		}
		return pa.BustHand > pb.BustHand || (pa.BustHand == 0 && pb.BustHand != 0)
	})
	ranks := make([]int, n)
	scores := make([]float64, n)
	for pos, seat := range order {
		ranks[seat] = pos + 1
		if pos > 0 {
			prev := s.Players[order[pos-1]]
			cur := s.Players[seat]
			if prev.Stack == cur.Stack && prev.BustHand == cur.BustHand {
				ranks[seat] = ranks[order[pos-1]]
			}
		}
	}
	winners := []int{}
	for i, p := range s.Players {
		scores[i] = float64(p.Stack - s.Start)
		if ranks[i] == 1 {
			winners = append(winners, i)
		}
	}
	reason := "one player has all the chips"
	if s.count(notOut) > 1 {
		reason = fmt.Sprintf("hand limit (%d) reached", s.MaxHands)
	}
	s.Outcome = &engine.Outcome{Winners: winners, Ranks: ranks, Scores: scores, Reason: reason}
	s.Street = "finished"
	return []engine.Event{{Type: "game_over", Data: map[string]any{"result": s.Outcome, "stacks": s.stacks()}}}
}

// --- engine.State ---------------------------------------------------------

func (s *State) ToAct() []int {
	if s.Outcome != nil {
		return nil
	}
	return []int{s.ToActSeat}
}

func (s *State) Result() *engine.Outcome { return s.Outcome }

func (s *State) toCall(seat int) int {
	p := s.Players[seat]
	return min(s.CurrentBet-p.Bet, p.Stack)
}

func (s *State) Legal(seat int) []engine.ActionSpec {
	if s.Outcome != nil || seat != s.ToActSeat {
		return nil
	}
	p := s.Players[seat]
	call := s.toCall(seat)
	out := []engine.ActionSpec{}
	if call > 0 {
		out = append(out, engine.ActionSpec{Type: "fold"})
		out = append(out, engine.ActionSpec{Type: "call", Args: map[string]any{"amount": call}})
	} else {
		out = append(out, engine.ActionSpec{Type: "check"})
	}
	maxTo := p.Bet + p.Stack
	minTo := s.CurrentBet + s.MinRaise
	if maxTo > s.CurrentBet {
		if maxTo > minTo {
			name := "raise"
			if s.CurrentBet == 0 {
				name = "bet"
			}
			out = append(out, engine.ActionSpec{Type: name, Description: "Total bet this street (not the increment)", Args: map[string]any{"to": map[string]int{"min": minTo, "max": maxTo}}})
		}
		out = append(out, engine.ActionSpec{Type: "all_in", Args: map[string]any{"to": maxTo}})
	}
	return out
}

func (s *State) Apply(seat int, a engine.Action) ([]engine.Event, error) {
	if s.Outcome != nil {
		return nil, engine.ErrGameOver
	}
	if seat != s.ToActSeat {
		return nil, engine.ErrNotYourTurn
	}
	p := s.Players[seat]
	call := s.toCall(seat)
	ev := engine.Event{Type: a.Type, Seat: engine.SeatPtr(seat), Data: map[string]any{}}
	switch a.Type {
	case "fold":
		if call == 0 {
			return nil, errors.New("nothing to call — check instead of folding")
		}
		p.Folded = true
	case "check":
		if call > 0 {
			return nil, fmt.Errorf("cannot check: %d to call", call)
		}
	case "call":
		if call == 0 {
			return nil, errors.New("nothing to call — use check")
		}
		s.post(seat, call)
		ev.Data["amount"] = call
	case "raise", "bet", "all_in":
		to := p.Bet + p.Stack
		if a.Type != "all_in" {
			v, ok := engine.IntArg(a.Data, "to")
			if !ok {
				v, ok = engine.IntArg(a.Data, "amount")
			}
			if !ok {
				return nil, errors.New(`raise needs {"to": total_bet_this_street}`)
			}
			to = v
		}
		maxTo := p.Bet + p.Stack
		if to > maxTo {
			return nil, fmt.Errorf("you only have %d (max total %d)", p.Stack, maxTo)
		}
		if to <= s.CurrentBet {
			return nil, fmt.Errorf("raise must exceed the current bet of %d", s.CurrentBet)
		}
		if to < s.CurrentBet+s.MinRaise && to != maxTo {
			return nil, fmt.Errorf("minimum raise is to %d (or all-in for %d)", s.CurrentBet+s.MinRaise, maxTo)
		}
		raiseBy := to - s.CurrentBet
		s.post(seat, to-p.Bet)
		if raiseBy >= s.MinRaise {
			s.MinRaise = raiseBy
			for i, o := range s.Players { // full raise reopens action
				if i != seat {
					o.Acted = false
				}
			}
		}
		s.CurrentBet = to
		ev.Data["to"] = to
		if p.AllIn {
			ev.Data["all_in"] = true
		}
	default:
		return nil, fmt.Errorf("unknown action %q", a.Type)
	}
	p.Acted = true
	ev.Data["pot"] = s.pot()
	evs := []engine.Event{ev}

	if s.count(inHand) == 1 {
		winner := s.next(seat, inHand)
		amount := s.pot()
		s.Players[winner].Stack += amount
		return append(evs, s.endHand(&HandSummary{Hand: s.HandNo, Board: cardStrings(s.Board), Pots: []PotResult{{Amount: amount, Winners: []int{winner}}}})...), nil
	}
	if s.roundComplete() {
		return append(evs, s.advance()...), nil
	}
	s.ToActSeat = s.next(seat, canAct)
	return evs, nil
}

func (s *State) TimeoutAction(seat int) engine.Action {
	if s.toCall(seat) == 0 {
		return engine.Action{Type: "check"}
	}
	return engine.Action{Type: "fold"}
}

func (s *State) View(seat int) any {
	players := make([]map[string]any, s.n())
	for i, p := range s.Players {
		pv := map[string]any{"seat": i, "stack": p.Stack, "bet": p.Bet, "committed": p.Committed, "folded": p.Folded, "all_in": p.AllIn, "out": p.Out}
		if i == s.Dealer {
			pv["dealer"] = true
		}
		if i == seat && len(p.Hole) > 0 {
			pv["hole"] = cardStrings(p.Hole)
		}
		players[i] = pv
	}
	v := map[string]any{
		"hand": s.HandNo, "street": s.Street, "board": cardStrings(s.Board), "pot": s.pot(),
		"current_bet": s.CurrentBet, "min_raise": s.MinRaise, "dealer": s.Dealer,
		"blinds": []int{s.SB, s.BB}, "players": players,
	}
	if s.Outcome == nil {
		v["to_act"] = s.ToActSeat
	} else {
		v["result"] = s.Outcome
	}
	if seat >= 0 && seat < s.n() {
		p := s.Players[seat]
		v["you"] = seat
		if len(p.Hole) > 0 {
			v["your_hole"] = cardStrings(p.Hole)
			if len(s.Board) >= 3 {
				val, _ := Best(append(append([]Card{}, p.Hole...), s.Board...))
				v["your_hand"] = val.Name()
			}
		}
		if seat == s.ToActSeat && s.Outcome == nil {
			v["to_call"] = s.toCall(seat)
		}
	}
	if s.LastHand != nil {
		v["last_hand"] = s.LastHand
	}
	return v
}

// --- bots -----------------------------------------------------------------

// preflopStrength is a rough 0..1 score for two hole cards.
func preflopStrength(h []Card) float64 {
	a, b := h[0].Rank(), h[1].Rank()
	if a < b {
		a, b = b, a
	}
	score := float64(a+b) / 28
	if a == b {
		score += 0.35 + float64(a)/40
	}
	if h[0].Suit() == h[1].Suit() {
		score += 0.06
	}
	if a-b == 1 {
		score += 0.04
	}
	return min(score, 1)
}

func (s *State) BotAction(seat int, level string, rng *rand.Rand) engine.Action {
	legal := s.Legal(seat)
	has := func(t string) *engine.ActionSpec {
		for i := range legal {
			if legal[i].Type == t {
				return &legal[i]
			}
		}
		return nil
	}
	p := s.Players[seat]
	call := s.toCall(seat)
	passive := engine.Action{Type: "check"}
	if call > 0 {
		passive = engine.Action{Type: "call"}
	}
	raiseTo := func(frac float64) *engine.Action {
		spec := has("raise")
		if spec == nil {
			spec = has("bet")
		}
		if spec == nil {
			return nil
		}
		r := spec.Args["to"].(map[string]int)
		to := r["min"] + int(float64(s.pot())*frac)
		if to > r["max"] {
			to = r["max"]
		}
		return &engine.Action{Type: spec.Type, Data: map[string]any{"to": to}}
	}
	if level == "random" {
		switch x := rng.Float64(); {
		case x < 0.15 && call > 0:
			return engine.Action{Type: "fold"}
		case x < 0.30:
			if a := raiseTo(rng.Float64()); a != nil {
				return *a
			}
		}
		return passive
	}
	// "basic": strength-driven with pot odds.
	var strength float64
	if len(s.Board) == 0 {
		strength = preflopStrength(p.Hole)
	} else {
		v, _ := Best(append(append([]Card{}, p.Hole...), s.Board...))
		strength = 0.25 + float64(v.Category())*0.15
		if v.Category() == HighCard {
			strength = 0.15
		}
	}
	strength += (rng.Float64() - 0.5) * 0.1
	potOdds := 0.0
	if call > 0 {
		potOdds = float64(call) / float64(s.pot()+call)
	}
	switch {
	case strength > 0.75:
		if a := raiseTo(0.6); a != nil {
			return *a
		}
		return passive
	case strength > 0.5:
		if call == 0 && rng.Float64() < 0.4 {
			if a := raiseTo(0.4); a != nil {
				return *a
			}
		}
		return passive
	case call == 0:
		return passive
	case strength > potOdds+0.15:
		return passive
	}
	return engine.Action{Type: "fold"}
}
