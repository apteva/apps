package main

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gobwas/ws"
)

// This is a bounded read-ahead allowance, not a second playback reserve. It lets
// the reader observe cancellation while the renderer admits a pressured batch.
// The queue never changes media cadence; the carrier pacer remains authoritative.
const agentOutputReadAheadBytes = 24000 // 500 ms plus one batch bounded by the negotiated backlog

// The renderer's reader may hold one delivered batch for the whole pressure grace
// (plus one retry tick) and then report the overflow to Core under its own write
// budget before it reads again. The local delivery deadline covers that path with
// margin, so ordinary backpressure no longer tears down the AI leg; it still
// bounds a renderer that has genuinely stopped reading.
const agentOutputDeliveryMargin = 250 * time.Millisecond
const agentOutputDeliveryTimeout = agentPlaybackPressureGrace + agentPlaybackPressureRetry + agentCoreWriteBudget + agentOutputDeliveryMargin

type agentRelayOutput struct {
	metadata []byte
	control  realtimeBridgeControl
	message  agentLinkMessage
	epoch    uint64
}

func (l *agentCoreLink) outputDiscarded(n int) {
	l.mu.Lock()
	l.stats.OutputOmittedMS += int64(n) * 1000 / int64(l.mediaRate())
	l.mu.Unlock()
}

// Staged audio fenced by a cancellation is also reported to the session, so the
// playback's cleared receipt includes it (or a receipt is sent when none is due).
func (l *agentCoreLink) outputCancelled(c realtimeBridgeControl, n int) {
	l.outputDiscarded(n)
	if l.discarded == nil || c.ItemID == "" {
		return
	}
	l.sendReceipt(l.discarded(c, n*1000/l.mediaRate()))
}
func (l *agentCoreLink) enqueueOutput(m agentRelayOutput) error {
	if m.message.op != ws.OpBinary {
		select {
		case l.outputControl <- m:
			return nil
		case <-l.ctx.Done():
			return l.ctx.Err()
		default:
			return errors.New("Core output control queue exhausted")
		}
	}
	n := len(m.message.data)
	if n == 0 {
		return nil
	}
	if l.rejects != nil && l.rejects(m.control) {
		l.outputCancelled(m.control, n)
		return nil
	}
	pending := l.outputBytes.Add(int64(n))
	if pending > l.maxOutputBytes || int64(n) > l.maxBatchBytes {
		l.outputBytes.Add(-int64(n))
		l.outputDiscarded(n)
		if m.control.ItemID == "" || l.interrupt == nil {
			return errors.New("unscoped Core output staging overflow")
		}
		l.interrupt(m.control)
		l.event("core_output_pressure_exhausted", 0)
		raw, _ := json.Marshal(map[string]any{"type": "playback.overflow", "item_id": m.control.ItemID, "session_generation": m.control.Generation, "reason": "renderer_read_ahead_exhausted"})
		if err := l.sendControl(agentLinkMessage{op: ws.OpText, data: raw, at: time.Now()}); err != nil {
			if l.ctx.Err() != nil {
				return l.ctx.Err()
			}
			return errors.New("Core overflow control unavailable")
		}
		return nil
	}
	l.mu.Lock()
	l.stats.MaxOutputStagingMS = max(l.stats.MaxOutputStagingMS, float64(pending)*1000/float64(l.mediaRate()))
	l.mu.Unlock()
	select {
	case l.output <- m:
		return nil
	case <-l.ctx.Done():
		l.outputBytes.Add(-int64(n))
		l.outputDiscarded(n)
		return l.ctx.Err()
	}
}

func (l *agentCoreLink) deliverOutput(writer *agentRelayWriter) {
	defer l.cancel()
	for {
		var m agentRelayOutput
		select {
		case <-l.ctx.Done():
			return
		case m = <-l.outputControl:
		default:
			select {
			case <-l.ctx.Done():
				return
			case m = <-l.outputControl:
			case m = <-l.output:
			}
		}
		n := 0
		if m.message.op == ws.OpBinary {
			n = len(m.message.data)
			if l.rejects != nil && l.rejects(m.control) {
				l.outputBytes.Add(-int64(n))
				l.outputCancelled(m.control, n)
				l.notifyOutput()
				continue
			}
		}
		if len(m.metadata) > 0 {
			if err := writer.Write(ws.OpText, m.metadata); err != nil {
				l.event("local_renderer_write_error", 0)
				return
			}
		}
		if err := writer.Write(m.message.op, m.message.data); err != nil {
			l.event("local_renderer_write_error", 0)
			return
		}
		if n > 0 {
			l.outputBytes.Add(-int64(n))
			l.outputFrames.Add(1)
			l.notifyOutput()
			if l.connected.Load() && m.epoch == l.epoch.Load() && l.recovering.CompareAndSwap(true, false) {
				l.event("core_media_resumed", 0)
			}
		}
	}
}

func (l *agentCoreLink) notifyOutput() {
	select {
	case l.outputProgress <- struct{}{}:
	default:
	}
}
func (l *agentCoreLink) drainOutput() {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	for l.outputBytes.Load() > 0 {
		select {
		case <-l.ctx.Done():
			return
		case <-timer.C:
			return
		case <-l.outputProgress:
		}
	}
}
