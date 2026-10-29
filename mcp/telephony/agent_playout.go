package main

import (
	"errors"
	"math"
	"time"
)

// Experimental opt-in controls. Zero values retain the established fixed path.
// The reserve is a target, not an instruction to pause a running stream.
type agentPlayoutConfig struct {
	Mode           string `json:"mode"`
	MinReserveMS   int    `json:"min_reserve_ms"`
	MaxReserveMS   int    `json:"max_reserve_ms"`
	RecoveryIdleMS int    `json:"recovery_idle_ms"`
	StableMS       int    `json:"stable_ms"`
	SpeedPercent   int    `json:"speed_percent"`
	// Drift compensation is expansion-only: when the source runs slower than
	// real time and the queue falls below the lead, speech is lengthened by up
	// to DriftPercent (silence by more) instead of running dry. Playback still
	// starts after the initial reserve; stretching then grows the buffer to
	// DriftLeadMS so later provider stalls do not empty it. "off" disables it;
	// zero values mean the defaults. Plain values keep profiles comparable.
	Drift            string  `json:"drift,omitempty"`
	DriftPercent     int     `json:"drift_percent,omitempty"`
	DriftLeadMS      int     `json:"drift_lead_ms,omitempty"`
	MatchCorrelation float64 `json:"match_correlation"`
	TraceFrames      int     `json:"trace_frames"`
}

// Live providers stream at about real time and drifted up to 3.5% slow on
// 2026-10-29 (testdata/agent-audio/openai-live-pacing-20261029.json).
const agentPlayoutDefaultDriftPercent = 6

// Same capture: single stalls reached 236 ms. Grown during a reply, not
// waited for at its start.
const agentPlayoutDefaultDriftLeadMS = 160

// Expansion repeats one pitch period found in up to 30 ms of recent input, so
// a 20 ms frame can be stretched without waiting for more audio.
const agentPlayoutHistoryMS = 30

func (c agentPlayoutConfig) driftPercent() int {
	if c.Drift == "off" {
		return 0
	}
	if c.DriftPercent > 0 {
		return c.DriftPercent
	}
	return agentPlayoutDefaultDriftPercent
}

