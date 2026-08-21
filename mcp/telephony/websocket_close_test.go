package main

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestGracefulWebSocketSendsCloseFrameBeforeClosingSocket(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	writer := newWebSocketWriterPump(server, ws.StateServerSide)
	closer := newGracefulWebSocket(server, writer)
	done := make(chan struct{})
	go func() {
		closer.Close(ws.StatusNormalClosure, "media complete")
		close(done)
	}()

	_ = client.SetDeadline(time.Now().Add(time.Second))
	frame, err := ws.ReadFrame(client)
	if err != nil {
		t.Fatalf("read close frame: %v", err)
	}
	code, reason := ws.ParseCloseFrameData(frame.Payload)
	if frame.Header.OpCode != ws.OpClose || code != ws.StatusNormalClosure || reason != "media complete" {
		t.Fatalf("close frame = op %d code %d reason %q", frame.Header.OpCode, code, reason)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("graceful close did not release the socket")
	}
}

func TestWebSocketControlAndDataFramesShareWriterPump(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))

	writer := newWebSocketWriterPump(server, ws.StateServerSide)
	defer writer.Stop()

	type readResult struct {
		payload []byte
		op      ws.OpCode
		err     error
	}
	inbound := make(chan readResult, 1)
	go func() {
		payload, op, err := readWebSocketData(server, ws.StateServerSide, writer)
		inbound <- readResult{payload: payload, op: op, err: err}
	}()

	const writers = 4
	const framesPerWriter = 25
	const dataFrames = writers * framesPerWriter
	received := make(chan error, 1)
	go func() {
		textFrames := 0
		pongs := 0
		for textFrames < dataFrames || pongs < 1 {
			frame, err := ws.ReadFrame(client)
			if err != nil {
				received <- err
				return
			}
			switch frame.Header.OpCode {
			case ws.OpText:
				textFrames++
			case ws.OpPong:
				if string(frame.Payload) != "probe" {
					received <- fmt.Errorf("pong payload = %q", frame.Payload)
					return
				}
				pongs++
			default:
				received <- fmt.Errorf("unexpected opcode %d", frame.Header.OpCode)
				return
			}
		}
		received <- nil
	}()

	writeErrors := make(chan error, dataFrames)
	var writes sync.WaitGroup
	for worker := 0; worker < writers; worker++ {
		writes.Add(1)
		go func(worker int) {
			defer writes.Done()
			for frame := 0; frame < framesPerWriter; frame++ {
				writeErrors <- writer.Write(ws.OpText, []byte(fmt.Sprintf("%d:%d", worker, frame)))
			}
		}(worker)
	}

	if err := wsutil.WriteClientMessage(client, ws.OpPing, []byte("probe")); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	if err := wsutil.WriteClientText(client, []byte("request")); err != nil {
		t.Fatalf("write data: %v", err)
	}

	writes.Wait()
	close(writeErrors)
	for err := range writeErrors {
		if err != nil {
			t.Fatalf("write application frame: %v", err)
		}
	}
	if err := <-received; err != nil {
		t.Fatalf("read serialized frames: %v", err)
	}
	result := <-inbound
	if result.err != nil || result.op != ws.OpText || string(result.payload) != "request" {
		t.Fatalf("read data = op %d payload %q err %v", result.op, result.payload, result.err)
	}
}

func TestWebSocketCloseStatePreservesFirstCause(t *testing.T) {
	state := &websocketCloseState{}
	leg, code, reason := state.Cause()
	if leg != mediaCloseLegLocalError || code != ws.StatusGoingAway || reason != "media bridge shutting down" {
		t.Fatalf("default close = leg %q code %d reason %q", leg, code, reason)
	}
	state.SetLeg(mediaCloseLegCarrier, ws.StatusNormalClosure, "carrier stop")
	state.SetLeg(mediaCloseLegCore, ws.StatusInternalServerError, "late writer error")
	leg, code, reason = state.Cause()
	if leg != mediaCloseLegCarrier || code != ws.StatusNormalClosure || reason != "carrier stop" {
		t.Fatalf("preserved close = leg %q code %d reason %q", leg, code, reason)
	}

	code, reason = websocketCloseDetails(nil)
	if code != ws.StatusNormalClosure || reason != "call media ended normally" {
		t.Fatalf("normal details = code %d reason %q", code, reason)
	}
}
