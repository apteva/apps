package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// fakeObserver is a ThreadObserver the test drives by hand.
type fakeObserver struct {
	mu        sync.Mutex
	watches   []chan sdk.AgentThreadsSnapshot
	queries   []sdk.AgentThreadsQuery
	activity  []sdk.ThreadActivityQuery
	threads   []sdk.ObservedThread
	steps     []sdk.ThreadStep
	contentOK bool
}

func (f *fakeObserver) GetAgentThreads(_ context.Context, q sdk.AgentThreadsQuery) (*sdk.AgentTopology, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	return &sdk.AgentTopology{AgentID: q.AgentID, Threads: f.threads, ThreadsAvailable: true, ThreadSchema: 1}, nil
}

func (f *fakeObserver) GetThreadActivity(_ context.Context, q sdk.ThreadActivityQuery) (*sdk.ThreadActivityPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activity = append(f.activity, q)
	if q.Projection == "content" && !f.contentOK {
		return nil, errors.New("telemetry.read forbidden")
	}
	return &sdk.ThreadActivityPage{AgentID: q.AgentID, ThreadID: q.ThreadID, Projection: q.Projection, Steps: f.steps}, nil
}

func (f *fakeObserver) WatchAgentThreads(_ context.Context, w sdk.AgentThreadsWatch) (<-chan sdk.AgentThreadsSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, w.AgentThreadsQuery)
	ch := make(chan sdk.AgentThreadsSnapshot, 8)
	f.watches = append(f.watches, ch)
	return ch, nil
}

func (f *fakeObserver) watchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.watches)
}

func (f *fakeObserver) send(snap sdk.AgentThreadsSnapshot) {
	f.mu.Lock()
	ch := f.watches[len(f.watches)-1]
	f.mu.Unlock()
	ch <- snap
}

func withFakeObserver(t *testing.T, a *App) *fakeObserver {
	t.Helper()
	obs := &fakeObserver{}
	a.workers.close()
	a.workers = newWorkerWatches(func() sdk.ThreadObserver { return obs })
	t.Cleanup(a.workers.close)
	return obs
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func spawnCall(t *testing.T, a *App, agent int64, thread, call, span, child string, at time.Time) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": call, "name": "spawn", "reason": "Start a helper", "tool_span_id": span, "args": map[string]any{"id": child, "directive": "secret directive"}})
	if err := a.ingestToolActivity("tool.call", agent, thread, string(body), at); err != nil {
		t.Fatal(err)
	}
}

