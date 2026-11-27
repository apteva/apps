package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (f *fakeObserver) activityCount(projection string, limit int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, q := range f.activity {
		if q.Projection == projection && (limit == 0 || q.Limit == limit) {
			n++
		}
	}
	return n
}

func (f *fakeObserver) setSteps(steps []sdk.ThreadStep) {
	f.mu.Lock()
	f.steps = steps
	f.mu.Unlock()
}

func TestHelperTitleIsTheSpawnReasonAndStepsCarryTheirReason(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	obs.contentOK = true
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	now := time.Now().UTC().Truncate(time.Millisecond)
	spawnCall(t, a, 41, thread, "call-1", "span-1", "researcher", now)
	obs.setSteps([]sdk.ThreadStep{
		{Kind: "tool", Tool: "web_search", Status: "running", Reason: "Searching supplier prices"},
		{Kind: "tool", Tool: "spawn", ChildThreadID: "fetcher", Reason: "Fetch each supplier page"},
	})
	started := now.Add(time.Second)
	researcher := func(since time.Time, tool string) sdk.ObservedThread {
		return sdk.ObservedThread{ID: "researcher", ParentID: thread, Depth: 1, Name: "researcher", Purpose: "You are a research helper. Find the three cheapest suppliers.",
			State: "working", StartedAt: started, Current: &sdk.ThreadCurrentStep{Kind: "tool", Tool: tool, Since: since},
			SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "span-1"}}
	}
	fetcher := sdk.ObservedThread{ID: "fetcher", ParentID: "researcher", Depth: 2, State: "working", StartedAt: started.Add(time.Second),
		SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: "researcher", ToolSpanID: "span-9"}}
	if err := a.reconcileWorkers(41, []sdk.ObservedThread{researcher(started, "web_search"), fetcher}, true); err != nil {
		t.Fatal(err)
	}
	workers, _ := a.store.conversationWorkers(conv.ID)
	if len(workers) != 2 {
		t.Fatalf("workers = %+v", workers)
	}
	r, f := workers[0], workers[1]
	if r.Label != "Start a helper" || r.Purpose == "" || r.Current == nil || r.Current.Reason != "Searching supplier prices" {
		t.Fatalf("researcher = %+v current=%+v", r, r.Current)
	}
	if f.Label != "Fetch each supplier page" {
		t.Fatalf("nested helper label = %q", f.Label)
	}
	reads := obs.activityCount("content", 5)
	// The same step again is not read again, and keeps its reason.
	if err := a.reconcileWorkers(41, []sdk.ObservedThread{researcher(started, "web_search"), fetcher}, true); err != nil {
		t.Fatal(err)
	}
	if got := obs.activityCount("content", 5); got != reads {
		t.Fatalf("unchanged step read again: %d → %d", reads, got)
	}
	// A new step is read once; a step with no reason shows none.
	obs.setSteps([]sdk.ThreadStep{{Kind: "tool", Tool: "files_read", Status: "running"}})
	if err := a.reconcileWorkers(41, []sdk.ObservedThread{researcher(started.Add(5*time.Second), "files_read"), fetcher}, true); err != nil {
		t.Fatal(err)
	}
	workers, _ = a.store.conversationWorkers(conv.ID)
	if c := workers[0].Current; c == nil || c.Tool != "files_read" || c.Reason != "" {
		t.Fatalf("next step = %+v", c)
	}
	if got := obs.activityCount("content", 5); got != reads+1 {
		t.Fatalf("new step reads = %d, want %d", got, reads+1)
	}
}

func TestEndedHelperSumsItsUsage(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	now := time.Now().UTC().Truncate(time.Millisecond)
	spawnCall(t, a, 41, thread, "call-1", "span-1", "researcher", now)
	started := now.Add(time.Second)
	obs.setSteps([]sdk.ThreadStep{
		{Kind: "end", Outcome: "completed", StartedAt: started.Add(time.Minute)},
		{Kind: "tool", Tool: "pace", StartedAt: started.Add(50 * time.Second)},
		{Kind: "model", TokensIn: 2000, TokensOut: 300, StartedAt: started.Add(40 * time.Second)},
		{Kind: "tool", Tool: "web_search", StartedAt: started.Add(20 * time.Second)},
		{Kind: "tool", Tool: "files_read", StartedAt: started.Add(10 * time.Second)},
		{Kind: "model", TokensIn: 1000, TokensOut: 200, StartedAt: started},
		// An earlier helper with the same id.
		{Kind: "model", TokensIn: 99999, StartedAt: started.Add(-time.Hour)},
	})
	done := sdk.ObservedThread{ID: "researcher", ParentID: thread, Depth: 1, State: "done", StartedAt: started,
		SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "span-1"},
		Ended:     &sdk.ThreadEnd{At: started.Add(time.Minute), Outcome: "completed", Result: "A, B and C."}}
	if err := a.reconcileWorkers(41, []sdk.ObservedThread{done}, true); err != nil {
		t.Fatal(err)
	}
	workers, _ := a.store.conversationWorkers(conv.ID)
	if len(workers) != 1 || workers[0].Tokens != 3500 || workers[0].ToolSteps != 2 {
		t.Fatalf("usage = %+v", workers)
	}
}

