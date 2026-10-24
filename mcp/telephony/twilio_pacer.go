package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	twilioMediaSampleRate = 8000
	// Keep a small amount of audio in Twilio's documented playback buffer. A
	// zero-lead sender underruns whenever JSON encoding, a mark write, or network
	// scheduling takes part of the next 20 ms packet interval.
	twilioTargetBufferedAudioSamples = twilioMediaSampleRate / 5 // 200 ms
	// Realtime models may generate faster than playback. Fifteen seconds keeps
	// normal multi-sentence turns intact while placing a hard bound on memory
	// and remote conversational drift.
	twilioMaxQueuedAudioSamples = twilioMediaSampleRate * 15
)

var (
	errTwilioPacerClosed   = errors.New("twilio audio pacer closed")
	errTwilioPacerOverflow = errors.New("twilio audio pacer queue overflow")
)

type twilioPacedPacket struct {
	Encoded    []byte
	Generation int64
	Silence    bool
	EnqueuedAt time.Time
	PCM        []int16
	ItemID     string
	AudioEndMS int
}

type twilioPacerResult struct {
	receipt   agentClearReceipt
	queuedMS  int
	clearedMS int
	droppedMS int
	err       error
}

type twilioPacerCommand struct {
	cancel   *realtimeBridgeControl
	packets  []twilioPacedPacket
	clear    bool
	response chan twilioPacerResult
}

// twilioAudioPacer is the single writer for outbound carrier media. It keeps
// generated audio in a local queue so interruption can discard it before the
// carrier sees it. A short carrier lead absorbs network jitter; later packets
// replenish that lead at absolute media deadlines.
type twilioAudioPacer struct {
	onSent                   func(bool)
	agentSession             *agentAudioSession
	pendingSamples           atomic.Int64
	pendingSpeechSamples     atomic.Int64
	diagnostics              livePacerStats
	ctx                      context.Context
	commands                 chan twilioPacerCommand
	clearCommands            chan twilioPacerCommand
	done                     chan struct{}
	streamSID                string
	playback                 *twilioPlaybackTracker
	write                    func([]byte) error
	onError                  func(error)
	maxQueuedSamples         int
	adaptiveMaxQueuedSamples int
	targetBufferedSamples    int
	trimToSamples            int
	dropStale                bool

	errMu  sync.Mutex
	err    error
	dropMu sync.Mutex
	drops  []audioDropEvent
}

func newTwilioAudioPacer(
	ctx context.Context,
	streamSID string,
	playback *twilioPlaybackTracker,
	write func([]byte) error,
	onError func(error),
) *twilioAudioPacer {
	return newTwilioAudioPacerWithPolicy(ctx, streamSID, playback, bufferedCarrierPacerPolicy(), write, onError)
}

func newTwilioAudioPacerWithPolicy(
	ctx context.Context,
	streamSID string,
	playback *twilioPlaybackTracker,
	policy carrierPacerPolicy,
	write func([]byte) error,
	onError func(error),
) *twilioAudioPacer {
	p := &twilioAudioPacer{
		onSent: policy.onSent, agentSession: policy.agentSession, ctx: ctx, commands: make(chan twilioPacerCommand), clearCommands: make(chan twilioPacerCommand), done: make(chan struct{}),
		streamSID: streamSID, playback: playback, write: write, onError: onError,
		maxQueuedSamples:         twilioMediaSampleRate * policy.maxQueueMS / 1000,
		adaptiveMaxQueuedSamples: twilioMediaSampleRate * policy.adaptiveMaxQueueMS / 1000,
		targetBufferedSamples:    twilioMediaSampleRate * policy.bufferMS / 1000,
		trimToSamples:            twilioMediaSampleRate * policy.trimToMS / 1000,
		dropStale:                policy.dropStale,
	}
	if p.maxQueuedSamples <= 0 {
		p.maxQueuedSamples = twilioMaxQueuedAudioSamples
	}
	if p.adaptiveMaxQueuedSamples < p.maxQueuedSamples {
		p.adaptiveMaxQueuedSamples = p.maxQueuedSamples
	}
	if p.targetBufferedSamples <= 0 {
		p.targetBufferedSamples = twilioTargetBufferedAudioSamples
	}
	go p.run()
	return p
}

func (p *twilioAudioPacer) dropEvents() []audioDropEvent {
	p.dropMu.Lock()
	defer p.dropMu.Unlock()
	return append([]audioDropEvent(nil), p.drops...)
}

func (p *twilioAudioPacer) recordDrop(event audioDropEvent) {
	p.diagnostics.dropped(event.Reason, event.DurationMS)
	p.dropMu.Lock()
	defer p.dropMu.Unlock()
	p.drops = append(p.drops, event)
	if len(p.drops) > 100 {
		p.drops = p.drops[len(p.drops)-100:]
	}
}

