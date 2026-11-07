package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// Exercises the real capture-release queue, source envelope, resampler, SIP
// pacer, codec and UDP writer together. Source times are synthetic; no carrier
// or browser network is contacted. The old pacer sent 0/50 delayed frames.
func TestSIPMicrophoneProgressSourceBudget(t *testing.T) {
	for _, codec := range []string{"PCMA", "PCMU", "G722"} {
		for _, age := range []time.Duration{60 * time.Millisecond, 180 * time.Millisecond} {
			t.Run(fmt.Sprintf("%s/%s", codec, age), func(t *testing.T) {
				sender, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if e != nil {
					t.Fatal(e)
				}
				defer sender.Close()
				receiver, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if e != nil {
					t.Fatal(e)
				}
				defer receiver.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: codec}}
				sent := make(chan []byte, 64)
				p := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, humanCarrierPacerPolicy(nil), nil, func(b []byte) { sent <- append([]byte(nil), b...) })
				defer func() { cancel(); <-p.done }()
				fixture := sipSpeechFixtures[0]
				speech := loadSIPSpeech(t, fixture.id, fixture.hash, 24000)
				resampler := newPCMResampler(24000, media.sampleRate())
				reference := &sipRTPMedia{offer: media.offer}
				var expected [][]byte
				stats := &rtcMediaStats{}
				var release rtcAudioRelease
				packet := func(i int, at time.Time) sipRTPOutboundPacket {
					if i >= 4 {
						time.Sleep(max(0, time.Until(release.next)))
						at = time.Now()
					}
					pcm := speech[i*480 : (i+1)*480]
					release.push(pcm, at.Add(-age), stats, "operator_to_carrier")
					f, ok := release.pop(at, stats, "operator_to_carrier")
					if !ok {
						t.Fatal("capture unexpectedly rejected input")
					}
					// Both call directions consume the same authenticated source envelope.
					direction := []string{"inbound", "outbound"}[i%2]
					raw, source := carrierMicrophoneSource(&callRow{PeerKind: peerKindHuman, Direction: direction}, encodeMicrophoneSource(pcm16ToBytes(f.pcm), f.source))
					samples := resampler.Process(bytesToPCM16(raw))
					if len(samples) > media.sampleRate()/50 {
						t.Fatal("unexpected multi-frame resampler output")
					}
					// Match bridgeSIPMedia padding of its initial filter-delay tail.
					padded := make([]int16, media.sampleRate()/50)
					copy(padded, samples)
					samples = padded
					expected = append(expected, reference.encodeAudio(append([]int16(nil), samples...)))
					return sipRTPOutboundPacket{pcm: samples, enqueuedAt: source}
				}
				start := time.Now()
				initial := make([]sipRTPOutboundPacket, 4)
				for i := range initial {
					initial[i] = packet(i, start.Add(time.Duration(i-3)*20*time.Millisecond))
				}
				if _, e := p.enqueue(initial); e != nil {
					t.Fatal(e)
				}
				for i := 4; i < 50; i++ {
					time.Sleep(max(0, time.Until(start.Add(time.Duration(i-3)*20*time.Millisecond))))
					if _, e := p.enqueue([]sipRTPOutboundPacket{packet(i, time.Now())}); e != nil {
						t.Fatal(e)
					}
				}
				limit := time.Now().Add(time.Second)
				for p.playback.hasPending() && time.Now().Before(limit) {
					time.Sleep(time.Millisecond)
				}
				cancel()
				<-p.done
				s := p.diagnostics.snapshot()
				if p.playback.hasPending() || s.SentMS+s.DroppedMS != 1000 || stats.snapshot().IngressDiscardedMS != 0 {
					t.Fatalf("bad accounting: %+v ingress=%+v", s, stats.snapshot())
				}
				if age == 60*time.Millisecond {
					if s.SentMS != 1000 || s.DroppedMS != 0 {
						t.Fatalf("healthy speech changed: %+v", s)
					}
					for i, want := range expected {
						select {
						case got := <-sent:
							if !bytes.Equal(got, want) {
								t.Fatalf("healthy codec waveform changed at frame %d", i)
							}
						default:
							t.Fatal("missing healthy speech")
						}
					}
				} else if s.SentMS < 900 || s.DroppedMS > 100 {
					t.Fatalf("expired head prevented usable speech: %+v", s)
				}
				if s.MaxSourceAgeMS <= s.MaxResidenceMS {
					t.Fatal("source age confused with local queue residence", s)
				}
				t.Logf("source_age=%s sent_ms=%d discarded_ms=%d max_source_age_ms=%d max_residence_ms=%d", age, s.SentMS, s.DroppedMS, s.MaxSourceAgeMS, s.MaxResidenceMS)
			})
		}
	}
}

