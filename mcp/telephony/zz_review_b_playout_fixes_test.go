package main

import (
	"context"
	"errors"
	"math"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func reviewBSpeech(n int) []int16 {
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(2*math.Pi*300*float64(i)/8000))
		if pcm[i] == 0 {
			pcm[i] = 1
		}
	}
	return pcm
}

// A running PCMU pacer whose RTP lands on a local receiver.
func reviewBRunningPacer(t *testing.T, ctx context.Context, s *agentAudioSession, onProgress func(twilioPlaybackProgress) error) (*sipRTPPacer, *net.UDPConn) {
	t.Helper()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sender.Close(); receiver.Close() })
	media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: "PCMU", PacketSamples: 160}}
	p := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, s.policy(), onProgress)
	return p, receiver
}

// Finding 1: an underrun tail fades out before its zero padding, the first
// speech after the exhaustion fades in, contiguous packets are untouched.
func TestReviewBUnderrunEdgeFades(t *testing.T) {
	p := manualAgentPacer("PCMU", 20*time.Millisecond)
	base := time.Unix(100, 0)
	speech := reviewBSpeech(400)
	if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: speech[:200], itemID: "a", generation: 1, audioEndMS: 25}}, base); err != nil {
		t.Fatal(err)
	}
	var sent [][]int16
	var raw [][]int16
	tick := func(at time.Duration) bool {
		if !p.prepareAgentTick(base.Add(at)) {
			return false
		}
		packet, ok := p.dequeueLivePacket(base.Add(at))
		if !ok {
			return false
		}
		raw = append(raw, append([]int16(nil), packet.pcm...))
		sent = append(sent, p.shapeAgentPCM(packet, base.Add(at)))
		p.playback.adjust(packet, -1)
		return true
	}
	if !tick(20*time.Millisecond) || !slices.Equal(sent[0], raw[0]) {
		t.Fatal("contiguous full packet altered")
	}
	if !tick(40*time.Millisecond) || len(raw[1]) != 40 || len(sent[1]) != 160 {
		t.Fatal("partial tail not padded", len(raw[1]))
	}
	if sent[1][39] != 0 || math.Abs(float64(sent[1][30])) >= math.Abs(float64(raw[1][30])) && raw[1][30] != 0 {
		t.Fatal("tail cut to zero padding without a fade", sent[1][30:40])
	}
	if !slices.Equal(sent[1][:16], raw[1][:16]) {
		t.Fatal("fade-out reached beyond 3 ms")
	}
	tick(60 * time.Millisecond) // exhausted
	if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: speech[200:360], itemID: "a", generation: 1, audioEndMS: 45}}, base.Add(70*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	for at := 80 * time.Millisecond; len(sent) < 3 && at < time.Second; at += 20 * time.Millisecond {
		tick(at)
	}
	// Nothing follows it, so this restart also ends on a 3 ms fade-out.
	if len(sent) != 3 || sent[2][0] == raw[2][0] || !slices.Equal(sent[2][24:136], raw[2][24:136]) {
		t.Fatal("restart after exhaustion did not fade in (only)", sent[2][:4], raw[2][:4])
	}
	if math.Abs(float64(sent[2][159])) > math.Abs(float64(raw[2][159]))/4+1 {
		t.Fatal("speech before silence ended on a hard cut", sent[2][150:])
	}
}

// Finding 9: the clear fade is armed only when audio was cleared, and is
// spent only on audible audio.
func TestReviewBClearFadeArmedOnlyByClearedAudio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, _ := reviewBRunningPacer(t, ctx, continuousAgentFixture(), nil)
	defer func() { cancel(); <-p.done }()
	r, err := p.cancelPlayback(ctx, realtimeBridgeControl{ItemID: "nothing", Generation: 1})
	if err != nil || r.LocalMS != 0 {
		t.Fatal(r, err)
	}
	if p.fadeAfterClear.Load() {
		t.Fatal("empty cancel armed the restart fade")
	}
	m := manualAgentPacer("PCMU", 20*time.Millisecond)
	m.fadeAfterClear.Store(true)
	m.shapeAgentPCM(sipRTPOutboundPacket{pcm: make([]int16, 160), silence: true}, time.Time{})
	if !m.fadeAfterClear.Load() {
		t.Fatal("fade spent on silence")
	}
	speech := reviewBSpeech(160)
	out := m.shapeAgentPCM(sipRTPOutboundPacket{pcm: append([]int16(nil), speech...), itemID: "b"}, time.Time{})
	if m.fadeAfterClear.Load() || out[1] == speech[1] {
		t.Fatal("fade not applied to first audible packet")
	}
}

