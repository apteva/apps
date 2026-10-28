package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var (
	errCarrierPacerClosed   = errors.New("carrier audio pacer closed")
	errCarrierPacerOverflow = errors.New("carrier audio pacer queue overflow")
)

type carrierPacedPacket struct {
	receivedAt time.Time
	Generation int64
	Silence    bool
	EnqueuedAt time.Time
	PCM        []int16
	ItemID     string
	AudioEndMS int
	tailFade   bool
	queuedAt   time.Time
}

// Estimated (mark-less) playback acks are due this long after the local lead
// runs out. bufferedThrough only covers local send time plus lead; carriers
// still add network transit (~20-100 ms) and a jitter buffer (~60-120 ms).
// Acking early drops the entry before it plays, so an interrupt in that window
// would skip the remote clear. 250 ms covers both with a small margin.
const carrierEstimatedPlaybackMargin = 250 * time.Millisecond

// Shapes without a remote clear command (Vonage binary) cannot recall audio
// already sent; their carrier lead is capped so a barge-in plays out at most
// this much stale audio.
const carrierNoClearMaxLead = 60 * time.Millisecond

// carrierUnderrunFadeGap is how far the carrier lead must have run dry before
// the next packet is treated as following silence. Carrier jitter buffers
// absorb shorter lateness and keep playing; fading in there would carve an
// audible dip into continuous speech.
const carrierUnderrunFadeGap = 20 * time.Millisecond

// carrierPacketizerTail shares a bridge's packetizer with its pacer so the
// pacer can commit a sub-packet remainder once input goes idle, as the SIP
// pacer does. The bridge holds mu across add+enqueue; the pacer only TryLocks,
// so an idle flush can never overtake full packets still on their way in.
type carrierPacketizerTail struct {
	mu          sync.Mutex
	packetizer  *carrierAudioPacketizer
	lastInputAt time.Time
}

// carrierTailRelease is when a short trailing agent packet may be committed.
// It waits one packet for a continuation and otherwise until the carrier lead
// drops to the guard, so the tail still plays contiguously at a natural end.
func carrierTailRelease(lastInputAt, bufferedThrough time.Time, bufferWindow time.Duration) time.Time {
	guard := max(20*time.Millisecond, bufferWindow/2)
	release := lastInputAt.Add(20 * time.Millisecond)
	if lead := bufferedThrough.Add(-guard); lead.After(release) {
		release = lead
	}
	return release
}

// applyCarrierTailFade ramps the last 3 ms of a tail that is followed by an
// underrun gap. Ordinary contiguous packet boundaries are never faded.
func applyCarrierTailFade(pcm []int16, rate int) {
	n := min(len(pcm), max(1, rate*3/1000))
	for i := 0; i < n; i++ {
		j := len(pcm) - 1 - i
		pcm[j] = int16(int64(pcm[j]) * int64(i) / int64(n))
	}
}

type carrierPacerResult struct {
	receipt   agentClearReceipt
	queuedMS  int
	clearedMS int
	droppedMS int
	err       error
}

type carrierPacerPolicy struct {
	onSent             func(bool)
	agentSession       *agentAudioSession
	bufferMS           int
	maxQueueMS         int
	adaptiveMaxQueueMS int
	trimToMS           int
	dropStale          bool
	tail               *carrierPacketizerTail
}

func bufferedCarrierPacerPolicy() carrierPacerPolicy {
	return carrierPacerPolicy{bufferMS: 200, maxQueueMS: 15000}
}

func liveHumanCarrierPacerPolicy() carrierPacerPolicy {
	// A human microphone already arrives at real-time cadence. A very small
	// cushion absorbs scheduler jitter; anything older than this is stale
	// conversation and must not turn into seconds of perceived latency.
	var config sdk.Config
	if globalCtx != nil {
		config = globalCtx.Config()
	}
	return humanCarrierPacerPolicy(config)
}

func humanCarrierPacerPolicy(config sdk.Config) carrierPacerPolicy {
	bufferMS := configInt(config, "human_audio_send_ahead_ms", "TELEPHONY_HUMAN_AUDIO_SEND_AHEAD_MS", 40)
	switch bufferMS {
	case 20, 40, 60, 80:
	default:
		bufferMS = 40
	}
	return carrierPacerPolicy{bufferMS: bufferMS, maxQueueMS: 180, adaptiveMaxQueueMS: 220, trimToMS: 110, dropStale: true}
}