func (c agentPlayoutConfig) driftLeadMS() int {
	if c.DriftLeadMS > 0 {
		return c.DriftLeadMS
	}
	return agentPlayoutDefaultDriftLeadMS
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
	if c.Drift != "" && c.Drift != "off" {
		return c, errors.New(`playout.drift must be "off" or omitted`)
	}
	if c.DriftPercent < 0 || c.DriftPercent > 6 {
		return c, errors.New("playout.drift_percent must be 0..6 (0 uses the default)")
	}
	if c.DriftLeadMS < 0 || c.DriftLeadMS > 400 || c.DriftLeadMS%20 != 0 {
		return c, errors.New("playout.drift_lead_ms must be 0..400 in 20 ms steps (0 uses the default)")
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
	cfg                      agentPlayoutConfig
	stats                    agentPlayoutStats
	anchor, lastArrival      time.Time
	windowStart              time.Time
	scheduled                time.Duration
	windowPeak, previousPeak time.Duration
	debt                     float64
	history                  [agentPlayoutHistoryMS * 48]int16 // recent input, newest last
	historyLen               int
	driftAllowed             bool      // set by the direct SIP pacer; carrier streams are unchanged
	leadArmed                bool      // this call nearly ran dry; build the drift lead from now on
	streamAt                 time.Time // first burst of the current stream: filled by the initial reserve
	burstAt                  time.Time // arrival time of the current burst of frames
	troughMS                 int       // queue depth just before that burst arrived
	item                     string
	generation               int64
	observedItem             string
	observedGeneration       int64
}

func newAgentPlayoutTuning(s *agentAudioSession, initial int) agentPlayoutTuning {
	c := agentPlayoutConfig{}
	if s.continuous() {
		c = s.profile.Playout
	}
	c, _ = resolveAgentPlayoutConfig(c, initial, max(initial, 2000))
	return agentPlayoutTuning{cfg: c, stats: agentPlayoutStats{TargetMS: initial, MaxTargetMS: initial}}
}

// Learned lateness survives a stream reset; only the delivery schedule restarts.
func (t *agentPlayoutTuning) resetStream() {
	t.anchor = time.Time{}
	t.lastArrival = time.Time{}
	t.scheduled = 0
	t.debt = 0
	t.historyLen = 0
	t.streamAt = time.Time{}
	t.item = ""
	t.generation = 0
}

// Lateness is measured against a running real-time delivery schedule: the
// audio delivered so far should have been due at anchor+scheduled. Early
// arrivals are credit (they are queued; the pacer's backlog bound and Core's
// playback credit already cap how far ahead delivery can run), so 20 ms writes at 0, 14 and 40 ms are on time. The target is the worst
// lateness within a rolling window of two stable_ms halves, so it decays after
// stable_ms of healthy delivery instead of 20 ms per stable_ms.
func (t *agentPlayoutTuning) observe(now time.Time, duration time.Duration) {
	if duration <= 0 {
		return
	}
	if t.cfg.Mode != "adaptive" {
		return
	}
	half := time.Duration(t.cfg.StableMS) * time.Millisecond / 2
	if t.windowStart.IsZero() {
		t.windowStart = now
	}
	// An idle pause is not evidence of healthy delivery; only rotate the
	// window while audio is actually arriving.
	if t.lastArrival.IsZero() || now.Sub(t.lastArrival) >= time.Duration(t.cfg.RecoveryIdleMS)*time.Millisecond {
		t.anchor = now
		t.scheduled = duration
		t.lastArrival = now
		return
	}
	if elapsed := now.Sub(t.windowStart); elapsed >= 2*half {
		t.previousPeak, t.windowPeak, t.windowStart = 0, 0, now
	} else if elapsed >= half {
		t.previousPeak, t.windowPeak, t.windowStart = t.windowPeak, 0, t.windowStart.Add(half)
	}
	lateness := now.Sub(t.anchor.Add(t.scheduled))
	if lateness > 0 {
		// The queue ran dry; playback restarts on the new schedule.
		t.windowPeak = max(t.windowPeak, lateness)
		t.stats.MaxDeficitMS = max(t.stats.MaxDeficitMS, float64(lateness)/float64(time.Millisecond))
		t.anchor = now.Add(-t.scheduled)
	}
	t.scheduled += duration
	t.lastArrival = now
	t.updateTarget()
}

// A new reply keeps half of the learned lateness: one bad turn should not pin
// every later turn at max_reserve_ms, and one good turn should not forget it.
func (t *agentPlayoutTuning) observeItem(item string, generation int64) {
	if t.cfg.Mode != "adaptive" || item == "" || item == t.observedItem && generation == t.observedGeneration {
		return
	}
	if t.observedItem != "" {
		t.windowPeak /= 2
		t.previousPeak /= 2
		t.updateTarget()
	}
	t.observedItem, t.observedGeneration = item, generation
}

func (t *agentPlayoutTuning) updateTarget() {
	peak := max(t.windowPeak, t.previousPeak)
	desired := int((peak + 19*time.Millisecond) / (20 * time.Millisecond) * 20)
	t.stats.TargetMS = min(t.cfg.MaxReserveMS, max(t.cfg.MinReserveMS, desired))
	t.stats.MaxTargetMS = max(t.stats.MaxTargetMS, t.stats.TargetMS)
}

func (t *agentPlayoutTuning) process(pcm []int16, rate, queuedMS int, f realtimeBridgeControl, now time.Time) []int16 {
	drift := 0
	if t.driftAllowed {
		drift = t.cfg.driftPercent()
	}
	// A provider delta arrives as several 20 ms frames at once. Judge the
	// whole burst by the queue just before it arrived (its trough): that is
	// what playback actually had left, not the depth the burst itself adds.
	if t.burstAt.IsZero() || now.Sub(t.burstAt) > 5*time.Millisecond {
		t.burstAt, t.troughMS = now, queuedMS
	}
	if t.streamAt.IsZero() {
		t.streamAt = t.burstAt
	}
	// A low queue while a stream's initial reserve fills is normal, not
	// starvation: playback has not started draining it yet.
	startup := t.burstAt.Sub(t.streamAt) < time.Duration(t.stats.TargetMS)*time.Millisecond
	// Healthy delivery is never modified. Only after this call's queue nearly
	// ran dry does compensation grow the buffer to the drift lead; a provider
	// that stalled once in a call tends to stall again.
	if drift > 0 && t.troughMS <= 20 && !startup {
		t.leadArmed = true
	}
	lead := t.stats.TargetMS
	if drift > 0 && t.leadArmed {
		lead = max(lead, t.cfg.driftLeadMS())
	}
	below := t.troughMS < lead-20 && !startup
	if drift > 0 && !t.leadArmed && t.cfg.SpeedPercent == 0 {
		// Unarmed, act only when nearly dry: normal 20 ms packet variation
		// around the reserve is not starvation.
		below = t.troughMS <= 20 && !startup
	}
	// Explicit silence is not evidence of speech starvation. Do not learn a
	// larger target from pauses in quiet provider output. Lengthening it is
	// inaudible, so it absorbs drift first.
	if f.Silence {
		t.resetStream()
		if drift > 0 && below && len(pcm) > 0 {
			return append(append([]int16(nil), pcm...), make([]int16, len(pcm)/4)...)
		}
		return pcm
	}
	t.observeItem(f.ItemID, f.Generation)
	t.observe(now, time.Duration(len(pcm))*time.Second/time.Duration(rate))
	defer t.remember(pcm, rate)
	if (t.cfg.SpeedPercent == 0 && drift == 0) || len(pcm) == 0 {
		return pcm
	}
	if t.item != f.ItemID || t.generation != f.Generation {
		t.debt = 0
		t.historyLen = 0
		t.item = f.ItemID
		t.generation = f.Generation
	}
	direction, percent := 0, 0
	if below {
		// Nearly dry: drift compensation rebuilds the reserve faster than the
		// source drifts. The configured speed_percent budget is never exceeded.
		boost := drift
		if t.troughMS <= 20 {
			boost *= 2
		}
		direction, percent = 1, max(t.cfg.SpeedPercent, boost)
	} else if t.troughMS > t.stats.TargetMS+40 && t.cfg.SpeedPercent > 0 {
		direction, percent = -1, t.cfg.SpeedPercent
	}
	if direction == 0 {
		// Keep bounded pending debt: the next trough continues it.
		return pcm
	}
	if t.debt*float64(direction) < 0 {
		t.debt = 0
	}
	// Debt accrues until it covers a pitch period; never unbounded catch-up.
	limit := float64(rate * 15 / 1000)
	t.debt += float64(direction*len(pcm)*percent) / 100
	t.debt = max(-limit, min(limit, t.debt))
	out, delta := t.stretchSpeech(pcm, rate, int(t.debt))
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

// agentPlayoutSpliceHook observes splices in tests; nil in production.
var agentPlayoutSpliceHook func(frame, history, at, other, period, sign int, correlation float64, voiced bool)

// remember keeps the most recent unmodified input for period search.
func (t *agentPlayoutTuning) remember(pcm []int16, rate int) {
	keep := min(len(t.history), rate*agentPlayoutHistoryMS/1000)
	joined := append(append(make([]int16, 0, t.historyLen+len(pcm)), t.history[:t.historyLen]...), pcm...)
	if len(joined) > keep {
		joined = joined[len(joined)-keep:]
	}
	t.historyLen = copy(t.history[:], joined)
}

// stretchSpeech lengthens (debt > 0) or shortens (debt < 0) one frame by a
// single pitch period, WSOLA style: it finds the lag at which the signal best
// repeats, using recent input as look-back, and overlap-adds across a full
// period with a raised-cosine fade. The frame's first and last samples stay
// where they were, so neighbouring frames join seamlessly. Quiet or unvoiced
// audio has no audible period and is spliced at the best available lag.
func (t *agentPlayoutTuning) stretchSpeech(x []int16, rate, debt int) ([]int16, int) {
	sign := 1
	if debt < 0 {
		sign, debt = -1, -debt
	}
	lo, hi := max(1, rate/400), min(rate*125/10000, debt)
	if debt < lo || hi < lo {
		return x, 0
	}
	h := 0
	if sign > 0 {
		h = t.historyLen
	}
	y := append(append(make([]int16, 0, h+len(x)), t.history[t.historyLen-h:t.historyLen]...), x...)
	var energy float64
	for _, v := range x {
		energy += float64(v) * float64(v)
	}
	voiced := energy/float64(len(x)) >= 300*300
	threshold := t.cfg.MatchCorrelation - .07 // 0.90 at the default 0.97
	maxStep := 0.0
	for i := 1; i < len(y); i++ {
		maxStep = math.Max(maxStep, math.Abs(float64(y[i])-float64(y[i-1])))
	}
	// A splice may not create a sharper edge than the audio already has.
	safe := func(at, other, d int) bool {
		previous := float64(y[0])
		if at > 0 {
			previous = float64(y[at-1])
		}
		for j := 0; j <= d; j++ {
			v := float64(y[other+d])
			if j < d {
				w := .5 - .5*math.Cos(math.Pi*float64(j)/float64(d))
				v = (1-w)*float64(y[at+j]) + w*float64(y[other+j])
			}
			if math.Abs(v-previous) > maxStep+2 {
				return false
			}
			previous = v
		}
		return true
	}
	best, bestD, bestAt := -2.0, 0, 0
	step := max(1, rate/2000)
	for d := lo; d <= hi; d++ {
		for at := h; ; at += step {
			other := at - d // expansion repeats y[other:at]
			if sign < 0 {
				other = at + d // compression skips y[at:other]
			}
			// Keep the frame's final sample original so the next frame joins exactly.
			if other < 0 || max(at, other)+d >= len(y) {
				if sign < 0 || at+d >= len(y) {
					break
				}
				continue
			}
			var aa, bb, ab float64
			for j := 0; j < d; j++ {
				u, v := float64(y[at+j]), float64(y[other+j])
				aa += u * u
				bb += v * v
				ab += u * v
			}
			// Repeat or skip only audio at a compatible level: a quiet frame must
			// not re-play the loud tail of the previous word from history.
			if quiet := float64(d) * 50 * 50; (aa > quiet || bb > quiet) && (bb > 4*aa || aa > 4*bb) {
				continue
			}
			corr := 1.0
			if aa > 0 && bb > 0 {
				corr = ab / math.Sqrt(aa*bb)
			}
			if corr > best && safe(at, other, d) {
				best, bestD, bestAt = corr, d, at
			}
		}
	}
	if bestD == 0 || voiced && best < threshold {
		return x, 0
	}
	d, at := bestD, bestAt
	other := at - d
	if sign < 0 {
		other = at + d
	}
	if agentPlayoutSpliceHook != nil {
		agentPlayoutSpliceHook(len(x), h, at, other, d, sign, best, voiced)
	}
	out := make([]int16, 0, len(x)+sign*d)
	out = append(out, y[h:at]...)
	for j := 0; j < d; j++ {
		w := .5 - .5*math.Cos(math.Pi*float64(j)/float64(d)) // 0 -> 1
		v := (1-w)*float64(y[at+j]) + w*float64(y[other+j])
		out = append(out, int16(math.Round(max(-32768, min(32767, v)))))
	}
	out = append(out, y[other+d:]...)
	return out, sign * d
}

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
