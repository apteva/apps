package main

// Workers are threads an agent spawns to do part of a job. Conversations
// links each one to the conversation whose thread spawned it, so the person
// can see what the agent's helpers are doing. The live thread list comes from
// the SDK thread observer; one watcher runs per agent only while that agent
// has unfinished workers linked to a conversation, or just spawned one.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ConversationWorker is one worker thread linked to a conversation.
type ConversationWorker struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"chat_id"`
	AgentID        int64  `json:"agent_id"`
	ThreadID       string `json:"thread_id"`
	ParentThreadID string `json:"parent_thread_id"`
	SpawnCallID    string `json:"spawn_call_id,omitempty"`
	Depth          int    `json:"depth"`
	Name           string `json:"name"`
	// Label is the reason the spawn call gave: the helper's title.
	Label           string         `json:"label,omitempty"`
	Purpose         string         `json:"purpose,omitempty"`
	State           string         `json:"state"`
	Current         *WorkerCurrent `json:"current,omitempty"`
	Model           string         `json:"model,omitempty"`
	NextWakeAt      string         `json:"next_wake_at,omitempty"`
	StartedAt       string         `json:"started_at"`
	LastActivityAt  string         `json:"last_activity_at,omitempty"`
	EndedAt         string         `json:"ended_at,omitempty"`
	Outcome         string         `json:"outcome,omitempty"`
	Result          string         `json:"result,omitempty"`
	ResultTruncated bool           `json:"result_truncated,omitempty"`
	// Tokens and ToolSteps are summed from the helper's steps once it ended.
	Tokens      int64 `json:"tokens,omitempty"`
	ToolSteps   int   `json:"tool_steps,omitempty"`
	Revision    int64 `json:"revision"`
	incarnation string
}

// WorkerCurrent is what an unfinished worker is doing now.
type WorkerCurrent struct {
	Kind  string `json:"kind"` // model or tool
	Tool  string `json:"tool,omitempty"`
	Since string `json:"since"`
	// Reason is the running tool call's "_reason": what the helper is doing.
	Reason string `json:"reason,omitempty"`
}

// sameStep reports whether two current steps are the same call.
func (c *WorkerCurrent) sameStep(o *WorkerCurrent) bool {
	return c != nil && o != nil && c.Kind == o.Kind && c.Tool == o.Tool && c.Since == o.Since
}

const (
	workerColumns = `id,conversation_id,agent_id,thread_id,incarnation,parent_thread_id,spawn_call_id,depth,name,purpose,state,current_json,model,next_wake_at,started_at,last_activity_at,ended_at,outcome,result,result_truncated,revision,label,tokens,tool_steps`
	// workerWatchCap bounds live telemetry watchers; agents past it poll.
	workerWatchCap  = 50
	workerPollEvery = 15 * time.Second
	// workerSpawnGrace keeps a watcher alive after a spawn until the new
	// thread shows up in the list and can be linked.
	workerSpawnGrace  = 2 * time.Minute
	workerResultLimit = 4000
	workerEndedWindow = 24 * time.Hour
)

func isSpawnTool(name string) bool { return strings.EqualFold(strings.TrimSpace(name), "spawn") }

// unfinishedWorkerState reports whether a worker in this state can still act.
func unfinishedWorkerState(state string) bool {
	switch state {
	case sdk.ThreadStateDone, sdk.ThreadStateFailed, sdk.ThreadStateStopped:
		return false
	}
	return true
}

// conversationThread reports threads that belong to Conversations itself
// (main and conversation threads): they are never workers.
func conversationThread(id string) bool {
	return id == mainThreadID || strings.HasPrefix(id, "chat-") || strings.HasPrefix(id, "voice-")
}

func scanWorker(row interface{ Scan(...any) error }) (ConversationWorker, error) {
	var w ConversationWorker
	var current string
	var truncated int
	err := row.Scan(&w.ID, &w.ConversationID, &w.AgentID, &w.ThreadID, &w.incarnation, &w.ParentThreadID, &w.SpawnCallID, &w.Depth,
		&w.Name, &w.Purpose, &w.State, &current, &w.Model, &w.NextWakeAt, &w.StartedAt, &w.LastActivityAt, &w.EndedAt,
		&w.Outcome, &w.Result, &truncated, &w.Revision, &w.Label, &w.Tokens, &w.ToolSteps)
	if err != nil {
		return w, err
	}
	w.ResultTruncated = truncated != 0
	if current != "" {
		var c WorkerCurrent
		if json.Unmarshal([]byte(current), &c) == nil && c.Kind != "" {
			w.Current = &c
		}
	}
	return w, nil
}

func (s *store) queryWorkers(query string, args ...any) ([]ConversationWorker, error) {
	rows, err := s.db.Query(`SELECT `+workerColumns+` FROM conversation_workers `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConversationWorker{}
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *store) conversationWorkers(chat string) ([]ConversationWorker, error) {
	return s.queryWorkers(`WHERE conversation_id=? ORDER BY id`, chat)
}

// workerTimeString formats a topology time the way tool activity rows do.
func workerTimeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return activityTime(t)
}

type spawnRow struct {
	chat, callID, thread, span, spawnThread, reason string
}

// workerWatches owns the per-agent thread watchers.
type workerWatches struct {
	mu      sync.Mutex
	watches map[int64]*agentWorkerWatch
	ctx     context.Context
	stop    context.CancelFunc
	// observer returns the platform's thread observer, or nil.
	observer func() sdk.ThreadObserver
	// unsupported is set when the platform has no thread observation, so
	// spawns are shown as plain tool rows without retrying every spawn.
	unsupportedUntil time.Time
	logged           bool
	// tried holds activity lookups already made (see firstTry).
	tried map[string]bool
}

type agentWorkerWatch struct {
	project   string
	cancel    context.CancelFunc
	lastSpawn time.Time
	polling   bool
}

func newWorkerWatches(observer func() sdk.ThreadObserver) *workerWatches {
	ctx, cancel := context.WithCancel(context.Background())
	return &workerWatches{watches: map[int64]*agentWorkerWatch{}, ctx: ctx, stop: cancel, observer: observer}
}

func (a *App) workerObserver() sdk.ThreadObserver {
	if mountedCtx == nil {
		return nil
	}
	return mountedCtx.ThreadObserverAPI()
}

// noteSpawn is called after a spawn tool call is recorded in chat.
func (a *App) noteSpawn(agent int64, chat string) {
	if a.workers == nil {
		return
	}
	conv, err := a.store.getConversationAny(chat)
	if err != nil || conv.Audience == "public" {
		return
	}
	a.workers.ensure(a, agent, conv.ProjectID, true)
}

// ensure starts the agent's watcher when none runs.
func (ws *workerWatches) ensure(a *App, agent int64, project string, spawned bool) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if w := ws.watches[agent]; w != nil {
		if spawned {
			w.lastSpawn = time.Now()
		}
		return
	}
	if time.Now().Before(ws.unsupportedUntil) {
		return
	}
	obs := ws.observer()
	if obs == nil {
		return
	}
	ctx, cancel := context.WithCancel(ws.ctx)
	w := &agentWorkerWatch{project: project, cancel: cancel, polling: len(ws.watches) >= workerWatchCap}
	if spawned {
		w.lastSpawn = time.Now()
	}
	ws.watches[agent] = w
	go ws.run(ctx, a, obs, agent, w)
}

func (ws *workerWatches) release(agent int64, w *agentWorkerWatch) {
	ws.mu.Lock()
	if ws.watches[agent] == w {
		delete(ws.watches, agent)
	}
	ws.mu.Unlock()
	w.cancel()
}

func (ws *workerWatches) markUnsupported(err error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.unsupportedUntil = time.Now().Add(10 * time.Minute)
	if !ws.logged && mountedCtx != nil {
		ws.logged = true
		mountedCtx.Logger().Info("thread observation unavailable; spawns show as tool rows", "err", err)
	}
}

func (ws *workerWatches) run(ctx context.Context, a *App, obs sdk.ThreadObserver, agent int64, w *agentWorkerWatch) {
	defer ws.release(agent, w)
	q := sdk.AgentThreadsQuery{ProjectID: w.project, AgentID: agent, IncludeEnded: workerEndedWindow}
	handle := func(threads []sdk.ObservedThread, complete bool) bool {
		if err := a.reconcileWorkers(agent, threads, complete); err != nil && mountedCtx != nil {
			mountedCtx.Logger().Warn("worker reconcile failed", "agent_id", agent, "err", err)
		}
		return ws.keep(a, agent, w)
	}
	if w.polling {
		for {
			top, err := obs.GetAgentThreads(ctx, q)
			if errors.Is(err, sdk.ErrThreadObservationUnsupported) {
				ws.markUnsupported(err)
				return
			}
			if err == nil && top != nil && top.ThreadSchema > 0 {
				if !handle(top.Threads, !top.Partial) {
					return
				}
			} else if err == nil {
				ws.markUnsupported(errors.New("thread schema missing"))
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(workerPollEvery):
			}
		}
	}
	ch, err := obs.WatchAgentThreads(ctx, sdk.AgentThreadsWatch{AgentThreadsQuery: q})
	if err != nil {
		if errors.Is(err, sdk.ErrThreadObservationUnsupported) {
			ws.markUnsupported(err)
		}
		return
	}
	for snap := range ch {
		if snap.Err != nil {
			if errors.Is(snap.Err, sdk.ErrThreadObservationUnsupported) {
				ws.markUnsupported(snap.Err)
				return
			}
			continue
		}
		if !handle(snap.Threads, true) {
			return
		}
	}
}

// keep reports whether the agent still needs its watcher.
func (ws *workerWatches) keep(a *App, agent int64, w *agentWorkerWatch) bool {
	ws.mu.Lock()
	recent := time.Since(w.lastSpawn) < workerSpawnGrace
	ws.mu.Unlock()
	if recent {
		return true
	}
	var open int
	_ = a.store.db.QueryRow(`SELECT COUNT(*) FROM conversation_workers WHERE agent_id=? AND ended_at=''`, agent).Scan(&open)
	return open > 0
}

func (ws *workerWatches) close() {
	ws.stop()
}

// resumeWorkerWatches restarts watchers for workers left unfinished by a
// restart.
func (a *App) resumeWorkerWatches() {
	rows, err := a.store.db.Query(`SELECT DISTINCT w.agent_id, c.project_id FROM conversation_workers w JOIN conversations c ON c.id=w.conversation_id WHERE w.ended_at=''`)
	if err != nil {
		return
	}
	type pair struct {
		agent   int64
		project string
	}
	var open []pair
	for rows.Next() {
		var p pair
		if rows.Scan(&p.agent, &p.project) == nil {
			open = append(open, p)
		}
	}
	_ = rows.Close()
	for _, p := range open {
		a.workers.ensure(a, p.agent, p.project, false)
	}
}

// reconcileWorkers links the agent's worker threads to conversations and
// stores what changed. complete is false for a partial list, where a missing
// thread says nothing.
func (a *App) reconcileWorkers(agent int64, threads []sdk.ObservedThread, complete bool) error {
	since := activityTime(time.Now().Add(-workerEndedWindow))
	spawns := []spawnRow{}
	rows, err := a.store.db.Query(`SELECT t.conversation_id,t.call_id,t.thread_id,t.tool_span_id,t.spawn_thread_id,t.reason FROM conversation_tool_activity t JOIN conversations c ON c.id=t.conversation_id
		WHERE t.agent_id=? AND lower(t.name)='spawn' AND t.started_at>=? AND c.audience<>'public' ORDER BY t.id`, agent, since)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s spawnRow
		if rows.Scan(&s.chat, &s.callID, &s.thread, &s.span, &s.spawnThread, &s.reason) == nil {
			spawns = append(spawns, s)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	bySpan := map[string]spawnRow{}
	byChild := map[string]spawnRow{} // parent thread + "\x00" + requested id; the newest wins
	byCall := map[string]string{}    // conversation + "\x00" + call id → spawn reason
	for _, s := range spawns {
		if s.reason != "" {
			byCall[s.chat+"\x00"+s.callID] = s.reason
		}
		if s.span != "" {
			bySpan[s.span] = s
		}
		if s.spawnThread != "" {
			byChild[s.thread+"\x00"+s.spawnThread] = s
		}
	}
	existing, err := a.store.queryWorkers(`WHERE agent_id=? AND (ended_at='' OR ended_at>=?) ORDER BY id`, agent, since)
	if err != nil {
		return err
	}
	byKey := map[string]ConversationWorker{}
	latest := map[string]ConversationWorker{} // thread id → newest incarnation
	for _, w := range existing {
		byKey[w.ThreadID+"\x00"+w.incarnation] = w
		if prev, ok := latest[w.ThreadID]; !ok || prev.ID < w.ID {
			latest[w.ThreadID] = w
		}
	}
	// Parents before children so nested workers inherit their conversation.
	sorted := append([]sdk.ObservedThread(nil), threads...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Depth < sorted[j].Depth })
	seen := map[int64]bool{}
	chosen := map[string]int64{} // thread id → row used for it in this pass
	var changed []ConversationWorker
	for _, t := range sorted {
		if t.ID == "" || conversationThread(t.ID) {
			continue
		}
		parent := t.ParentID
		span := ""
		if t.SpawnedBy != nil {
			if t.SpawnedBy.ThreadID != "" {
				parent = t.SpawnedBy.ThreadID
			}
			span = t.SpawnedBy.ToolSpanID
		}
		if parent == "" {
			continue
		}
		incarnation := ""
		if span != "" {
			incarnation = "span:" + span
		} else if !t.StartedAt.IsZero() {
			incarnation = "start:" + workerTimeString(t.StartedAt)
		}
		var w ConversationWorker
		found := false
		if incarnation != "" {
			w, found = byKey[t.ID+"\x00"+incarnation]
		}
		if !found {
			// The spawn span can arrive after the thread is first listed (Core
			// reports worker.created a moment later), and a start time can
			// shift between lists. An open row for this thread without a
			// different span is the same helper: adopt it.
			if prev, ok := latest[t.ID]; ok && prev.EndedAt == "" && (span == "" || !strings.HasPrefix(prev.incarnation, "span:") || prev.incarnation == incarnation) {
				w, found = prev, true
			}
		}
		if !found {
			chat, call, depth := "", "", 0
			if s, ok := bySpan[span]; ok && span != "" {
				chat, call, depth = s.chat, s.callID, 1
			} else if pw, ok := latest[parent]; ok {
				chat, depth = pw.ConversationID, pw.Depth+1
			} else if s, ok := byChild[parent+"\x00"+t.ID]; ok && conversationThread(parent) {
				chat, call, depth = s.chat, s.callID, 1
			}
			if chat == "" {
				continue
			}
			if incarnation == "" {
				incarnation = "start:" + workerTimeString(time.Now())
			}
			w = ConversationWorker{ConversationID: chat, AgentID: agent, ThreadID: t.ID, ParentThreadID: parent, SpawnCallID: call, Depth: depth, incarnation: incarnation}
		}
		before := w
		if span != "" {
			// The span is the identity once known.
			w.incarnation = incarnation
		}
		if w.Label == "" && w.SpawnCallID != "" {
			w.Label = truncateActivity(byCall[w.ConversationID+"\x00"+w.SpawnCallID], 200)
		}
		applyObservedThread(&w, t)
		if found && workerEqual(before, w) {
			seen[w.ID], chosen[w.ThreadID] = true, w.ID
			continue
		}
		saved, err := a.store.saveWorker(w)
		if err != nil {
			return err
		}
		seen[saved.ID], chosen[saved.ThreadID] = true, saved.ID
		byKey[saved.ThreadID+"\x00"+saved.incarnation] = saved
		if prev, ok := latest[saved.ThreadID]; !ok || prev.ID <= saved.ID {
			latest[saved.ThreadID] = saved
		}
		changed = append(changed, saved)
	}
	if complete {
		// An unfinished worker Core no longer lists, not even as ended, was
		// lost (a restart or a kill without telemetry).
		listed := map[string]bool{}
		for _, t := range threads {
			listed[t.ID] = true
		}
		for _, w := range existing {
			// Still listed, and no other row took the thread: keep it.
			if w.EndedAt != "" || seen[w.ID] || (listed[w.ThreadID] && chosen[w.ThreadID] == 0) {
				continue
			}
			w.State, w.Outcome, w.EndedAt, w.Current = sdk.ThreadStateStopped, "lost", activityTime(time.Now()), nil
			saved, err := a.store.saveWorker(w)
			if err != nil {
				return err
			}
			changed = append(changed, saved)
		}
	}
	a.enrichWorkers(changed)
	for i := range changed {
		w := changed[i]
		a.hub.publishStream(StreamFrame{Type: "stream", ConversationID: w.ConversationID, AgentID: w.AgentID, ThreadID: w.ThreadID, CreatedAt: time.Now().UTC(), Worker: &w})
	}
	return nil
}

func applyObservedThread(w *ConversationWorker, t sdk.ObservedThread) {
	if w.Outcome == workerOutcomeStoppedByOperator && w.EndedAt != "" {
		// Stopped here: the kill can reach the thread list a moment later.
		return
	}
	w.Name = truncateActivity(t.Name, 200)
	if t.Purpose != "" {
		w.Purpose = truncateActivity(t.Purpose, 300)
	}
	state := t.State
	if state == "" {
		state = map[string]string{"running": sdk.ThreadStateWorking, "sleeping": sdk.ThreadStateSleeping, "waiting": sdk.ThreadStateWaiting, "paused": sdk.ThreadStatePaused, "stopped": sdk.ThreadStateStopped}[t.Status]
	}
	if t.Ended != nil && unfinishedWorkerState(state) {
		state = threadEndState(t.Ended.Outcome)
	}
	if state != "" {
		w.State = state
	}
	if t.Model != "" {
		w.Model = truncateActivity(t.Model, 120)
	}
	w.NextWakeAt = workerTimeString(t.NextWakeAt)
	if s := workerTimeString(t.StartedAt); s != "" && w.StartedAt == "" {
		w.StartedAt = s
	}
	if w.StartedAt == "" {
		w.StartedAt = activityTime(time.Now())
	}
	if s := workerTimeString(t.LastActivityAt); s != "" {
		w.LastActivityAt = s
	}
	previous := w.Current
	w.Current = nil
	if t.Current != nil && t.Current.Kind != "" && unfinishedWorkerState(w.State) {
		w.Current = &WorkerCurrent{Kind: t.Current.Kind, Tool: truncateActivity(t.Current.Tool, 256), Since: workerTimeString(t.Current.Since)}
		if w.Current.sameStep(previous) {
			// The reason was read from the step's activity once.
			w.Current.Reason = previous.Reason
		}
	}
	if t.Ended != nil {
		w.EndedAt = workerTimeString(t.Ended.At)
		if w.EndedAt == "" {
			w.EndedAt = activityTime(time.Now())
		}
		w.Outcome = t.Ended.Outcome
		w.Result = truncateActivity(t.Ended.Result, workerResultLimit)
		w.ResultTruncated = t.Ended.ResultTruncated || len([]rune(strings.TrimSpace(t.Ended.Result))) > workerResultLimit
	} else if !unfinishedWorkerState(w.State) && w.EndedAt == "" {
		w.EndedAt = activityTime(time.Now())
	} else if unfinishedWorkerState(w.State) {
		w.EndedAt, w.Outcome = "", ""
	}
}

func threadEndState(outcome string) string {
	switch outcome {
	case "failed":
		return sdk.ThreadStateFailed
	case "cancelled":
		return sdk.ThreadStateStopped
	}
	return sdk.ThreadStateDone
}

func workerEqual(a, b ConversationWorker) bool {
	ac, _ := json.Marshal(a.Current)
	bc, _ := json.Marshal(b.Current)
	a.Current, b.Current = nil, nil
	return a == b && string(ac) == string(bc)
}

func (s *store) saveWorker(w ConversationWorker) (ConversationWorker, error) {
	current := ""
	if w.Current != nil {
		raw, _ := json.Marshal(w.Current)
		current = string(raw)
	}
	if w.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO conversation_workers(conversation_id,agent_id,thread_id,incarnation,parent_thread_id,spawn_call_id,depth,name,purpose,state,current_json,model,next_wake_at,started_at,last_activity_at,ended_at,outcome,result,result_truncated,label,tokens,tool_steps)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(agent_id,thread_id,incarnation) DO NOTHING`,
			w.ConversationID, w.AgentID, w.ThreadID, w.incarnation, w.ParentThreadID, w.SpawnCallID, w.Depth, w.Name, w.Purpose, w.State, current,
			w.Model, w.NextWakeAt, w.StartedAt, w.LastActivityAt, w.EndedAt, w.Outcome, w.Result, boolToInt(w.ResultTruncated), w.Label, w.Tokens, w.ToolSteps)
		if err != nil {
			return w, err
		}
		if id, _ := res.LastInsertId(); id > 0 {
			w.ID, w.Revision = id, 1
			return w, nil
		}
		return scanWorker(s.db.QueryRow(`SELECT `+workerColumns+` FROM conversation_workers WHERE agent_id=? AND thread_id=? AND incarnation=?`, w.AgentID, w.ThreadID, w.incarnation))
	}
	w.Revision++
	_, err := s.db.Exec(`UPDATE conversation_workers SET incarnation=?,name=?,purpose=?,state=?,current_json=?,model=?,next_wake_at=?,started_at=?,last_activity_at=?,ended_at=?,outcome=?,result=?,result_truncated=?,revision=?,label=?,tokens=?,tool_steps=? WHERE id=?`,
		w.incarnation, w.Name, w.Purpose, w.State, current, w.Model, w.NextWakeAt, w.StartedAt, w.LastActivityAt, w.EndedAt, w.Outcome, w.Result, boolToInt(w.ResultTruncated), w.Revision, w.Label, w.Tokens, w.ToolSteps, w.ID)
	return w, err
}

