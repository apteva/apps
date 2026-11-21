package main

// lifecycle.go — every chat-* thread has an end.
//
//   - Deleted conversations: their threads are recorded in
//     retired_conversation_threads by the delete trigger. killRetiredThreads
//     stops each one in Core, retrying with backoff until it succeeds or Core
//     reports the thread gone. Main is never retired or killed.
//   - Idle conversations: after idle_thread_days without any message, the
//     thread gets one tracked [conversation idle check] event. A thread that
//     still has ongoing timed work calls conversations_keep_thread; otherwise,
//     an hour later, main receives a short dormancy note and the thread is
//     stopped. The binding stays, marked dormant; the next inbound message
//     recreates the thread through EnsureThread with a resume notice.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	defaultIdleThreadDays = 14
	idleCheckGrace        = time.Hour
	lifecycleBatch        = 50
)

// idleThreadDays reads the install's idle_thread_days setting. 0 disables
// idle checks; deleted conversations are still cleaned up.
func idleThreadDays(app *sdk.AppCtx) int {
	if app == nil {
		return defaultIdleThreadDays
	}
	raw := strings.TrimSpace(app.Config().Get("idle_thread_days"))
	if raw == "" {
		return defaultIdleThreadDays
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 {
		return defaultIdleThreadDays
	}
	return days
}

func (a *App) runThreadLifecycle(app *sdk.AppCtx) error {
	if err := a.killRetiredThreads(app); err != nil {
		return err
	}
	days := idleThreadDays(app)
	if days == 0 {
		return nil
	}
	now := time.Now().UTC()
	if err := a.sendIdleChecks(app, days, now); err != nil {
		return err
	}
	return a.sleepIdleThreads(app, days, now)
}

func killBackoff(attempts int) time.Duration {
	backoff := time.Minute << min(attempts, 9)
	return min(backoff, 6*time.Hour)
}

func sqliteTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// killRetiredThreads stops threads whose conversation was deleted.
func (a *App) killRetiredThreads(app *sdk.AppCtx) error {
	if app == nil {
		return nil
	}
	type retired struct {
		project, thread string
		agent           int64
		attempts        int
	}
	rows, err := a.store.db.Query(`SELECT project_id,agent_id,thread_id,kill_attempts FROM retired_conversation_threads
		WHERE killed_at IS NULL AND thread_id!=? AND (next_kill_at IS NULL OR next_kill_at<=?)
		ORDER BY kill_attempts LIMIT ?`, mainThreadID, sqliteTime(time.Now()), lifecycleBatch)
	if err != nil {
		return err
	}
	var batch []retired
	for rows.Next() {
		var r retired
		if rows.Scan(&r.project, &r.agent, &r.thread, &r.attempts) == nil {
			batch = append(batch, r)
		}
	}
	_ = rows.Close()
	for _, r := range batch {
		err := app.WithProject(r.project).PlatformAPI().KillThread(r.agent, r.thread)
		if err == nil || voiceThreadGone(err) {
			_, _ = a.store.db.Exec(`UPDATE retired_conversation_threads SET killed_at=CURRENT_TIMESTAMP,kill_error=''
				WHERE project_id=? AND agent_id=? AND thread_id=?`, r.project, r.agent, r.thread)
			continue
		}
		next := time.Now().Add(killBackoff(r.attempts + 1))
		_, _ = a.store.db.Exec(`UPDATE retired_conversation_threads SET kill_attempts=kill_attempts+1,kill_error=?,next_kill_at=?
			WHERE project_id=? AND agent_id=? AND thread_id=?`, truncate(err.Error(), 500), sqliteTime(next), r.project, r.agent, r.thread)
		app.Logger().Warn("retired conversation thread not stopped yet; will retry",
			"agent", r.agent, "thread", r.thread, "attempt", r.attempts+1, "err", err)
	}
	return nil
}

// idleCandidates are chat-* bindings whose conversation has been quiet.
// Activity is any message, from the user or the agent. Threads holding
// pending approvals or interactions, queued deliveries or a live voice session
// are never touched.
const idleCandidateWhere = `t.thread_id LIKE 'chat-%' AND t.status='active'
	AND (t.keep_until IS NULL OR julianday(t.keep_until) <= julianday(?))
	AND julianday(COALESCE((SELECT MAX(m.created_at) FROM messages m WHERE m.conversation_id=c.id), c.updated_at)) < julianday(?)
	AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id=c.id AND m.action_status='pending')
	AND NOT EXISTS (SELECT 1 FROM deliveries d JOIN messages m ON m.id=d.message_id
		WHERE m.conversation_id=c.id AND d.status IN ('pending','processing'))
	AND NOT EXISTS (SELECT 1 FROM conversation_voice_sessions v
		WHERE v.conversation_id=c.id AND v.status IN ('starting','active'))`

type idleThread struct {
	conversationID, projectID, title, threadID string
	agentID                                    int64
	checkAt                                    string
}

func (a *App) idleThreads(where string, args ...any) ([]idleThread, error) {
	rows, err := a.store.db.Query(`SELECT c.id,c.project_id,c.title,t.thread_id,t.agent_id,COALESCE(t.idle_check_at,'')
		FROM conversation_agent_threads t JOIN conversations c ON c.id=t.conversation_id
		WHERE `+where+` LIMIT `+strconv.Itoa(lifecycleBatch), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []idleThread
	for rows.Next() {
		var it idleThread
		if err := rows.Scan(&it.conversationID, &it.projectID, &it.title, &it.threadID, &it.agentID, &it.checkAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func idleCheckEventID(it idleThread, cutoff time.Time) string {
	return fmt.Sprintf("conversation:%s:idle-check:%d:%s", it.conversationID, it.agentID, cutoff.Format("20060102"))
}

// sendIdleChecks asks each quiet thread once whether it still has timed work.
func (a *App) sendIdleChecks(app *sdk.AppCtx, days int, now time.Time) error {
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	candidates, err := a.idleThreads(idleCandidateWhere+` AND t.idle_check_at IS NULL`, sqliteTime(now), sqliteTime(cutoff))
	if err != nil {
		return err
	}
	client := app.AgentEventsAPI()
	for _, it := range candidates {
		if client == nil {
			return nil
		}
		eventID := idleCheckEventID(it, now)
		message := fmt.Sprintf("[conversation idle check] Conversation %q (%s) has had no messages for %d days."+
			" If you still have ongoing timed work here, call conversations_keep_thread with conversation_id=%s and a short reason."+
			" Otherwise do nothing: this thread will be stopped and recreated with the full history on the next message.",
			it.title, it.conversationID, days, it.conversationID)
		receipt, err := client.SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: it.agentID, ThreadID: it.threadID, SourceEventID: eventID, Message: message})
		if err != nil {
			if voiceThreadGone(err) {
				// Core no longer has the thread; nothing can be ongoing there.
				a.markDormant(it)
				continue
			}
			app.Logger().Warn("idle check not delivered; will retry", "conversation", it.conversationID, "agent", it.agentID, "err", err)
			continue
		}
		if receipt == nil || (!receipt.Accepted && !receipt.Duplicate) {
			continue
		}
		_, _ = a.store.db.Exec(`UPDATE conversation_agent_threads SET idle_check_at=? WHERE conversation_id=? AND agent_id=? AND status='active'`,
			sqliteTime(now), it.conversationID, it.agentID)
	}
	return nil
}

// sleepIdleThreads stops threads that did not ask to stay after their check.
func (a *App) sleepIdleThreads(app *sdk.AppCtx, days int, now time.Time) error {
	// Any message after the check means the conversation is active again.
	if _, err := a.store.db.Exec(`UPDATE conversation_agent_threads SET idle_check_at=NULL
		WHERE idle_check_at IS NOT NULL AND EXISTS (SELECT 1 FROM messages m
			WHERE m.conversation_id=conversation_agent_threads.conversation_id
			AND julianday(m.created_at) > julianday(conversation_agent_threads.idle_check_at))`); err != nil {
		return err
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	due, err := a.idleThreads(idleCandidateWhere+` AND t.idle_check_at IS NOT NULL AND julianday(t.idle_check_at) <= julianday(?)`,
		sqliteTime(now), sqliteTime(cutoff), sqliteTime(now.Add(-idleCheckGrace)))
	if err != nil {
		return err
	}
	client := app.AgentEventsAPI()
	for _, it := range due {
		if client != nil {
			note := fmt.Sprintf("[conversation dormant] Conversation %q (%s) had no messages for %d days, so its thread was stopped.%s"+
				" It resumes with its full history on the next message. Read conversations_history if you need the details.",
				it.title, it.conversationID, days, a.lastFinalReply(it))
			eventID := fmt.Sprintf("conversation:%s:dormant:%d:%s", it.conversationID, it.agentID, strings.NewReplacer(" ", "T", ":", "").Replace(it.checkAt))
			receipt, err := client.SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: it.agentID, ThreadID: mainThreadID, SourceEventID: eventID, Message: note})
			if err != nil || receipt == nil || (!receipt.Accepted && !receipt.Duplicate) {
				app.Logger().Warn("dormancy note to main not delivered; will retry", "conversation", it.conversationID, "agent", it.agentID, "err", err)
				continue
			}
		}
		err := app.WithProject(it.projectID).PlatformAPI().KillThread(it.agentID, it.threadID)
		if err != nil && !voiceThreadGone(err) {
			app.Logger().Warn("idle conversation thread not stopped yet; will retry", "conversation", it.conversationID, "agent", it.agentID, "err", err)
			continue
		}
		a.markDormant(it)
	}
	return nil
}

func (a *App) lastFinalReply(it idleThread) string {
	var content string
	err := a.store.db.QueryRow(`SELECT content FROM messages WHERE conversation_id=? AND role='agent' AND phase='final' AND content!=''
		ORDER BY id DESC LIMIT 1`, it.conversationID).Scan(&content)
	if err != nil || strings.TrimSpace(content) == "" {
		return ""
	}
	return " Last reply: " + truncate(strings.TrimSpace(content), 400)
}

func (a *App) markDormant(it idleThread) {
	_, _ = a.store.db.Exec(`UPDATE conversation_agent_threads SET status='dormant',dormant_at=CURRENT_TIMESTAMP,idle_check_at=NULL
		WHERE conversation_id=? AND agent_id=? AND thread_id=?`, it.conversationID, it.agentID, it.threadID)
	spawnedThreads.Delete(fmt.Sprintf("%d/%s", it.agentID, it.conversationID))
}

// threadDormant reports whether the agent's chat thread for the conversation
// was stopped after inactivity.
func (s *store) threadDormant(conversationID string, agentID int64) bool {
	var status string
	err := s.db.QueryRow(`SELECT status FROM conversation_agent_threads WHERE conversation_id=? AND agent_id=?`, conversationID, agentID).Scan(&status)
	return err == nil && status == "dormant"
}

// markThreadActive records that a dormant thread was recreated.
func (s *store) markThreadActive(conversationID string, agentID int64) {
	_, _ = s.db.Exec(`UPDATE conversation_agent_threads SET status='active',dormant_at=NULL,idle_check_at=NULL
		WHERE conversation_id=? AND agent_id=? AND status='dormant'`, conversationID, agentID)
}

// withResumeNotice prefixes the first inbound event after dormancy.
func withResumeNotice(payload any, conv *Conversation) any {
	notice := fmt.Sprintf("[conversation resumed] This conversation was inactive, so its thread was restarted."+
		" Read conversations_history conversation_id=%s before answering.\n", conv.ID)
	switch p := payload.(type) {
	case string:
		return notice + p
	case []map[string]any:
		return append([]map[string]any{{"type": "text", "text": strings.TrimSpace(notice)}}, p...)
	}
	return payload
}

// toolKeepThread lets a conversation thread with ongoing timed work stay
// awake through an idle check.
func (a *App) toolKeepThread(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := requireAgentCaller(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := a.requireParticipant(from, stringArg(args, "conversation_id"))
	if err != nil {
		return nil, err
	}
	state, err := a.store.AgentThread(conv.ID, from.AgentID)
	if err == sql.ErrNoRows || (err == nil && (state.ThreadID != from.ThreadID || from.ThreadID == mainThreadID)) {
		return nil, fmt.Errorf("conversations_keep_thread is only available to the conversation's own thread")
	}
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(stringArg(args, "reason"))
	if reason == "" {
		return nil, fmt.Errorf("reason required: name the ongoing timed work that needs this thread")
	}
	days := idleThreadDays(app)
	if days == 0 {
		days = defaultIdleThreadDays
	}
	until := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
	if _, err := a.store.db.Exec(`UPDATE conversation_agent_threads SET keep_until=?,keep_reason=?,idle_check_at=NULL
		WHERE conversation_id=? AND agent_id=?`, sqliteTime(until), truncate(reason, 300), conv.ID, from.AgentID); err != nil {
		return nil, err
	}
	app.Logger().Info("conversation thread kept awake", "conversation", conv.ID, "agent", from.AgentID, "reason", reason, "until", until)
	return map[string]any{"kept_until": until.Format(time.RFC3339), "reason": reason}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
