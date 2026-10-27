package main

import (
	"errors"
	"math"
	"time"
)

// Experimental opt-in controls. Zero values retain the established fixed path.
// The reserve is a target, not an instruction to pause a running stream.
type agentPlayoutConfig struct {
	Mode             string  `json:"mode"`
	MinReserveMS     int     `json:"min_reserve_ms"`
	MaxReserveMS     int     `json:"max_reserve_ms"`
	RecoveryIdleMS   int     `json:"recovery_idle_ms"`
	StableMS         int     `json:"stable_ms"`
	SpeedPercent     int     `json:"speed_percent"`
	MatchCorrelation float64 `json:"match_correlation"`
	TraceFrames      int     `json:"trace_frames"`
}

func resolveAgentPlayoutConfig(c agentPlayoutConfig, initial, backlog int) (agentPlayoutConfig, error) {
	if c.Mode == "" {
		c.Mode = "fixed"
	}
	if c.Mode != "fixed" && c.Mode != "adaptive" {
		return c, errors.New("playout.mode must be fixed or adaptive")
	}
	if c.MinReserveMS == 0 {
		c.MinReserveMS = initial
	}
	if c.MaxReserveMS == 0 {
		c.MaxReserveMS = min(200, backlog)
	}
	if c.RecoveryIdleMS == 0 {
		c.RecoveryIdleMS = 1000
	}
	if c.StableMS == 0 {
		c.StableMS = 5000
	}
	if c.MatchCorrelation == 0 {
		c.MatchCorrelation = .97
	}
	if c.MinReserveMS < 20 || c.MinReserveMS%20 != 0 || c.MaxReserveMS%20 != 0 || c.MaxReserveMS < c.MinReserveMS || c.MaxReserveMS > min(400, backlog) || initial < c.MinReserveMS || initial > c.MaxReserveMS {
		return c, errors.New("playout reserves must be 20 ms steps, include send_ahead_ms, and fit within min(400, backlog_ms)")
	}
	if c.RecoveryIdleMS < 500 || c.RecoveryIdleMS > 5000 {
		return c, errors.New("playout.recovery_idle_ms must be 500..5000")
	}
	if c.StableMS < 1000 || c.StableMS > 60000 {
		return c, errors.New("playout.stable_ms must be 1000..60000")
	}
	if c.SpeedPercent < 0 || c.SpeedPercent > 3 {
		return c, errors.New("playout.speed_percent must be 0..3")
	}
	if math.IsNaN(c.MatchCorrelation) || math.IsInf(c.MatchCorrelation, 0) || c.MatchCorrelation < .95 || c.MatchCorrelation > 1 {
		return c, errors.New("playout.match_correlation must be .95..1")
	}
	if c.TraceFrames < 0 || c.TraceFrames > 2000 {
		return c, errors.New("playout.trace_frames must be 0..2000 (0 disables capture)")
	}
	return c, nil
}

type agentPlayoutStats struct {
	TargetMS        int     `json:"target_reserve_ms"`
	MaxTargetMS     int     `json:"max_target_reserve_ms"`
	AdjustedFrames  int     `json:"adjusted_frames"`
	UnmatchedFrames int     `json:"unmatched_frames"`
	ExpandedMS      float64 `json:"expanded_ms"`
	CompressedMS    float64 `json:"compressed_ms"`
	MaxDeficitMS    float64 `json:"max_delivery_deficit_ms"`
}

// Owned by one pacer; copied before admission so a rejected/retried batch cannot
// advance estimator or correction debt. Contains no shared slices or pointers.
type agentPlayoutTuning struct {
	cfg                                             agentPlayoutConfig
	stats                                           agentPlayoutStats
	burstStart, lastArrival, lastDeficit, lastDecay time.Time
	burstAudio                                      time.Duration
	debt                                            float64
	item                                            string
	generation                                      int64
}