func (p *twilioAudioPacer) enqueue(ctx context.Context, packets []twilioAudioPacket, frame realtimeBridgeControl) (int, error) {
	queuedMS, _, err := p.enqueueWithDiagnostics(ctx, packets, frame)
	return queuedMS, err
}

func (p *twilioAudioPacer) enqueueWithDiagnostics(ctx context.Context, packets []twilioAudioPacket, frame realtimeBridgeControl) (int, int, error) {
	if len(packets) == 0 {
		return 0, 0, nil
	}
	paced := make([]twilioPacedPacket, len(packets))
	for i, packet := range packets {
		at := packet.SourceAt
		if at.IsZero() {
			at = time.Now()
		}
		paced[i] = twilioPacedPacket{Generation: frame.Generation, Silence: frame.Silence, EnqueuedAt: at, PCM: packet.PCM, Encoded: packet.Encoded, ItemID: frame.ItemID, AudioEndMS: packet.AudioEndMS}
	}
	result := p.command(ctx, twilioPacerCommand{packets: paced})
	return result.queuedMS, result.droppedMS, result.err
}

func (p *twilioAudioPacer) clear(ctx context.Context) (int, error) {
	result := p.command(ctx, twilioPacerCommand{clear: true})
	return result.clearedMS, result.err
}

func (p *twilioAudioPacer) command(ctx context.Context, command twilioPacerCommand) twilioPacerResult {
	command.response = make(chan twilioPacerResult, 1)
	destination := p.commands
	if command.clear {
		destination = p.clearCommands
	}
	select {
	case destination <- command:
	case <-ctx.Done():
		return twilioPacerResult{err: ctx.Err()}
	case <-p.done:
		return twilioPacerResult{err: p.failure()}
	}
	select {
	case result := <-command.response:
		return result
	case <-ctx.Done():
		return twilioPacerResult{err: ctx.Err()}
	case <-p.done:
		return twilioPacerResult{err: p.failure()}
	}
}

func (p *twilioAudioPacer) failure() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	if p.err != nil {
		return p.err
	}
	return errTwilioPacerClosed
}

func (p *twilioAudioPacer) finish(err error) {
	if err != nil {
		p.errMu.Lock()
		p.err = err
		p.errMu.Unlock()
		if p.onError != nil {
			p.onError(err)
		}
	}
	close(p.done)
}

