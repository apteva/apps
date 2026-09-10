package main

import "time"

// Counts decoded carrier packets, including silence, independently of AI VAD.
// AudioMS is received sample duration; MaxGapMS is local inter-arrival time,
// not network latency. Outbound queue diagnostics remain supplied by the pacer.
type audioTransportSnapshot struct {
	Frames   int64 `json:"frames"`
	Samples  int64 `json:"samples"`
	AudioMS  int64 `json:"audio_ms"`
	MaxGapMS int64 `json:"max_gap_ms"`
}

func (f *carrierAudioFrontend) observeInput(samples int, at time.Time) {
	if samples == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transport.Frames++
	f.transport.Samples += int64(samples)
	f.transport.AudioMS = f.transport.Samples * 1000 / int64(f.sampleRate)
	if !f.lastInputAt.IsZero() {
		f.transport.MaxGapMS = max(f.transport.MaxGapMS, at.Sub(f.lastInputAt).Milliseconds())
	}
	f.lastInputAt = at
}

func (f *carrierAudioFrontend) transportSnapshot() audioTransportSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transport
}
