package main

import (
	"context"
	"errors"
	"time"
)

const agentPlaybackPressureGrace = 200 * time.Millisecond

type agentPlaybackPressureSnapshot struct {
	Events    int64   `json:"events"`
	Recovered int64   `json:"recovered"`
	Exhausted int64   `json:"exhausted"`
	MaxWaitMS float64 `json:"max_wait_ms"`
}

// Short pressure is not an interruption. Retain this single ordered batch while
// the carrier consumes its existing bounded queue. The overall grace is fixed;
// a producer cannot extend it by repeatedly submitting a failing batch. The Core
// link observes cancellation independently so queued interrupts bypass this wait.
func (s *agentAudioSession) withPlaybackPressure(ctx context.Context, frame realtimeBridgeControl, enqueue func() error) error {
	err := enqueue()
	overflow := func(e error) bool {
		return errors.Is(e, errCarrierPacerOverflow) || errors.Is(e, errTwilioPacerOverflow)
	}
	if !overflow(err) || !s.continuous() {
		return err
	}
	started := time.Now()
	s.mu.Lock()
	s.pressure.Events++
	s.mu.Unlock()
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	deadline := time.NewTimer(agentPlaybackPressureGrace)
	defer deadline.Stop()
	defer func() {
		s.mu.Lock()
		s.pressure.MaxWaitMS = max(s.pressure.MaxWaitMS, float64(time.Since(started))/float64(time.Millisecond))
		s.mu.Unlock()
	}()
	for {
		if s.rejects(frame.ItemID, frame.Generation) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			s.mu.Lock()
			s.pressure.Exhausted++
			s.mu.Unlock()
			return err
		case <-timer.C:
			if s.rejects(frame.ItemID, frame.Generation) {
				return nil
			}
			err = enqueue()
			if !overflow(err) {
				if err == nil {
					s.mu.Lock()
					s.pressure.Recovered++
					s.mu.Unlock()
				}
				return err
			}
			timer.Reset(20 * time.Millisecond)
		}
	}
}

// No fade is applied to normal packet boundaries or quiet/silent segments.
func applyAgentStartFade(pcm []int16, rate int) {
	n := min(len(pcm), max(1, rate*3/1000))
	for i := 0; i < n; i++ {
		pcm[i] = int16(int64(pcm[i]) * int64(i+1) / int64(n))
	}
}