func newAgentPlayoutTuning(s *agentAudioSession, initial int) agentPlayoutTuning {
	c := agentPlayoutConfig{}
	if s.continuous() {
		c = s.profile.Playout
	}
	c, _ = resolveAgentPlayoutConfig(c, initial, max(initial, 2000))
	return agentPlayoutTuning{cfg: c, stats: agentPlayoutStats{TargetMS: initial, MaxTargetMS: initial}}
}
func (t *agentPlayoutTuning) resetStream() {
	t.burstStart = time.Time{}
	t.lastArrival = time.Time{}
	t.burstAudio = 0
	t.debt = 0
	t.item = ""
	t.generation = 0
}
func (t *agentPlayoutTuning) observe(now time.Time, duration time.Duration) {
	if duration <= 0 {
		return
	}
	if t.cfg.Mode != "adaptive" {
		return
	}
	// Core splits bursts into 20 ms writes. Treat neighbouring writes as one
	// delivery burst instead of interpreting each as a fresh network shortage.
	if t.lastArrival.IsZero() || now.Sub(t.lastArrival) >= time.Duration(t.cfg.RecoveryIdleMS)*time.Millisecond {
		t.burstStart = now
		t.burstAudio = duration
		t.lastArrival = now
		t.lastDecay = now
		return
	}
	if now.Sub(t.lastArrival) > 5*time.Millisecond {
		deficit := now.Sub(t.burstStart) - t.burstAudio
		if deficit > 5*time.Millisecond {
			t.stats.MaxDeficitMS = max(t.stats.MaxDeficitMS, float64(deficit)/float64(time.Millisecond))
			desired := t.cfg.MinReserveMS + int((deficit+19*time.Millisecond)/(20*time.Millisecond))*20
			t.stats.TargetMS = min(t.cfg.MaxReserveMS, max(t.stats.TargetMS, desired))
			t.lastDeficit = now
			t.lastDecay = now
		} else if now.Sub(t.lastDecay) >= time.Duration(t.cfg.StableMS)*time.Millisecond {
			t.stats.TargetMS = max(t.cfg.MinReserveMS, t.stats.TargetMS-20)
			t.lastDecay = now
		}
		t.burstStart = now
		t.burstAudio = 0
	}
	t.burstAudio += duration
	t.lastArrival = now
	t.stats.MaxTargetMS = max(t.stats.MaxTargetMS, t.stats.TargetMS)
}
func (t *agentPlayoutTuning) process(pcm []int16, rate, queuedMS int, f realtimeBridgeControl, now time.Time) []int16 {
	// Explicit silence is not evidence of speech starvation. Do not learn a
	// larger target from pauses in quiet provider output.
	if f.Silence {
		t.resetStream()
		return pcm
	}
	t.observe(now, time.Duration(len(pcm))*time.Second/time.Duration(rate))
	if t.cfg.SpeedPercent == 0 || len(pcm) == 0 {
		return pcm
	}
	if t.item != f.ItemID || t.generation != f.Generation {
		t.debt = 0
		t.item = f.ItemID
		t.generation = f.Generation
	}
	direction := 0
	if queuedMS < t.stats.TargetMS-20 {
		direction = 1
	} else if queuedMS > t.stats.TargetMS+40 {
		direction = -1
	}
	if direction == 0 || f.Silence {
		t.debt = 0
		return pcm
	}
	if t.debt*float64(direction) < 0 {
		t.debt = 0
	}
	// Never accumulate unbounded "catch-up" work across unsuitable speech.
	t.debt += float64(direction*len(pcm)*t.cfg.SpeedPercent) / 100
	t.debt = max(-float64(rate)/100, min(float64(rate)/100, t.debt))
	out, delta := matchSpeechCycle(pcm, rate, int(t.debt), t.cfg.MatchCorrelation)
	if delta == 0 {
		t.stats.UnmatchedFrames++
		return pcm
	}
	t.debt -= float64(delta)
	t.stats.AdjustedFrames++
	if delta > 0 {
		t.stats.ExpandedMS += float64(delta) * 1000 / float64(rate)
	} else {
		t.stats.CompressedMS += float64(-delta) * 1000 / float64(rate)
	}
	return out
}

