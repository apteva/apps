package main

import (
	"errors"
	"strings"
	"time"
)

type mockProvider struct {
	tick int64 // monotonically increasing; mutated each Universe() call
}

func newMockProvider() Provider { return &mockProvider{} }

// Universe — single source of truth for "what's quotable in v0.1".
// Mirrors the universe the desk UI used to ship with as mock data,
// so the visual story stays continuous as we wire up the real engine.
type seed struct {
	symbol     string
	assetClass string
	anchor     float64 // current price (or YES prob for polymarket)
	bias       int     // controls drift sign in walk
	noAnchor   float64 // polymarket NO; ignored otherwise
	volume     float64
}

var mockUniverse = []seed{

	{"AAPL", "equity", 224.31, 1, 0, 38_412_000},
	{"NVDA", "equity", 138.07, 2, 0, 211_900_000},
	{"MSFT", "equity", 421.18, -1, 0, 19_800_000},
	{"TSLA", "equity", 244.72, -2, 0, 88_710_000},
	{"GOOGL", "equity", 167.92, 1, 0, 22_100_000},
	{"META", "equity", 591.04, 1, 0, 14_200_000},

	{"SPY", "etf", 567.41, 1, 0, 41_300_000},

	{"BTC-USD", "crypto", 67_842.10, 2, 0, 28_400_000_000},
	{"ETH-USD", "crypto", 3_412.55, -1, 0, 12_700_000_000},
	{"SOL-USD", "crypto", 218.73, 3, 0, 3_900_000_000},
	{"AVAX-USD", "crypto", 32.18, 1, 0, 402_000_000},
	{"DOGE-USD", "crypto", 0.142, -2, 0, 1_120_000_000},

	{"POLY:fed-cut-march", "polymarket", 0.32, -1, 0.68, 4_280_000},
	{"POLY:recession-2026", "polymarket", 0.41, 1, 0.59, 8_910_000},
	{"POLY:btc-100k-2026", "polymarket", 0.78, 3, 0.22, 14_220_000},
	{"POLY:trump-approval-50", "polymarket", 0.34, -1, 0.66, 2_180_000},
	{"POLY:openai-ipo-2026", "polymarket", 0.18, -1, 0.82, 612_000},
	{"POLY:gpt5-2026", "polymarket", 0.62, 2, 0.38, 1_840_000},
}

func (m *mockProvider) Universe() []*Mark {
	receivedAt := time.Now().UTC()
	now := receivedAt.Format(time.RFC3339)
	out := make([]*Mark, 0, len(mockUniverse))
	for _, s := range mockUniverse {

		drift := walk(s.symbol, m.tick) * s.anchor * 0.0008
		bias := float64(s.bias) * s.anchor * 0.00005
		price := s.anchor + drift + bias
		mk := &Mark{
			Symbol:        s.symbol,
			AssetClass:    s.assetClass,
			Price:         price,
			MarkedAt:      now,
			TimestampKind: "generated",
			Source:        "mock",
		}
		mk.Instrument = defaultInstrument(s.symbol, s.assetClass, "mock", receivedAt)
		mk.Instrument.Exchange = "SIMULATED"
		if s.assetClass == "polymarket" {
			no := 1 - price
			if price < 0.01 {
				price = 0.01
				no = 0.99
			}
			if price > 0.99 {
				price = 0.99
				no = 0.01
			}
			mk.Price = round4(price)
			mk.NoPrice = ptr(round4(no))
		}
		pc := s.anchor
		mk.PrevClose = &pc
		v := s.volume
		mk.Volume24h = &v
		mk.VolumeUnit = mk.Instrument.VolumeUnit
		out = append(out, mk)
	}
	m.tick++
	return out
}

func (m *mockProvider) Quote(symbol string) (*Mark, error) {
	for _, mk := range m.Universe() {
		if mk.Symbol == symbol {
			return mk, nil
		}
	}
	return nil, errors.New("symbol not in mock universe: " + symbol)
}