type fakeKiller struct {
	mu     sync.Mutex
	killed []string
	fail   map[string]error
}

func (k *fakeKiller) KillThread(agent int64, thread string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.killed = append(k.killed, thread)
	return k.fail[thread]
}

type fakeEvents struct {
	mu   sync.Mutex
	sent []sdk.AgentEventRequest
}

func (e *fakeEvents) SendTrackedAgentEvent(r sdk.AgentEventRequest) (*sdk.AgentEventReceipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sent = append(e.sent, r)
	return &sdk.AgentEventReceipt{SourceEventID: r.SourceEventID, ThreadID: r.ThreadID, Accepted: true}, nil
}

func TestStopHelperStopsItsSubtreeAndTellsTheParent(t *testing.T) {
	a, _, _ := newTestEnv(t)
	withFakeObserver(t, a)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	now := time.Now().UTC().Truncate(time.Millisecond)
	spawnCall(t, a, 41, thread, "call-1", "span-1", "researcher", now)
	spawnCall(t, a, 41, thread, "call-2", "span-2", "writer", now.Add(time.Millisecond))
	started := now.Add(time.Second)
	threads := []sdk.ObservedThread{
		{ID: "researcher", ParentID: thread, Depth: 1, State: "working", StartedAt: started, SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "span-1"}},
		{ID: "fetcher", ParentID: "researcher", Depth: 2, State: "working", StartedAt: started, SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: "researcher", ToolSpanID: "span-9"}},
		{ID: "writer", ParentID: thread, Depth: 1, State: "working", StartedAt: started, SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "span-2"}},
	}
	if err := a.reconcileWorkers(41, threads, true); err != nil {
		t.Fatal(err)
	}
	workers, _ := a.store.conversationWorkers(conv.ID)
	byThread := map[string]ConversationWorker{}
	for _, w := range workers {
		byThread[w.ThreadID] = w
	}
	killer, events := &fakeKiller{}, &fakeEvents{}
	prev := stopPlatform
	stopPlatform = func(string) (threadKiller, sdk.AgentEventClient) { return killer, events }
	t.Cleanup(func() { stopPlatform = prev })
	stop := func(chat string, id int64, authorized bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"chat_id": chat, "worker_id": id})
		req := httptest.NewRequest("POST", "/workers/stop", strings.NewReader(string(body)))
		if authorized {
			authorizeTestRequest(req)
		}
		rec := httptest.NewRecorder()
		a.handleStopWorker(rec, req)
		return rec
	}
	other := mkConversation(t, a, 41)
	if rec := stop(other.ID, byThread["researcher"].ID, true); rec.Code != 404 {
		t.Fatalf("stop from another conversation = %d", rec.Code)
	}
	if rec := stop(conv.ID, byThread["researcher"].ID, false); rec.Code != 404 {
		t.Fatalf("unauthorized stop = %d", rec.Code)
	}
	if len(killer.killed) != 0 {
		t.Fatalf("refused stops killed %v", killer.killed)
	}
	feed, cancel := a.hub.subscribeFrames(conv.ID)
	defer cancel()
	rec := stop(conv.ID, byThread["researcher"].ID, true)
	if rec.Code != 200 {
		t.Fatalf("stop = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Join(killer.killed, ",") != "fetcher,researcher" {
		t.Fatalf("killed = %v, want children first", killer.killed)
	}
	workers, _ = a.store.conversationWorkers(conv.ID)
	for _, w := range workers {
		stoppedHere := w.ThreadID != "writer"
		if stoppedHere != (w.State == sdk.ThreadStateStopped && w.Outcome == workerOutcomeStoppedByOperator && w.EndedAt != "") {
			t.Fatalf("after stop %s = %+v", w.ThreadID, w)
		}
	}
	frames := map[string]bool{}
	waitFor(t, "stop frames", func() bool {
		select {
		case fr := <-feed:
			if fr.Worker != nil && fr.Worker.State == sdk.ThreadStateStopped {
				frames[fr.Worker.ThreadID] = true
			}
		default:
		}
		return frames["researcher"] && frames["fetcher"]
	})
	if len(events.sent) != 1 {
		t.Fatalf("parent notices = %+v", events.sent)
	}
	notice := events.sent[0]
	text, _ := notice.Message.(string)
	if notice.ThreadID != thread || notice.AgentID != 41 || !strings.Contains(text, "researcher") ||
		!strings.Contains(text, "Do not start it again") || !strings.Contains(text, "1 helper(s) it started") {
		t.Fatalf("notice = %+v", notice)
	}
	if !a.streamer.responseActive(conv.ID, 41) {
		t.Fatal("the conversation thread's answer to the stop is not shown as working")
	}
	// The kill reaching the thread list later does not reopen the helper.
	threads[0].State, threads[0].Ended = "stopped", &sdk.ThreadEnd{At: time.Now(), Outcome: "cancelled"}
	if err := a.reconcileWorkers(41, threads, true); err != nil {
		t.Fatal(err)
	}
	threads[0].State, threads[0].Ended = "working", nil
	if err := a.reconcileWorkers(41, threads, true); err != nil {
		t.Fatal(err)
	}
	workers, _ = a.store.queryWorkers(`WHERE conversation_id=? AND thread_id='researcher'`, conv.ID)
	if len(workers) != 1 || workers[0].Outcome != workerOutcomeStoppedByOperator || workers[0].EndedAt == "" {
		t.Fatalf("researcher after late listing = %+v", workers)
	}
	// An ended helper cannot be stopped; a failed kill reports the error.
	if rec := stop(conv.ID, byThread["researcher"].ID, true); rec.Code != 409 {
		t.Fatalf("stop ended helper = %d", rec.Code)
	}
	killer.fail = map[string]error{"writer": errors.New("core unavailable")}
	if rec := stop(conv.ID, byThread["writer"].ID, true); rec.Code != 502 {
		t.Fatalf("failed kill = %d", rec.Code)
	}
	if w, _ := a.store.queryWorkers(`WHERE id=?`, byThread["writer"].ID); w[0].EndedAt != "" {
		t.Fatalf("writer marked stopped after a failed kill: %+v", w[0])
	}
}

