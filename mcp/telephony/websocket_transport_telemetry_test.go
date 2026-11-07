package main

import (
	"bytes"
	"github.com/gobwas/ws"
	"net"
	"sync"
	"testing"
	"time"
)

type deadlineRecordingConn struct {
	net.Conn
	mu        sync.Mutex
	deadline  time.Time
	remaining time.Duration
}

func (c *deadlineRecordingConn) SetWriteDeadline(at time.Time) error {
	c.mu.Lock()
	c.deadline = at
	c.remaining = time.Until(at)
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(at)
}
func TestLiveWriteDeadlineIndependentOfQueueAndSourceAge(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	c := &deadlineRecordingConn{Conn: server}
	p := idleAudioWriter()
	p.conn = c
	p.state = ws.StateServerSide
	pcm := bytes.Repeat([]byte{3, 0}, 480)
	payload := encodeSourceAudio(pcm, sourceMedia("edge", 1000, 42), mediaClockMS()-280, mediaClockMS(), 1)
	p.audioBytes = 960
	p.audio <- websocketWriteRequest{op: ws.OpBinary, payload: payload, pcmBytes: 960, enqueued: time.Now().Add(-200 * time.Millisecond), timeout: time.Millisecond}
	go p.run()
	defer p.Stop()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	f, err := ws.ReadFrame(client)
	if err != nil || !bytes.Equal(f.Payload[64:], pcm) {
		t.Fatalf("live audio changed: %v", err)
	}
	c.mu.Lock()
	remaining := c.remaining
	c.mu.Unlock()
	if remaining < liveAudioWriteTimeout-10*time.Millisecond || remaining > liveAudioWriteTimeout {
		t.Fatalf("deadline shortened by frame age: %v", remaining)
	}
}
func TestExpiredQueueDiagnosticsDoNotAttemptSocketWrite(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	p := idleAudioWriter()
	p.conn = server
	p.state = ws.StateServerSide
	p.audioBytes = 960
	p.audio <- websocketWriteRequest{op: ws.OpBinary, payload: make([]byte, 960), pcmBytes: 960, enqueued: time.Now().Add(-time.Second)}
	go p.run()
	defer p.Stop()
	until := time.Now().Add(time.Second)
	for p.audioSnapshot().StaleBytes == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	s := p.audioSnapshot()
	if s.StaleBytes != 960 || s.Transport.Writes != 0 || s.Transport.WriteTimeouts != 0 || len(s.DropEvents) != 1 {
		t.Fatalf("stale audio became socket failure: %+v", s)
	}
	e := s.DropEvents[0]
	if e.At == "" || e.Reason != "queue_expired" || e.QueueAgeMS < 1000 || e.DeadlineMS != 0 || e.PCMBytes != 960 {
		t.Fatalf("missing timestamped drop: %+v", e)
	}
	s.DropEvents[0].Reason = "mutated"
	if p.audioSnapshot().DropEvents[0].Reason != "queue_expired" {
		t.Fatal("snapshot aliases writer diagnostics")
	}
}
func TestGracefulCleanupInterruptsActiveWriteAndConcurrentClosers(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	started := make(chan struct{})
	p := newWebSocketWriterPump(&signaledWriteConn{Conn: server, started: started}, ws.StateServerSide)
	result := make(chan error, 1)
	go func() { result <- p.Write(ws.OpText, []byte("blocked control")) }()
	<-started
	closer := newGracefulWebSocket(server, p)
	begin := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); closer.Close(ws.StatusNormalClosure, "finished") }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("cleanup waited behind five-second write")
	}
	if time.Since(begin) > time.Second {
		t.Fatal("forced socket closure was not bounded")
	}
	if err := <-result; err == nil {
		t.Fatal("blocked writer did not stop")
	}
	s := p.audioSnapshot().Transport
	forcedEvents := 0
	for _, e := range s.Events {
		if e.Reason == "forced_close" {
			forcedEvents++
		}
	}
	if s.WriteErrors != 1 || s.CleanupWriteErrors != 0 || s.ForcedCloses != 1 || s.LastCloseAt == "" || s.CloseMS < 450 || forcedEvents != 1 {
		t.Fatalf("missing forced cleanup evidence: %+v", s)
	}
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("writer remained alive")
	}
}
func TestWriteEvidenceAndReconnectMergeAreBounded(t *testing.T) {
	p := idleAudioWriter()
	p.audioMu.Lock()
	for i := 0; i < 100; i++ {
		p.recordWriteEventLocked(websocketWriteRequest{pcmBytes: 960, enqueued: time.Now()}, "queue_expired", 0, 0, 0)
	}
	p.audioMu.Unlock()
	s := mergeLiveAudioSnapshots(p.audioSnapshot(), p.audioSnapshot())
	if len(s.DropEvents) != maxWriteDiagnosticEvents || len(s.Transport.Events) != maxWriteDiagnosticEvents {
		t.Fatal("unbounded reconnect evidence")
	}
}

func TestProtocolCloseReplyCannotWaitBehindBlockedData(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	started := make(chan struct{})
	p := newWebSocketWriterPump(&signaledWriteConn{Conn: server, started: started}, ws.StateServerSide)
	go p.Write(ws.OpText, []byte("blocked"))
	<-started
	done := make(chan error, 1)
	go func() { done <- closeWebSocketProtocolError(p, "fixture invalid control frame") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("protocol error missing")
		}
	case <-time.After(time.Second):
		t.Fatal("protocol close waited for ordinary data deadline")
	}
	// Timer bookkeeping can finish just after the socket interruption.
	until := time.Now().Add(time.Second)
	for p.audioSnapshot().Transport.ForcedCloses == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if p.audioSnapshot().Transport.ForcedCloses != 1 {
		t.Fatal("forced protocol cleanup not recorded")
	}
}

func TestExpiredSourceEvidenceRetainsOriginalTiming(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	p := newWebSocketWriterPump(server, ws.StateServerSide)
	defer p.Stop()
	p.setDiagnosticID("generation:core")
	p.queueAudio(encodeSourceAudio(make([]byte, 960), sourceMedia("edge", 600, 73), mediaClockMS()-1000, mediaClockMS(), 1), 64)
	until := time.Now().Add(time.Second)
	for p.audioSnapshot().SourceStaleBytes == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	s := p.audioSnapshot()
	if s.SourceStaleBytes != 960 || s.Transport.Writes != 0 || len(s.DropEvents) != 1 {
		t.Fatalf("bad source accounting: %+v", s)
	}
	e := s.DropEvents[0]
	if e.Reason != "source_expired" || e.At == "" || e.ConnectionID != "generation:core" || e.SourceAgeMS < 1000 || e.SourceSequence != 73 || e.SourceTimestampMS != 600 || e.DeadlineMS != 0 {
		t.Fatalf("source evidence missing: %+v", e)
	}
}
