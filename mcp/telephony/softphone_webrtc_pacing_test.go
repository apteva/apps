package main

import (
	"fmt"
	"math"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
)

// ─── Frozen v0.17.0 sender ─────────────────────────────────────────
//
// One frame per tick, timestamps taken from the wall clock after encoding, and
// any backlog above target eventually trimmed from speech, however young.

type review170Clock struct {
	base     time.Time
	baseTS   uint32
	lastSlot int64
	sent     bool
}

func (c *review170Clock) slot(now time.Time) int64 {
	return int64(now.Sub(c.base) / (20 * time.Millisecond))
}

func (c *review170Clock) next(now time.Time) (uint32, int) {
	slot, skipped := c.slot(now), 0
	if c.sent && slot > c.lastSlot+1 {
		skipped = int((slot - c.lastSlot - 1) * 20)
	}
	c.lastSlot, c.sent = slot, true
	return c.baseTS + uint32(slot*960), skipped
}

func review170TrimBacklog(f *rtcPCMFramer, frames <-chan rtcPCMFrame, frame rtcPCMFrame, now time.Time, stats *rtcMediaStats) rtcPCMFrame {
	const speechTicks = 20
	if frame.pcm == nil || len(frames) <= rtcPlaybackTargetFrames {
		f.backlogTicks = 0
		return frame
	}
	f.backlogTicks++
	silent := rtcPCMSilent(frame.pcm)
	if f.backlogTicks < rtcBacklogSilenceTicks || !silent && f.backlogTicks < speechTicks {
		return frame
	}
	next, _ := nextRTCFrame(frames, now, stats)
	if next.pcm == nil {
		return frame
	}
	if silent {
		stats.drop("webrtc_playback_backlog_silence", stats.mediaDirection(), 0, 20)
		return next
	}
	stats.drop("webrtc_playback_backlog_speech", stats.mediaDirection(), 0, 20)
	f.backlogTicks = speechTicks - rtcBacklogSpeechEvery
	return rtcPCMFrame{pcm: rtcCrossfadeFrames(frame.pcm, next.pcm), expires: frame.expires}
}

// ─── Stall simulation over the real framer, queue and tick ─────────

type rtcPacingRecord func(frame rtcPCMFrame, timestamp uint32, now time.Time)

type rtcPacingSender func(now time.Time, frames <-chan rtcPCMFrame, pending *rtcPCMFramer, last time.Time, stats *rtcMediaStats, record rtcPacingRecord)

func currentRTCPacingSender(start time.Time) rtcPacingSender {
	clock := rtcRTPClock{base: start}
	silence := make([]int16, 480)
	return func(now time.Time, frames <-chan rtcPCMFrame, pending *rtcPCMFramer, last time.Time, stats *rtcMediaStats, record rtcPacingRecord) {
		playRTCTick(&clock, now, frames, pending, last, silence, liveAudioMaxAge, stats, func(frame rtcPCMFrame, now time.Time, catchup bool) (time.Time, bool) {
			if catchup {
				stats.mu.Lock()
				stats.value.PacingCatchupFrames++
				stats.mu.Unlock()
			}
			record(frame, clock.take(now), now)
			return now, true
		})
	}
}

func review170RTCPacingSender(start time.Time) rtcPacingSender {
	clock := review170Clock{base: start}
	silence := make([]int16, 480)
	return func(now time.Time, frames <-chan rtcPCMFrame, pending *rtcPCMFramer, last time.Time, stats *rtcMediaStats, record rtcPacingRecord) {
		if clock.sent && clock.slot(now) <= clock.lastSlot {
			return
		}
		frame, _ := nextRTCFrame(frames, now, stats)
		frame = review170TrimBacklog(pending, frames, frame, now, stats)
		if frame.pcm == nil {
			frame = rtcPCMFrame{pcm: silence, expires: now.Add(liveAudioMaxAge)}
		}
		timestamp, skipped := clock.next(now)
		if skipped > 0 {
			stats.drop("webrtc_pacing_gap", stats.mediaDirection(), 0, skipped)
		}
		record(frame, timestamp, now)
	}
}