func TestAgentWorkCarriesHelperTitleAndStopID(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	now := time.Now().UTC().Truncate(time.Millisecond)
	spawnCall(t, a, 41, thread, "call-1", "span-1", "researcher", now)
	helper := sdk.ObservedThread{ID: "researcher", ParentID: thread, Depth: 1, State: "working", StartedAt: now,
		Purpose: "Long instructions", SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "span-1"}}
	if err := a.reconcileWorkers(41, []sdk.ObservedThread{helper}, true); err != nil {
		t.Fatal(err)
	}
	obs.mu.Lock()
	obs.threads = []sdk.ObservedThread{{ID: thread, State: "waiting"}, helper}
	obs.mu.Unlock()
	req := httptest.NewRequest("GET", "/agent-work?chat_id="+conv.ID, nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	a.handleAgentWork(rec, req)
	var got struct {
		Agents []struct {
			Threads []AgentWorkThread `json:"threads"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Agents) != 1 {
		t.Fatalf("agent work = %s", rec.Body.String())
	}
	var found *AgentWorkThread
	for i := range got.Agents[0].Threads {
		if got.Agents[0].Threads[i].ID == "researcher" {
			found = &got.Agents[0].Threads[i]
		}
	}
	if found == nil || found.Label != "Start a helper" || found.WorkerID == 0 || !found.ThisConversation || found.ConversationID != conv.ID {
		t.Fatalf("helper row = %+v", found)
	}
}

func TestResponseWorkIsKeptOnItsLastMessage(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	ctx := boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	start := time.Now()
	a.streamer.emitAck(conv.ID, thread, 41, 0)
	a.streamer.Ingest("llm.start", 41, thread, `{}`, start)
	a.streamer.Ingest("llm.done", 41, thread, `{"tokens_in":1200,"tokens_out":80}`, start.Add(time.Second))
	a.streamer.Ingest("llm.start", 41, thread, `{}`, start.Add(2*time.Second))
	a.streamer.Ingest("llm.done", 41, thread, `{"tokens_in":1500,"tokens_out":120}`, start.Add(3*time.Second))
	// Another thread's model calls are not this response's.
	a.streamer.Ingest("llm.done", 41, "helper-1", `{"tokens_in":9000,"tokens_out":900}`, start.Add(3*time.Second))
	a.streamer.mu.Lock()
	a.streamer.responses[responseProgressKey(conv.ID, 41)].StartedAt = start.Add(-12 * time.Second)
	a.streamer.mu.Unlock()
	res, err := a.toolSend(ctx, nil, map[string]any{"text": "Done: three suppliers found.", "phase": "final"})
	if err != nil {
		t.Fatal(err)
	}
	id := res.(map[string]any)["message_id"].(int64)
	msg, err := a.store.GetMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(msg.Metadata["work"])
	var work TurnWork
	if err := json.Unmarshal(raw, &work); err != nil || work.Tokens != 2900 || work.DurationMS < 12000 {
		t.Fatalf("work = %s", raw)
	}
	// Recorded once: a later response's end does not overwrite it.
	a.recordTurnWork(conv.ID, 41, id, 0, TurnWork{DurationMS: 1, Tokens: 1})
	again, _ := a.store.GetMessage(id)
	raw2, _ := json.Marshal(again.Metadata["work"])
	if string(raw2) != string(raw) {
		t.Fatalf("work overwritten: %s → %s", raw, raw2)
	}
}