func TestWorkersLinkBySpawnSpanAndInheritToNestedWorkers(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	now := time.Now().UTC().Truncate(time.Millisecond)

	if obs.watchCount() != 0 {
		t.Fatal("watcher before any spawn")
	}
	spawnCall(t, a, 41, thread, "call-1", "tool-span-1", "researcher", now)
	waitFor(t, "watcher", func() bool { return obs.watchCount() == 1 })
	if q := obs.queries[0]; q.AgentID != 41 || q.ProjectID != testProject || q.IncludeEnded != 24*time.Hour {
		t.Fatalf("watch query = %+v", q)
	}
	// A second spawn reuses the running watcher.
	spawnCall(t, a, 41, thread, "call-2", "tool-span-2", "other", now.Add(time.Millisecond))
	time.Sleep(20 * time.Millisecond)
	if obs.watchCount() != 1 {
		t.Fatal("second watcher started")
	}

	feed, cancel := a.hub.subscribeFrames(conv.ID)
	defer cancel()
	started := now.Add(time.Second)
	obs.send(sdk.AgentThreadsSnapshot{Reason: "event", Live: true, Threads: []sdk.ObservedThread{
		{ID: mainThreadID, State: "waiting"},
		{ID: thread, State: "working"},
		{ID: "researcher", ParentID: thread, Depth: 1, Name: "researcher", Purpose: "Find the three cheapest suppliers.", State: "working", Model: "large",
			StartedAt: started, Current: &sdk.ThreadCurrentStep{Kind: "tool", Tool: "web_search", Since: started.Add(time.Second)},
			SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "tool-span-1"}},
		{ID: "fetcher", ParentID: "researcher", Depth: 2, State: "sleeping", StartedAt: started.Add(2 * time.Second),
			SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: "researcher", ToolSpanID: "tool-span-9"}},
		{ID: "unrelated", ParentID: mainThreadID, Depth: 1, State: "working", SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: mainThreadID, ToolSpanID: "span-x"}},
	}})
	var workers []ConversationWorker
	waitFor(t, "linked workers", func() bool {
		workers, _ = a.store.conversationWorkers(conv.ID)
		return len(workers) == 2
	})
	r, f := workers[0], workers[1]
	if r.ThreadID != "researcher" || r.SpawnCallID != "call-1" || r.ParentThreadID != thread || r.Depth != 1 ||
		r.Purpose != "Find the three cheapest suppliers." || r.State != "working" || r.Current == nil || r.Current.Tool != "web_search" {
		t.Fatalf("researcher = %+v", r)
	}
	if f.ThreadID != "fetcher" || f.ParentThreadID != "researcher" || f.Depth != 2 || f.SpawnCallID != "" || f.State != "sleeping" {
		t.Fatalf("fetcher = %+v", f)
	}
	gotFrames := map[string]bool{}
	waitFor(t, "worker frames", func() bool {
		select {
		case fr := <-feed:
			if fr.Worker != nil {
				gotFrames[fr.Worker.ThreadID] = true
			}
		default:
		}
		return gotFrames["researcher"] && gotFrames["fetcher"]
	})

	// The same snapshot again changes nothing and publishes nothing.
	obs.send(sdk.AgentThreadsSnapshot{Reason: "refresh", Threads: []sdk.ObservedThread{
		{ID: "researcher", ParentID: thread, Depth: 1, Name: "researcher", Purpose: "Find the three cheapest suppliers.", State: "working", Model: "large",
			StartedAt: started, Current: &sdk.ThreadCurrentStep{Kind: "tool", Tool: "web_search", Since: started.Add(time.Second)},
			SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "tool-span-1"}},
		{ID: "fetcher", ParentID: "researcher", Depth: 2, State: "sleeping", StartedAt: started.Add(2 * time.Second),
			SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: "researcher", ToolSpanID: "tool-span-9"}},
	}})
	time.Sleep(50 * time.Millisecond)
	workers, _ = a.store.conversationWorkers(conv.ID)
	if workers[0].Revision != 1 || workers[1].Revision != 1 {
		t.Fatalf("unchanged snapshot bumped revisions: %+v", workers)
	}

	// The researcher finishes with a result; the fetcher disappears from a
	// complete list and is marked lost.
	obs.send(sdk.AgentThreadsSnapshot{Reason: "event", Threads: []sdk.ObservedThread{
		{ID: "researcher", ParentID: thread, Depth: 1, Name: "researcher", State: "done", StartedAt: started,
			SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "tool-span-1"},
			Ended:     &sdk.ThreadEnd{At: started.Add(time.Minute), Outcome: "completed", Result: "Supplier A, B and C."}},
	}})
	waitFor(t, "ended workers", func() bool {
		workers, _ = a.store.conversationWorkers(conv.ID)
		return workers[0].EndedAt != "" && workers[1].EndedAt != ""
	})
	if w := workers[0]; w.State != "done" || w.Outcome != "completed" || w.Result != "Supplier A, B and C." || w.Current != nil || w.Purpose == "" {
		t.Fatalf("finished researcher = %+v", w)
	}
	if w := workers[1]; w.State != "stopped" || w.Outcome != "lost" {
		t.Fatalf("lost fetcher = %+v", w)
	}
}