// Finding 3: lateness against a running schedule credits early writes, so
// ordinary write jitter never ratchets the target; new replies decay it.
func TestReviewBEstimatorEarlyCreditAndItemDecay(t *testing.T) {
	s := &agentAudioSession{profile: agentAudioProfile{Mode: "continuous", Playout: agentPlayoutConfig{Mode: "adaptive"}}}
	a := newAgentPlayoutTuning(s, 60)
	base := time.Unix(100, 0)
	// 20 ms writes at 0, 14, 40, 54, 80 ... (the old per-gap rule saw 6 ms deficits).
	for i := 0; i < 300; i++ {
		at := time.Duration(i/2)*40*time.Millisecond + time.Duration(i%2)*14*time.Millisecond
		a.observe(base.Add(at), 20*time.Millisecond)
	}
	if a.stats.TargetMS != 60 || a.stats.MaxDeficitMS != 0 {
		t.Fatal("early arrivals counted as lateness", a.stats)
	}
	a.observe(base.Add(6200*time.Millisecond), 20*time.Millisecond) // 200 ms hold
	if a.stats.TargetMS != 200 {
		t.Fatal(a.stats)
	}
	a.observeItem("one", 1)
	a.observeItem("two", 1)
	if a.stats.TargetMS != 100 {
		t.Fatal("new reply did not decay the learned excess", a.stats)
	}
	for i := 1; i <= 300; i++ { // 6 s of healthy delivery
		a.observe(base.Add(6200*time.Millisecond+time.Duration(i)*20*time.Millisecond), 20*time.Millisecond)
	}
	if a.stats.TargetMS != 60 {
		t.Fatal("rolling window did not decay", a.stats)
	}
}

