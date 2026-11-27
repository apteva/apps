package main

// Helper details read from thread activity: what a helper's running step is
// doing (the call's "_reason"), the title of a helper another helper spawned,
// and the tokens and tool calls a helper used once it ended. Each lookup runs
// once per step or helper, in parallel, bounded by a short timeout.

import (
	"context"
	"strconv"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	workerEnrichTimeout = 4 * time.Second
	// workerUsagePages bounds the steps read to sum a helper's usage.
	workerUsagePages = 10
	workerTriedCap   = 4096
	// workerOutcomeStoppedByOperator marks a helper the person stopped.
	workerOutcomeStoppedByOperator = "stopped_by_operator"
)

// firstTry reports whether key has not been looked up yet, and records it.
// An unchanged step, or a helper whose activity could not be read, is not
// fetched again on every snapshot.
func (ws *workerWatches) firstTry(key string) bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.tried == nil || len(ws.tried) >= workerTriedCap {
		ws.tried = map[string]bool{}
	}
	if ws.tried[key] {
		return false
	}
	ws.tried[key] = true
	return true
}

type workerEnrichment struct {
	reason, label string
	tokens        int64
	toolSteps     int
	usage         bool
}

// enrichWorkers fills in what the thread list does not carry and saves the
// rows that changed. changed is updated in place.
func (a *App) enrichWorkers(changed []ConversationWorker) {
	if len(changed) == 0 || a.workers == nil {
		return
	}
	obs := a.workerObserverFor()
	if obs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), workerEnrichTimeout)
	defer cancel()
	projects := map[string]string{}
	results := make([]workerEnrichment, len(changed))
	var wg sync.WaitGroup
	for i := range changed {
		w := changed[i]
		project, ok := projects[w.ConversationID]
		if !ok {
			_ = a.store.db.QueryRow(`SELECT project_id FROM conversations WHERE id=?`, w.ConversationID).Scan(&project)
			projects[w.ConversationID] = project
		}
		key := strconv.FormatInt(w.AgentID, 10) + "\x00" + w.ThreadID + "\x00" + w.incarnation
		res := &results[i]
		if w.Current != nil && w.Current.Kind == "tool" && w.Current.Reason == "" && a.workers.firstTry("reason\x00"+key+"\x00"+w.Current.Tool+"\x00"+w.Current.Since) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res.reason = stepReason(ctx, obs, project, w.AgentID, w.ThreadID, w.Current.Tool)
			}()
		}
		if w.Label == "" && w.SpawnCallID == "" && w.ParentThreadID != "" && !conversationThread(w.ParentThreadID) && a.workers.firstTry("label\x00"+key) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res.label = spawnReason(ctx, obs, project, w.AgentID, w.ParentThreadID, w.ThreadID)
			}()
		}
		if w.EndedAt != "" && w.Tokens == 0 && w.ToolSteps == 0 && w.Outcome != "lost" && a.workers.firstTry("usage\x00"+key) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res.tokens, res.toolSteps, res.usage = threadUsage(ctx, obs, project, w.AgentID, w.ThreadID, w.StartedAt)
			}()
		}
	}
	wg.Wait()
	for i := range changed {
		res, w := results[i], &changed[i]
		dirty := false
		if res.reason != "" && w.Current != nil {
			w.Current.Reason, dirty = truncateActivity(res.reason, 200), true
		}
		if res.label != "" {
			w.Label, dirty = truncateActivity(res.label, 200), true
		}
		if res.usage && (res.tokens > 0 || res.toolSteps > 0) {
			w.Tokens, w.ToolSteps, dirty = res.tokens, res.toolSteps, true
		}
		if !dirty {
			continue
		}
		if saved, err := a.store.saveWorker(*w); err == nil {
			*w = saved
		}
	}
}

// stepReason returns the reason of the thread's newest call of tool.
func stepReason(ctx context.Context, obs sdk.ThreadObserver, project string, agent int64, thread, tool string) string {
	page, err := obs.GetThreadActivity(ctx, sdk.ThreadActivityQuery{ProjectID: project, AgentID: agent, ThreadID: thread, Limit: 5, Projection: "content"})
	if err != nil || page == nil {
		return ""
	}
	for _, s := range page.Steps { // newest first
		if s.Kind == "tool" && s.Tool == tool {
			return s.Reason
		}
	}
	return ""
}

// spawnReason returns the reason of the parent's spawn call that started
// child.
func spawnReason(ctx context.Context, obs sdk.ThreadObserver, project string, agent int64, parent, child string) string {
	page, err := obs.GetThreadActivity(ctx, sdk.ThreadActivityQuery{ProjectID: project, AgentID: agent, ThreadID: parent, Limit: 50, Projection: "content"})
	if err != nil || page == nil {
		return ""
	}
	for _, s := range page.Steps {
		if s.Kind == "tool" && isSpawnTool(s.Tool) && s.ChildThreadID == child {
			return s.Reason
		}
	}
	return ""
}

// threadUsage sums the tokens of a thread's model steps and counts its tool
// calls since started (a reused thread id has older steps). Pace, wait and
// done are bookkeeping, not work.
func threadUsage(ctx context.Context, obs sdk.ThreadObserver, project string, agent int64, thread, started string) (int64, int, bool) {
	since, _ := time.Parse(time.RFC3339Nano, started)
	var tokens int64
	var tools int
	cursor := ""
	for range workerUsagePages {
		page, err := obs.GetThreadActivity(ctx, sdk.ThreadActivityQuery{ProjectID: project, AgentID: agent, ThreadID: thread, Cursor: cursor, Limit: 200, Projection: "metadata"})
		if err != nil || page == nil {
			return tokens, tools, cursor != ""
		}
		older := false
		for _, s := range page.Steps {
			if !since.IsZero() && !s.StartedAt.IsZero() && s.StartedAt.Before(since.Add(-time.Second)) {
				older = true
				break
			}
			switch s.Kind {
			case "model":
				tokens += s.TokensIn + s.TokensOut
			case "tool":
				if !bookkeepingTool(s.Tool) {
					tools++
				}
			}
		}
		if older || page.NextCursor == "" {
			return tokens, tools, true
		}
		cursor = page.NextCursor
	}
	return tokens, tools, true
}

func bookkeepingTool(name string) bool {
	switch name {
	case "pace", "wait", "done", "send", "search_tools":
		return true
	}
	return false
}
