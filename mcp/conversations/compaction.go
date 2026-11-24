package main

// compaction.go — show Core's context compaction in the conversation.
//
// When a thread's context gets too large, Core shrinks it before the next
// model call. Emergency recovery (a request refused as over budget) can take
// a minute or more with nothing else to show, so the chat looked stuck. The
// compaction is recorded as one activity row named context_compaction: it is
// running while Core compacts and is completed (or failed) afterwards, so it
// is both the live indicator and the marker left in the timeline. It is not a
// message: no unread count, no notification, nothing the agent reads.
//
// Core events:
//   - emergency recovery starts with llm.provider_error class
//     context_length_exceeded, reports each pass with llm.context_recovery,
//     and ends when the retried call starts (llm.start) or the turn fails
//     (llm.error);
//   - planned compaction emits llm.compaction_started and
//     llm.compaction_done (llm.compaction_failed falls back to trimming and
//     is still followed by llm.compaction_done).
//
// Ownership follows tool activity: main's compaction appears only while
// main owns the conversation's open response; a chat-* thread belongs to
// its conversation.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

const compactionActivityName = "context_compaction"

// compactionEvents are the Core events this file consumes, added to the
// telemetry subscription for chat and main threads.
var compactionEvents = []string{"llm.compaction_started", "llm.compaction_done", "llm.context_recovery"}

type openCompaction struct {
	chat      string
	id        int64
	callID    string
	recovery  bool
	startedAt time.Time
	before    int
	after     int
}

func compactionKey(agent int64, thread string) string {
	return strconv.FormatInt(agent, 10) + "\x00" + thread
}

type compactionData struct {
	Class  string `json:"class"`
	Before int    `json:"before_tokens_est"`
	After  int    `json:"after_tokens_est"`
}

// ingestCompaction opens, updates and closes the compaction row for one
// telemetry event. Events that do not concern compaction are no-ops.
func (a *App) ingestCompaction(event string, agent int64, thread, data string, ts time.Time) error {
	switch event {
	case "llm.provider_error", "llm.compaction_started", "llm.context_recovery", "llm.compaction_done",
		"llm.start", "llm.error", "llm.err", "thread.done":
	default:
		return nil
	}
	var d compactionData
	_ = json.Unmarshal([]byte(data), &d)
	key := compactionKey(agent, thread)
	a.compactionMu.Lock()
	open := a.compactions[key]
	a.compactionMu.Unlock()

	switch event {
	case "llm.provider_error":
		if d.Class != "context_length_exceeded" || open != nil {
			return nil
		}
		return a.startCompaction(key, agent, thread, ts, true)
	case "llm.compaction_started":
		if open != nil {
			return nil
		}
		return a.startCompaction(key, agent, thread, ts, false)
	case "llm.context_recovery":
		if open != nil {
			a.compactionMu.Lock()
			if open.before == 0 {
				open.before = d.Before
			}
			if d.After > 0 {
				open.after = d.After
			}
			a.compactionMu.Unlock()
		}
		return nil
	case "llm.compaction_done":
		if open == nil {
			return nil
		}
		a.compactionMu.Lock()
		if open.before == 0 {
			open.before = d.Before
		}
		if d.After > 0 {
			open.after = d.After
		}
		a.compactionMu.Unlock()
		return a.finishCompaction(key, agent, thread, ts, "completed")
	case "llm.start":
		if open == nil || !open.recovery {
			return nil
		}
		return a.finishCompaction(key, agent, thread, ts, "completed")
	case "llm.error", "llm.err":
		if open == nil {
			return nil
		}
		return a.finishCompaction(key, agent, thread, ts, "failed")
	case "thread.done":
		if open == nil {
			return nil
		}
		return a.finishCompaction(key, agent, thread, ts, "completed")
	}
	return nil
}

func (a *App) compactionChat(agent int64, thread string) string {
	if a.streamer == nil || a.streamer.resolve == nil {
		return ""
	}
	if thread == mainThreadID {
		// Main also compacts during background work; only a compaction inside
		// the response to this conversation belongs here.
		if owned, received := a.streamer.mainResponseChat(agent); received {
			return owned
		}
		return ""
	}
	return a.streamer.resolve(agent, thread)
}

func (a *App) startCompaction(key string, agent int64, thread string, ts time.Time, recovery bool) error {
	chat := a.compactionChat(agent, thread)
	if chat == "" {
		return nil
	}
	at := activityTime(ts)
	call := "compaction-" + strconv.FormatInt(ts.UnixNano(), 10)
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A replayed start reuses the row (same call id and start time).
	if _, err = tx.Exec(`INSERT INTO conversation_tool_activity(conversation_id,agent_id,thread_id,call_id,name,reason,started_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		chat, agent, thread, call, compactionActivityName, "", at); err != nil {
		return err
	}
	item, err := scanActivity(tx.QueryRow(`SELECT `+activityColumns+` FROM conversation_tool_activity WHERE conversation_id=? AND agent_id=? AND thread_id=? AND call_id=? AND started_at=?`, chat, agent, thread, call, at))
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if item.Status != "running" {
		return nil
	}
	a.compactionMu.Lock()
	if a.compactions == nil {
		a.compactions = map[string]*openCompaction{}
	}
	a.compactions[key] = &openCompaction{chat: chat, id: item.ID, callID: call, recovery: recovery, startedAt: ts}
	a.compactionMu.Unlock()
	a.hub.publishStream(StreamFrame{Type: "stream", ConversationID: chat, AgentID: agent, ThreadID: thread, CallID: call, CreatedAt: ts, Activity: &item})
	return nil
}

func (a *App) finishCompaction(key string, agent int64, thread string, ts time.Time, status string) error {
	a.compactionMu.Lock()
	open := a.compactions[key]
	delete(a.compactions, key)
	a.compactionMu.Unlock()
	if open == nil {
		return nil
	}
	item, err := scanActivity(a.store.db.QueryRow(`SELECT `+activityColumns+` FROM conversation_tool_activity WHERE id=?`, open.id))
	if err != nil || item.Status != "running" {
		return nil
	}
	item.Status = status
	item.EndedAt = activityTime(ts)
	duration := float64(ts.Sub(open.startedAt).Milliseconds())
	if duration < 0 {
		duration = 0
	}
	item.DurationMs = &duration
	item.Reason = compactionSizes(open.before, open.after)
	item.Revision++
	if _, err := a.store.db.Exec(`UPDATE conversation_tool_activity SET status=?,ended_at=?,revision=?,duration_ms=?,reason=? WHERE id=?`,
		item.Status, item.EndedAt, item.Revision, item.DurationMs, item.Reason, item.ID); err != nil {
		return err
	}
	a.hub.publishStream(StreamFrame{Type: "stream", ConversationID: open.chat, AgentID: agent, ThreadID: thread, CallID: open.callID, CreatedAt: ts, Activity: &item})
	return nil
}

// compactionSizes is the language-neutral detail kept on the row, such as
// "77k → 24k". The UI shows a localized label and puts this in Details.
func compactionSizes(before, after int) string {
	if before <= 0 || after <= 0 {
		return ""
	}
	return fmt.Sprintf("%s → %s", compactTokens(before), compactTokens(after))
}

func compactTokens(n int) string {
	if n >= 1000 {
		return strconv.Itoa((n+500)/1000) + "k"
	}
	return strconv.Itoa(n)
}
