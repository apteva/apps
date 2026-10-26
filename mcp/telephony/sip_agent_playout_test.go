package main

import (
	"context"
	"fmt"
	"github.com/pion/rtp"
	"net"
	"slices"
	"testing"
	"time"
)

func manualAgentPacer(codec string, reserve time.Duration) *sipRTPPacer {
	media := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
	return &sipRTPPacer{media: media, playback: &sipPlaybackState{}, agentSession: continuousAgentFixture(), packetizer: newCarrierAudioPacketizer(media.sampleRate()), queue: make(chan sipRTPOutboundPacket, 50), agentPlayout: sipAgentPlayout{reserve: reserve}}
}

// Run the real converter/packetizer/admission at a deterministic 20ms send
// cadence. Compare every original sample, not just queue size or loss counters.
func TestAgentAudioSIPSpeechChunkContinuity(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		for _, chunk := range []int{480, 600, 2400, 2412} {
			for _, fixture := range sipSpeechFixtures {
				t.Run(fmt.Sprintf("%s/%d/%s", codec, chunk, fixture.id), func(t *testing.T) {
					input := loadSIPSpeech(t, fixture.id, fixture.hash, 24000)
					p := manualAgentPacer(codec, 60*time.Millisecond)
					converter := newAgentMediaConverter(p.agentSession, p.media.sampleRate(), false)
					type arrival struct {
						at         time.Duration
						start, end int
					}
					var arrivals []arrival
					for start := 0; start < len(input); start += chunk {
						at := time.Duration(start) * time.Second / 24000
						if start/chunk%4 == 2 {
							at += min(30*time.Millisecond, time.Duration(chunk)*time.Second/24000/3)
						}
						arrivals = append(arrivals, arrival{at, start, min(len(input), start+chunk)})
					}
					base := time.Unix(100, 0)
					next := 0
					var expected, actual []int16
					var first, last time.Duration
					var packets, padding, maxGap int
					end := time.Duration(len(input))*time.Second/24000 + time.Second
					for tick := time.Duration(0); tick <= end; tick += 20 * time.Millisecond {
						for next < len(arrivals) && arrivals[next].at <= tick {
							a := arrivals[next]
							for start := a.start; start < a.end; start += 480 {
								stop := min(start+480, a.end)
								pcm := converter.output(pcm16ToBytes(input[start:stop]))
								expected = append(expected, pcm...)
								if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: pcm, itemID: "reply", generation: 1, audioEndMS: stop / 24}}, base.Add(a.at)); err != nil {
									t.Fatal(err)
								}
							}
							next++
						}
						if !p.prepareAgentTick(base.Add(tick)) {
							continue
						}
						packet, ok := p.dequeueLivePacket(base.Add(tick))
						if !ok {
							continue
						}
						if packets == 0 {
							first = tick
						} else {
							maxGap = max(maxGap, int((tick-last)/time.Millisecond))
						}
						last = tick
						packets++
						actual = append(actual, packet.pcm...)
						padding += p.media.sampleRate()/50 - len(packet.pcm)
						p.playback.adjust(packet, -1)
					}
					if !slices.Equal(actual, expected) {
						t.Fatal("speech changed, reordered or lost")
					}
					if padding >= p.media.sampleRate()/50 {
						t.Fatalf("interior chunk padding: %d samples", padding)
					}
					if maxGap > 20 {
						t.Fatalf("speech interrupted: %dms", maxGap)
					}
					if first != 60*time.Millisecond {
						t.Fatalf("reserve ignored: %s", first)
					}
					if p.playback.hasPendingSpeech() {
						t.Fatal("playback accounting leaked")
					}
					t.Logf("samples=%d preserved=100%% max_gap_ms=%d padding_ms=%.3f first_audio_ms=%d", len(actual), maxGap, float64(padding)*1000/float64(p.media.sampleRate()), first/time.Millisecond)
				})
			}
		}
	}
}