type rtcPacingScenario struct {
	// The sender goroutine is suspended, so ticks and chunk handling both wait.
	senderStall time.Duration
	// Only the source is late: the sender keeps its ticks.
	sourceStall time.Duration
	// A stall can end with the waiting tick seen before the waiting chunks.
	tickFirst bool
	// Source silence, for the free trim at a pause.
	pauseAt, pauseFor time.Duration
}

type rtcPacingResult struct {
	Holes           int // RTP timestamp steps other than one frame
	SkippedMS       int64
	CatchupFrames   uint64
	MissingFrames   int // source speech frames that never reached the wire
	SilenceInSpeech int // silent packets between the first and last speech frames
	SpeechTrimMS    int
	SilenceTrimMS   int
	OverflowMS      int
	WorstAge        time.Duration
	LastAge         time.Duration
}

// Continuous 20 ms source frames, each numbered in its samples. Ticks run 5 ms
// after chunks. Chunks are stamped with their source time, the conservative
// case: a stall counts in full against their age.
func simulateRTCPacing(sender func(time.Time) rtcPacingSender, s rtcPacingScenario) rtcPacingResult {
	const stallStart, sourceEnd, total = 1000 * time.Millisecond, 3800 * time.Millisecond, 4000 * time.Millisecond
	start := time.Unix(1700000000, 0)
	frames := make(chan rtcPCMFrame, 6)
	stats := &rtcMediaStats{}
	done := make(chan struct{})
	pending := rtcPCMFramer{maxAge: liveAudioMaxAge}
	send := sender(start)
	var r rtcPacingResult
	var last time.Time
	type chunk struct {
		pcm []int16
		at  time.Time
	}
	var held []chunk
	var sent []int // source frame number per packet, -1 for silence
	var lastTS uint32
	record := func(frame rtcPCMFrame, timestamp uint32, now time.Time) {
		if len(sent) > 0 && timestamp-lastTS != 960 {
			r.Holes++
		}
		lastTS = timestamp
		if rtcPCMSilent(frame.pcm) {
			sent = append(sent, -1)
			return
		}
		sent = append(sent, int(frame.pcm[479])-1000)
		age := pending.frameAge(frame, now)
		r.WorstAge = max(r.WorstAge, age)
		r.LastAge = age
	}
	tick := func(now time.Time) {
		send(now, frames, &pending, last, stats, record)
	}
	deliver := func(n int) {
		for _, c := range held[:n] {
			last = c.at
			pending.push(pcm16ToBytes(c.pcm), c.at.Add(liveAudioMaxAge), func(f rtcPCMFrame) bool { return queueRTCFrame(frames, f, done, stats) })
		}
		held = held[n:]
	}
	missedTick := false
	number := 0
	for at := time.Duration(0); at <= total; at += time.Millisecond {
		now := start.Add(at)
		if at%(20*time.Millisecond) == 0 && at < sourceEnd {
			pcm := make([]int16, 480)
			if s.pauseFor == 0 || at < s.pauseAt || at >= s.pauseAt+s.pauseFor {
				for i := range pcm {
					pcm[i] = int16(1000 + number)
				}
				number++
			}
			held = append(held, chunk{pcm, now})
		}
		tickDue := at%(20*time.Millisecond) == 5*time.Millisecond
		if at >= stallStart && at < stallStart+s.senderStall {
			missedTick = missedTick || tickDue
			continue
		}
		sourceHeld := at >= stallStart && at < stallStart+s.sourceStall
		if !sourceHeld && s.tickFirst && missedTick {
			// The tick's drain takes the chunk the blocked reader holds. The reader
			// has not yet read the next one from the socket.
			deliver(min(1, len(held)))
			tick(now)
			missedTick = false
		}
		if !sourceHeld {
			deliver(len(held))
		}
		if tickDue || missedTick {
			tick(now)
			missedTick = false
		}
	}
	v := stats.snapshot()
	r.SkippedMS = v.PacingSkippedMS
	r.CatchupFrames = v.PacingCatchupFrames
	for _, e := range v.Events {
		switch e.Reason {
		case "webrtc_playback_backlog_speech":
			r.SpeechTrimMS += e.DurationMS
		case "webrtc_playback_backlog_silence":
			r.SilenceTrimMS += e.DurationMS
		case "webrtc_playback_overflow":
			r.OverflowMS += e.DurationMS
		}
	}
	previous, first, lastSpeech := -1, -1, -1
	for i, n := range sent {
		if n >= 0 {
			if first < 0 {
				first = i
			}
			lastSpeech = i
		}
	}
	for i := first; i >= 0 && i <= lastSpeech; i++ {
		if sent[i] < 0 {
			if s.pauseFor == 0 {
				r.SilenceInSpeech++
			}
			continue
		}
		if previous >= 0 {
			r.MissingFrames += sent[i] - previous - 1
		}
		previous = sent[i]
	}
	return r
}

