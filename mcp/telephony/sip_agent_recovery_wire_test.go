package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/pion/rtp"
	"net"
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
				sequenceLoss := 0
				var payload []byte
				for i := 0; i < chunks*5; i++ {
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
					if i > 0 {
						maxGap = max(maxGap, now.Sub(previous))
						maxTS = max(maxTS, packet.Timestamp-prevTS)
						sequenceLoss += int(uint16(packet.SequenceNumber-prevSeq)) - 1
					}
					previous, prevSeq, prevTS = now, packet.SequenceNumber, packet.Timestamp
					payload = append(payload, packet.Payload...)
				}
				if e := <-sent; e != nil {
					t.Fatal(e)
				}
				reference := (&sipRTPMedia{offer: sipMediaOffer{Codec: codec}}).encodeAudio(input)
				if !bytes.Equal(payload, reference) || sequenceLoss != 0 {
					t.Fatal("wire bytes changed or lost")
				}
				result := map[string]any{"pattern": pattern, "codec": codec, "input_ms": 2000, "packets_received": chunks * 5, "sequence_loss": sequenceLoss, "encoded_bytes_preserved": true, "max_receive_gap_ms": float64(maxGap) / float64(time.Millisecond), "max_rtp_timestamp_step_ms": float64(maxTS) / 8, "local_max_send_gap_ms": float64(media.observations.maxPacingGap.Load()) / 1e6, "local_max_write_ms": float64(media.observations.maxSend.Load()) / 1e6, "playout": p.agentPlayoutSnapshot()}
				raw, _ := json.Marshal(result)
				t.Log(string(raw))
				bound := 100 * time.Millisecond
				if pattern == "late_320ms_burst" {
					bound = 320 * time.Millisecond
				}
				if maxGap > bound {
					t.Fatalf("receiver gap %s exceeds %s", maxGap, bound)
				}
			})
		}
	}
}
