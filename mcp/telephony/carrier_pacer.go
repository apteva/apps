package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

var (
	errCarrierPacerClosed   = errors.New("carrier audio pacer closed")
	errCarrierPacerOverflow = errors.New("carrier audio pacer queue overflow")
)

type carrierPacedPacket struct {
	PCM        []int16
	ItemID     string
	AudioEndMS int
}

type carrierPacerResult struct {
	queuedMS  int
	clearedMS int
	err       error
}

type carrierPacerCommand struct {
	packets  []carrierPacedPacket
	clear    bool
	response chan carrierPacerResult
}

type estimatedPlaybackAck struct {
	name string
	due  time.Time
}

type carrierAudioSegment struct {
	pcm     []int16
	offset  int
	itemID  string
	startMS int
	endMS   int
}

type carrierAudioPacketizer struct {
	packetSamples int
	queuedSamples int
	segments      []carrierAudioSegment
}

func newCarrierAudioPacketizer(sampleRate int) *carrierAudioPacketizer {
	return &carrierAudioPacketizer{packetSamples: sampleRate / 50}
}

func (p *carrierAudioPacketizer) add(pcm []int16, pcm24Samples int, frame realtimeBridgeControl) []carrierPacedPacket {
	if len(pcm) == 0 || p.packetSamples <= 0 {
		return nil
	}
	durationMS := 0
	startMS := frame.AudioEndMS
	if frame.AudioEndMS > 0 && pcm24Samples > 0 {
		durationMS = (pcm24Samples*1000 + 12000) / 24000
		startMS = max(0, frame.AudioEndMS-durationMS)
	}
	p.segments = append(p.segments, carrierAudioSegment{
		pcm: append([]int16(nil), pcm...), itemID: frame.ItemID, startMS: startMS, endMS: frame.AudioEndMS,
	})
	p.queuedSamples += len(pcm)
	packets := make([]carrierPacedPacket, 0, p.queuedSamples/p.packetSamples)
	for p.queuedSamples >= p.packetSamples {
		packet := carrierPacedPacket{PCM: make([]int16, 0, p.packetSamples)}
		remaining := p.packetSamples
		for remaining > 0 && len(p.segments) > 0 {
			segment := &p.segments[0]
			available := len(segment.pcm) - segment.offset
			take := min(remaining, available)
			packet.PCM = append(packet.PCM, segment.pcm[segment.offset:segment.offset+take]...)
			segment.offset += take
			remaining -= take
			packet.ItemID = segment.itemID
			if segment.endMS > 0 {
				packet.AudioEndMS = segment.startMS
				duration := max(0, segment.endMS-segment.startMS)
				if duration > 0 {
					packet.AudioEndMS += (segment.offset*duration + len(segment.pcm)/2) / len(segment.pcm)
				}
				if segment.offset == len(segment.pcm) || packet.AudioEndMS > segment.endMS {
					packet.AudioEndMS = segment.endMS
				}
			}
			if segment.offset == len(segment.pcm) {
				p.segments = p.segments[1:]
			}
		}
		p.queuedSamples -= p.packetSamples
		packets = append(packets, packet)
	}
	return packets
}

func (p *carrierAudioPacketizer) clear() {
	p.segments = nil
	p.queuedSamples = 0
}

type jsonCarrierAudioPacer struct {
	ctx           context.Context
	commands      chan carrierPacerCommand
	clearCommands chan carrierPacerCommand
	done          chan struct{}
	sampleRate    int
	codec         string
	shape         string
	streamID      string
	marks         bool
	playback      *twilioPlaybackTracker
	write         func([]byte) error
	onProgress    func(twilioPlaybackProgress) error
	onError       func(error)

	errMu sync.Mutex
	err   error
}

func newJSONCarrierAudioPacer(ctx context.Context, sampleRate int, codec, shape, streamID string, marks bool,
	playback *twilioPlaybackTracker, write func([]byte) error, onProgress func(twilioPlaybackProgress) error, onError func(error),
) *jsonCarrierAudioPacer {
	p := &jsonCarrierAudioPacer{
		ctx: ctx, commands: make(chan carrierPacerCommand), clearCommands: make(chan carrierPacerCommand), done: make(chan struct{}),
		sampleRate: sampleRate, codec: codec, shape: shape, streamID: streamID, marks: marks,
		playback: playback, write: write, onProgress: onProgress, onError: onError,
	}
	go p.run()
	return p
}

func (p *jsonCarrierAudioPacer) enqueue(ctx context.Context, packets []carrierPacedPacket) (int, error) {
	if len(packets) == 0 {
		return 0, nil
	}
	result := p.command(ctx, carrierPacerCommand{packets: packets})
	return result.queuedMS, result.err
}

func (p *jsonCarrierAudioPacer) clear(ctx context.Context) (int, error) {
	result := p.command(ctx, carrierPacerCommand{clear: true})
	return result.clearedMS, result.err
}

func (p *jsonCarrierAudioPacer) command(ctx context.Context, command carrierPacerCommand) carrierPacerResult {
	command.response = make(chan carrierPacerResult, 1)
	destination := p.commands
	if command.clear {
		destination = p.clearCommands
	}
	select {
	case destination <- command:
	case <-ctx.Done():
		return carrierPacerResult{err: ctx.Err()}
	case <-p.done:
		return carrierPacerResult{err: p.failure()}
	}
	select {
	case result := <-command.response:
		return result
	case <-ctx.Done():
		return carrierPacerResult{err: ctx.Err()}
	case <-p.done:
		return carrierPacerResult{err: p.failure()}
	}
}