func TestRTCSenderRepaysStallsWithoutRemovingSpeech(t *testing.T) {
	for _, stall := range []time.Duration{5, 10, 20, 40, 80, 100} {
		stall *= time.Millisecond
		for _, tickFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/tick_first=%v", stall, tickFirst), func(t *testing.T) {
				s := rtcPacingScenario{senderStall: stall, tickFirst: tickFirst}
				fixed := simulateRTCPacing(currentRTCPacingSender, s)
				t.Logf("fixed: %+v", fixed)
				if fixed.Holes != 0 || fixed.SkippedMS != 0 {
					t.Fatal("stall opened a hole in the media timeline", fixed)
				}
				if fixed.MissingFrames != 0 || fixed.SpeechTrimMS != 0 || fixed.OverflowMS != 0 {
					t.Fatal("stall removed speech", fixed)
				}
				if fixed.SilenceInSpeech != 0 {
					t.Fatal("silence took the place of delayed speech", fixed)
				}
				if fixed.LastAge > 25*time.Millisecond {
					t.Fatal("latency did not return after the stall", fixed)
				}
				if fixed.WorstAge > stall+45*time.Millisecond {
					t.Fatal("repayment added latency", fixed)
				}
				if stall >= 40*time.Millisecond && fixed.CatchupFrames == 0 {
					t.Fatal("stall was not repaid", fixed)
				}
				if stall >= 40*time.Millisecond {
					old := simulateRTCPacing(review170RTCPacingSender, s)
					t.Logf("v0.17.0: %+v", old)
					if old.Holes == 0 || old.SkippedMS == 0 || old.SpeechTrimMS == 0 || old.MissingFrames == 0 {
						t.Fatal("did not reproduce the v0.17.0 damage", old)
					}
				}
			})
		}
	}
}

// Past the queue bound the delay cannot be repaid. The timeline jumps, and what
// is still queued drains at once instead of staying as latency.
func TestRTCSenderJumpsAfterLongPauseWithoutLastingLatency(t *testing.T) {
	for _, stall := range []time.Duration{160, 200, 240} {
		stall *= time.Millisecond
		t.Run(stall.String(), func(t *testing.T) {
			r := simulateRTCPacing(currentRTCPacingSender, rtcPacingScenario{senderStall: stall})
			t.Logf("%+v", r)
			if r.Holes != 1 || r.SkippedMS == 0 {
				t.Fatal("long pause did not jump the timeline once", r)
			}
			if r.LastAge > 25*time.Millisecond || r.SpeechTrimMS != 0 {
				t.Fatal("queued audio stayed as latency or was trimmed", r)
			}
		})
	}
}