func TestWorkersFallBackToRequestedThreadIDWithoutSpan(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	now := time.Now().UTC()
	spawnCall(t, a, 41, thread, "call-1", "", "helper", now)
	waitFor(t, "watcher", func() bool { return obs.watchCount() == 1 })
	obs.send(sdk.AgentThreadsSnapshot{Reason: "initial", Threads: []sdk.ObservedThread{
		{ID: "helper", ParentID: thread, Depth: 1, State: "working", StartedAt: now.Add(time.Second)},
	}})
	waitFor(t, "linked worker", func() bool {
		w, _ := a.store.conversationWorkers(conv.ID)
		return len(w) == 1 && w[0].SpawnCallID == "call-1"
	})
}

func TestWorkersNeverLinkToPublicConversations(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	conv, err := a.store.CreateConversation(CreateConversationInput{ProjectID: testProject, LeadAgentID: 41, Title: "Visitor", OwnerUserID: 1,
		ConversationKey: "app:webchat:visitor-9", Origin: "app", Audience: "public"})
	if err != nil {
		t.Fatal(err)
	}
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	spawnCall(t, a, 41, thread, "call-1", "span-1", "helper", time.Now().UTC())
	time.Sleep(30 * time.Millisecond)
	if obs.watchCount() != 0 {
		t.Fatal("public conversation started a watcher")
	}
	// Even with a watcher started by another conversation, the public
	// conversation's spawn is never linked.
	if err := a.reconcileWorkers(41, []sdk.ObservedThread{{ID: "helper", ParentID: thread, State: "working",
		SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "span-1"}}}, true); err != nil {
		t.Fatal(err)
	}
	if w, _ := a.store.conversationWorkers(conv.ID); len(w) != 0 {
		t.Fatalf("public conversation got workers: %+v", w)
	}
	req := httptest.NewRequest("GET", "/workers?chat_id="+conv.ID, nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	a.handleWorkers(rec, req)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("public /workers = %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/agent-work?chat_id="+conv.ID, nil)
	authorizeTestRequest(req)
	rec = httptest.NewRecorder()
	a.handleAgentWork(rec, req)
	if rec.Code != 404 {
		t.Fatalf("public /agent-work = %d", rec.Code)
	}
}

func TestWorkerWatcherStopsWhenNothingIsOpen(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	spawnCall(t, a, 41, thread, "call-1", "span-1", "helper", time.Now().UTC())
	waitFor(t, "watcher", func() bool { return obs.watchCount() == 1 })
	// Pretend the spawn grace has passed.
	a.workers.mu.Lock()
	a.workers.watches[41].lastSpawn = time.Now().Add(-time.Hour)
	a.workers.mu.Unlock()
	obs.send(sdk.AgentThreadsSnapshot{Reason: "event", Threads: []sdk.ObservedThread{
		{ID: "helper", ParentID: thread, State: "done", SpawnedBy: &sdk.ThreadSpawnLink{ThreadID: thread, ToolSpanID: "span-1"},
			Ended: &sdk.ThreadEnd{At: time.Now(), Outcome: "completed"}},
	}})
	waitFor(t, "watcher released", func() bool {
		a.workers.mu.Lock()
		defer a.workers.mu.Unlock()
		return a.workers.watches[41] == nil
	})
	// A later spawn starts a new one.
	spawnCall(t, a, 41, thread, "call-2", "span-2", "helper-2", time.Now().UTC())
	waitFor(t, "second watcher", func() bool { return obs.watchCount() == 2 })
}

func TestResumeWorkerWatchesAfterRestart(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	conv := mkConversation(t, a, 41)
	if _, err := a.store.saveWorker(ConversationWorker{ConversationID: conv.ID, AgentID: 41, ThreadID: "helper", incarnation: "span:s", State: "working", StartedAt: activityTime(time.Now())}); err != nil {
		t.Fatal(err)
	}
	a.resumeWorkerWatches()
	waitFor(t, "resumed watcher", func() bool { return obs.watchCount() == 1 })
}

