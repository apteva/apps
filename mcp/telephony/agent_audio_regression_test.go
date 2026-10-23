package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAgentAudioDiagnosticsSurviveAICallEndAndReplacement(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	for _, direction := range []string{"inbound", "outbound"} {
		for _, peer := range []string{peerKindRealtime, peerKindHuman, peerKindExternal} {
			id := direction + "-" + peer
			row := testCall(id, "completed")
			row.Direction, row.PeerKind, row.ProjectID = direction, peer, ctx.CurrentProject()
			if err := a.db().insertCall(row); err != nil {
				t.Fatal(err)
			}
			if _, err := a.db().db.Exec(`UPDATE calls SET media_generation='current' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			d := carrierAudioDiagnostics{Provider: "telnyx", Codec: carrierCodecL16_16, SampleRate: 16000, MaxQueuedMS: 160, AgentAudio: continuousAgentFixture().snapshot()}
			d.AgentAudio.ReplyTiming.MaxCoreWaitMS = 2100
			d.AgentAudio.ReplyTiming.MaxDispatchMS = 60
			if err := a.db().updateCarrierAudioDiagnostics(id, d, "old"); err != nil {
				t.Fatal(err)
			}
			got, _ := a.db().findCall(id)
			if got.CarrierAudioDiagnostics != "{}" {
				t.Fatal("replaced bridge overwrote evidence")
			}
			if err := a.db().updateCarrierAudioDiagnostics(id, d, "current"); err != nil {
				t.Fatal(err)
			}
			got, _ = a.db().findCall(id)
			var saved carrierAudioDiagnostics
			if err := json.Unmarshal([]byte(got.CarrierAudioDiagnostics), &saved); err != nil || saved.AgentAudio == nil || saved.MaxQueuedMS != 160 {
				t.Fatalf("lost ended %s audio report: %s (%v)", id, got.CarrierAudioDiagnostics, err)
			}
			detail := dashboardTestResult(t, a, "?call_id="+id)
			if detail["carrier"].(map[string]any)["agent_audio"] == nil {
				t.Fatal("audio-health omitted AI report", id)
			}
			if err := a.db().refreshAudioDashboard(context.Background()); err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := a.db().db.QueryRow(`SELECT summary_json FROM telephony_audio_reports WHERE call_id=?`, id).Scan(&raw); err != nil {
				t.Fatal("retained report missing from audio health list", id, err)
			}
			var summary audioDashboardSummary
			if err := json.Unmarshal([]byte(raw), &summary); err != nil || summary.Metrics["agent_reply_core_wait_ms"] != 2100 || summary.Metrics["agent_reply_dispatch_ms"] != 60 {
				t.Fatalf("timing stages not preserved: %s (%v)", raw, err)
			}
		}
	}
}

func TestAgentAudioProfileAcceptsOnlyTrustedProjectMetadata(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	if _, err := a.toolAgentAudioProfile(context.Background(), ctx, map[string]any{"_project_id": ctx.CurrentProject(), "scope": "agent/7", "config": map[string]any{"mode": "continuous"}}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{"other-project", 123, nil, map[string]any{"project": "other"}} {
		if _, err := a.toolAgentAudioProfile(context.Background(), ctx, map[string]any{"_project_id": v, "scope": "agent/7", "reset": true}); err == nil {
			t.Fatalf("accepted forged metadata %v", v)
		}
	}
	r, err := a.toolAgentAudioProfile(context.Background(), ctx, map[string]any{"_project_id": ctx.CurrentProject(), "scope": "agent/7"})
	if err != nil || r.(map[string]any)["override_active"] != true {
		t.Fatal("invalid metadata changed saved policy", r, err)
	}
}

func TestAgentAudioSilentMarksDoNotImplySpeaking(t *testing.T) {
	p := newTwilioPlaybackTracker()
	silent := p.addPacket("quiet", 1, 20, true)
	if !p.hasPending() || p.hasPendingSpeech() {
		t.Fatal("pending silence treated as speech")
	}
	spoken := p.addPacket("reply", 1, 20, false)
	if !p.hasPendingSpeech() {
		t.Fatal("real speech no longer interruptible")
	}
	if _, ok := p.acknowledge(spoken); !ok {
		t.Fatal("missing speech mark")
	}
	if !p.hasPending() || p.hasPendingSpeech() {
		t.Fatal("only silent marks remain")
	}
	p.acknowledge(spoken)
	if p.pendingSpeech != 0 {
		t.Fatal("duplicate mark corrupted count")
	}
	p.acknowledge(silent)
	for i := 0; i < 2200; i++ {
		p.addPacket("bounded", 1, (i+1)*20, i%2 == 0)
	}
	count := 0
	for _, v := range p.pending {
		if !v.Silent {
			count++
		}
	}
	if p.pendingSpeech != count || len(p.pending) > 1024 {
		t.Fatal("eviction corrupted speech evidence")
	}
	p.clear()
	if p.hasPendingSpeech() || p.hasPending() {
		t.Fatal("clear left pending marks")
	}
}

func TestAgentAudioSIPSilentQueueDoesNotImplySpeaking(t *testing.T) {
	p := &sipRTPPacer{playback: &sipPlaybackState{}, queue: make(chan sipRTPOutboundPacket, 8)}
	if _, err := p.enqueue([]sipRTPOutboundPacket{{silence: true}, {silence: false}, {silence: true}}); err != nil {
		t.Fatal(err)
	}
	if !p.playback.hasPendingSpeech() {
		t.Fatal("queued speech missing")
	}
	a := <-p.queue
	p.playback.adjust(a, -1)
	b := <-p.queue
	p.playback.adjust(b, -1)
	if !p.playback.hasPending() || p.playback.hasPendingSpeech() {
		t.Fatal("SIP silence treated as speech")
	}
	p.drain()
	if p.playback.pending.Load() != 0 || p.playback.pendingSpeech.Load() != 0 {
		t.Fatal("SIP drain leaked speech counter")
	}
}

func TestAgentAudioReplyTimingSeparatesCoreWaitAndDispatch(t *testing.T) {
	s := continuousAgentFixture()
	base := time.Unix(1000, 0)
	s.observeCaller(true, base)
	s.observeCaller(false, base.Add(199*time.Millisecond))
	if s.timing.waitingCore {
		t.Fatal("short silence split caller speech")
	}
	s.observeCaller(false, base.Add(200*time.Millisecond))
	if !s.acceptAt(realtimeBridgeControl{ItemID: "quiet", Generation: 1, Silence: true}, base.Add(time.Second)) {
		t.Fatal("silence was dropped")
	}
	if s.snapshot().ReplyTiming.ReplyCount != 0 {
		t.Fatal("silence counted as reply")
	}
	s.acceptAt(realtimeBridgeControl{ItemID: "reply", Generation: 1}, base.Add(2100*time.Millisecond))
	s.observeSent("reply", 2, false, base.Add(2150*time.Millisecond))
	s.observeSent("reply", 1, true, base.Add(2150*time.Millisecond))
	s.observeSent("reply", 1, false, base.Add(2160*time.Millisecond))
	r := s.snapshot().ReplyTiming
	if r.ReplyCount != 1 || r.MaxCoreWaitMS != 2100 || r.MaxDispatchMS != 60 || r.Recent[0].Outcome != "carrier_write_succeeded" {
		t.Fatalf("incorrect timing: %+v", r)
	}
	for i := 0; i < 100; i++ {
		at := base.Add(time.Duration(i+10) * time.Second)
		s.observeCaller(true, at)
		s.observeCaller(false, at.Add(time.Second))
		item := fmt.Sprint(i)
		s.acceptAt(realtimeBridgeControl{ItemID: item, Generation: 1}, at.Add(2*time.Second))
		s.cancel(item, 1)
		s.observeSent(item, 1, false, at.Add(3*time.Second))
	}
	r = s.snapshot().ReplyTiming
	if r.ReplyCount != 101 || len(r.Recent) != agentReplyHistoryLimit || r.Recent[len(r.Recent)-1].Outcome != "cancelled_before_carrier_write" {
		t.Fatal("timing ring or cancellation failed", r)
	}
}

func TestAgentAudioTimingConcurrentSnapshotAndCancellation(t *testing.T) {
	s := continuousAgentFixture()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 300; n++ {
				at := time.Now()
				switch i {
				case 0:
					s.observeCaller(n%10 == 0, at)
				case 1:
					s.acceptAt(realtimeBridgeControl{ItemID: "reply", Generation: 1, Silence: n%5 == 0}, at)
				case 2:
					s.observeSent("reply", 1, false, at)
				case 3:
					s.snapshot()
					s.cancel("other", 1)
				}
			}
		}(i)
	}
	wg.Wait()
}

func BenchmarkAgentAudioReplyTimingSteadyFrame(b *testing.B) {
	s := continuousAgentFixture()
	at := time.Now()
	f := realtimeBridgeControl{ItemID: "steady", Generation: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.observeCaller(false, at)
		s.accept(f)
		s.observeSent(f.ItemID, f.Generation, false, at)
	}
}
