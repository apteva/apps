package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
)

type cancelledVoiceRuntime struct {
	*auditPlatform
	mu                  sync.Mutex
	db                  store
	bridge              string
	threads             map[string]string
	stopped             map[string]bool
	startup, release    chan struct{}
	pendingTool         bool
	destroyBeforeResult bool
}

func (p *cancelledVoiceRuntime) SpawnRuntimeAgent(_ string, r sdk.RuntimeAgentSpawnRequest) (*sdk.RuntimeAgent, error) {
	return &sdk.RuntimeAgent{Alias: r.Alias}, nil
}
func (p *cancelledVoiceRuntime) SpawnRuntimeRealtimeThread(_, agent string, r sdk.RuntimeRealtimeSpawnRequest) (*sdk.RealtimeSpawnResult, error) {
	p.mu.Lock()
	if p.threads == nil {
		p.threads = map[string]string{}
	}
	p.threads[agent] = r.ThreadID
	p.mu.Unlock()
	if p.startup != nil && agent == "main" {
		close(p.startup)
		<-p.release
	}
	return &sdk.RealtimeSpawnResult{ThreadID: r.ThreadID, AudioBridgeURL: p.bridge + "?thread=" + url.QueryEscape(r.ThreadID)}, nil
}
func (p *cancelledVoiceRuntime) StopRuntimeAgent(string, string) error { return nil }
func (p *cancelledVoiceRuntime) WaitRuntimeAgent(string, string, sdk.RuntimeAgentWaitRequest) (*sdk.RuntimeAgentExecution, error) {
	return &sdk.RuntimeAgentExecution{Status: "stopped"}, nil
}
func (p *cancelledVoiceRuntime) StopRuntimeRealtimeThread(_, _, thread string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped[thread] = true
	return nil
}
func (p *cancelledVoiceRuntime) ListRuntimeAgentTelemetry(_, agent string, _ time.Time, _ int) ([]sdk.RuntimeTelemetryEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []sdk.RuntimeTelemetryEvent
	for thread := range p.stopped {
		if (agent == "main") == strings.HasPrefix(thread, "reception-") {
			out = append(out, sdk.RuntimeTelemetryEvent{ThreadID: thread, Type: "thread.done", Time: time.Now(), Data: json.RawMessage(`{}`)})
		}
	}
	if p.pendingTool && agent == "main" {
		out = append(out, sdk.RuntimeTelemetryEvent{ThreadID: p.threads[agent], Type: "tool.call", Time: time.Now(), Data: json.RawMessage(`{"name":"delayed_backend","id":"backend"}`)})
	}
	return out, nil
}
func (p *cancelledVoiceRuntime) DestroyRuntime(id string) error {
	var running int
	if err := p.db.db.QueryRow(`SELECT COUNT(*) FROM environment_voice_calls WHERE status='running'`).Scan(&running); err != nil {
		return err
	}
	p.mu.Lock()
	p.destroyBeforeResult = running > 0
	p.mu.Unlock()
	return p.auditPlatform.DestroyRuntime(id)
}

func TestActualVoiceHandlerFinalizesOnRunStop(t *testing.T) {
	for _, phase := range []string{"startup", "conversation", "pending_backend"} {
		t.Run(phase, func(t *testing.T) {
			s, base := auditService(t)
			p := &cancelledVoiceRuntime{auditPlatform: base, db: s.db, stopped: map[string]bool{}, pendingTool: phase == "pending_backend"}
			inputSeen := make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{voiceAudioSessionHeader: []string{`{"version":1,"continuous_conversation":true,"provider_interruption":true}`}})
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					kind, data, err := conn.ReadMessage()
					if err != nil {
						return
					}
					if kind == websocket.BinaryMessage && len(data) == 960 {
						once.Do(func() { close(inputSeen) })
					}
				}
			}))
			defer server.Close()
			p.bridge = "ws" + strings.TrimPrefix(server.URL, "http")
			if phase == "startup" {
				p.startup = make(chan struct{})
				p.release = make(chan struct{})
			}
			s.ctx = sdk.NewAppCtxForTest(nil, s.db.db, nil, p, nil).WithProject("audit")
			run := &Run{ID: "run", RuntimeID: "runtime", Status: "running", StartedAt: time.Now()}
			if err := s.db.createRun(run); err != nil {
				t.Fatal(err)
			}
			type result struct {
				call *VoiceCall
				err  error
			}
			done := make(chan result, 1)
			go func() {
				call, err := s.runVoiceCall(context.Background(), run, VoiceFixtureSpec{TargetAgent: "main", CallerGoal: "Wait for greeting", TimeoutSeconds: 30})
				done <- result{call, err}
			}()
			if phase == "startup" {
				<-p.startup
			} else {
				select {
				case <-inputSeen:
				case <-time.After(2 * time.Second):
					t.Fatal("media never reached receiving bridge")
				}
			}
			stopped := make(chan error, 1)
			go func() { stopped <- s.stopRun(run) }()
			if phase == "startup" {
				deadline := time.Now().Add(time.Second)
				for {
					stored, lookupErr := s.db.getRun(run.ID)
					if lookupErr != nil {
						t.Fatal(lookupErr)
					}
					if stored.Status == "stopping" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("stop not persisted")
					}
					time.Sleep(time.Millisecond)
				}
				close(p.release)
			}
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("voice finalization failed to release runtime")
			}
			select {
			case r := <-done:
				if r.call == nil || r.call.Status != "cancelled" || r.call.Cleanup == nil || r.call.Cleanup.Status != "complete" {
					t.Fatalf("call=%+v err=%v", r.call, r.err)
				}
				stored, err := s.db.getVoiceCall(r.call.ID)
				if err != nil || stored.Status != "cancelled" {
					t.Fatal(fmt.Sprint("result missing: ", err))
				}
			case <-time.After(time.Second):
				t.Fatal("voice handler still running")
			}
			p.mu.Lock()
			early := p.destroyBeforeResult
			p.mu.Unlock()
			if early {
				t.Fatal("runtime deleted before cancelled voice result persisted")
			}
		})
	}
}