func TestAgentAudioSIPReserveTailAndRecovery(t *testing.T) {
	base := time.Unix(100, 0)
	for _, reserve := range []time.Duration{20 * time.Millisecond, 60 * time.Millisecond, 200 * time.Millisecond} {
		p := manualAgentPacer("PCMU", reserve)
		put := func(at time.Duration, n int) {
			t.Helper()
			if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: make([]int16, n), itemID: "a", generation: 1}}, base.Add(at)); err != nil {
				t.Fatal(err)
			}
		}
		put(0, 200) // One full frame plus 5ms tail.
		if p.prepareAgentTick(base.Add(reserve - time.Nanosecond)) {
			t.Fatal("reserve bypassed")
		}
		if !p.prepareAgentTick(base.Add(reserve)) || p.packetizer.queuedSamples != 40 {
			t.Fatal("tail flushed before full audio")
		}
		frame, ok := p.dequeueLivePacket(base.Add(reserve))
		if !ok {
			t.Fatal("full audio missing")
		}
		p.playback.adjust(frame, -1)
		// Continuation fills the partial packet instead of committing 15ms zeros.
		put(reserve+time.Millisecond, 120)
		if !p.prepareAgentTick(base.Add(reserve + 20*time.Millisecond)) {
			t.Fatal("chunk boundary re-primed")
		}
		frame, ok = p.dequeueLivePacket(base.Add(reserve + 20*time.Millisecond))
		if !ok || len(frame.pcm) != 160 {
			t.Fatal("continuation not preserved")
		}
		p.playback.adjust(frame, -1)
		p.prepareAgentTick(base.Add(time.Second)) // A long stall really exhausted the queue.
		put(2*time.Second, 80)
		if p.prepareAgentTick(base.Add(2*time.Second + reserve - time.Nanosecond)) {
			t.Fatal("recovery reserve bypassed")
		}
		if !p.prepareAgentTick(base.Add(2*time.Second + reserve)) {
			t.Fatal("short final tail stuck")
		}
		frame, ok = p.dequeueLivePacket(base.Add(2*time.Second + reserve))
		if !ok || len(frame.pcm) != 80 {
			t.Fatal("final tail lost")
		}
		p.playback.adjust(frame, -1)
		if p.agentPlayout.starts != 2 || p.agentPlayout.exhaustions != 1 {
			t.Fatal(p.agentPlayout)
		}
	}
	// Human media has no additional AI cushion.
	p := manualAgentPacer("PCMU", 200*time.Millisecond)
	p.packetizer = nil
	if !p.prepareAgentTick(base) {
		t.Fatal("human path delayed")
	}
}

// Real UDP writes verify that startup delay and partial packets survive the
// goroutine/codec path, not only the deterministic sample-level harness.
func TestAgentAudioSIPWireChunkContinuity(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA"} {
		t.Run(codec, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer receiver.Close()
			media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
			s := continuousAgentFixture()
			p := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, s.policy(), nil)
			defer func() { cancel(); <-p.done }()
			input := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, 24000)[:24000]
			done := make(chan error, 1)
			origin := time.Now()
			go func() {
				converter := newAgentMediaConverter(s, 8000, false)
				for start := 0; start < len(input); start += 600 {
					time.Sleep(time.Until(origin.Add(time.Duration(start) * time.Second / 24000)))
					for off := start; off < start+600; off += 480 {
						stop := min(off+480, start+600)
						pcm := converter.output(pcm16ToBytes(input[off:stop]))
						if _, e := p.enqueue([]sipRTPOutboundPacket{{pcm: pcm, itemID: "reply", generation: 1, audioEndMS: stop / 24}}); e != nil {
							done <- e
							return
						}
					}
				}
				done <- nil
			}()
			var received []byte
			var previous uint32
			buf := make([]byte, 2048)
			for n := 0; n < 50; n++ {
				receiver.SetReadDeadline(time.Now().Add(time.Second))
				size, _, e := receiver.ReadFromUDP(buf)
				if e != nil {
					t.Fatal(e)
				}
				var packet rtp.Packet
				if e = packet.Unmarshal(buf[:size]); e != nil {
					t.Fatal(e)
				}
				if n == 0 && time.Since(origin) < 60*time.Millisecond {
					t.Fatal("reserve ignored")
				}
				if n > 0 && packet.Timestamp-previous != 160 {
					t.Fatalf("RTP discontinuity: %d", packet.Timestamp-previous)
				}
				previous = packet.Timestamp
				received = append(received, packet.Payload...)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			reference := newAgentMediaConverter(s, 8000, false).output(pcm16ToBytes(input))
			reference = append(reference, make([]int16, 8000-len(reference))...)
			if !slices.Equal(received, encodeSIPG711(reference, codec)) {
				t.Fatal("wire speech changed or padded between chunks")
			}
			d := p.agentPlayoutSnapshot()
			if d.PaddingMS >= 20 || d.Starts != 1 {
				t.Fatal(d)
			}
		})
	}
}

func TestAgentAudioSIPCancelDuringReservePreservesNextReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	s := continuousAgentFixture()
	s.profile.SendAheadMS = 200
	progress := make(chan twilioPlaybackProgress, 10)
	media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: "PCMU", PacketSamples: 160}}
	p := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, s.policy(), func(v twilioPlaybackProgress) error { progress <- v; return nil })
	defer func() { cancel(); <-p.done }()
	for _, id := range []string{"old", "next"} {
		if _, err = p.enqueue([]sipRTPOutboundPacket{{pcm: make([]int16, 200), itemID: id, generation: 1, audioEndMS: 25}}); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Now()
	r, err := p.cancelPlayback(ctx, realtimeBridgeControl{ItemID: "old", Generation: 1})
	if err != nil || r.LocalMS != 40 {
		t.Fatalf("cancel failed: %+v %v", r, err)
	}
	if time.Since(before) > 100*time.Millisecond {
		t.Fatal("cancel waited for reserve")
	}
	if _, err = p.enqueue([]sipRTPOutboundPacket{{pcm: make([]int16, 160), itemID: "old", generation: 1, audioEndMS: 45}}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		select {
		case v := <-progress:
			if v.ItemID != "next" {
				t.Fatal("cancelled reply revived", v)
			}
		case <-ctx.Done():
			t.Fatal("next reply stalled")
		}
	}
	if p.agentPlayoutSnapshot().Starts != 1 {
		t.Fatal("scoped cancellation restarted retained reply reserve")
	}
}
