package main

import "time"

// Receive starvation is independent of source-clock resynchronization. Only a
// real timeline reset (generation change, e.g. hold/SSRC) requires initial priming.
// Protected by rtpJitterBuffer.mu. Keep bounded latest-recovery evidence.
type sipReceiveRecovery struct {
	generation                  uint64
	state                       string
	started, returned, rendered time.Time
	count                       int
	resumeMS                    float64
	maxResumeMS                 float64
}

func (b *rtpJitterBuffer) syncReceiveRecovery() {
	r := &b.receiveRecovery
	if r.state == "" || r.generation != b.generation.Load() {
		*r = sipReceiveRecovery{generation: b.generation.Load(), state: "initial", count: r.count, maxResumeMS: r.maxResumeMS}
	}
}
func (b *rtpJitterBuffer) beginReceiveRecovery(now time.Time) {
	b.syncReceiveRecovery()
	r := &b.receiveRecovery
	if b.policy.Receiver != "adaptive" || r.state == "recovering" {
		return
	}
	r.state = "recovering"
	r.started = now
	r.returned = time.Time{}
	r.rendered = time.Time{}
	r.resumeMS = 0
	r.count++
	b.event(now, "receive_starved", b.expected, b.expectedTimestamp, 0, 0)
}

// Only received PCM owns admission capacity. Replacement audio is generated
// locally and must not evict real speech from the encoded+decoded reserve.
func (f *rtpAudioFramer) realPendingSamples(skip int) int {
	total := 0
	for _, s := range f.segments {
		n := min(skip, s.samples)
		skip -= n
		if !s.synthetic {
			total += s.samples - n
		}
	}
	return total
}

func (f *rtpAudioFramer) discardRecoverySynthetic(rate int) {
	b := f.jitter
	b.mu.Lock()
	defer b.mu.Unlock()
	b.syncReceiveRecovery()
	if b.policy.Receiver != "adaptive" || b.receiveRecovery.state != "recovering" || b.buffered == 0 {
		return
	}
	offset, kept, segments := 0, 0, 0
	for _, s := range f.segments {
		if !s.synthetic {
			copy(f.pendingPCM[kept:], f.pendingPCM[offset:offset+s.samples])
			kept += s.samples
			f.segments[segments] = s
			segments++
		}
		offset += s.samples
	}
	f.pendingPCM = f.pendingPCM[:kept]
	f.segments = f.segments[:segments]
	b.pendingPCMSamples.Store(int64(kept * 8000 / rate))
}

// Call before consuming segments. A synthetic frame cannot complete recovery
// or mark an RTP packet played. A frame can contain parts of several packets.
func (f *rtpAudioFramer) noteRealRender(now time.Time, samples int) {
	b := f.jitter
	b.mu.Lock()
	defer b.mu.Unlock()
	f.noteRealRenderLocked(now, samples)
}

func (f *rtpAudioFramer) noteRealRenderLocked(now time.Time, samples int) {
	b := f.jitter
	if b.generation.Load() != f.generation {
		return
	}
	for _, s := range f.segments {
		n := min(samples, s.samples)
		samples -= n
		if n > 0 && !s.synthetic {
			if b.trace != nil {
				b.trace.render(s.rtpTimestamp, now)
			}
			r := &b.receiveRecovery
			if r.state == "recovering" && !r.returned.IsZero() && !s.arrival.Before(r.returned) {
				r.rendered = now
				r.resumeMS = max(0, float64(now.Sub(r.returned))/float64(time.Millisecond))
				r.maxResumeMS = max(r.maxResumeMS, r.resumeMS)
				r.state = "playing"
				b.event(now, "receive_playback_resumed", 0, s.rtpTimestamp, 0, now.Sub(r.returned))
			}
		}
		if samples == 0 {
			break
		}
	}
}