// Conservative pitch-synchronous overlap/add, not resampling: insert/remove a
// matched cycle without changing sample rate or RTP cadence. No modification of
// silence, unvoiced/noisy frames or short/transient chunks without a strong match.
// Endpoints are retained. This is experimental and deliberately allowed to do
// nothing; it must never synthesize missing words to conceal a long outage.
func matchSpeechCycle(x []int16, rate, debt int, threshold float64) ([]int16, int) {
	if debt == 0 {
		return x, 0
	}
	sign := 1
	if debt < 0 {
		sign = -1
		debt = -debt
	}
	lo, hi := max(1, rate/400), min(rate/80, debt)
	w := max(8, rate/400)
	if hi < lo || len(x) < lo+3*w {
		return x, 0
	}
	maxStep := 0.0
	for i := 1; i < len(x); i++ {
		maxStep = max(maxStep, math.Abs(float64(x[i])-float64(x[i-1])))
	}
	best, period, at := threshold, 0, 0
	// A zero-mean comparison rejects DC/hum-like constant frames and requires
	// matching energy as well as correlation; crossfade spans at least 2.5 ms.
	for d := lo; d <= hi; d++ {
		for k := d + w; k+w < len(x)-w; k += max(1, w/2) {
			a, b := k, k-d
			if sign < 0 {
				a, b = k-d, k
			}
			var aa, bb, ab, ma, mb float64
			for j := 0; j < w; j++ {
				ma += float64(x[a+j])
				mb += float64(x[b+j])
			}
			ma /= float64(w)
			mb /= float64(w)
			for j := 0; j < w; j++ {
				u, v := float64(x[a+j])-ma, float64(x[b+j])-mb
				aa += u * u
				bb += v * v
				ab += u * v
			}
			if aa < float64(w)*100*100 || bb < aa*.8 || bb > aa*1.25 || math.Abs(ma-mb) > 300 {
				continue
			}
			corr := ab / math.Sqrt(aa*bb)
			// Also compare a longer region to avoid matching an accidental monotone
			// fragment at a transient. Available 20 ms input remains sufficient.
			n := min(len(x)-max(a, b), max(w, d))
			var err, energy float64
			for j := 0; j < n; j++ {
				u, v := float64(x[a+j]), float64(x[b+j])
				err += (u - v) * (u - v)
				energy += u*u + v*v
			}
			if energy == 0 || err/energy > .01 {
				continue
			}
			// Reject a candidate that would introduce a sharper local edge than
			// already exists in this chunk, including the end of the overlap.
			previous := float64(x[a-1])
			safe := true
			for j := 0; j <= w; j++ {
				v := float64(x[b+w])
				if j < w {
					v = (float64(w-j)*float64(x[a+j]) + float64(j)*float64(x[b+j])) / float64(w)
				}
				if math.Abs(v-previous) > maxStep+2 {
					safe = false
					break
				}
				previous = v
			}
			if !safe {
				continue
			}
			if corr > best {
				best = corr
				period = d
				at = a
			}
		}
	}
	if period == 0 {
		return x, 0
	}
	delta := sign * period
	other := at - delta
	out := make([]int16, 0, len(x)+delta)
	out = append(out, x[:at]...)
	for j := 0; j < w; j++ {
		v := (float64(w-j)*float64(x[at+j]) + float64(j)*float64(x[other+j])) / float64(w)
		out = append(out, int16(math.Round(v)))
	}
	out = append(out, x[other+w:]...)
	return out, delta
}
func (s *agentAudioSession) publishPlayout(t agentPlayoutTuning) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playout = t.stats
}
