package main

import (
	"sync/atomic"
	"time"
)

type rtcReceiverFeedback struct {
	at      time.Time
	highest uint32
	loss    int
}
type rtcVoiceControl struct {
	feedback            atomic.Pointer[rtcReceiverFeedback]
	codec               atomic.Pointer[rtcEncoderState]
	configurationErrors atomic.Uint64
}
type rtcEncoderState struct {
	FECRequested bool   `json:"fec_requested"`
	Capability   string `json:"capability"`
	Bitrate      int    `json:"bitrate_bps"`
	ExpectedLoss int    `json:"expected_loss_percent"`
	FEC          bool   `json:"fec_enabled"`
	UpdatedAt    string `json:"updated_at"`
}

func (c *rtcVoiceControl) observe(highest uint32, fraction uint8, now time.Time) {
	old := c.feedback.Load()
	if old != nil && (int32(highest-old.highest) <= 0 || !now.After(old.at)) {
		return
	}
	c.feedback.Store(&rtcReceiverFeedback{at: now, highest: highest, loss: (int(fraction)*100 + 255) / 256})
}

// RTCP observations never perform encoder work. The sender alone applies
// bounded changes, no more than once per two seconds, with slow recovery.
type rtcVoicePolicy struct {
	bitrate, maximum, loss int
	last, stable, observed time.Time
}

func newRTCVoicePolicy(maximum int) rtcVoicePolicy {
	return rtcVoicePolicy{bitrate: maximum, maximum: maximum, loss: 10}
}
func (p *rtcVoicePolicy) next(now time.Time, feedback *rtcReceiverFeedback) (int, int, bool) {
	if feedback == nil || now.Sub(feedback.at) > 5*time.Second || !feedback.at.After(p.observed) || !p.last.IsZero() && now.Sub(p.last) < 2*time.Second {
		return p.bitrate, p.loss, false
	}
	p.observed = feedback.at
	p.last = now
	oldBitrate, oldLoss := p.bitrate, p.loss
	p.loss = max(10, min(30, feedback.loss))
	if feedback.loss >= 3 {
		p.stable = time.Time{}
		p.bitrate = max(16000, p.bitrate-4000)
	} else {
		if p.stable.IsZero() {
			p.stable = now
		}
		if now.Sub(p.stable) >= 10*time.Second {
			p.bitrate = min(p.maximum, p.bitrate+2000)
			p.stable = now
		}
	}
	return p.bitrate, p.loss, p.bitrate != oldBitrate || p.loss != oldLoss
}
