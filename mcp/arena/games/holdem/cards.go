package holdem

import (
	"fmt"
	"sort"
	"strings"
)

// Card is encoded as rank*4 + suit, rank 2..14 (A=14), suit 0..3 (c,d,h,s).
type Card int

const ranks = "23456789TJQKA"
const suits = "cdhs"

func (c Card) Rank() int { return int(c) / 4 }
func (c Card) Suit() int { return int(c) % 4 }

func (c Card) String() string {
	return string(ranks[c.Rank()-2]) + string(suits[c.Suit()])
}

// ParseCard parses "As", "td", "9h".
func ParseCard(s string) (Card, error) {
	if len(s) != 2 {
		return 0, fmt.Errorf("bad card %q", s)
	}
	r := strings.IndexByte(ranks, strings.ToUpper(s[:1])[0])
	su := strings.IndexByte(suits, strings.ToLower(s[1:])[0])
	if r < 0 || su < 0 {
		return 0, fmt.Errorf("bad card %q", s)
	}
	return Card((r+2)*4 + su), nil
}

func newDeck() []Card {
	d := make([]Card, 0, 52)
	for r := 2; r <= 14; r++ {
		for s := 0; s < 4; s++ {
			d = append(d, Card(r*4+s))
		}
	}
	return d
}

func cardStrings(cs []Card) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.String()
	}
	return out
}

// Hand categories.
const (
	HighCard = iota
	OnePair
	TwoPair
	Trips
	Straight
	Flush
	FullHouse
	Quads
	StraightFlush
)

var categoryNames = []string{"high card", "pair", "two pair", "three of a kind", "straight", "flush", "full house", "four of a kind", "straight flush"}

// HandValue is comparable: higher is better.
type HandValue int

func (v HandValue) Category() int { return int(v) >> 20 }
func (v HandValue) Name() string  { return categoryNames[v.Category()] }

func pack(cat int, kick ...int) HandValue {
	v := cat
	for i := 0; i < 5; i++ {
		v <<= 4
		if i < len(kick) {
			v |= kick[i]
		}
	}
	return HandValue(v)
}

// eval5 scores exactly five cards.
func eval5(cs []Card) HandValue {
	counts := map[int]int{}
	flush := true
	for i, c := range cs {
		counts[c.Rank()]++
		if i > 0 && c.Suit() != cs[0].Suit() {
			flush = false
		}
	}
	type rc struct{ r, n int }
	groups := make([]rc, 0, 5)
	for r, n := range counts {
		groups = append(groups, rc{r, n})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].n != groups[j].n {
			return groups[i].n > groups[j].n
		}
		return groups[i].r > groups[j].r
	})
	ordered := make([]int, len(groups))
	for i, g := range groups {
		ordered[i] = g.r
	}
	straightHigh := 0
	if len(groups) == 5 {
		if ordered[0]-ordered[4] == 4 {
			straightHigh = ordered[0]
		} else if ordered[0] == 14 && ordered[1] == 5 { // wheel A-2-3-4-5
			straightHigh = 5
		}
	}
	switch {
	case straightHigh > 0 && flush:
		return pack(StraightFlush, straightHigh)
	case groups[0].n == 4:
		return pack(Quads, ordered...)
	case groups[0].n == 3 && groups[1].n == 2:
		return pack(FullHouse, ordered...)
	case flush:
		return pack(Flush, ordered...)
	case straightHigh > 0:
		return pack(Straight, straightHigh)
	case groups[0].n == 3:
		return pack(Trips, ordered...)
	case groups[0].n == 2 && groups[1].n == 2:
		return pack(TwoPair, ordered...)
	case groups[0].n == 2:
		return pack(OnePair, ordered...)
	}
	return pack(HighCard, ordered...)
}

// Best returns the best five-card value from 5..7 cards and the cards used.
func Best(cs []Card) (HandValue, []Card) {
	n := len(cs)
	var best HandValue = -1
	var bestCards []Card
	pick := make([]Card, 5)
	var rec func(start, depth int)
	rec = func(start, depth int) {
		if depth == 5 {
			if v := eval5(pick); v > best {
				best = v
				bestCards = append([]Card(nil), pick...)
			}
			return
		}
		for i := start; i <= n-(5-depth); i++ {
			pick[depth] = cs[i]
			rec(i+1, depth+1)
		}
	}
	rec(0, 0)
	return best, bestCards
}