// workerViewer decides what a request may see about workers: nothing in a
// public conversation or for an application user, everything for an operator.
func workerViewer(r *http.Request, conv *Conversation) bool {
	return conv != nil && conv.Audience == "operator" && delegatedFrom(r) == nil
}

func (a *App) handleWorkers(w http.ResponseWriter, r *http.Request) {
	conv, err := a.authorizeConversation(r, r.URL.Query().Get("chat_id"))
	if err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	if !workerViewer(r, conv) {
		writeJSON(w, []ConversationWorker{})
		return
	}
	rows, err := a.store.conversationWorkers(conv.ID)
	if err != nil {
		http.Error(w, "workers unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, rows)
}

// WorkerStep is one step of a worker shown in its expanded card.
type WorkerStep struct {
	Kind       string `json:"kind"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Status     string `json:"status"`
	Model      string `json:"model,omitempty"`
	Tool       string `json:"tool,omitempty"`
	ChildID    string `json:"child_thread_id,omitempty"`
	To         string `json:"to,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Text       string `json:"text,omitempty"`
	Arguments  string `json:"arguments,omitempty"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// handleWorkerActivity pages one linked worker's steps, newest first.
// detail=1 adds tool arguments and results.
func (a *App) handleWorkerActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	conv, err := a.authorizeConversation(r, q.Get("chat_id"))
	if err != nil || !workerViewer(r, conv) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	agent, _ := strconv.ParseInt(q.Get("agent_id"), 10, 64)
	thread := q.Get("thread_id")
	var linked int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM conversation_workers WHERE conversation_id=? AND agent_id=? AND thread_id=?`, conv.ID, agent, thread).Scan(&linked); err != nil || linked == 0 {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}
	obs := a.workerObserverFor()
	if obs == nil {
		http.Error(w, "thread activity unavailable", http.StatusNotImplemented)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	detail := q.Get("detail") == "1"
	query := sdk.ThreadActivityQuery{ProjectID: conv.ProjectID, AgentID: agent, ThreadID: thread, Cursor: q.Get("cursor"), Limit: limit, Projection: "content"}
	page, err := obs.GetThreadActivity(r.Context(), query)
	if err != nil && !errors.Is(err, sdk.ErrThreadObservationUnsupported) {
		// Content needs telemetry.read on an agent the installing user owns;
		// names and timing still help.
		query.Projection = "metadata"
		page, err = obs.GetThreadActivity(r.Context(), query)
	}
	if err != nil || page == nil {
		status := http.StatusBadGateway
		if errors.Is(err, sdk.ErrThreadObservationUnsupported) {
			status = http.StatusNotImplemented
		}
		http.Error(w, "thread activity unavailable", status)
		return
	}
	steps := make([]WorkerStep, 0, len(page.Steps))
	for _, s := range page.Steps {
		step := WorkerStep{Kind: s.Kind, StartedAt: workerTimeString(s.StartedAt), EndedAt: workerTimeString(s.EndedAt), DurationMS: s.DurationMS,
			Status: s.Status, Model: s.Model, Tool: s.Tool, ChildID: s.ChildThreadID, To: s.To, Outcome: s.Outcome,
			Reason: s.Reason, Text: s.Text, Error: s.Error, Truncated: s.TextTruncated}
		if detail {
			step.Arguments, step.Result = s.Arguments, s.Result
			step.Truncated = step.Truncated || s.ArgumentsTruncated || s.ResultTruncated
		}
		steps = append(steps, step)
	}
	writeJSON(w, map[string]any{"steps": steps, "next_cursor": page.NextCursor, "projection": page.Projection})
}

func (a *App) workerObserverFor() sdk.ThreadObserver {
	if a.workers != nil && a.workers.observer != nil {
		return a.workers.observer()
	}
	return a.workerObserver()
}

// AgentWorkThread is one thread in the Agent work section.
type AgentWorkThread struct {
	ID               string         `json:"id"`
	ParentID         string         `json:"parent_id,omitempty"`
	Kind             string         `json:"kind"` // main, chat, voice or worker
	Name             string         `json:"name,omitempty"`
	Purpose          string         `json:"purpose,omitempty"`
	State            string         `json:"state"`
	NextWakeAt       string         `json:"next_wake_at,omitempty"`
	Current          *WorkerCurrent `json:"current,omitempty"`
	StartedAt        string         `json:"started_at,omitempty"`
	EndedAt          string         `json:"ended_at,omitempty"`
	Outcome          string         `json:"outcome,omitempty"`
	BlockedReason    string         `json:"blocked_reason,omitempty"`
	ConversationID   string         `json:"chat_id,omitempty"`
	Title            string         `json:"title,omitempty"`
	ThisConversation bool           `json:"this_conversation,omitempty"`
	// Helpers linked to a conversation: their title, and the row id this
	// conversation can stop them by.
	Label     string `json:"label,omitempty"`
	WorkerID  int64  `json:"worker_id,omitempty"`
	Tokens    int64  `json:"tokens,omitempty"`
	ToolSteps int    `json:"tool_steps,omitempty"`
}

// handleAgentWork lists each participant agent's threads for operators.
func (a *App) handleAgentWork(w http.ResponseWriter, r *http.Request) {
	conv, err := a.authorizeConversation(r, r.URL.Query().Get("chat_id"))
	if err != nil || !workerViewer(r, conv) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	obs := a.workerObserverFor()
	if obs == nil {
		writeJSON(w, map[string]any{"available": false, "agents": []any{}})
		return
	}
	agents, err := a.store.AgentParticipants(conv.ID)
	if err != nil {
		http.Error(w, "agents unavailable", http.StatusInternalServerError)
		return
	}
	type agentWork struct {
		AgentID   int64             `json:"agent_id"`
		Available bool              `json:"available"`
		Threads   []AgentWorkThread `json:"threads"`
	}
	out := []agentWork{}
	available := false
	for _, agent := range agents {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		top, err := obs.GetAgentThreads(ctx, sdk.AgentThreadsQuery{ProjectID: conv.ProjectID, AgentID: agent, IncludeEnded: workerEndedWindow})
		cancel()
		item := agentWork{AgentID: agent, Threads: []AgentWorkThread{}}
		if err == nil && top != nil && top.ThreadsAvailable && top.ThreadSchema > 0 {
			item.Available, available = true, true
			item.Threads = a.agentWorkThreads(agent, conv.ID, top.Threads)
		}
		out = append(out, item)
	}
	writeJSON(w, map[string]any{"available": available, "agents": out})
}

func (a *App) agentWorkThreads(agent int64, current string, threads []sdk.ObservedThread) []AgentWorkThread {
	chats := map[string][2]string{} // thread id → conversation id, title
	if rows, err := a.store.db.Query(`SELECT t.thread_id,c.id,c.title FROM conversation_agent_threads t JOIN conversations c ON c.id=t.conversation_id WHERE t.agent_id=? AND c.archived_at IS NULL AND c.audience<>'public'`, agent); err == nil {
		for rows.Next() {
			var thread, id, title string
			if rows.Scan(&thread, &id, &title) == nil {
				chats[thread] = [2]string{id, title}
			}
		}
		_ = rows.Close()
	}
	if id, title, ok := a.store.mainConversationFor(agent); ok {
		chats[mainThreadID] = [2]string{id, title}
	}
	linked := map[string]ConversationWorker{} // thread id → newest linked row
	if rows, err := a.store.queryWorkers(`WHERE agent_id=? AND (ended_at='' OR ended_at>=?) ORDER BY id`, agent, activityTime(time.Now().Add(-workerEndedWindow))); err == nil {
		for _, w := range rows {
			linked[w.ThreadID] = w
		}
	}
	out := make([]AgentWorkThread, 0, len(threads))
	for _, t := range threads {
		kind := "worker"
		switch {
		case t.ID == mainThreadID:
			kind = "main"
		case strings.HasPrefix(t.ID, "chat-"):
			kind = "chat"
		case strings.HasPrefix(t.ID, "voice-"):
			kind = "voice"
		}
		state := t.State
		if state == "" {
			state = t.Status
		}
		item := AgentWorkThread{ID: t.ID, ParentID: t.ParentID, Kind: kind, Name: t.Name, Purpose: t.Purpose, State: state,
			NextWakeAt: workerTimeString(t.NextWakeAt), StartedAt: workerTimeString(t.StartedAt), BlockedReason: t.BlockedReason}
		if t.SpawnedBy != nil && t.SpawnedBy.ThreadID != "" {
			item.ParentID = t.SpawnedBy.ThreadID
		}
		if t.Current != nil && t.Current.Kind != "" {
			item.Current = &WorkerCurrent{Kind: t.Current.Kind, Tool: t.Current.Tool, Since: workerTimeString(t.Current.Since)}
		}
		if t.Ended != nil {
			item.EndedAt, item.Outcome = workerTimeString(t.Ended.At), t.Ended.Outcome
			item.Current = nil
		}
		if c, ok := chats[t.ID]; ok && kind != "worker" {
			// Public conversations are left out above: their titles can carry
			// visitor details.
			item.ConversationID, item.Title, item.ThisConversation = c[0], c[1], c[0] == current
		}
		if w, ok := linked[t.ID]; ok && kind == "worker" {
			item.Label, item.ConversationID, item.ThisConversation = w.Label, w.ConversationID, w.ConversationID == current
			item.Tokens, item.ToolSteps = w.Tokens, w.ToolSteps
			if item.ThisConversation {
				item.WorkerID = w.ID
			}
			if item.Current != nil && w.Current != nil && item.Current.Kind == w.Current.Kind && item.Current.Tool == truncateActivity(w.Current.Tool, 256) {
				item.Current.Reason = w.Current.Reason
			}
		}
		out = append(out, item)
	}
	return out
}

// mainConversationFor returns the conversation main answers for this agent.
func (s *store) mainConversationFor(agent int64) (string, string, bool) {
	var id, title string
	err := s.db.QueryRow(`SELECT c.id,c.title FROM conversation_agent_threads t JOIN conversations c ON c.id=t.conversation_id WHERE t.agent_id=? AND t.thread_id=? AND c.archived_at IS NULL AND c.audience<>'public' LIMIT 1`, agent, mainThreadID).Scan(&id, &title)
	if err == sql.ErrNoRows || err != nil {
		return "", "", false
	}
	return id, title, true
}
