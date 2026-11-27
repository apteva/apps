package main

// Stopping a helper from the conversation. Only an operator of the
// conversation the helper is linked to can stop it. The helpers it started
// are stopped with it, and the thread that spawned it is told, so the agent
// says what was and was not done instead of starting it again.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// threadKiller is the part of the platform client a stop needs.
type threadKiller interface {
	KillThread(agentID int64, threadID string) error
}

// stopPlatform returns the platform client for a project; tests replace it.
var stopPlatform = func(project string) (threadKiller, sdk.AgentEventClient) {
	if mountedCtx == nil {
		return nil, nil
	}
	app := mountedCtx.WithProject(project)
	return app.PlatformAPI(), app.AgentEventsAPI()
}

func (a *App) handleStopWorker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ChatID   string `json:"chat_id"`
		WorkerID int64  `json:"worker_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil || body.WorkerID <= 0 {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	conv, err := a.authorizeConversation(r, body.ChatID)
	if err != nil || !workerViewer(r, conv) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	found, err := a.store.queryWorkers(`WHERE conversation_id=? AND id=?`, conv.ID, body.WorkerID)
	if err != nil || len(found) == 0 {
		http.Error(w, "helper not found", http.StatusNotFound)
		return
	}
	target := found[0]
	if target.EndedAt != "" || !unfinishedWorkerState(target.State) {
		http.Error(w, "helper already ended", http.StatusConflict)
		return
	}
	killer, events := stopPlatform(conv.ProjectID)
	if killer == nil {
		http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
		return
	}
	open, err := a.store.queryWorkers(`WHERE conversation_id=? AND agent_id=? AND ended_at='' ORDER BY id`, conv.ID, target.AgentID)
	if err != nil {
		http.Error(w, "helpers unavailable", http.StatusInternalServerError)
		return
	}
	subtree := workerSubtree(target, open)
	// Children first, so none reports into a parent that is being stopped.
	for i := len(subtree) - 1; i >= 0; i-- {
		err := killer.KillThread(subtree[i].AgentID, subtree[i].ThreadID)
		if err != nil && !voiceThreadGone(err) {
			if i == 0 {
				http.Error(w, "could not stop the helper: "+err.Error(), http.StatusBadGateway)
				return
			}
			if mountedCtx != nil {
				mountedCtx.Logger().Warn("stopping a nested helper failed", "thread_id", subtree[i].ThreadID, "err", err)
			}
		}
	}
	now := activityTime(time.Now())
	stopped := make([]ConversationWorker, 0, len(subtree))
	for _, wk := range subtree {
		wk.State, wk.Outcome, wk.EndedAt, wk.Current, wk.NextWakeAt = sdk.ThreadStateStopped, workerOutcomeStoppedByOperator, now, nil, ""
		saved, err := a.store.saveWorker(wk)
		if err != nil {
			http.Error(w, "could not save the helper", http.StatusInternalServerError)
			return
		}
		stopped = append(stopped, saved)
		a.hub.publishStream(StreamFrame{Type: "stream", ConversationID: saved.ConversationID, AgentID: saved.AgentID, ThreadID: saved.ThreadID, CreatedAt: time.Now().UTC(), Worker: &saved})
	}
	notified := a.notifyWorkerStopped(events, conv, stopped[0], len(stopped)-1)
	writeJSON(w, map[string]any{"workers": stopped, "parent_notified": notified})
}

// workerSubtree returns target and its open descendants, parents first.
func workerSubtree(target ConversationWorker, open []ConversationWorker) []ConversationWorker {
	out := []ConversationWorker{target}
	seen := map[string]bool{target.ThreadID: true}
	for i := 0; i < len(out); i++ {
		for _, w := range open {
			if w.ParentThreadID == out[i].ThreadID && !seen[w.ThreadID] {
				seen[w.ThreadID] = true
				out = append(out, w)
			}
		}
	}
	return out
}

// workerTitle is how a helper is named to the agent and in the UI.
func workerTitle(w ConversationWorker) string {
	if w.Label != "" {
		return w.Label
	}
	if w.Name != "" {
		return w.Name
	}
	return w.ThreadID
}

// notifyWorkerStopped tells the thread that spawned the helper.
func (a *App) notifyWorkerStopped(events sdk.AgentEventClient, conv *Conversation, wk ConversationWorker, nested int) bool {
	if events == nil || wk.ParentThreadID == "" {
		return false
	}
	text := fmt.Sprintf("The operator stopped your helper %s (%q) from conversation %s.", wk.ThreadID, workerTitle(wk), conv.ID)
	if nested > 0 {
		text += fmt.Sprintf(" The %d helper(s) it started were stopped with it.", nested)
	}
	text += " Do not start it again unless the person asks. Tell the person in that conversation what this helper had and had not finished, then carry on with the rest of the request."
	var afterID int64
	_ = a.store.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM messages WHERE conversation_id=?`, conv.ID).Scan(&afterID)
	id := fmt.Sprintf("conversation:%s:worker:%d:stopped", conv.ID, wk.ID)
	receipt, err := events.SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: wk.AgentID, ThreadID: wk.ParentThreadID, SourceEventID: id, Message: text})
	if err != nil || receipt == nil || (!receipt.Accepted && !receipt.Duplicate) {
		if mountedCtx != nil {
			mountedCtx.Logger().Warn("telling the parent about a stopped helper failed", "thread_id", wk.ParentThreadID, "err", err)
		}
		return false
	}
	if conversationThread(wk.ParentThreadID) && !receipt.Duplicate {
		// The conversation's own thread answers: show it working.
		a.streamer.emitEventAck(conv.ID, wk.ParentThreadID, wk.AgentID, afterID, text)
	}
	return true
}
