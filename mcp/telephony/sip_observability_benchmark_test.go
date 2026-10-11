package main

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

// The baseline duplicates only the pre-observation packet decoder, in test
// code. Production has no switch that can disable security or stale protection.
func sipTelemetryBaselineDecode(m *sipRTPMedia, raw []byte) (*rtp.Packet, error) {
	if m.security != nil {
		var err error
		raw, err = m.security.RemoteContext.DecryptRTP(nil, raw, nil)
		if err != nil {
			return nil, err
		}
	}
	var p rtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return nil, err
	}
	return &p, nil
}
func BenchmarkSIPTelemetryPacketPipeline(b *testing.B) {
	for _, secure := range []bool{false, true} {
		for _, calls := range []int{1, 10, 50, 100} {
			for _, observe := range []bool{false, true} {
				protocol := "RTP"
				if secure {
					protocol = "SRTP"
				}
				b.Run(fmt.Sprintf("%s/%d_calls/observe_%t", protocol, calls, observe), func(b *testing.B) {
					ms := make([]*sipRTPMedia, calls)
					senders := make([]*srtp.Context, calls)
					key := bytes.Repeat([]byte{0x42}, 30)
					for i := range ms {
						ms[i] = &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU", PayloadType: 0}}
						if secure {
							rx, err := srtp.CreateContext(key[:16], key[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
							if err != nil {
								b.Fatal(err)
							}
							senders[i], err = srtp.CreateContext(key[:16], key[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
							if err != nil {
								b.Fatal(err)
							}
							ms[i].security = &sipMediaSecurity{RemoteContext: rx}
						}
					}
					stop := make(chan struct{})
					var wg sync.WaitGroup
					if observe {
						wg.Add(1)
						go func() {
							defer wg.Done()
							ticker := time.NewTicker(time.Millisecond)
							defer ticker.Stop()
							for {
								select {
								case now := <-ticker.C:
									for _, m := range ms {
										_ = m.observations.snapshot(now)
									}
								case <-stop:
									return
								}
							}
						}()
					}
					packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SSRC: 10}, Payload: bytes.Repeat([]byte{0xff}, 160)}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						slot := i % calls
						seq := i / calls
						packet.SequenceNumber = uint16(seq)
						packet.Timestamp = uint32(seq * 160)
						raw, _ := packet.Marshal()
						if secure {
							var err error
							raw, err = senders[slot].EncryptRTP(nil, raw, nil)
							if err != nil {
								b.Fatal(err)
							}
						}
						var p *rtp.Packet
						var err error
						if observe {
							p, err = ms[slot].decodePacket(raw)
						} else {
							p, err = sipTelemetryBaselineDecode(ms[slot], raw)
						}
						if err != nil {
							b.Fatal(err)
						}
						pcm := ms[slot].decodeAudio(p.Payload)
						if len(pcm) != 160 {
							b.Fatal("changed audio output")
						}
					}
					b.StopTimer()
					close(stop)
					wg.Wait()
					b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)*50*float64(calls)/1e7, "CPU-percent")
				})
			}
		}
	}
}
func BenchmarkSIPTelemetryBusySampler(b *testing.B) {
	var o sipRTPObservations
	o.timingMu.Lock()
	defer o.timingMu.Unlock()
	p := rtp.Packet{Header: rtp.Header{SSRC: 1}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o.received(&p, 172, time.Now(), true)
	}
}
func BenchmarkCarrierTelemetryFullQueue(b *testing.B) {
	var c carrierTelemetryCollector
	c.init()
	r := carrierTelemetryRecord{ID: "id", At: "2026-10-11T10:00:00Z", CallID: "call", Kind: "event"}
	for i := 0; i < carrierEvidenceLimit; i++ {
		c.enqueue(r)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.enqueue(r)
	}
}
func TestSIPTelemetryCollectionCost(t *testing.T) {
	if os.Getenv("TELEPHONY_TELEMETRY_BENCH") != "1" {
		t.Skip("explicit telemetry persistence benchmark")
	}
	for _, calls := range []int{1, 10, 50, 100} {
		t.Run(fmt.Sprint(calls), func(t *testing.T) {
			a, _ := withTelephonyTestContext(t, &answerPlatform{})
			for i := 0; i < calls; i++ {
				row := recoveryCall(t, a, fmt.Sprintf("perf-%d", i), "telnyx")
				d := a.newSIPDiagnostics(row, fmt.Sprint(i))
				m := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU"}}
				jitter := newRTPJitterBuffer()
				for k := 0; k < 6; k++ {
					jitter.push(uint16(k), bytes.Repeat([]byte{0xff}, 160))
				}
				m.jitterTelemetry.Store(jitter)
				m.pacer.Store(&sipRTPPacer{})
				m.coreForwardTelemetry.Store(&websocketWriterPump{})
				d.attach(m, fmt.Sprint(i))
			}
			flushCarrierTest(t, a)
			now := time.Unix(time.Now().Unix()/5*5, 0)
			result := testing.Benchmark(func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					a.carrierTelemetry.sample(now.Add(time.Duration(i) * time.Second))
					if _, err := a.carrierTelemetry.flush(t.Context(), a.db(), now.Add(time.Duration(i)*time.Second)); err != nil {
						b.Fatal(err)
					}
					if err := a.db().refreshAudioDashboard(t.Context()); err != nil {
						b.Fatal(err)
					}
				}
			})
			t.Logf("%d calls: complete 1s sample+SQLite+dashboard batch %s; %s; one-core duty %.3f%%", calls, result.String(), result.MemString(), float64(result.NsPerOp())/1e7)
		})
	}
}

func BenchmarkSIPTelemetrySendBookkeeping(b *testing.B) {
	var o sipRTPObservations
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		o.sentPacket(172, time.Now())
		atomicMax(&o.maxSend, int64(time.Since(start)))
	}
}