// Bars synthesises a longer series anchored to the symbol's current
// mark. Uses a cumulative Brownian-style walk (each bar = previous
// close + small random delta) so the chart actually looks like a
// price series rather than white noise around a mean. Without this
// the chart panel renders a sawtooth — the LCG produces correlated
// outputs for sequential bar indexes, and rendering them as absolute
// offsets from anchor amplifies the artifact into visible spikes.
//
// Polymarket bars return YES probability history bounded to (0.01,
// 0.99) with a mild mean-reversion pull toward the anchor so the
// random walk doesn't drift off the playable range over 720 bars.
func (m *mockProvider) Bars(symbol, rng string) ([]Bar, error) {
	var anchor, noAnchor float64
	var class string
	for _, s := range mockUniverse {
		if s.symbol == symbol {
			anchor = s.anchor
			noAnchor = s.noAnchor
			class = s.assetClass
			break
		}
	}
	if anchor == 0 {
		return nil, errors.New("symbol not in mock universe: " + symbol)
	}
	n := bucketsForRange(rng)
	now := time.Now().UTC().Unix()
	step := stepForRange(rng)
	out := make([]Bar, 0, n)

	prev := anchor
	priceVol := 0.003
	polyVol := 0.015
	if class == "polymarket" {
		priceVol = polyVol
	}
	for i := 0; i < n; i++ {
		t := now - int64(n-i)*step
		w := walk(symbol+rng, int64(i))
		delta := w * anchor * priceVol
		if class == "polymarket" {

			delta = w * polyVol
		}

		revert := (anchor - prev) * 0.02
		next := prev + delta + revert
		if class == "polymarket" {
			if next < 0.01 {
				next = 0.01
			}
			if next > 0.99 {
				next = 0.99
			}
			out = append(out, Bar{T: t, Yes: round4(next)})
		} else {

			o := prev
			c := next
			hi := o
			lo := o
			if c > hi {
				hi = c
			}
			if c < lo {
				lo = c
			}
			hi *= 1.0008
			lo *= 0.9992
			out = append(out, Bar{T: t, O: o, H: hi, L: lo, C: c, V: 1_000_000})
		}
		prev = next
	}
	_ = noAnchor
	return out, nil
}

func bucketsForRange(rng string) int {
	switch strings.ToUpper(rng) {
	case "1D":
		return 78
	case "5D":
		return 130
	case "1M":
		return 220
	case "3M":
		return 320
	case "1Y":
		return 540
	case "ALL":
		return 720
	default:
		return 78
	}
}

func stepForRange(rng string) int64 {
	switch strings.ToUpper(rng) {
	case "1D":
		return 5 * 60
	case "5D":
		return 30 * 60
	case "1M":
		return 4 * 3600
	case "3M":
		return 8 * 3600
	case "1Y":
		return 24 * 3600
	case "ALL":
		return 24 * 3600
	default:
		return 5 * 60
	}
}

// walk — deterministic [-1, +1] given a string seed and a tick index.
// Uses SplitMix64 mixing (golden-ratio multiplier + two avalanche
// rounds) so sequential ticks decorrelate properly. The previous
// Numerical-Recipes-LCG implementation produced a visible sawtooth
// in the chart panel because adjacent inputs landed in nearby output
// regions — one LCG step isn't enough mixing for low-bit perturbations.
func walk(seed string, tick int64) float64 {
	// FNV-1a-style seed hash.
	var s uint64 = 14695981039346656037
	for i := 0; i < len(seed); i++ {
		s ^= uint64(seed[i])
		s *= 1099511628211
	}

	s += uint64(tick) * 0x9E3779B97F4A7C15

	s = (s ^ (s >> 30)) * 0xBF58476D1CE4E5B9
	s = (s ^ (s >> 27)) * 0x94D049BB133111EB
	s ^= s >> 31

	return (float64(s>>11)/float64(1<<53))*2 - 1
}