func TestSIPMicrophoneProgressDrainsExpiredWithoutSpendingMediaTime(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprint(buffered), func(t *testing.T) {
			now := time.Now()
			p := &sipRTPPacer{queue: make(chan sipRTPOutboundPacket, 9), playback: &sipPlaybackState{}, media: &sipRTPMedia{}, dropStale: !buffered}
			for i := 0; i < 9; i++ {
				at := now.Add(-time.Second)
				if i == 8 {
					at = now.Add(-180 * time.Millisecond)
				}
				p.queue <- sipRTPOutboundPacket{enqueuedAt: at, queuedAt: now.Add(-20 * time.Millisecond), itemID: fmt.Sprint(i)}
				p.playback.pending.Add(1)
			}
			packet, ok := p.dequeueLivePacket(now)
			if !ok {
				t.Fatal("fresh tail not available in same tick")
			}
			want := "8"
			drops := int64(160)
			pending := int64(1)
			if buffered {
				want = "0"
				drops = 0
				pending = 9
			}
			if packet.itemID != want || p.diagnostics.snapshot().DroppedMS != drops || p.playback.pending.Load() != pending {
				t.Fatalf("wrong dequeue: packet=%s stats=%+v pending=%d", packet.itemID, p.diagnostics.snapshot(), p.playback.pending.Load())
			}
			for _, e := range p.dropEvents() {
				if e.FrameAgeMS != 1000 || e.QueueResidenceMS != 20 {
					t.Fatal("age evidence lost", e)
				}
			}
		})
	}
}

func TestSIPMicrophoneProgressExpiredOnlyQueueRemainsBounded(t *testing.T) {
	now := time.Now()
	p := &sipRTPPacer{queue: make(chan sipRTPOutboundPacket, 9), playback: &sipPlaybackState{}, media: &sipRTPMedia{}, dropStale: true}
	for i := 0; i < 9; i++ {
		p.queue <- sipRTPOutboundPacket{enqueuedAt: now.Add(-time.Second)}
		p.playback.pending.Add(1)
	}
	if _, ok := p.dequeueLivePacket(now); ok || p.playback.hasPending() || p.droppedMS() != 180 {
		t.Fatal("expired backlog retained or sent")
	}
	p.queue <- sipRTPOutboundPacket{enqueuedAt: now}
	p.playback.pending.Add(1)
	if _, ok := p.dequeueLivePacket(now); !ok {
		t.Fatal("expired backlog poisoned fresh audio")
	}
}

func BenchmarkSIPMicrophoneProgressDequeue(b *testing.B) {
	p := &sipRTPPacer{queue: make(chan sipRTPOutboundPacket, 9), playback: &sipPlaybackState{}, media: &sipRTPMedia{}, dropStale: true}
	now := time.Now()
	packet := sipRTPOutboundPacket{enqueuedAt: now.Add(-60 * time.Millisecond), queuedAt: now}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.queue <- packet
		if _, ok := p.dequeueLivePacket(now); !ok {
			b.Fatal("fresh rejected")
		}
	}
}

