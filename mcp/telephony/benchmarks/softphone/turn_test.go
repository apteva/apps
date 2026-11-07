package softphonebench

import (
	"bytes"
	"math/rand/v2"
	"net"
	"testing"
	"time"
)

func TestUDPRandomLossAndBurstAreSeededAndRecover(t *testing.T) {
	now := time.Unix(100, 0)
	a := datagramSchedule{link: Link{Kbps: 10000, UDPLossProbability: .1}, rng: rand.New(rand.NewPCG(42, 43))}
	b := datagramSchedule{link: a.link, rng: rand.New(rand.NewPCG(42, 43))}
	loss := 0
	for i := 0; i < 1000; i++ {
		at := now.Add(time.Duration(i) * 20 * time.Millisecond)
		_, x := a.due(at, 100)
		_, y := b.due(at, 100)
		if x != y {
			t.Fatal("loss not seeded")
		}
		if !x {
			loss++
		}
	}
	if loss < 60 || loss > 140 {
		t.Fatal("incorrect loss rate", loss)
	}
	a = datagramSchedule{epoch: now, link: Link{Kbps: 10000, UDPBurstAtMS: 1000, UDPBurstMS: 120}, rng: rand.New(rand.NewPCG(1, 2))}
	for _, test := range []struct {
		ms       int
		accepted bool
	}{{980, true}, {1000, false}, {1100, false}, {1120, true}} {
		if _, ok := a.due(now.Add(time.Duration(test.ms)*time.Millisecond), 100); ok != test.accepted {
			t.Fatal(test)
		}
	}
}

func TestDatagramScheduleCountsOverheadAndBoundsQueue(t *testing.T) {
	now := time.Unix(100, 0)
	s := datagramSchedule{link: Link{Kbps: 64}, rng: rand.New(rand.NewPCG(1, 2))}
	due, ok := s.due(now, 1000)
	if !ok || due.Sub(now) != 128500*time.Microsecond {
		t.Fatalf("IPv4/UDP serialization missing: %v %v", due.Sub(now), ok)
	}
	free := s.free
	if _, ok = s.due(now, 1000); ok || !s.free.Equal(free) {
		t.Fatal("excess queued datagram admitted or consumed capacity")
	}
	if _, ok = s.due(now.Add(130*time.Millisecond), 1000); !ok {
		t.Fatal("shaper did not recover after congestion")
	}
}
func TestShapedUDPCountsDropsAndPreservesBothDirections(t *testing.T) {
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := newShapedPacketConn(socket, Link{Kbps: 64}, Link{Kbps: 64}, 42)
	defer c.Close()
	client, err := net.Dial("udp4", socket.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))
	c.mu.Lock()
	c.epoch = time.Now()
	c.mu.Unlock()
	payload := bytes.Repeat([]byte{12}, 1000)
	start := time.Now()
	for range 10 {
		if _, err = client.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	ready := make(chan datagram, 1)
	go func() {
		buf := make([]byte, 2000)
		n, a, e := c.ReadFrom(buf)
		if e == nil {
			ready <- datagram{data: buf[:n], addr: a}
		}
	}()
	var packet datagram
	select {
	case packet = <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("UDP ingress stalled")
	}
	if time.Since(start) < 120*time.Millisecond || !bytes.Equal(packet.data, payload) {
		t.Fatal("ingress bypassed budget or corrupted bytes")
	}
	for range 10 {
		if _, err = c.WriteTo(payload, packet.addr); err != nil {
			t.Fatal(err)
		}
	}
	start = time.Now()
	buf := make([]byte, 2000)
	n, err := client.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], payload) || time.Since(start) < 120*time.Millisecond {
		t.Fatalf("egress bypass/corruption: %v", err)
	}
	// Let the delivery counter complete after the operating system wakes client.
	time.Sleep(10 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, stats := range []DatagramStats{c.upStats, c.downStats} {
		if stats.Packets != 10 || stats.DroppedPackets != 9 || stats.DeliveredPackets != 1 || stats.DeliveredWireBytes != 1028 || stats.WireBytes != 10280 || stats.MaxDelayMS > 200 {
			t.Fatalf("bad UDP loss accounting: %+v", stats)
		}
	}
}

func TestSharedBudgetCannotGiveUDPAndTCPSeparateCapacity(t *testing.T) {
	now := time.Unix(100, 0)
	b := newWireBudgets(64, 64)
	b.Arm(now)
	tcp, ok := b.Up.reserve(now, 800, 0)
	if !ok || tcp.Sub(now) != 100*time.Millisecond {
		t.Fatal("TCP budget not charged")
	}
	s := datagramSchedule{link: Link{Kbps: 64}, rng: rand.New(rand.NewPCG(1, 2)), budget: b.Up}
	udp, ok := s.due(now, 772) // plus 28 bytes UDP/IP
	if !ok || udp.Sub(now) != 200*time.Millisecond {
		t.Fatalf("UDP bypassed TCP capacity: %v %v", udp.Sub(now), ok)
	}
	if _, ok = s.due(now, 772); ok {
		t.Fatal("shared congestion did not drop excess UDP")
	}
	other, ok := b.Down.reserve(now, 800, 200*time.Millisecond)
	if !ok || other.Sub(now) != 100*time.Millisecond {
		t.Fatal("directions incorrectly share capacity")
	}
	if b.Up.stats.ChargedWireBytes != 1600 || b.Down.stats.ChargedWireBytes != 800 {
		t.Fatal("dropped packets charged or accepted bytes omitted")
	}
}