// Finding 6: audio discarded for hold / remote hold is reported as a local
// clear for its playback, never as progress.
func TestReviewBHoldDiscardsReturnPlaybackCredit(t *testing.T) {
	type discard struct {
		item string
		ms   int
	}
	var mu sync.Mutex
	var discards []discard
	record := func(item string, _ int64, ms int) error {
		mu.Lock()
		discards = append(discards, discard{item, ms})
		mu.Unlock()
		return nil
	}
	// Hold start drains the queue and the packetizer remainder.
	p := manualAgentPacer("PCMU", 60*time.Millisecond)
	p.receipts = newSIPReceiptPump(func(twilioPlaybackProgress) error { t.Fatal("discard reported as progress"); return nil })
	p.setDiscardReceipt(record)
	if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: reviewBSpeech(400), itemID: "a", generation: 1, audioEndMS: 50}}, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	p.drain()
	p.receipts.flush()
	if len(discards) != 1 || discards[0] != (discard{"a", 50}) {
		t.Fatal("hold drain leaked credit", discards)
	}
	// A packet replaced by hold music at dequeue.
	discards = nil
	if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: reviewBSpeech(160), itemID: "b", generation: 1, audioEndMS: 20}}, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	p.media.hold.Store(&localSIPHold{pcm: make([]int16, 160)})
	if packet, ok := p.dequeueLivePacket(time.Unix(201, 0)); !ok || packet.itemID != "" {
		t.Fatal("hold frame not substituted")
	}
	p.receipts.flush()
	if len(discards) != 1 || discards[0] != (discard{"b", 20}) {
		t.Fatal("hold substitution leaked credit", discards)
	}

	// Remote hold drops in the send loop.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	discards = nil
	running, _ := reviewBRunningPacer(t, ctx, continuousAgentFixture(), func(twilioPlaybackProgress) error { t.Error("remote-hold drop reported as progress"); return nil })
	defer func() { cancel(); <-running.done }()
	running.setDiscardReceipt(record)
	running.media.remoteMode.Store(1)
	if _, err := running.enqueue([]sipRTPOutboundPacket{{pcm: reviewBSpeech(320), itemID: "c", generation: 1, audioEndMS: 40}}); err != nil {
		t.Fatal(err)
	}
	for {
		mu.Lock()
		total := 0
		for _, d := range discards {
			total += d.ms
		}
		mu.Unlock()
		if total == 40 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("remote hold leaked credit", discards)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Finding 7: a slow or failing Core receipt write never blocks or ends RTP,
// and progress queued before a cancel is written before its clear.
func TestReviewBProgressOffTheRTPTick(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var mu sync.Mutex
	var order []string
	progress := func(v twilioPlaybackProgress) error {
		time.Sleep(150 * time.Millisecond) // slow Core socket
		mu.Lock()
		order = append(order, v.ItemID)
		mu.Unlock()
		return errors.New("core write failed")
	}
	p, receiver := reviewBRunningPacer(t, ctx, continuousAgentFixture(), progress)
	defer func() { cancel(); <-p.done }()
	speech := reviewBSpeech(8000)
	for i := 0; i < 50; i++ {
		if _, err := p.enqueue([]sipRTPOutboundPacket{{pcm: speech[i*160 : (i+1)*160], itemID: "a", generation: 1, audioEndMS: (i + 1) * 20}}); err != nil {
			t.Fatal(err)
		}
	}
	var prev uint32
	buf := make([]byte, 2048)
	for i := 0; i < 30; i++ {
		receiver.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := receiver.ReadFromUDP(buf)
		if err != nil {
			t.Fatal("RTP stopped behind a slow/failed progress write", err)
		}
		var packet rtp.Packet
		if err := packet.Unmarshal(buf[:n]); err != nil {
			t.Fatal(err)
		}
		if i > 0 && packet.Timestamp-prev != 160 {
			t.Fatalf("RTP timestamp hole mid-speech: %d", packet.Timestamp-prev)
		}
		prev = packet.Timestamp
	}
	select {
	case err := <-p.Err():
		t.Fatal("progress failure ended outgoing audio", err)
	default:
	}
	if _, err := p.cancelPlayback(ctx, realtimeBridgeControl{ItemID: "a", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	written := len(order)
	mu.Unlock()
	if written == 0 {
		t.Fatal("cancel returned before queued progress was written")
	}
	p.receipts.mu.Lock()
	pending := len(p.receipts.pending)
	p.receipts.mu.Unlock()
	if pending != 0 {
		t.Fatal("progress left queued behind the clear", pending)
	}
}

// Finding 7: coalescing keeps the latest progress per playback and never
// merges progress across a discard of the same playback.
func TestReviewBReceiptPumpCoalescingOrder(t *testing.T) {
	var got []string
	r := newSIPReceiptPump(func(v twilioPlaybackProgress) error {
		got = append(got, "progress:"+v.ItemID+":"+strconv.Itoa(v.AudioEndMS))
		return nil
	})
	r.setDiscard(func(item string, _ int64, ms int) error {
		got = append(got, "cleared:"+item+":"+strconv.Itoa(ms))
		return nil
	})
	r.progress(twilioPlaybackProgress{ItemID: "a", Generation: 1, AudioEndMS: 20})
	r.progress(twilioPlaybackProgress{ItemID: "b", Generation: 1, AudioEndMS: 20})
	r.progress(twilioPlaybackProgress{ItemID: "a", Generation: 1, AudioEndMS: 40})
	r.discard("a", 1, 20)
	r.discard("a", 1, 20)
	r.progress(twilioPlaybackProgress{ItemID: "a", Generation: 1, AudioEndMS: 100})
	r.flush()
	want := []string{"progress:a:40", "progress:b:20", "cleared:a:40", "progress:a:100"}
	if !slices.Equal(got, want) {
		t.Fatal(got)
	}
}

// Finding 8: the output resampler forgets the previous reply.
func TestReviewBOutputConverterReset(t *testing.T) {
	s := continuousAgentFixture()
	a, b := reviewBSpeech(2400), make([]int16, 2400)
	for i := range b {
		b[i] = int16(5000 * math.Sin(2*math.Pi*700*float64(i)/24000))
	}
	used := newAgentMediaConverter(s, 8000, false)
	used.output(pcm16ToBytes(a))
	used.reset()
	if !slices.Equal(used.output(pcm16ToBytes(b)), newAgentMediaConverter(s, 8000, false).output(pcm16ToBytes(b))) {
		t.Fatal("previous reply leaked through the resampler")
	}
}

// Finding 10: legacy chunks carry their remainder instead of padding each
// chunk with zeros; a tail without continuation is still committed.
func TestReviewBLegacyChunksCarryRemainder(t *testing.T) {
	committed := make(chan []sipRTPOutboundPacket, 1)
	f := &sipLegacyFramer{rate: 8000, commit: func(p []sipRTPOutboundPacket) { committed <- p }}
	defer f.stop()
	speech := reviewBSpeech(500)
	var got []int16
	for start := 0; start < 500; start += 125 {
		for _, p := range f.frames(speech[start:start+125], sipRTPOutboundPacket{itemID: "a", generation: 1, audioEndMS: (start + 125) / 8}) {
			if len(p.pcm) != 160 {
				t.Fatal("interior packet not full", len(p.pcm))
			}
			got = append(got, p.pcm...)
		}
		f.arm()
	}
	select {
	case tail := <-committed:
		if len(tail) != 1 || len(tail[0].pcm) != 20 || tail[0].audioEndMS != 62 {
			t.Fatal("tail", tail)
		}
		got = append(got, tail[0].pcm...)
	case <-time.After(time.Second):
		t.Fatal("tail never committed")
	}
	if !slices.Equal(got, speech) {
		t.Fatal("legacy chunk boundaries changed audio")
	}
	// A new playback flushes the previous remainder first.
	f.frames(speech[:100], sipRTPOutboundPacket{itemID: "x", generation: 1})
	out := f.frames(speech[:160], sipRTPOutboundPacket{itemID: "y", generation: 1})
	if len(out) != 2 || out[0].itemID != "x" || len(out[0].pcm) != 100 || out[1].itemID != "y" {
		t.Fatal("playback boundary", out)
	}
}

// Finding 11: transmit traces report the real duration of a padded partial.
func TestReviewBTraceReportsRealDuration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := continuousAgentFixture()
	s.profile.Playout.TraceFrames = 10
	p, receiver := reviewBRunningPacer(t, ctx, s, nil)
	defer func() { cancel(); <-p.done }()
	if _, err := p.enqueue([]sipRTPOutboundPacket{{pcm: reviewBSpeech(80), itemID: "a", generation: 1, audioEndMS: 10}}); err != nil {
		t.Fatal(err)
	}
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := receiver.ReadFromUDP(make([]byte, 2048)); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		for _, e := range s.snapshot().TimingTrace {
			if e.Stage == "rtp_transmitted" {
				if e.DurationMS != 10 {
					t.Fatal("padded partial traced as", e.DurationMS)
				}
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no transmit trace")
}