type carrierPacerCommand struct {
	cancel   *realtimeBridgeControl
	packets  []carrierPacedPacket
	clear    bool
	response chan carrierPacerResult
}

type estimatedPlaybackAck struct {
	name string
	due  time.Time
}

type carrierAudioSegment struct {
	generation int64
	silence    bool
	sourceAt   time.Time
	pcm        []int16
	offset     int
	itemID     string
	startMS    int
	endMS      int
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
	return p.addAt(pcm, pcm24Samples, frame, time.Time{})
}
func (p *carrierAudioPacketizer) addAt(pcm []int16, pcm24Samples int, frame realtimeBridgeControl, at time.Time) []carrierPacedPacket {
	if p.packetSamples <= 0 || len(pcm) == 0 && p.queuedSamples == 0 {
		return nil
	}
	durationMS := 0
	startMS := frame.AudioEndMS
	if frame.AudioEndMS > 0 && pcm24Samples > 0 {
		durationMS = (pcm24Samples*1000 + 12000) / 24000
		startMS = max(0, frame.AudioEndMS-durationMS)
	}
	var boundary []carrierPacedPacket
	if len(pcm) > 0 {
		if len(p.segments) > 0 {
			last := p.segments[len(p.segments)-1]
			if last.itemID != frame.ItemID || last.generation != frame.Generation || last.silence != frame.Silence {
				boundary = p.flushPartial()
			}
		}
		p.segments = append(p.segments, carrierAudioSegment{
			generation: frame.Generation, silence: frame.Silence, sourceAt: at, pcm: append([]int16(nil), pcm...), itemID: frame.ItemID, startMS: startMS, endMS: frame.AudioEndMS,
		})
		p.queuedSamples += len(pcm)
	}
	packets := boundary
	for p.queuedSamples >= p.packetSamples {
		packet := carrierPacedPacket{PCM: make([]int16, 0, p.packetSamples)}
		remaining := p.packetSamples
		for remaining > 0 && len(p.segments) > 0 {
			segment := &p.segments[0]
			source := segment.sourceAt
			if !source.IsZero() {
				source = source.Add(time.Duration(segment.offset) * 20 * time.Millisecond / time.Duration(p.packetSamples))
				if packet.EnqueuedAt.IsZero() || source.Before(packet.EnqueuedAt) {
					packet.EnqueuedAt = source
				}
			}
			available := len(segment.pcm) - segment.offset
			take := min(remaining, available)
			packet.PCM = append(packet.PCM, segment.pcm[segment.offset:segment.offset+take]...)
			segment.offset += take
			remaining -= take
			packet.ItemID = segment.itemID
			packet.Generation = segment.generation
			packet.Silence = segment.silence
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

// flushPartial preserves real samples and their playback identity. Transports
// that require fixed packets pad only when actually transmitting the tail.
func (p *carrierAudioPacketizer) flushPartial() []carrierPacedPacket {
	if p.queuedSamples == 0 {
		return nil
	}
	packet := carrierPacedPacket{PCM: make([]int16, 0, p.queuedSamples)}
	for _, segment := range p.segments {
		packet.PCM = append(packet.PCM, segment.pcm[segment.offset:]...)
		packet.ItemID, packet.Generation, packet.Silence, packet.AudioEndMS = segment.itemID, segment.generation, segment.silence, segment.endMS
		source := segment.sourceAt
		if !source.IsZero() {
			source = source.Add(time.Duration(segment.offset) * 20 * time.Millisecond / time.Duration(p.packetSamples))
			if packet.EnqueuedAt.IsZero() || source.Before(packet.EnqueuedAt) {
				packet.EnqueuedAt = source
			}
		}
	}
	p.clear()
	return []carrierPacedPacket{packet}
}

func (p *carrierAudioPacketizer) clear() {
	p.segments = nil
	p.queuedSamples = 0
}

type jsonCarrierAudioPacer struct {
	pendingSamples       atomic.Int64
	pendingSpeechSamples atomic.Int64
	diagnostics          livePacerStats
	ctx                  context.Context
	commands             chan carrierPacerCommand
	clearCommands        chan carrierPacerCommand
	done                 chan struct{}
	sampleRate           int
	codec                string
	shape                string
	streamID             string
	marks                bool
	playback             *twilioPlaybackTracker
	write                func([]byte) error
	onProgress           func(twilioPlaybackProgress) error
	onError              func(error)
	policy               carrierPacerPolicy

	errMu  sync.Mutex
	err    error
	dropMu sync.Mutex
	drops  []audioDropEvent
}

func (p *jsonCarrierAudioPacer) dropEvents() []audioDropEvent {
	p.dropMu.Lock()
	defer p.dropMu.Unlock()
	return append([]audioDropEvent(nil), p.drops...)
}

func (p *jsonCarrierAudioPacer) recordDrop(event audioDropEvent) {
	p.diagnostics.dropped(event.Reason, event.DurationMS)
	p.dropMu.Lock()
	defer p.dropMu.Unlock()
	p.drops = append(p.drops, event)
	if len(p.drops) > 100 {
		p.drops = p.drops[len(p.drops)-100:]
	}
}

func newJSONCarrierAudioPacer(ctx context.Context, sampleRate int, codec, shape, streamID string, marks bool,
	playback *twilioPlaybackTracker, policy carrierPacerPolicy, write func([]byte) error, onProgress func(twilioPlaybackProgress) error, onError func(error),
) *jsonCarrierAudioPacer {
	if policy.bufferMS <= 0 {
		policy = bufferedCarrierPacerPolicy()
	}
	p := &jsonCarrierAudioPacer{
		ctx: ctx, commands: make(chan carrierPacerCommand), clearCommands: make(chan carrierPacerCommand), done: make(chan struct{}),
		sampleRate: sampleRate, codec: codec, shape: shape, streamID: streamID, marks: marks,
		playback: playback, write: write, onProgress: onProgress, onError: onError,
		policy: policy,
	}
	go p.run()
	return p
}

func (p *jsonCarrierAudioPacer) enqueue(ctx context.Context, packets []carrierPacedPacket) (int, int, error) {
	// With a shared packetizer an empty enqueue still marks input activity, so
	// the pacer can commit a remainder held there once input goes idle.
	if len(packets) == 0 && p.policy.tail == nil {
		return 0, 0, nil
	}
	now := time.Now()
	for i := range packets {
		if packets[i].EnqueuedAt.IsZero() {
			packets[i].EnqueuedAt = now
		}
	}
	result := p.command(ctx, carrierPacerCommand{packets: packets})
	return result.queuedMS, result.droppedMS, result.err
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
	tuning := newAgentPlayoutTuning(p.policy.agentSession, p.policy.bufferMS)
	var (
		queue           []carrierPacedPacket
		queuedSamples   int
		bufferedThrough time.Time
		estimated       []estimatedPlaybackAck
		timer           *time.Timer
		timerC          <-chan time.Time
	)
	bufferSamples := p.sampleRate * p.policy.bufferMS / 1000
	maxQueuedSamples := p.sampleRate * p.policy.maxQueueMS / 1000
	adaptiveMaxQueuedSamples := p.sampleRate * p.policy.adaptiveMaxQueueMS / 1000
	trimToSamples := p.sampleRate * p.policy.trimToMS / 1000
	if maxQueuedSamples <= 0 {
		maxQueuedSamples = p.sampleRate * 15
	}
	if adaptiveMaxQueuedSamples < maxQueuedSamples {
		adaptiveMaxQueuedSamples = maxQueuedSamples
	}
	bufferWindow := time.Duration(bufferSamples) * time.Second / time.Duration(p.sampleRate)
	clearFrame, _ := json.Marshal(buildCarrierClear(p.shape, p.streamID))
	noRemoteClear := string(clearFrame) == "null"
	capLead := func() {
		if noRemoteClear && bufferWindow > carrierNoClearMaxLead {
			bufferWindow = carrierNoClearMaxLead
		}
	}
	capLead()
	packetDuration := 20 * time.Millisecond
	var lastInputAt, tailDue, heldUntil time.Time
	droppedSamples := 0
	activeMaxQueuedSamples := maxQueuedSamples
	var adaptiveUntil time.Time
	var lastSentTail []int16
	needsCrossfade := false
	fadeAfterClear := false
	resumeFade := false // the previous speech packet faded out to silence
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
		if !heldUntil.IsZero() {
			deadline = heldUntil
		}
		if !tailDue.IsZero() && (deadline.IsZero() || tailDue.Before(deadline)) {
			deadline = tailDue
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
		p.pendingSamples.Store(int64(queuedSamples))
		if !packet.Silence {
			defer p.pendingSpeechSamples.Add(-int64(len(packet.PCM)))
		}
		if p.policy.agentSession.rejects(packet.ItemID, packet.Generation) {
			return nil
		}
		if p.policy.dropStale && !packet.EnqueuedAt.IsZero() && time.Since(packet.EnqueuedAt) >= liveAudioMaxAge {
			droppedSamples += len(packet.PCM)
			needsCrossfade = true
			p.recordDrop(audioDropEvent{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Direction: "operator_to_carrier", Reason: "live_audio_age_limit", DurationMS: carrierSamplesToMS(len(packet.PCM), p.sampleRate)})
			return nil
		}
		pcm := packet.PCM
		// Underrun: the carrier lead ran out before this packet even arrived.
		underrun := p.policy.agentSession != nil && !packet.Silence && !bufferedThrough.IsZero() && bufferedThrough.Add(carrierUnderrunFadeGap).Before(packet.queuedAt)
		if fadeAfterClear || underrun || resumeFade && !packet.Silence {
			pcm = append([]int16(nil), pcm...)
			applyAgentStartFade(pcm, p.sampleRate)
			fadeAfterClear, resumeFade = false, false
		}
		if packet.tailFade && !packet.Silence {
			pcm = append([]int16(nil), pcm...)
			applyCarrierTailFade(pcm, p.sampleRate)
			resumeFade = true
		}
		if needsCrossfade && len(lastSentTail) > 0 {
			pcm = append([]int16(nil), pcm...)
			applyPCMOverlapCrossfade(lastSentTail, pcm, p.sampleRate/200)
			needsCrossfade = false
		}
		payload := encodeCarrierPacket(pcm, p.codec)
		frame, _ := json.Marshal(buildCarrierOutbound(p.shape, p.streamID, payload))
		writeStarted := time.Now()
		if err := p.write(frame); err != nil {
			return err
		}
		p.policy.agentSession.observeSent(packet.ItemID, packet.Generation, packet.Silence, time.Time{})
		p.policy.agentSession.traceSent(packet.ItemID, packet.Generation, packet.Silence, packet.AudioEndMS, carrierSamplesToMS(len(packet.PCM), p.sampleRate), "carrier_write", time.Now(), time.Since(writeStarted))
		if p.policy.onSent != nil {
			p.policy.onSent(packet.Silence)
		}
		p.diagnostics.sent(carrierSamplesToMS(len(packet.PCM), p.sampleRate), writeStarted)
		duration := time.Duration(len(packet.PCM)) * time.Second / time.Duration(p.sampleRate)
		if duration <= 0 {
			duration = time.Millisecond
		}
		now := time.Now()
		if bufferedThrough.IsZero() || bufferedThrough.Before(now) {
			bufferedThrough = now
		}
		bufferedThrough = bufferedThrough.Add(duration)
		tailSamples := min(len(pcm), max(1, p.sampleRate/200))
		lastSentTail = append(lastSentTail[:0], pcm[len(pcm)-tailSamples:]...)
		if markName := p.playback.addPacket(packet.ItemID, packet.Generation, packet.AudioEndMS, packet.Silence); markName != "" {
			if p.marks {
				mark, _ := json.Marshal(map[string]any{"event": "mark", "mark": map[string]string{"name": markName}})
				if err := p.write(mark); err != nil {
					return err
				}
			} else {
				estimated = append(estimated, estimatedPlaybackAck{name: markName, due: bufferedThrough.Add(carrierEstimatedPlaybackMargin)})
			}
		}
		return nil
	}
	fillLead := func() error {
		target := time.Now().Add(bufferWindow)
		heldUntil = time.Time{}
		for len(queue) > 0 && (bufferedThrough.IsZero() || bufferedThrough.Before(target)) {
			if p.policy.agentSession != nil && len(queue) == 1 && !queue[0].Silence {
				// The last queued speech is held for its continuation while the
				// carrier still has lead. Released without one, it fades out:
				// an underrun gap follows, and a hard cut there would click.
				if release := carrierTailRelease(lastInputAt, bufferedThrough, bufferWindow); time.Now().Before(release) {
					heldUntil = release
					break
				}
				queue[0].tailFade = true
			}
			if err := sendNext(); err != nil {
				return err
			}
		}
		reschedule()
		return nil
	}
	flushIdleTail := func() {
		if tailDue.IsZero() || time.Now().Before(tailDue) {
			return
		}
		tailDue = time.Time{}
		t := p.policy.tail
		if !t.mu.TryLock() {
			// The bridge is mid add+enqueue; its command re-arms the check.
			tailDue = time.Now().Add(packetDuration)
			return
		}
		var packets []carrierPacedPacket
		if time.Since(t.lastInputAt) >= packetDuration {
			packets = t.packetizer.flushPartial()
		} else if t.packetizer.queuedSamples > 0 {
			tailDue = t.lastInputAt.Add(packetDuration)
		}
		t.mu.Unlock()
		now := time.Now()
		for _, packet := range packets {
			if packet.EnqueuedAt.IsZero() {
				packet.EnqueuedAt = now
			}
			if !packet.Silence {
				p.pendingSpeechSamples.Add(int64(len(packet.PCM)))
			}
			queuedSamples += len(packet.PCM)
			packet.queuedAt = now
			queue = append(queue, packet)
		}
		p.pendingSamples.Store(int64(queuedSamples))
	}
	clearPlayback := func() (int, error) {
		cleared := queuedSamples
		queue = nil
		queuedSamples = 0
		p.pendingSamples.Store(int64(queuedSamples))
		p.pendingSpeechSamples.Store(0)
		bufferedThrough = time.Time{}
		estimated = nil
		p.playback.clear()
		frame, _ := json.Marshal(buildCarrierClear(p.shape, p.streamID))
		if string(frame) == "null" {
			return carrierSamplesToMS(cleared, p.sampleRate), nil
		}
		return carrierSamplesToMS(cleared, p.sampleRate), p.write(frame)
	}
	dropOldest := func(targetSamples int) {
		before := queuedSamples
		residence := 0.0
		if len(queue) > 0 && !queue[0].EnqueuedAt.IsZero() {
			residence = max(0.0, float64(time.Since(queue[0].EnqueuedAt))/float64(time.Millisecond))
		}
		for queuedSamples > targetSamples && len(queue) > 0 {
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
				Reason: "stale_live_audio", Trigger: "queue_depth", QueueResidenceMS: residence, DurationMS: carrierSamplesToMS(dropped, p.sampleRate),
				QueueBeforeMS: carrierSamplesToMS(before, p.sampleRate), QueueAfterMS: carrierSamplesToMS(queuedSamples, p.sampleRate),
			})
		}
	}
	handleCommand := func(command carrierPacerCommand) error {
		if command.cancel != nil {
			tuning.resetStream()
			c := command.cancel
			fadeAfterClear = p.policy.agentSession.continuous()
			receipt := agentClearReceipt{Scope: "local"}
			kept := queue[:0]
			for _, packet := range queue {
				if matchesAgentPlayback(packet.ItemID, packet.Generation, *c) {
					receipt.LocalMS += carrierSamplesToMS(len(packet.PCM), p.sampleRate)
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
				raw, _ := json.Marshal(buildCarrierClear(p.shape, p.streamID))
				if string(raw) != "null" {
					p.playback.clear()
					estimated = nil
					bufferedThrough = time.Time{}
					receipt.Scope = "stream"
					receipt.CollateralPlaybacks = collateral
					for _, ref := range collateral {
						receipt.Collateral = append(receipt.Collateral, ref.ItemID)
					}
					for _, ref := range collateral {
						p.policy.agentSession.cancel(ref.ItemID, ref.Generation)
					}
					err = p.write(raw)
					receipt.RemoteCommandSent = err == nil
				}
			}
			if err == nil {
				err = fillLead()
			}
			command.response <- carrierPacerResult{clearedMS: receipt.LocalMS, receipt: receipt, err: err}
			return err
		}
		if command.clear {
			tuning.resetStream()
			clearedMS, err := clearPlayback()
			command.response <- carrierPacerResult{clearedMS: clearedMS, err: err}
			return err
		}
		incoming := 0
		for _, packet := range command.packets {
			incoming += len(packet.PCM)
		}
		if !p.policy.dropStale && queuedSamples+incoming > maxQueuedSamples {
			if p.policy.agentSession.continuous() {
				command.response <- carrierPacerResult{err: errCarrierPacerOverflow}
				return nil
			}
			clearedMS, err := clearPlayback()
			if err == nil {
				err = errCarrierPacerOverflow
			}
			command.response <- carrierPacerResult{clearedMS: clearedMS, err: err}
			return nil
		}
		if p.policy.agentSession.continuous() {
			candidate := tuning
			adjusted := append([]carrierPacedPacket(nil), command.packets...)
			total := 0
			now := time.Now()
			leadMS := max(0, int(bufferedThrough.Sub(now)/time.Millisecond))
			for i := range adjusted {
				f := &adjusted[i]
				if p.policy.agentSession.rejects(f.ItemID, f.Generation) {
					f.PCM = nil
					continue
				}
				arrival := f.receivedAt
				if arrival.IsZero() {
					arrival = now
				}
				f.PCM = candidate.process(f.PCM, p.sampleRate, carrierSamplesToMS(queuedSamples+total, p.sampleRate)+leadMS, realtimeBridgeControl{ItemID: f.ItemID, Generation: f.Generation, Silence: f.Silence}, arrival)
				total += len(f.PCM)
			}
			if queuedSamples+total > maxQueuedSamples {
				command.response <- carrierPacerResult{err: errCarrierPacerOverflow}
				return nil
			}
			tuning = candidate
			command.packets = adjusted[:0]
			for _, f := range adjusted {
				if len(f.PCM) > 0 {
					command.packets = append(command.packets, f)
				}
			}
			incoming = total
			bufferWindow = time.Duration(tuning.stats.TargetMS) * time.Millisecond
			capLead()
			p.policy.agentSession.publishPlayout(tuning)
		}
		lastInputAt = time.Now()
		if p.policy.tail != nil && !p.policy.agentSession.continuous() {
			tailDue = lastInputAt.Add(packetDuration)
		}

		if !adaptiveUntil.IsZero() && time.Now().After(adaptiveUntil) {
			activeMaxQueuedSamples = maxQueuedSamples
			adaptiveUntil = time.Time{}
		}
		queuedAt := time.Now()
		for i, packet := range command.packets {
			if !packet.Silence {
				p.pendingSpeechSamples.Add(int64(len(packet.PCM)))
			}
			command.packets[i].queuedAt = queuedAt
		}
		queue = append(queue, command.packets...)
		queuedSamples += incoming
		p.pendingSamples.Store(int64(queuedSamples))
		p.diagnostics.queued(carrierSamplesToMS(queuedSamples, p.sampleRate))
		leadFilled := false
		// A small startup/recovery batch may fit within the existing carrier
		// lead plus the local queue. Fill that lead before deciding to trim;
		// oversized bursts still trim first, without sending their oldest head.
		if p.policy.dropStale && queuedSamples > activeMaxQueuedSamples && queuedSamples <= activeMaxQueuedSamples+bufferSamples {
			if err := fillLead(); err != nil {
				command.response <- carrierPacerResult{err: err}
				return err
			}
			leadFilled = true
		}
		if p.policy.dropStale && queuedSamples > activeMaxQueuedSamples {
			dropOldest(trimToSamples)
			activeMaxQueuedSamples = adaptiveMaxQueuedSamples
			adaptiveUntil = time.Now().Add(5 * time.Second)
		}
		queuedAtEnqueue := queuedSamples
		var err error
		if !leadFilled {
			err = fillLead()
		} else {
			reschedule()
		}
		command.response <- carrierPacerResult{
			queuedMS:  carrierSamplesToMS(queuedAtEnqueue, p.sampleRate),
			droppedMS: carrierSamplesToMS(droppedSamples, p.sampleRate),
			err:       err,
		}
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
			flushIdleTail()
			if err := fillLead(); err != nil {
				p.finish(err)
				return
			}
		}
	}
}

func applyPCMOverlapCrossfade(previousTail, next []int16, samples int) {
	n := min(samples, min(len(previousTail), len(next)))
	if n <= 0 {
		return
	}
	previousTail = previousTail[len(previousTail)-n:]
	for i := 0; i < n; i++ {
		// Equal-power overlap avoids the discontinuity produced by joining two
		// unrelated waveform phases after stale packets are removed.
		t := float64(i+1) / float64(n+1)
		left := float64(previousTail[i]) * (1 - t)
		right := float64(next[i]) * t
		mixed := int(left + right)
		if mixed > 32767 {
			mixed = 32767
		}
		if mixed < -32768 {
			mixed = -32768
		}
		next[i] = int16(mixed)
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

func (p *jsonCarrierAudioPacer) cancelPlayback(ctx context.Context, c realtimeBridgeControl) (agentClearReceipt, error) {
	p.policy.agentSession.cancel(c.ItemID, c.Generation)
	r := p.command(ctx, carrierPacerCommand{clear: true, cancel: &c})
	return r.receipt, r.err
}
func (p *carrierAudioPacketizer) cancelPlayback(c realtimeBridgeControl) {
	kept := p.segments[:0]
	p.queuedSamples = 0
	for _, s := range p.segments {
		if !matchesAgentPlayback(s.itemID, s.generation, c) {
			kept = append(kept, s)
			p.queuedSamples += len(s.pcm) - s.offset
		}
	}
	p.segments = kept
}