func (p *jsonCarrierAudioPacer) failure() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	if p.err != nil {
		return p.err
	}
	return errCarrierPacerClosed
}

func (p *jsonCarrierAudioPacer) finish(err error) {
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

func (p *jsonCarrierAudioPacer) run() {
	var (
		queue           []carrierPacedPacket
		queuedSamples   int
		bufferedThrough time.Time
		estimated       []estimatedPlaybackAck
		timer           *time.Timer
		timerC          <-chan time.Time
	)
	bufferSamples := p.sampleRate / 5
	maxQueuedSamples := p.sampleRate * 15
	bufferWindow := time.Duration(bufferSamples) * time.Second / time.Duration(p.sampleRate)
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
	schedule := func(deadline time.Time) {
		stopTimer()
		delay := time.Until(deadline)
		if delay < 0 {
			delay = 0
		}
		timer = time.NewTimer(delay)
		timerC = timer.C
	}
	nextSendDeadline := func() time.Time {
		if len(queue) == 0 || bufferedThrough.IsZero() {
			return time.Time{}
		}
		duration := time.Duration(len(queue[0].PCM)) * time.Second / time.Duration(p.sampleRate)
		return bufferedThrough.Add(-bufferWindow).Add(duration)
	}
	reschedule := func() {
		var deadline time.Time
		if next := nextSendDeadline(); !next.IsZero() {
			deadline = next
		}
		if len(estimated) > 0 && (deadline.IsZero() || estimated[0].due.Before(deadline)) {
			deadline = estimated[0].due
		}
		if deadline.IsZero() {
			stopTimer()
		} else {
			schedule(deadline)
		}
	}
	deliverProgress := func() error {
		now := time.Now()
		for len(estimated) > 0 && !estimated[0].due.After(now) {
			ack := estimated[0]
			estimated = estimated[1:]
			progress, ok := p.playback.acknowledge(ack.name)
			if ok && p.onProgress != nil {
				if err := p.onProgress(progress); err != nil {
					return err
				}
			}
		}
		return nil
	}
	sendNext := func() error {
		if len(queue) == 0 {
			return nil
		}
		packet := queue[0]
		queue = queue[1:]
		queuedSamples -= len(packet.PCM)
		payload := encodeCarrierPacket(packet.PCM, p.codec)
		frame, _ := json.Marshal(buildCarrierOutbound(p.shape, p.streamID, payload))
		if err := p.write(frame); err != nil {
			return err
		}
		duration := time.Duration(len(packet.PCM)) * time.Second / time.Duration(p.sampleRate)
		if duration <= 0 {
			duration = time.Millisecond
		}
		now := time.Now()
		if bufferedThrough.IsZero() || bufferedThrough.Before(now) {
			bufferedThrough = now
		}
		bufferedThrough = bufferedThrough.Add(duration)
		if markName := p.playback.add(packet.ItemID, packet.AudioEndMS); markName != "" {
			if p.marks {
				mark, _ := json.Marshal(map[string]any{"event": "mark", "mark": map[string]string{"name": markName}})
				if err := p.write(mark); err != nil {
					return err
				}
			} else {
				estimated = append(estimated, estimatedPlaybackAck{name: markName, due: bufferedThrough})
			}
		}
		return nil
	}
	fillLead := func() error {
		target := time.Now().Add(bufferWindow)
		for len(queue) > 0 && (bufferedThrough.IsZero() || bufferedThrough.Before(target)) {
			if err := sendNext(); err != nil {
				return err
			}
		}
		reschedule()
		return nil
	}
	clearPlayback := func() (int, error) {
		cleared := queuedSamples
		queue = nil
		queuedSamples = 0
		bufferedThrough = time.Time{}
		estimated = nil
		p.playback.clear()
		frame, _ := json.Marshal(buildCarrierClear(p.shape, p.streamID))
		if string(frame) == "null" {
			return carrierSamplesToMS(cleared, p.sampleRate), nil
		}
		return carrierSamplesToMS(cleared, p.sampleRate), p.write(frame)
	}
	handleCommand := func(command carrierPacerCommand) error {
		if command.clear {
			clearedMS, err := clearPlayback()
			command.response <- carrierPacerResult{clearedMS: clearedMS, err: err}
			return err
		}
		incoming := 0
		for _, packet := range command.packets {
			incoming += len(packet.PCM)
		}
		if queuedSamples+incoming > maxQueuedSamples {
			clearedMS, err := clearPlayback()
			if err == nil {
				err = errCarrierPacerOverflow
			}
			command.response <- carrierPacerResult{clearedMS: clearedMS, err: err}
			return nil
		}
		queue = append(queue, command.packets...)
		queuedSamples += incoming
		queuedAtEnqueue := queuedSamples
		err := fillLead()
		command.response <- carrierPacerResult{queuedMS: carrierSamplesToMS(queuedAtEnqueue, p.sampleRate), err: err}
		return err
	}

	defer stopTimer()
	for {
		select {
		case command := <-p.clearCommands:
			if err := handleCommand(command); err != nil {
				p.finish(err)
				return
			}
			continue
		default:
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
			if err := deliverProgress(); err != nil {
				p.finish(err)
				return
			}
			if err := fillLead(); err != nil {
				p.finish(err)
				return
			}
		}
	}
}

func encodeCarrierPacket(pcm []int16, codec string) string {
	var raw []byte
	if codec == carrierCodecPCMU8 {
		raw = pcm16ToUlaw(pcm)
	} else {
		raw = pcm16ToBytes(pcm)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func carrierSamplesToMS(samples, sampleRate int) int {
	if sampleRate <= 0 {
		return 0
	}
	return (samples*1000 + sampleRate - 1) / sampleRate
}
