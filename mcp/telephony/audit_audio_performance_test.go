package main

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

func BenchmarkAuditCallListJSON(b *testing.B) {
	for _, drops := range []int{0, 100} {
		b.Run(fmt.Sprintf("100calls_%ddrops_per_direction", drops), func(b *testing.B) {
			events := make([]audioDropEvent, drops)
			for i := range events {
				events[i] = audioDropEvent{Timestamp: "2026-09-05T07:20:00.000Z", Direction: "operator_to_carrier", Reason: "websocket_backpressure", DurationMS: 20, QueueBeforeMS: 180, QueueAfterMS: 110, Sequence: uint64(i + 1)}
			}
			browser, _ := json.Marshal(browserAudioDiagnostics{DropEvents: events, AudioContextRate: 24000})
			carrier, _ := json.Marshal(carrierAudioDiagnostics{DropEvents: events, SampleRate: 16000, Provider: "telnyx"})
			rows := make([]callRow, 100)
			for i := range rows {
				rows[i] = testCall(fmt.Sprintf("audit-%d", i), "completed")
				rows[i].PeerKind = peerKindHuman
				rows[i].BrowserAudioDiagnostics = string(browser)
				rows[i].CarrierAudioDiagnostics = string(carrier)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				data, err := json.Marshal(map[string]any{"calls": callsPanelPublic(rows)})
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(len(data)), "payload-bytes")
			}
		})
	}
}

// Regression: sequence numbers count transmitted packets, not silent time.
func TestAuditRTPMustRecoverAfterDiscontinuousTransmission(t *testing.T) {
	b := newRTPJitterBuffer()
	for i := uint16(100); i < 103; i++ {
		b.push(i, make([]byte, 160))
	}
	for i := 0; i < 3; i++ {
		b.pop()
	}
	for i := 0; i < 50; i++ {
		b.pop()
	} // one second without transmitted audio
	heard := 0
	for i := uint16(103); i < 203; i++ {
		b.push(i, make([]byte, 160))
		payload, _ := b.pop()
		if len(payload) > 0 {
			heard++
		}
	}
	if heard == 0 {
		t.Fatal("all 100 resumed RTP packets discarded after a one-second transmission gap")
	}
}

func BenchmarkAuditAudioPipeline(b *testing.B) {
	for _, rate := range []int{8000, 16000} {
		for _, peer := range []string{peerKindHuman, peerKindRealtime} {
			name := peer + "/8k"
			if rate == 16000 {
				name = peer + "/16k"
			}
			b.Run(name, func(b *testing.B) {
				f := newCarrierAudioFrontend(rate)
				row := &callRow{PeerKind: peer}
				in := newPCMResampler(rate, 24000)
				out := newPCMResampler(24000, rate)
				caller := make([]int16, rate/50)
				operator := make([]int16, 480)
				for i := range caller {
					caller[i] = int16(2500*math.Sin(float64(i)*2*math.Pi*190/float64(rate)) + 1000*math.Sin(float64(i)*2*math.Pi*770/float64(rate)))
				}
				for i := range operator {
					operator[i] = int16(2500 * math.Sin(float64(i)*2*math.Pi*190/24000))
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					processed := processCarrierInput(row, f, caller)
					in.Process(processed.PCM)
					out.Process(operator)
				}
			})
		}
	}
}