func TestWorkerActivityRouteChecksLinkAndDetail(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	conv := mkConversation(t, a, 41)
	other := mkConversation(t, a, 41)
	if _, err := a.store.saveWorker(ConversationWorker{ConversationID: conv.ID, AgentID: 41, ThreadID: "helper", incarnation: "span:s", State: "working", StartedAt: activityTime(time.Now())}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	obs.steps = []sdk.ThreadStep{
		{Kind: "tool", Tool: "web_search", StartedAt: now, Status: "ok", Reason: "Searching suppliers", Arguments: `{"q":"steel"}`, Result: "3 results"},
		{Kind: "message", To: "chat-x", Text: "Found three suppliers", StartedAt: now},
	}
	get := func(url string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest("GET", url, nil)
		authorizeTestRequest(req)
		rec := httptest.NewRecorder()
		a.handleWorkerActivity(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec, body
	}
	if rec, _ := get("/workers/activity?chat_id=" + other.ID + "&agent_id=41&thread_id=helper"); rec.Code != 404 {
		t.Fatalf("unlinked worker served: %d", rec.Code)
	}
	// Content is refused by the platform: metadata still answers.
	rec, body := get("/workers/activity?chat_id=" + conv.ID + "&agent_id=41&thread_id=helper")
	if rec.Code != 200 || body["projection"] != "metadata" {
		t.Fatalf("metadata fallback = %d %v", rec.Code, body)
	}
	obs.contentOK = true
	rec, _ = get("/workers/activity?chat_id=" + conv.ID + "&agent_id=41&thread_id=helper")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "steel") || !strings.Contains(rec.Body.String(), "Found three suppliers") {
		t.Fatalf("summary view = %s", rec.Body.String())
	}
	rec, _ = get("/workers/activity?chat_id=" + conv.ID + "&agent_id=41&thread_id=helper&detail=1")
	if !strings.Contains(rec.Body.String(), "steel") || !strings.Contains(rec.Body.String(), "3 results") {
		t.Fatalf("detail view = %s", rec.Body.String())
	}
	last := obs.activity[len(obs.activity)-1]
	if last.ProjectID != testProject || last.Limit != 20 {
		t.Fatalf("activity query = %+v", last)
	}
}

func TestAgentWorkNamesConversationThreads(t *testing.T) {
	a, _, _ := newTestEnv(t)
	obs := withFakeObserver(t, a)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	obs.threads = []sdk.ObservedThread{
		{ID: mainThreadID, State: "sleeping", NextWakeAt: time.Now().Add(time.Hour)},
		{ID: thread, ParentID: mainThreadID, State: "working"},
		{ID: "helper", ParentID: thread, State: "working", Purpose: "Draft the summary."},
	}
	req := httptest.NewRequest("GET", "/agent-work?chat_id="+conv.ID, nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	a.handleAgentWork(rec, req)
	var body struct {
		Available bool `json:"available"`
		Agents    []struct {
			AgentID int64             `json:"agent_id"`
			Threads []AgentWorkThread `json:"threads"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.Available || len(body.Agents) != 1 {
		t.Fatalf("agent work = %s", rec.Body.String())
	}
	kinds := map[string]AgentWorkThread{}
	for _, th := range body.Agents[0].Threads {
		kinds[th.Kind] = th
	}
	if c := kinds["chat"]; c.ConversationID != conv.ID || c.Title != "Test" || !c.ThisConversation {
		t.Fatalf("chat thread = %+v", c)
	}
	if m := kinds["main"]; m.State != "sleeping" || m.NextWakeAt == "" {
		t.Fatalf("main = %+v", m)
	}
	if w := kinds["worker"]; w.ParentID != thread || w.Purpose != "Draft the summary." {
		t.Fatalf("worker = %+v", w)
	}
}