// A late source is not the sender's stall: its slots already went out as
// silence, so what arrives afterwards queues up. It stays while within budget.
func TestRTCLateSourceSpeechKeptUntilTooOld(t *testing.T) {
	young := rtcPacingScenario{sourceStall: 60 * time.Millisecond}
	fixed := simulateRTCPacing(currentRTCPacingSender, young)
	old := simulateRTCPacing(review170RTCPacingSender, young)
	t.Logf("fixed: %+v", fixed)
	t.Logf("v0.17.0: %+v", old)
	if old.SpeechTrimMS == 0 {
		t.Fatal("did not reproduce young speech being removed", old)
	}
	if fixed.SpeechTrimMS != 0 || fixed.MissingFrames != 0 || fixed.LastAge >= rtcPlaybackSpeechBudget {
		t.Fatal("speech within budget was removed", fixed)
	}

	tooOld := simulateRTCPacing(currentRTCPacingSender, rtcPacingScenario{sourceStall: 120 * time.Millisecond})
	t.Logf("too old: %+v", tooOld)
	if tooOld.SpeechTrimMS == 0 || tooOld.SpeechTrimMS > 60 {
		t.Fatal("old speech not trimmed back to the budget, or trimmed further", tooOld)
	}
	if tooOld.LastAge >= rtcPlaybackSpeechBudget {
		t.Fatal("audio left over budget", tooOld)
	}

	// Silence is free: the first pause returns the queue to its target.
	paused := simulateRTCPacing(currentRTCPacingSender, rtcPacingScenario{sourceStall: 60 * time.Millisecond, pauseAt: 2 * time.Second, pauseFor: 500 * time.Millisecond})
	t.Logf("pause: %+v", paused)
	if paused.SilenceTrimMS == 0 || paused.SpeechTrimMS != 0 || paused.LastAge > 45*time.Millisecond {
		t.Fatal("pause did not absorb the backlog", paused)
	}
}

func TestRTCSpeechBudgetFollowsSenderLifetime(t *testing.T) {
	now := time.Unix(1700000000, 0)
	frame := rtcPCMFrame{expires: now.Add(120 * time.Millisecond)}
	if age := (&rtcPCMFramer{maxAge: listenerMaxAge}).frameAge(frame, now); age != 80*time.Millisecond {
		t.Fatal("listener age", age)
	}
	if age := (&rtcPCMFramer{}).frameAge(frame, now); age != 130*time.Millisecond {
		t.Fatal("default age", age)
	}
}

// ─── The real loop on a real transport ─────────────────────────────

func rtcPacingLoopback(t *testing.T) (*webrtc.TrackLocalStaticRTP, <-chan rtcTestPacket) {
	t.Helper()
	api, err := softphoneRTCAPI(softphoneRTCConfig{})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sender.Close(); receiver.Close() })
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "pacing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sender.AddTrack(track); err != nil {
		t.Fatal(err)
	}
	received := make(chan rtcTestPacket, 1024)
	receiver.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			for {
				p, _, err := remote.ReadRTP()
				if err != nil {
					return
				}
				received <- rtcTestPacket{rtcTestRTP{p.Timestamp, p.SequenceNumber, time.Now()}, append([]byte(nil), p.Payload...)}
			}
		}()
	})
	connected := make(chan struct{})
	sender.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			close(connected)
		}
	})
	offer, err := sender.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(sender)
	if err = sender.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err = receiver.SetRemoteDescription(*sender.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := receiver.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered = webrtc.GatheringCompletePromise(receiver)
	if err = receiver.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if err = sender.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("loopback did not connect")
	}
	return track, received
}

type rtcTestPacket struct {
	rtcTestRTP
	payload []byte
}

