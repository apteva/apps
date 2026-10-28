package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// The renderer reader may hold a batch for the pressure grace and then write an
// overflow receipt; the staged next batch must survive that, not end the AI leg.
func TestReviewDOutputDeliverySurvivesPressureThenOverflowWrite(t *testing.T) {
	if agentOutputDeliveryTimeout <= agentPlaybackPressureGrace+agentPlaybackPressureRetry+agentCoreWriteBudget {
		t.Fatal("delivery deadline does not cover pressure grace plus receipt write", agentOutputDeliveryTimeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, p := net.Pipe()
	defer p.Close()
	go func() {
		for i := 0; i < 2; i++ {
			_ = wsutil.WriteServerMessage(p, ws.OpText, []byte(`{"type":"audio.frame","item_id":"reply","session_generation":1,"audio_end_ms":20}`))
			if wsutil.WriteServerMessage(p, ws.OpBinary, make([]byte, 960)) != nil {
				return
			}
		}
		<-ctx.Done()
	}()
	link := newAgentCoreLink(ctx, c, func(context.Context) (net.Conn, error) { return nil, io.EOF }, nil)
	defer link.Close()
	for i := 0; i < 2; i++ {
		if _, op, err := wsutil.ReadServerData(link); err != nil || op != ws.OpText {
			t.Fatal("metadata", i, err)
		}
		if _, op, err := wsutil.ReadServerData(link); err != nil || op != ws.OpBinary {
			t.Fatal("batch", i, err)
		}
		if i == 0 {
			// Full pressure grace, one retry tick and a slow overflow write.
			time.Sleep(agentPlaybackPressureGrace + agentPlaybackPressureRetry + 230*time.Millisecond)
		}
	}
	if link.ctx.Err() != nil {
		t.Fatal("backpressure tore down the Core link")
	}
}

func TestReviewDControlQueueCoalescesProgressAndKeepsReceipts(t *testing.T) {
	l := &agentCoreLink{controlReady: make(chan struct{}, 1)}
	progress := func(end int) agentLinkMessage {
		return agentLinkMessage{op: ws.OpText, data: []byte{byte(end)}, coalesce: true, progress: agentPlaybackKey{1, "a"}, progressEnd: end}
	}
	if !l.queueControl(progress(20)) || !l.queueControl(agentLinkMessage{data: []byte("cleared")}) || !l.queueControl(progress(40)) || !l.queueControl(progress(30)) {
		t.Fatal("queue refused")
	}
	m, _ := l.popControl()
	if !m.coalesce || m.progressEnd != 40 {
		t.Fatal("progress not superseded in place", m)
	}
	if m, _ = l.popControl(); string(m.data) != "cleared" {
		t.Fatal("receipt order", m)
	}
	if _, ok := l.popControl(); ok {
		t.Fatal("stale progress kept")
	}
	for i := 0; i < agentCoreControlLimit; i++ {
		if !l.queueControl(agentLinkMessage{data: []byte("receipt")}) {
			t.Fatal("receipt dropped", i)
		}
	}
	if l.queueControl(agentLinkMessage{data: []byte("over")}) {
		t.Fatal("non-coalescible control exceeded its bound")
	}
	if !l.queueControl(progress(60)) {
		t.Fatal("progress refused while receipts are full")
	}
}

// A carrier mark burst while Core reads slowly must coalesce, not end the call.
func TestReviewDProgressBurstDoesNotKillLink(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	c, p := net.Pipe()
	defer p.Close()
	var mu sync.Mutex
	var got []string
	go func() {
		for {
			data, _, err := wsutil.ReadClientData(p)
			if err != nil {
				return
			}
			mu.Lock()
			got = append(got, string(data))
			mu.Unlock()
			time.Sleep(60 * time.Millisecond)
		}
	}()
	link := newAgentCoreLink(ctx, c, func(context.Context) (net.Conn, error) { return nil, io.EOF }, nil)
	defer link.Close()
	for i := 1; i <= 80; i++ {
		raw, _ := json.Marshal(map[string]any{"type": "playback.progress", "item_id": "a", "session_generation": 1, "audio_end_ms": i * 20})
		if err := wsutil.WriteClientMessage(link, ws.OpText, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := wsutil.WriteClientMessage(link, ws.OpText, []byte(`{"type":"playback.cleared","item_id":"a","session_generation":1,"scope":"local","local_discarded_ms":0}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		all := strings.Join(got, "\n")
		n := len(got)
		mu.Unlock()
		if strings.Contains(all, `"audio_end_ms":1600`) && strings.Contains(all, "playback.cleared") {
			if n > 20 {
				t.Fatal("progress was not coalesced", n)
			}
			break
		}
		if time.Now().After(deadline) || link.ctx.Err() != nil {
			t.Fatal("receipts lost or link cancelled", link.ctx.Err(), all)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if link.ctx.Err() != nil {
		t.Fatal("progress burst tore down the Core link")
	}
}

// Audio fenced in the link's staging queue and in the renderer is reported in
// the cleared receipt for that playback.
func TestReviewDClearedReceiptIncludesStagedAndRendererDiscards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, p := net.Pipe()
	defer p.Close()
	s := continuousAgentFixture()
	go func() {
		// Three 200 ms batches; the renderer reads nothing until the interrupt
		// has fenced the playback, so at least two remain staged in the link.
		for i := 1; i <= 3; i++ {
			meta, _ := json.Marshal(map[string]any{"type": "audio.frame", "item_id": "reply", "session_generation": 1, "audio_end_ms": i * 200})
			_ = wsutil.WriteServerMessage(p, ws.OpText, meta)
			_ = wsutil.WriteServerMessage(p, ws.OpBinary, make([]byte, 9600))
		}
		_ = wsutil.WriteServerMessage(p, ws.OpText, []byte(`{"type":"interrupt","item_id":"reply","session_generation":1}`))
		<-ctx.Done()
	}()
	interrupt := func(c realtimeBridgeControl) {
		if c.Type == "interrupt" {
			s.expectClear(c)
		}
		s.cancel(c.ItemID, c.Generation)
	}
	link := newAgentCoreLink(ctx, c, func(context.Context) (net.Conn, error) { return nil, io.EOF }, interrupt, agentCoreLinkOptions{rejects: func(c realtimeBridgeControl) bool { return s.rejects(c.ItemID, c.Generation) }, discarded: s.linkDiscarded})
	defer link.Close()
	s.mu.Lock()
	s.link = link
	s.mu.Unlock()
	for !s.rejects("reply", 1) && ctx.Err() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	var control realtimeBridgeControl
	rendered := 0
	for control.Type != "interrupt" {
		data, op, err := wsutil.ReadServerData(link)
		if err != nil {
			t.Fatal(err)
		}
		if op != ws.OpText {
			continue
		}
		c, _ := parseRealtimeBridgeControl(data)
		if c.Type == "audio.frame" {
			// The renderer received this batch after the fence; it drops it.
			if s.accept(c) {
				t.Fatal("cancelled frame accepted")
			}
			rendered += 200
			continue
		}
		control = c
	}
	for link.snapshot().OutputOmittedMS+int64(rendered) < 600 && ctx.Err() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	_ = s.cleared(nil, control, agentClearReceipt{Scope: "local", LocalMS: 40})
	snap := s.snapshot()
	if len(snap.Clears) != 1 || snap.Clears[0].LocalMS != 640 {
		t.Fatalf("cleared receipt under-reports discarded audio: %+v (rendered %d)", snap.Clears, rendered)
	}
	// A late drop after the receipt is diagnostics only, not a second receipt.
	if raw := s.linkDiscarded(realtimeBridgeControl{ItemID: "reply", Generation: 1}, 20); raw != nil || s.snapshot().Clears[0].LateDiscardedMS != 20 {
		t.Fatal("late discard", string(raw))
	}
}

func TestReviewDPressureDiscardJoinsClearedReceipt(t *testing.T) {
	s := continuousAgentFixture()
	frame := realtimeBridgeControl{ItemID: "reply", Generation: 1, AudioEndMS: 120}
	if !s.accept(frame) {
		t.Fatal("frame rejected")
	}
	interrupt := realtimeBridgeControl{Type: "interrupt", ItemID: "reply", Generation: 1}
	go func() {
		time.Sleep(30 * time.Millisecond)
		s.expectClear(interrupt)
		s.cancel("reply", 1)
	}()
	if err := s.withPlaybackPressure(context.Background(), frame, func() error { return errCarrierPacerOverflow }); err != nil {
		t.Fatal(err)
	}
	_ = s.cleared(nil, interrupt, agentClearReceipt{Scope: "local", LocalMS: 60})
	if got := s.snapshot().Clears; len(got) != 1 || got[0].LocalMS != 180 {
		t.Fatalf("pressure discard missing from receipt: %+v", got)
	}
}

func TestReviewDDiscardWithoutDueClearSendsReceipt(t *testing.T) {
	s := continuousAgentFixture()
	s.cancel("x", 2) // e.g. a carrier overflow; no cleared receipt follows
	raw := s.linkDiscarded(realtimeBridgeControl{ItemID: "x", Generation: 2}, 100)
	var r struct {
		Type    string `json:"type"`
		ItemID  string `json:"item_id"`
		Gen     int64  `json:"session_generation"`
		LocalMS int    `json:"local_discarded_ms"`
		Reason  string `json:"reason"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Type != "playback.cleared" || r.ItemID != "x" || r.Gen != 2 || r.LocalMS != 100 || r.Reason != "discarded_after_cancel" {
		t.Fatal(string(raw))
	}
	if again := s.linkDiscarded(realtimeBridgeControl{ItemID: "x", Generation: 2}, 40); again != nil {
		t.Fatal("duplicate receipt", string(again))
	}
}

func TestReviewDTraceKeepsHeadAndRecentRing(t *testing.T) {
	s := &agentAudioSession{profile: agentAudioProfile{Playout: agentPlayoutConfig{TraceFrames: 400}}}
	base := time.Unix(100, 0)
	for i := 0; i < 1000; i++ {
		s.traceSent("a", 1, false, i, 20, "rtp_transmitted", base, 0)
	}
	snap := s.snapshot()
	if len(snap.TimingTrace) != 400 || snap.TraceOmitted != 600 || snap.TraceHeadFrames != 200 {
		t.Fatal(len(snap.TimingTrace), snap.TraceOmitted, snap.TraceHeadFrames)
	}
	for i, e := range snap.TimingTrace {
		want := i
		if i >= 200 {
			want = 600 + i
		}
		if e.AudioEndMS != want {
			t.Fatalf("event %d has end %d, want %d", i, e.AudioEndMS, want)
		}
	}
}

func TestReviewDTraceRecordsProviderReadTiming(t *testing.T) {
	s := &agentAudioSession{profile: agentAudioProfile{Playout: agentPlayoutConfig{TraceFrames: 10}}, cancelled: map[agentPlaybackKey]bool{}}
	newer, _ := parseRealtimeBridgeControl([]byte(`{"type":"audio.frame","item_id":"a","session_generation":1,"timing":{"provider_received_at":"2026-10-28T10:00:00.1Z","provider_read_started_at":"2026-10-28T10:00:00.05Z","provider_read_wait_ms":50.5,"provider_delta_duration_ms":240,"provider_to_write_ms":2,"duration_ms":20}}`))
	older, _ := parseRealtimeBridgeControl([]byte(`{"type":"audio.frame","item_id":"a","session_generation":1,"timing":{"provider_received_at":"2026-10-28T10:00:00.1Z","provider_to_write_ms":2,"duration_ms":20}}`))
	s.accept(newer)
	s.accept(older)
	trace := s.snapshot().TimingTrace
	raw, _ := json.Marshal(trace)
	for _, field := range []string{`"provider_read_started_at":"2026-10-28T10:00:00.05Z"`, `"provider_read_wait_ms":50.5`, `"provider_delta_duration_ms":240`} {
		if !bytes.Contains(raw, []byte(field)) {
			t.Fatal("missing", field, string(raw))
		}
	}
	old, _ := json.Marshal(trace[1])
	if bytes.Contains(old, []byte("provider_read")) || bytes.Contains(old, []byte("provider_delta")) {
		t.Fatal("older Core frame gained fields", string(old))
	}
}
