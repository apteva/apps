package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
)

// A slow process-wide metrics reader must not own a call's routing lock.
// Hold the real cache lock to model another sampler being descheduled, and
// wait until the diagnostic goroutine reaches it before forwarding audio.
func TestProcessMetricsContentionDoesNotBlockCallAudio(t *testing.T) {
	browser := &websocketWriterPump{audio: make(chan websocketWriteRequest, 8)}
	peer := &websocketWriterPump{audio: make(chan websocketWriteRequest, 8)}
	hub := &softphoneHub{browser: browser, peer: peer}
	mediaProcessCache.Lock()
	snapshot := make(chan serverAudioDiagnostics, 1)
	go func() { snapshot <- hub.serverAudioSnapshot() }()
	defer func() {
		mediaProcessCache.Unlock()
		select {
		case s := <-snapshot:
			if s.Process.SampledAt == "" {
				t.Error("process metrics missing after contention clears")
			}
		case <-time.After(2 * time.Second):
			t.Error("diagnostic snapshot did not finish")
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stacks := make([]byte, 1<<20)
		n := runtime.Stack(stacks, true)
		waiting := false
		for _, stack := range strings.Split(string(stacks[:n]), "\n\n") {
			if strings.Contains(stack, ".sampleMediaProcess(") && strings.Contains(stack, "[sync.Mutex.Lock]") {
				waiting = true
				break
			}
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot never reached the contended process sampler")
		}
		time.Sleep(time.Millisecond)
	}
	frame := bytes.Repeat([]byte{0x12, 0x34}, 480)
	forwarded := make(chan struct{})
	go func() {
		hub.toBrowser(ws.OpBinary, frame)
		hub.toPeer(ws.OpBinary, frame)
		close(forwarded)
	}()
	select {
	case <-forwarded:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("process metrics blocked two-way audio routing")
	}
	for _, writer := range []*websocketWriterPump{browser, peer} {
		select {
		case request := <-writer.audio:
			if !bytes.Equal(request.payload, frame) {
				t.Fatal("audio samples changed")
			}
		default:
			t.Fatal("audio was not queued")
		}
	}
}