// Holding the stats lock suspends the running sender loop at its next chunk,
// the same way a scheduling stall does: ticks and chunk handling both wait.
func runRTCPacingOnWire(t *testing.T, stall time.Duration) (string, error) {
	rtcTestCtx(t)
	track, received := rtcPacingLoopback(t)
	bridge, source := net.Pipe()
	done := make(chan struct{})
	once := make(chan struct{}, 1)
	stop := func() {
		select {
		case once <- struct{}{}:
			close(done)
		default:
		}
	}
	defer stop()
	defer bridge.Close()
	defer source.Close()
	stats := &rtcMediaStats{}
	input, signal := newWebSocketWriterPump(bridge, ws.StateClientSide), newWebSocketWriterPump(bridge, ws.StateClientSide)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		sendRTCAudio(bridge, input, signal, track, 32000, stats, done, stop, true, liveAudioMaxAge, 320, nil, 48000)
	}()
	defer func() { stop(); <-finished }()

	const speech = 3 * time.Second
	pcm := make([]int16, 480)
	sample := 0
	begin := time.Now()
	sourceDone := make(chan error, 1)
	go func() {
		for i := 0; time.Duration(i)*20*time.Millisecond < speech; i++ {
			if wait := time.Until(begin.Add(time.Duration(i) * 20 * time.Millisecond)); wait > 0 {
				time.Sleep(wait)
			}
			for k := range pcm {
				pcm[k] = int16(8000 * math.Sin(2*math.Pi*440*float64(sample)/24000))
				sample++
			}
			if err := wsutil.WriteServerBinary(source, pcm16ToBytes(pcm)); err != nil {
				sourceDone <- err
				return
			}
		}
		sourceDone <- nil
	}()
	for _, at := range []time.Duration{time.Second, 2 * time.Second} {
		time.Sleep(time.Until(begin.Add(at)))
		stats.mu.Lock()
		time.Sleep(stall)
		stats.mu.Unlock()
	}
	if err := <-sourceDone; err != nil {
		return "", err
	}
	time.Sleep(200 * time.Millisecond)

	decoder, err := newRTCOpusDecoderWithFEC(true)
	if err != nil {
		return "", err
	}
	defer decoder.Close()
	decoded := make([]int16, 2880)
	var packets []rtcTestPacket
	for drained := false; !drained; {
		select {
		case p := <-received:
			packets = append(packets, p)
		default:
			drained = true
		}
	}
	if len(packets) < 100 {
		return "", fmt.Errorf("only %d packets received", len(packets))
	}
	voiced := make([]bool, len(packets))
	first, last := -1, -1
	for i, p := range packets {
		n, err := decoder.decode(p.payload, decoded)
		if err != nil {
			return "", err
		}
		voiced[i] = speechRMS(decoded[:n]) > 300
		if voiced[i] {
			if first < 0 {
				first = i
			}
			last = i
		}
		if i > 0 {
			if packets[i].sequence-packets[i-1].sequence != 1 || packets[i].timestamp-packets[i-1].timestamp != 960 {
				return "", fmt.Errorf("packet %d: seq %d->%d ts %d->%d", i, packets[i-1].sequence, packets[i].sequence, packets[i-1].timestamp, packets[i].timestamp)
			}
		}
	}
	// A real silent packet in mid speech would be delayed speech lost to the clock.
	for i := first; i >= 0 && i <= last; i++ {
		if !voiced[i] {
			return "", fmt.Errorf("packet %d of %d..%d went silent", i, first, last)
		}
	}
	v := stats.snapshot()
	for _, e := range v.Events {
		if e.Reason == "webrtc_playback_backlog_speech" || e.Reason == "webrtc_playback_overflow" {
			return "", fmt.Errorf("speech removed: %+v", e)
		}
	}
	if v.PacingSkippedMS != 0 {
		return "", fmt.Errorf("pacing skipped %d ms", v.PacingSkippedMS)
	}
	if v.PacingCatchupFrames == 0 {
		return "", fmt.Errorf("no frames repaid")
	}
	return fmt.Sprintf("packets=%d voiced=%d..%d catchup_frames=%d max_queue_ms=%d", len(packets), first, last, v.PacingCatchupFrames, v.MaxQueueMS), nil
}

func TestRTCSenderRepaysRealStallOnTheWire(t *testing.T) {
	// A loaded test machine adds stalls of its own, which can exceed the limit.
	var lastErr error
	for attempt := range 3 {
		result, err := runRTCPacingOnWire(t, 80*time.Millisecond)
		if err == nil {
			t.Logf("attempt %d: %s", attempt+1, result)
			return
		}
		t.Logf("attempt %d: %v", attempt+1, err)
		lastErr = err
	}
	t.Fatal(lastErr)
}