func (p *twilioAudioPacer) run() {
	var (
		queue                  []twilioPacedPacket
		queuedSamples          int
		timer                  *time.Timer
		timerC                 <-chan time.Time
		bufferedThrough        time.Time
		droppedSamples         int
		activeMaxQueuedSamples = p.maxQueuedSamples
		adaptiveUntil          time.Time
		lastSentTail           []int16
		needsCrossfade         bool
		fadeAfterClear         bool
	)
	bufferWindow := time.Duration(p.targetBufferedSamples) * time.Second / twilioMediaSampleRate
	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer, timerC = nil, nil
	}
	clear := func() (int, error) {
		clearedSamples := queuedSamples
		stopTimer()
		bufferedThrough = time.Time{}
		queue = nil
		queuedSamples = 0
		p.pendingSamples.Store(int64(queuedSamples))
		p.pendingSpeechSamples.Store(0)
		p.playback.clear()
		payload, _ := json.Marshal(map[string]string{"event": "clear", "streamSid": p.streamSID})
		return samplesToMS(clearedSamples), p.write(payload)
	}
	scheduleAt := func(deadline time.Time) {
		stopTimer()
		delay := time.Until(deadline)
		if delay < 0 {
			delay = 0
		}
		timer = time.NewTimer(delay)
		timerC = timer.C
	}
	sendNext := func() (time.Duration, error) {
		if len(queue) == 0 {
			return 0, nil
		}
		packet := queue[0]
		queue = queue[1:]
		queuedSamples -= len(packet.PCM)
		p.pendingSamples.Store(int64(queuedSamples))
		if !packet.Silence {
			defer p.pendingSpeechSamples.Add(-int64(len(packet.PCM)))
		}
		if p.agentSession.rejects(packet.ItemID, packet.Generation) {
			return 0, nil
		}

		if p.dropStale && !packet.EnqueuedAt.IsZero() && time.Since(packet.EnqueuedAt) >= liveAudioMaxAge {
			droppedSamples += len(packet.PCM)
			needsCrossfade = true
			p.recordDrop(audioDropEvent{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Direction: "operator_to_carrier", Reason: "live_audio_age_limit", DurationMS: carrierSamplesToMS(len(packet.PCM), twilioMediaSampleRate)})
			return 0, nil
		}
		pcm := packet.PCM
		encoded := packet.Encoded
		if fadeAfterClear {
			encoded = nil
			pcm = append([]int16(nil), pcm...)
			applyAgentStartFade(pcm, twilioMediaSampleRate)
			fadeAfterClear = false
		}
		if needsCrossfade && len(lastSentTail) > 0 {
			encoded = nil
			pcm = append([]int16(nil), pcm...)
			applyPCMOverlapCrossfade(lastSentTail, pcm, twilioMediaSampleRate/200)
			needsCrossfade = false
		}
		out := twilioOutbound{Event: "media", StreamSID: p.streamSID}
		if encoded == nil {
			encoded = pcm16ToUlaw(pcm)
		}
		out.Media.Payload = base64.StdEncoding.EncodeToString(encoded)
		payload, _ := json.Marshal(out)
		writeStarted := time.Now()
		if err := p.write(payload); err != nil {
			return 0, err
		}
		p.agentSession.observeSent(packet.ItemID, packet.Generation, packet.Silence, time.Time{})
		if p.onSent != nil {
			p.onSent(packet.Silence)
		}
		p.diagnostics.sent(carrierSamplesToMS(len(packet.PCM), twilioMediaSampleRate), writeStarted)
		if markName := p.playback.addPacket(packet.ItemID, packet.Generation, packet.AudioEndMS, packet.Silence); markName != "" {
			mark, _ := json.Marshal(map[string]any{
				"event": "mark", "streamSid": p.streamSID, "mark": map[string]string{"name": markName},
			})
			if err := p.write(mark); err != nil {
				return 0, err
			}
		}
		duration := time.Duration(len(packet.PCM)) * time.Second / twilioMediaSampleRate
		if duration <= 0 {
			duration = time.Millisecond
		}
		now := time.Now()
		if bufferedThrough.IsZero() || bufferedThrough.Before(now) {
			bufferedThrough = now
		}
		bufferedThrough = bufferedThrough.Add(duration)
		tailSamples := min(len(pcm), max(1, twilioMediaSampleRate/200))
		lastSentTail = append(lastSentTail[:0], pcm[len(pcm)-tailSamples:]...)
		return duration, nil
	}
	fillCarrierLead := func() error {
		stopTimer()
		target := time.Now().Add(bufferWindow)
		for len(queue) > 0 && (bufferedThrough.IsZero() || bufferedThrough.Before(target)) {
			_, err := sendNext()
			if err != nil {
				return err
			}
		}
		if len(queue) > 0 {
			nextDuration := time.Duration(len(queue[0].PCM)) * time.Second / twilioMediaSampleRate
			if nextDuration <= 0 {
				nextDuration = time.Millisecond
			}
			scheduleAt(bufferedThrough.Add(-bufferWindow).Add(nextDuration))
		}
		return nil
	}
	handleCommand := func(command twilioPacerCommand) error {
		if command.cancel != nil {
			c := command.cancel
			fadeAfterClear = p.agentSession.continuous()
			receipt := agentClearReceipt{Scope: "local"}
			kept := queue[:0]
			for _, packet := range queue {
				if matchesAgentPlayback(packet.ItemID, packet.Generation, *c) {
					receipt.LocalMS += samplesToMS(len(packet.PCM))
					queuedSamples -= len(packet.PCM)
					if !packet.Silence {
						p.pendingSpeechSamples.Add(-int64(len(packet.PCM)))
					}
				} else {
					kept = append(kept, packet)
				}
			}
			queue = kept
			p.pendingSamples.Store(int64(queuedSamples))
			remote, collateral := p.playback.affectedPlaybacks(c.ItemID, c.Generation)
			remote = remote || c.ItemID == ""
			var err error
			if remote {
				p.playback.clear()
				bufferedThrough = time.Time{}
				receipt.Scope = "stream"
				receipt.CollateralPlaybacks = collateral
				for _, ref := range collateral {
					receipt.Collateral = append(receipt.Collateral, ref.ItemID)
				}
				for _, ref := range collateral {
					p.agentSession.cancel(ref.ItemID, ref.Generation)
				}
				raw, _ := json.Marshal(map[string]string{"event": "clear", "streamSid": p.streamSID})
				err = p.write(raw)
				receipt.RemoteCommandSent = err == nil
			}
			if err == nil {
				err = fillCarrierLead()
			}
			command.response <- twilioPacerResult{clearedMS: receipt.LocalMS, receipt: receipt, err: err}
			return err
		}
		if command.clear {
			clearedMS, err := clear()
			command.response <- twilioPacerResult{clearedMS: clearedMS, err: err}
			return err
		}
		incomingSamples := 0
		for _, packet := range command.packets {
			incomingSamples += len(packet.PCM)
		}
		if !p.dropStale && queuedSamples+incomingSamples > p.maxQueuedSamples {
			if p.agentSession.continuous() {
				command.response <- twilioPacerResult{err: errTwilioPacerOverflow}
				return nil
			}
			clearedMS, err := clear()
			if err == nil {
				err = errTwilioPacerOverflow
			}
			command.response <- twilioPacerResult{clearedMS: clearedMS, err: err}
			return nil
		}
		if !adaptiveUntil.IsZero() && time.Now().After(adaptiveUntil) {
			activeMaxQueuedSamples = p.maxQueuedSamples
			adaptiveUntil = time.Time{}
		}
		for _, packet := range command.packets {
			if !packet.Silence {
				p.pendingSpeechSamples.Add(int64(len(packet.PCM)))
			}
		}
		queue = append(queue, command.packets...)
		queuedSamples += incomingSamples
		p.pendingSamples.Store(int64(queuedSamples))
		p.diagnostics.queued(samplesToMS(queuedSamples))
		leadFilled := false
		// Use only the existing carrier lead for a near-limit startup batch.
		// Large bursts retain the trim-first policy and bounded local queue.
		if p.dropStale && queuedSamples > activeMaxQueuedSamples && queuedSamples <= activeMaxQueuedSamples+p.targetBufferedSamples {
			if err := fillCarrierLead(); err != nil {
				command.response <- twilioPacerResult{err: err}
				return err
			}
			leadFilled = true
		}
		if p.dropStale && queuedSamples > activeMaxQueuedSamples {
			before := queuedSamples
			residence := 0.0
			if len(queue) > 0 && !queue[0].EnqueuedAt.IsZero() {
				residence = max(0.0, float64(time.Since(queue[0].EnqueuedAt))/float64(time.Millisecond))
			}
			for queuedSamples > p.trimToSamples && len(queue) > 0 {
				droppedSamples += len(queue[0].PCM)
				if !queue[0].Silence {
					p.pendingSpeechSamples.Add(-int64(len(queue[0].PCM)))
				}
				queuedSamples -= len(queue[0].PCM)
				p.pendingSamples.Store(int64(queuedSamples))
				queue = queue[1:]
			}
			if dropped := before - queuedSamples; dropped > 0 {
				needsCrossfade = true
				p.recordDrop(audioDropEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Direction: "operator_to_carrier",
					Reason: "stale_live_audio", Trigger: "queue_depth", QueueResidenceMS: residence, DurationMS: samplesToMS(dropped),
					QueueBeforeMS: samplesToMS(before), QueueAfterMS: samplesToMS(queuedSamples),
				})
				activeMaxQueuedSamples = p.adaptiveMaxQueuedSamples
				adaptiveUntil = time.Now().Add(5 * time.Second)
			}
		}
		queuedAtEnqueue := queuedSamples
		var err error
		if !leadFilled {
			err = fillCarrierLead()
		}
		command.response <- twilioPacerResult{queuedMS: samplesToMS(queuedAtEnqueue), droppedMS: samplesToMS(droppedSamples), err: err}
		if err != nil {
			return err
		}
		return nil
	}

	defer stopTimer()
	for {
		// Clear is the only command that outranks playback. Normal enqueue work
		// must never starve an already-due carrier packet while the model is
		// generating audio faster than real time.
		select {
		case command := <-p.clearCommands:
			if err := handleCommand(command); err != nil {
				p.finish(err)
				return
			}
			continue
		default:
		}
		if timerC != nil {
			select {
			case <-timerC:
				timer, timerC = nil, nil
				if err := fillCarrierLead(); err != nil {
					p.finish(err)
					return
				}
				continue
			default:
			}
		}

		select {
		case <-p.ctx.Done():
			p.finish(nil)
			return
		case command := <-p.clearCommands:
			if err := handleCommand(command); err != nil {
				p.finish(err)
				return
			}
		case command := <-p.commands:
			if err := handleCommand(command); err != nil {
				p.finish(err)
				return
			}
		case <-timerC:
			timer, timerC = nil, nil
			if err := fillCarrierLead(); err != nil {
				p.finish(err)
				return
			}
		}
	}
}

func samplesToMS(samples int) int {
	return (samples*1000 + twilioMediaSampleRate - 1) / twilioMediaSampleRate
}

func (p *twilioAudioPacer) cancelPlayback(ctx context.Context, c realtimeBridgeControl) (agentClearReceipt, error) {
	p.agentSession.cancel(c.ItemID, c.Generation)
	r := p.command(ctx, twilioPacerCommand{clear: true, cancel: &c})
	return r.receipt, r.err
}
