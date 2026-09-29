package softphonebench

import (
	"bytes"
	"io"
	"math/rand"
	"net"
	"testing"
	"time"
)

func TestBandwidthAndLatencyArePipelined(t *testing.T) {
	now := time.Unix(100, 0)
	s := Schedule{Link: Link{Kbps: 1000, LatencyMS: 20}, RNG: rand.New(rand.NewSource(1))}
	a, _ := s.Due(now, 1000)
	b, _ := s.Due(now, 1000)
	if a.Sub(now) != 28*time.Millisecond || b.Sub(now) != 36*time.Millisecond {
		t.Fatalf("latency incorrectly compounds: %v %v", a.Sub(now), b.Sub(now))
	}
}
func TestRecoveryAndOutageKeepBytesOrdered(t *testing.T) {
	now := time.Unix(100, 0)
	s := Schedule{Link: Link{Kbps: 1000, RecoveryProbability: 1, RecoveryMS: 180}, RNG: rand.New(rand.NewSource(1))}
	a, recovery := s.Due(now, 1000)
	b, _ := s.Due(now, 1000)
	if !recovery || b.Before(a) || a.Sub(now) < 180*time.Millisecond {
		t.Fatal("missing ordered retransmission stall")
	}
	s = Schedule{Epoch: now, Link: Link{Kbps: 1000, OutageAtMS: 100, OutageMS: 2000}, RNG: rand.New(rand.NewSource(1))}
	at, _ := s.Due(now.Add(120*time.Millisecond), 1000)
	if at.Sub(now) != 2100*time.Millisecond {
		t.Fatal(at.Sub(now))
	}
}
func TestProxyPreservesRealTCPBytesBothDirections(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := listener.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c)
	}()
	profile := Profile{Up: Link{Kbps: 1000, LatencyMS: 5, RecoveryProbability: .1, RecoveryMS: 10}, Down: Link{Kbps: 1000, LatencyMS: 5}}
	proxy, err := NewProxy(listener.Addr().String(), profile, 42)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	c, err := net.Dial("tcp", proxy.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	payload := bytes.Repeat([]byte{1, 2, 3, 4, 5}, 4000)
	if _, err = c.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("proxy lost/reordered bytes")
	}
	c.Close()
	proxy.Close()
	<-done
}
func TestMarkersDecodeAfterGainAndPacketBoundaries(t *testing.T) {
	for _, rate := range []int{16000, 24000, 48000} {
		d := NewDecoder(rate)
		for frame := 0; frame < 100; frame++ {
			pcm := make([]int16, rate/50)
			for i := range pcm {
				pcm[i] = Sample(frame*len(pcm)+i, rate, 128) / 2
			}
			d.Push(pcm, float64((frame+1)*20))
		}
		if len(d.Markers) != 4 {
			t.Fatalf("rate %d markers: %+v", rate, d.Markers)
		}
		for i, m := range d.Markers {
			if m.ID != i+128 || m.AtMS != float64(i*500) {
				t.Fatalf("marker: %+v", m)
			}
		}
	}
}
func TestTruncatedOrMissingSymbolCannotLookLikeAnIntactMarker(t *testing.T) {
	d := NewDecoder(16000)
	for frame := 0; frame < 25; frame++ {
		pcm := make([]int16, 320)
		for i := range pcm {
			sample := frame*320 + i
			if sample < 800 || sample >= 1280 {
				pcm[i] = Sample(sample, 16000, 1)
			}
		}
		d.Push(pcm, float64((frame+1)*20))
	}
	if len(d.Markers) != 0 {
		t.Fatalf("corrupt marker accepted: %+v", d.Markers)
	}
}
