package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
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
	PCM        []int16
	ItemID     string
	AudioEndMS int
}

type twilioPacerResult struct {
	queuedMS  int
	clearedMS int
	droppedMS int
	err       error
}

type twilioPacerCommand struct {
	packets  []twilioPacedPacket
	clear    bool
	response chan twilioPacerResult
}

// twilioAudioPacer is the single writer for outbound carrier media. It keeps
// generated audio in a local queue so interruption can discard it before the
// carrier sees it. A short carrier lead absorbs network jitter; later packets
// replenish that lead at absolute media deadlines.
type twilioAudioPacer struct {
	ctx                   context.Context
	commands              chan twilioPacerCommand
	clearCommands         chan twilioPacerCommand
	done                  chan struct{}
	streamSID             string
	playback              *twilioPlaybackTracker
	write                 func([]byte) error
	onError               func(error)
	maxQueuedSamples      int
	targetBufferedSamples int
	trimToSamples         int
	dropStale             bool

	errMu sync.Mutex
	err   error
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
		ctx: ctx, commands: make(chan twilioPacerCommand), clearCommands: make(chan twilioPacerCommand), done: make(chan struct{}),
		streamSID: streamSID, playback: playback, write: write, onError: onError,
		maxQueuedSamples:      twilioMediaSampleRate * policy.maxQueueMS / 1000,
		targetBufferedSamples: twilioMediaSampleRate * policy.bufferMS / 1000,
		trimToSamples:         twilioMediaSampleRate * policy.trimToMS / 1000,
		dropStale:             policy.dropStale,
	}
	if p.maxQueuedSamples <= 0 {
		p.maxQueuedSamples = twilioMaxQueuedAudioSamples
	}
	if p.targetBufferedSamples <= 0 {
		p.targetBufferedSamples = twilioTargetBufferedAudioSamples
	}
	go p.run()
	return p
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
		paced[i] = twilioPacedPacket{PCM: packet.PCM, ItemID: frame.ItemID, AudioEndMS: packet.AudioEndMS}
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
		queue           []twilioPacedPacket
		queuedSamples   int
		timer           *time.Timer
		timerC          <-chan time.Time
		bufferedThrough time.Time
		droppedSamples  int
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

		out := twilioOutbound{Event: "media", StreamSID: p.streamSID}
		out.Media.Payload = base64.StdEncoding.EncodeToString(pcm16ToUlaw(packet.PCM))
		payload, _ := json.Marshal(out)
		if err := p.write(payload); err != nil {
			return 0, err
		}
		if markName := p.playback.add(packet.ItemID, packet.AudioEndMS); markName != "" {
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
			clearedMS, err := clear()
			if err == nil {
				err = errTwilioPacerOverflow
			}
			command.response <- twilioPacerResult{clearedMS: clearedMS, err: err}
			return nil
		}
		queue = append(queue, command.packets...)
		queuedSamples += incomingSamples
		if p.dropStale && queuedSamples > p.maxQueuedSamples {
			for queuedSamples > p.trimToSamples && len(queue) > 0 {
				droppedSamples += len(queue[0].PCM)
				queuedSamples -= len(queue[0].PCM)
				queue = queue[1:]
			}
		}
		queuedAtEnqueue := queuedSamples
		err := fillCarrierLead()
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
