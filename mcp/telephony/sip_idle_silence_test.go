package main

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// Two Telephony legs bridged to each other each wait for the other to speak.
// Without keepalive RTP neither sends a packet and both time out after 30 s,
// so an idle pacer must stream encoded silence once nothing is queued.
func TestSIPIdlePacerStreamsSilenceKeepalive(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		t.Run(codec, func(t *testing.T) {
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
			media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
			started := time.Now()
			p := newSIPRTPPacer(ctx, media, &sipPlaybackState{}, nil)
			defer func() { cancel(); <-p.done }()

			reference := &sipRTPMedia{offer: media.offer}
			reference.initCodec()
			silence := reference.encodeAudio(make([]int16, media.sampleRate()/50))
			var first time.Time
			var previous rtp.Packet
			for i := 0; i < 15; i++ {
				_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
				buf := make([]byte, 2048)
				n, _, err := receiver.ReadFromUDP(buf)
				if err != nil {
					t.Fatalf("packet %d: idle pacer sent no keepalive: %v", i, err)
				}
				var packet rtp.Packet
				if err := packet.Unmarshal(buf[:n]); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = time.Now()
					if wait := first.Sub(started); wait < 150*time.Millisecond || wait > 600*time.Millisecond {
						t.Fatalf("first keepalive after %s, want about %s", wait, sipRTPIdleSilenceAfter)
					}
				} else {
					// A late ticker may skip a slot; silence then simply resumes on the
					// wall clock. Sequence numbers stay contiguous.
					if step := packet.Timestamp - previous.Timestamp; packet.SequenceNumber != previous.SequenceNumber+1 || step == 0 || step%160 != 0 || step > 160*5 {
						t.Fatalf("keepalive not contiguous: seq %d→%d ts %d→%d", previous.SequenceNumber, packet.SequenceNumber, previous.Timestamp, packet.Timestamp)
					}
				}
				if codec != "G722" && !bytes.Equal(packet.Payload, silence) {
					t.Fatal("keepalive payload is not encoded silence")
				}
				previous = packet
			}
			if elapsed := time.Since(first); elapsed > 600*time.Millisecond {
				t.Fatalf("14 keepalive packets took %s; want a 20 ms cadence", elapsed)
			}
			// The counter advances just after each write; allow it to catch up.
			for deadline := time.Now().Add(200 * time.Millisecond); p.idleSilencePackets.Load() < 15 && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			if p.idleSilencePackets.Load() < 15 {
				t.Fatalf("keepalive count=%d", p.idleSilencePackets.Load())
			}
			if sent := p.diagnostics.snapshot().SentMS; sent != 0 {
				t.Fatalf("keepalive counted as sent audio: %v ms", sent)
			}
		})
	}
}

func TestSIPIdleKeepaliveRespectsRemoteSendonlyAndInactive(t *testing.T) {
	for _, mode := range []int32{1, 3} {
		ctx, cancel := context.WithCancel(context.Background())
		sender, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		receiver, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: "PCMU", PacketSamples: 160}}
		media.remoteMode.Store(mode)
		p := newSIPRTPPacer(ctx, media, &sipPlaybackState{}, nil)
		_ = receiver.SetReadDeadline(time.Now().Add(600 * time.Millisecond))
		if _, _, err := receiver.ReadFromUDP(make([]byte, 2048)); err == nil {
			t.Fatalf("remote mode %d received keepalive RTP", mode)
		}
		cancel()
		<-p.done
		sender.Close()
		receiver.Close()
	}
}