// Ten virtual minutes at normal capture cadence. The released behavior is kept
// only as a reference: one expired dequeue per tick can reject the entire call.
func TestSIPMicrophoneProgressLongDelivery(t *testing.T) {
	for _, age := range []time.Duration{60 * time.Millisecond, 180 * time.Millisecond} {
		t.Run(age.String(), func(t *testing.T) {
			start := time.Now()
			makePacer := func() *sipRTPPacer {
				return &sipRTPPacer{queue: make(chan sipRTPOutboundPacket, 9), playback: &sipPlaybackState{}, media: &sipRTPMedia{}, dropStale: true}
			}
			current, previous := makePacer(), makePacer()
			publish := func(p *sipRTPPacer, source, queued time.Time) {
				p.playback.pending.Add(1)
				p.queue <- sipRTPOutboundPacket{enqueuedAt: source, queuedAt: queued}
			}
			for i := 0; i < 4; i++ {
				source := start.Add(time.Duration(i-3)*20*time.Millisecond - age)
				publish(current, source, start)
				publish(previous, source, start)
			}
			sent, oldSent := 0, 0
			for i := 1; i <= 30000; i++ {
				now := start.Add(time.Duration(i) * 20 * time.Millisecond)
				if _, ok := current.dequeueLivePacket(now); ok {
					sent++
					current.playback.pending.Add(-1)
				}
				// Exact 0.12.25 expiry behavior: one dequeue consumes an entire tick.
				select {
				case packet := <-previous.queue:
					if now.Sub(packet.enqueuedAt) < liveAudioMaxAge {
						oldSent++
					}
					previous.playback.pending.Add(-1)
				default:
				}
				if i < 30000 {
					publish(current, now.Add(-age), now)
					publish(previous, now.Add(-age), now)
				}
			}
			if age == 60*time.Millisecond {
				if sent != 30000 || oldSent != 30000 || current.droppedMS() != 0 {
					t.Fatal("normal delivery changed", sent, oldSent)
				}
			} else if oldSent != 0 || sent != 30000 || current.droppedMS() != 20 {
				t.Fatal("sustained source-budget recovery failed", sent, oldSent, current.droppedMS())
			}
			t.Logf("10 minutes age=%s: 0.12.25 sent=%dms current sent=%dms current discarded=%dms", age, oldSent*20, sent*20, current.droppedMS())
		})
	}
}

func TestSIPMicrophoneProgressConcurrentCalls(t *testing.T) {
	const calls = 64
	results := make(chan int, calls)
	for call := 0; call < calls; call++ {
		go func() {
			now := time.Now()
			p := &sipRTPPacer{queue: make(chan sipRTPOutboundPacket, 9), playback: &sipPlaybackState{}, media: &sipRTPMedia{}, dropStale: true}
			for i := 0; i < 4; i++ {
				p.playback.pending.Add(1)
				p.queue <- sipRTPOutboundPacket{enqueuedAt: now.Add(time.Duration(i-3)*20*time.Millisecond - 180*time.Millisecond), queuedAt: now}
			}
			sent := 0
			for i := 0; i < 3000; i++ {
				now = now.Add(20 * time.Millisecond)
				if _, ok := p.dequeueLivePacket(now); ok {
					sent++
					p.playback.pending.Add(-1)
				}
				p.playback.pending.Add(1)
				p.queue <- sipRTPOutboundPacket{enqueuedAt: now.Add(-180 * time.Millisecond), queuedAt: now}
			}
			p.drain()
			if p.playback.hasPending() {
				sent = -1
			}
			results <- sent
		}()
	}
	for call := 0; call < calls; call++ {
		if sent := <-results; sent != 3000 {
			t.Fatalf("concurrent call failed progress: %d", sent)
		}
	}
}

func TestSIPMicrophoneProgressHoldIsFreshGeneratedAudio(t *testing.T) {
	now := time.Now()
	media := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMA"}}
	media.hold.Store(&localSIPHold{pcm: []int16{123}})
	p := &sipRTPPacer{queue: make(chan sipRTPOutboundPacket, 9), playback: &sipPlaybackState{}, media: media, dropStale: true}
	p.queue <- sipRTPOutboundPacket{enqueuedAt: now.Add(-time.Second), itemID: "old-microphone"}
	p.playback.pending.Add(1)
	packet, ok := p.dequeueLivePacket(now)
	if !ok || len(packet.pcm) != 160 || packet.pcm[0] != 123 || !packet.enqueuedAt.Equal(now) || packet.itemID != "" || p.droppedMS() != 0 {
		t.Fatal("hold inherited microphone expiry", packet)
	}
}
