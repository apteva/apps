package main

import (
	"bytes"

	"context"
	"encoding/json"
	"github.com/gotranspile/g722"
	"github.com/pion/rtp"
	"net"
	"sync"
	"testing"
	"time"
)

func TestAgentSIPRecoveryWire(t *testing.T) {
	for _, codec := range []string{"G722", "PCMA"} {
		for _, pattern := range []string{"regular", "late_320ms_burst", "slow_110ms_chunks"} {
			t.Run(codec+"/"+pattern, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
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
				payloadType := uint8(9)
				if codec == "PCMA" {
					payloadType = 8
				}
				media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: codec, PayloadType: payloadType, PacketSamples: 160}}
				session := continuousAgentFixture()
				p := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, session.policy(), nil)
				defer func() { cancel(); <-p.done }()
				// Keepalive silence fills gaps of 200 ms or more; it is not speech.
				var keepaliveMu sync.Mutex
				keepaliveSeqs := map[uint16]bool{}
				observe := func(seq uint16) { keepaliveMu.Lock(); keepaliveSeqs[seq] = true; keepaliveMu.Unlock() }
				p.keepaliveObserver.Store(&observe)
				const chunks = 20
				rate := media.sampleRate()
				size := rate / 10
				input := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, rate)
				for len(input) < chunks*size {
					input = append(input, input...)
				}
				input = input[:chunks*size]
				sent := make(chan error, 1)
				origin := time.Now()
				go func() {
					for i := 0; i < chunks; i++ {
						at := time.Duration(i) * 100 * time.Millisecond
						if pattern == "late_320ms_burst" && i >= 5 && i <= 8 {
							at = max(at, 820*time.Millisecond)
						}
						if pattern == "slow_110ms_chunks" {
							at = time.Duration(i) * 110 * time.Millisecond
						}
						time.Sleep(time.Until(origin.Add(at)))
						_, e := p.enqueue([]sipRTPOutboundPacket{{pcm: input[i*size : (i+1)*size], itemID: "continuous", generation: 1, audioEndMS: (i + 1) * 100}})
						if e != nil {
							sent <- e
							return
						}
					}
					sent <- nil
				}()
				var previous time.Time
				var prevSeq uint16
				var prevTS uint32
				var maxGap time.Duration
				maxTS := uint32(0)
				var resumed []int
				sequenceLoss := 0
				var payload []byte
				// keepaliveBefore[i] counts keepalive packets sent just before speech
				// packet i; the reference encoder must advance through them too.
				keepaliveBefore := map[int]int{}
				pendingKeepalive, received := 0, 0
				for i := 0; i < chunks*5; {
					receiver.SetReadDeadline(time.Now().Add(2 * time.Second))
					buf := make([]byte, 2048)
					n, _, e := receiver.ReadFromUDP(buf)
					if e != nil {
						t.Fatal(e)
					}
					now := time.Now()
					var packet rtp.Packet
					if e = packet.Unmarshal(buf[:n]); e != nil {
						t.Fatal(e)
					}
					if received > 0 {
						sequenceLoss += int(uint16(packet.SequenceNumber-prevSeq)) - 1
					}
					received++
					prevSeq = packet.SequenceNumber
					keepaliveMu.Lock()
					keepalive := keepaliveSeqs[packet.SequenceNumber]
					keepaliveMu.Unlock()
					if keepalive {
						if i == 0 {
							continue // before the reply: not part of the replayed input
						}
						pendingKeepalive++
						continue
					}
					if i > 0 {
						if packet.Timestamp-prevTS > 160 || pendingKeepalive > 0 {
							resumed = append(resumed, i)
						}
						maxGap = max(maxGap, now.Sub(previous))
						maxTS = max(maxTS, packet.Timestamp-prevTS)
					}
					if pendingKeepalive > 0 {
						keepaliveBefore[i] = pendingKeepalive
						pendingKeepalive = 0
					}
					previous, prevTS = now, packet.Timestamp
					payload = append(payload, packet.Payload...)
					i++
				}
				if e := <-sent; e != nil {
					t.Fatal(e)
				}
				// The first packet after an underrun gap fades in (3 ms) and the last
				// speech before a gap, or at the end, fades out (3 ms); all other
				// samples are transmitted unchanged. A timestamp step can also be
				// a scheduler slip with audio queued (no fade), so packets beside a
				// step may match either form. The encoder state follows the chosen one.
				resumedAt := map[int]bool{}
				for _, i := range resumed {
					resumedAt[i] = true
				}
				ref := &sipRTPMedia{offer: sipMediaOffer{Codec: codec}}
				ref.initCodec()
				samples, size := rate/50, len(payload)/(chunks*5)
				preserved := len(payload) == chunks*5*size
				// A near-miss starvation fades out and straight back in without a
				// timestamp step: a soft dip in place of a cut, at most once per chunk.
				endFaded, dips := false, 0
				for i := 0; preserved && i < chunks*5; i++ {
					if endFaded {
						resumedAt[i] = true
					}
					for range keepaliveBefore[i] {
						ref.encodeAudio(make([]int16, samples))
					}
					want := payload[i*size : (i+1)*size]
					var saved *g722.Encoder
					if ref.g722Encoder != nil {
						copied := *ref.g722Encoder
						saved = &copied
					}
					got := ref.encodeAudio(input[i*samples : (i+1)*samples])
					beforeGap := true
					endFaded = false
					for _, form := range [][2]bool{{true, false}, {false, true}, {true, true}} {
						if bytes.Equal(got, want) || form[0] && !resumedAt[i] || form[1] && !beforeGap {
							continue
						}
						if saved != nil {
							*ref.g722Encoder = *saved
						}
						faded := append([]int16(nil), input[i*samples:(i+1)*samples]...)
						if form[0] {
							applyAgentStartFade(faded, rate)
						}
						if form[1] {
							applyAgentEndFade(faded, rate)
						}
						got = ref.encodeAudio(faded)
						if bytes.Equal(got, want) && form[1] {
							endFaded = true
							if i+1 < chunks*5 && !resumedIndex(resumed, i+1) {
								dips++
							}
						}
					}
					preserved = bytes.Equal(got, want)
				}
				if !preserved || sequenceLoss != 0 {
					t.Fatal("wire bytes changed or lost")
				}
				if dips > chunks {
					t.Fatalf("%d near-miss dips for %d chunks", dips, chunks)
				}
				result := map[string]any{"pattern": pattern, "codec": codec, "input_ms": 2000, "packets_received": chunks * 5, "sequence_loss": sequenceLoss, "encoded_bytes_preserved": true, "max_receive_gap_ms": float64(maxGap) / float64(time.Millisecond), "max_rtp_timestamp_step_ms": float64(maxTS) / 8, "local_max_send_gap_ms": float64(media.observations.maxPacingGap.Load()) / 1e6, "local_max_write_ms": float64(media.observations.maxSend.Load()) / 1e6, "playout": p.agentPlayoutSnapshot()}
				raw, _ := json.Marshal(result)
				t.Log(string(raw))
				bound := 100 * time.Millisecond
				if pattern == "late_320ms_burst" {
					// The hold plus the bounded 60 ms recovery cushion.
					bound = 380 * time.Millisecond
				}
				if maxGap > bound {
					t.Fatalf("receiver gap %s exceeds %s", maxGap, bound)
				}
			})
		}
	}
}

func resumedIndex(resumed []int, i int) bool {
	for _, r := range resumed {
		if r == i {
			return true
		}
	}
	return false
}
