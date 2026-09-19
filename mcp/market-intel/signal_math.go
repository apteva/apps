package main

import (
	"errors"
	"math"
)

func extractSignalFeatures(bars []Candle) (SignalFeatures, error) {
	// Ignore a still-forming candle. Acting on partial-volume and partial-price
	// bars makes historical results impossible to reproduce.
	clean := make([]Candle, 0, len(bars))
	for _, b := range bars {
		if b.Close > 0 && b.High >= b.Low && b.Closed {
			clean = append(clean, b)
		}
	}
	if len(clean) < 60 {
		return SignalFeatures{}, errors.New("at least 60 closed candles required")
	}
	closes := make([]float64, len(clean))
	volumes := make([]float64, len(clean))
	for i, b := range clean {
		closes[i], volumes[i] = b.Close, b.Volume
	}
	last := len(clean) - 1
	ema20 := ema(closes, 20)
	ema50 := ema(closes, 50)
	atr := atr(clean, 14)
	volatility := stddev(logReturns(closes[len(closes)-25:])) * 10000
	returnBps := func(n int) float64 { return (closes[last]/closes[last-n] - 1) * 10000 }
	prior := clean[len(clean)-21 : len(clean)-1]
	high, low := prior[0].High, prior[0].Low
	for _, b := range prior[1:] {
		if b.High > high {
			high = b.High
		}
		if b.Low < low {
			low = b.Low
		}
	}
	trend := 0.0
	if atr > 0 {
		trend = (ema20 - ema50) / atr
	}
	return SignalFeatures{
		Price: closes[last], EMA20: ema20, EMA50: ema50, RSI14: rsi(closes, 14), ATR14: atr,
		Return1Bps: returnBps(1), Return6Bps: returnBps(6), Return24Bps: returnBps(24),
		VolatilityBps: volatility, VolumeZ20: zscore(volumes[len(volumes)-20:]),
		PriceZ20: zscore(closes[len(closes)-20:]), PriorHigh20: high, PriorLow20: low,
		TrendStrength: trend,
	}, nil
}

func generateCandidates(f SignalFeatures) []SignalCandidate {
	out := []SignalCandidate{}
	// Trend and momentum must agree; this avoids turning every moving-average
	// difference into a noisy alert.
	if math.Abs(f.TrendStrength) >= .45 && math.Abs(f.Return6Bps) >= 18 {
		dir := "BUY"
		aligned := f.TrendStrength > 0 && f.Return6Bps > 0
		if f.TrendStrength < 0 {
			dir = "SELL"
			aligned = f.Return6Bps < 0
		}
		if aligned {
			score := clamp(math.Abs(f.TrendStrength)/2.2+.25*math.Min(math.Abs(f.Return24Bps)/250, 1), 0, 1)
			out = append(out, SignalCandidate{Strategy: "trend_momentum", Direction: dir, Score: score,
				Confidence: clamp(.52+.34*score, 0, .9), ExpectedMoveBps: math.Max(35, math.Min(math.Abs(f.Return24Bps)*.45, 350)),
				Rationale: []string{"EMA20/EMA50 trend and 6-bar momentum agree", "signal uses only closed candles"}})
		}
	}
	// Mean reversion is only allowed when the long trend is not extreme.
	if math.Abs(f.PriceZ20) >= 2 && math.Abs(f.TrendStrength) < 1.25 {
		dir := "SELL"
		rsiOK := f.RSI14 >= 68
		if f.PriceZ20 < 0 {
			dir = "BUY"
			rsiOK = f.RSI14 <= 32
		}
		if rsiOK {
			score := clamp((math.Abs(f.PriceZ20)-1.5)/2+.2*math.Abs(f.RSI14-50)/50, 0, 1)
			out = append(out, SignalCandidate{Strategy: "mean_reversion", Direction: dir, Score: score,
				Confidence: clamp(.5+.35*score, 0, .88), ExpectedMoveBps: math.Max(30, math.Min(math.Abs(f.PriceZ20)*f.VolatilityBps*.55, 300)),
				Rationale: []string{"20-bar price z-score and RSI are both extreme", "extreme long-trend regimes are excluded"}})
		}
	}
	// A breakout must be accompanied by abnormal volume.
	if f.VolumeZ20 >= 1.25 && (f.Price > f.PriorHigh20 || f.Price < f.PriorLow20) {
		dir := "BUY"
		if f.Price < f.PriorLow20 {
			dir = "SELL"
		}
		score := clamp(.45+.18*math.Min(f.VolumeZ20, 3)+.12*math.Min(math.Abs(f.TrendStrength), 2), 0, 1)
		out = append(out, SignalCandidate{Strategy: "volume_breakout", Direction: dir, Score: score,
			Confidence: clamp(.5+.38*score, 0, .9), ExpectedMoveBps: math.Max(40, math.Min(f.VolatilityBps*1.5, 400)),
			Rationale: []string{"price broke the prior 20-bar range", "volume is materially above its 20-bar baseline"}})
	}
	return out
}

func ema(xs []float64, n int) float64 {
	a := 2 / float64(n+1)
	v := xs[0]
	for _, x := range xs[1:] {
		v = a*x + (1-a)*v
	}
	return v
}
func rsi(xs []float64, n int) float64 {
	var g, l float64
	for i := len(xs) - n; i < len(xs); i++ {
		d := xs[i] - xs[i-1]
		if d >= 0 {
			g += d
		} else {
			l -= d
		}
	}
	if l == 0 {
		return 100
	}
	rs := (g / float64(n)) / (l / float64(n))
	return 100 - 100/(1+rs)
}
func atr(bs []Candle, n int) float64 {
	var sum float64
	for i := len(bs) - n; i < len(bs); i++ {
		pc := bs[i-1].Close
		tr := math.Max(bs[i].High-bs[i].Low, math.Max(math.Abs(bs[i].High-pc), math.Abs(bs[i].Low-pc)))
		sum += tr
	}
	return sum / float64(n)
}
func logReturns(xs []float64) []float64 {
	out := make([]float64, 0, len(xs)-1)
	for i := 1; i < len(xs); i++ {
		out = append(out, math.Log(xs[i]/xs[i-1]))
	}
	return out
}
func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
func stddev(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	m := mean(xs)
	var s float64
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)))
}
func zscore(xs []float64) float64 {
	d := stddev(xs)
	if d == 0 {
		return 0
	}
	return (xs[len(xs)-1] - mean(xs)) / d
}
func clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }
