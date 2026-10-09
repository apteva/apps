package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
)

type timeoutOnceConn struct {
	net.Conn
	mu        sync.Mutex
	first     bool
	partial   int
	err       error
	attempted chan struct{}
}

func (c *timeoutOnceConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	first := !c.first
	c.first = true
	c.mu.Unlock()
	if first {
		defer close(c.attempted)
		if c.partial > 0 {
			n, err := c.Conn.Write(data[:min(c.partial, len(data))])
			if err != nil {
				return n, err
			}
			return n, c.err
		}
		return 0, c.err
	}
	return c.Conn.Write(data)
}

func TestLiveAudioUnsentTimeoutDropsOnlyExpiredFrameAndRecovers(t *testing.T) {
	for _, state := range []ws.State{ws.StateServerSide, ws.StateClientSide} {
		t.Run(strconv.Itoa(int(state)), func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			defer server.Close()
			conn := &timeoutOnceConn{Conn: server, err: os.ErrDeadlineExceeded, attempted: make(chan struct{})}
			writer := newWebSocketWriterPump(conn, state)
			defer writer.Stop()
			writer.QueueAudio(bytes.Repeat([]byte{1}, 960))
			<-conn.attempted
			fresh := bytes.Repeat([]byte{2}, 960)
			writer.QueueAudio(fresh)
			client.SetReadDeadline(time.Now().Add(time.Second))
			frame, err := ws.ReadFrame(client)
			if err != nil {
				t.Fatalf("fresh audio did not recover: %v", err)
			}
			if frame.Header.Masked {
				ws.Cipher(frame.Payload, frame.Header.Mask, 0)
			}
			if !bytes.Equal(frame.Payload, fresh) {
				t.Fatal("expired speech was replayed or fresh speech changed")
			}
			// A control barrier also verifies accounting and fresh deadlines.
			done := make(chan error, 1)
			go func() { done <- writer.Write(ws.OpText, []byte("barrier")) }()
			if _, err = ws.ReadFrame(client); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			s := writer.audioSnapshot()
			if s.StaleBytes != 960 || s.SentBytes != 960 || s.FailedBytes != 0 || s.WriteErrors != 1 || s.WriteTimeoutDrops != 1 || s.EnqueuedBytes != s.StaleBytes+s.SentBytes {
				t.Fatalf("loss accounting: %+v", s)
			}
			merged := mergeLiveAudioSnapshots(s, s)
			if merged.WriteTimeoutDrops != 2 || merged.StaleBytes != 1920 {
				t.Fatal("reconnect aggregation lost recovery accounting")
			}
		})
	}
}

func TestLiveAudioPartialTimeoutAndNonTimeoutRemainFatal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		partial int
		err     error
	}{
		{"header_partial", 1, os.ErrDeadlineExceeded},
		{"payload_partial", 8, os.ErrDeadlineExceeded},
		{"connection_failure", 0, net.ErrClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			conn := &timeoutOnceConn{Conn: server, partial: tc.partial, err: tc.err, attempted: make(chan struct{})}
			writer := newWebSocketWriterPump(conn, ws.StateServerSide)
			defer writer.Stop()
			if tc.partial > 0 {
				go io.Copy(io.Discard, client)
			}
			writer.QueueAudio(make([]byte, 960))
			select {
			case <-writer.done:
			case <-time.After(time.Second):
				t.Fatal("unsafe transport was left open")
			}
			s := writer.audioSnapshot()
			if s.FailedBytes != 960 || s.StaleBytes != 0 || s.WriteTimeoutDrops != 0 || !errors.Is(writer.terminalError(), tc.err) {
				t.Fatalf("fatal error changed: %+v %v", s, writer.terminalError())
			}
		})
	}
}

func TestControlTimeoutWithoutBytesRemainsFatal(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	conn := &timeoutOnceConn{Conn: server, err: os.ErrDeadlineExceeded, attempted: make(chan struct{})}
	writer := newWebSocketWriterPump(conn, ws.StateServerSide)
	defer writer.Stop()
	if err := writer.Write(ws.OpText, []byte("control")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("control failure hidden: %v", err)
	}
	select {
	case <-writer.done:
	case <-time.After(time.Second):
		t.Fatal("failed control left socket open")
	}
}
