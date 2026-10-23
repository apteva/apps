package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func transcriptSession(t *testing.T, a *App, chat string) voiceSession {
	t.Helper()
	r := voiceRequest(t, a, http.MethodPost, chat)
	if r.Code != 200 {
		t.Fatalf("voice start %d: %s", r.Code, r.Body.String())
	}
	var session voiceSession
	if err := json.Unmarshal(r.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return session
}

func transcriptEvent(session voiceSession, serial int, role, text, id, status string, start, end int) sdk.TelemetryStreamEvent {
	data, _ := json.Marshal(map[string]any{"text": text, "transcript": voiceTranscript{ID: id, Generation: 1, ItemID: id, Status: status, FinalizationReason: status, StartMS: start, EndMS: end}})
	return sdk.TelemetryStreamEvent{ID: fmt.Sprintf("delivery-%d", serial), AgentID: session.AgentID, ThreadID: session.ThreadID, Type: "realtime." + role, Time: time.Date(2026, 10, 23, 9, 6, 0, serial*1000000, time.UTC), Data: data}
}

// Replay the 13 live checkpoints and 12 shutdown updates observed in the real
// GPT Live session. They represent six segments, never 25 independent turns.
func TestVoiceTranscriptCapturedSessionRevisions(t *testing.T) {
	a, _, platform := newTestEnv(t)
	chat := mkConversation(t, a, 41)
	session := transcriptSession(t, a, chat.ID)
	stream, release := a.hub.subscribeConversation(chat.ID)
	defer release()
	type part struct {
		role, text, id string
		start, end     int
	}
	parts := []part{
		{"assistant", " Hello,", "a0", 10600, 10800}, {"user", " Hello", "u1", 10800, 11000},
		{"assistant", " how's", "a1", 11000, 11200}, {"assistant", " how's it going", "a1", 11000, 11400}, {"assistant", " how's it going?", "a1", 11000, 11600},
		{"user", " I'm", "u2", 19000, 19200}, {"user", " I'm good", "u2", 19000, 19400}, {"user", " I'm good and you", "u2", 19000, 19800},
		{"assistant", " I'm doing", "a11", 20000, 20200}, {"assistant", " good.", "a12", 20200, 20400}, {"assistant", " good. What's", "a12", 20200, 20800}, {"assistant", " good. What's on", "a12", 20200, 21000}, {"assistant", " good. What's on your mind?", "a12", 20200, 21400},
	}
	latest := map[string]part{}
	ids := map[string]int64{}
	revisions := map[string]int64{}
	serial := 0
	ingest := func(p part, status string) {
		t.Helper()
		serial++
		event := transcriptEvent(session, serial, p.role, p.text, p.id, status, p.start, p.end)
		if err := a.ingestVoiceEvent(event); err != nil {
			t.Fatal(err)
		}
		select {
		case message := <-stream:
			if ids[p.id] != 0 && (message.ID != ids[p.id] || message.Revision <= revisions[p.id]) {
				t.Fatalf("revision failed: %+v", message)
			}
			ids[p.id] = message.ID
			revisions[p.id] = message.Revision
		default:
			t.Fatal("missing live message revision")
		}
	}
	for _, p := range parts {
		ingest(p, "application_grouped")
		latest[p.id] = p
	}
	before, err := a.store.MessagePage(chat.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Messages) != 6 {
		t.Fatalf("live checkpoints created %d messages", len(before.Messages))
	}
	if _, err := a.store.db.Exec(`UPDATE conversation_voice_sessions SET status='ended',ended_at=CURRENT_TIMESTAMP WHERE id=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"bridge_disconnected", "stopped"} {
		for _, id := range []string{"a0", "u1", "a1", "u2", "a11", "a12"} {
			p := latest[id]
			serial++
			event := transcriptEvent(session, serial, p.role, p.text, p.id, "interrupted", p.start, p.end)
			var payload map[string]any
			_ = json.Unmarshal(event.Data, &payload)
			payload["transcript"].(map[string]any)["finalization_reason"] = reason
			event.Data, _ = json.Marshal(payload)
			if err := a.ingestVoiceEvent(event); err != nil {
				t.Fatal(err)
			}
			<-stream
		}
	}
	page, err := newStore(a.store.db).MessagePage(chat.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 6 {
		t.Fatalf("25 event deliveries created %d messages", len(page.Messages))
	}
	for i, m := range page.Messages {
		if m.ID != before.Messages[i].ID || !m.CreatedAt.Equal(before.Messages[i].CreatedAt) {
			t.Fatal("updates moved message identity/order")
		}
		if m.Metadata["voice_session_id"] != session.ID || m.Metadata["transcript"] == nil {
			t.Fatal("lost transcript provenance")
		}
	}
	changes, err := a.store.MessageChanges(chat.ID, before.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	changedIDs := map[int64]bool{}
	for _, message := range changes.Messages {
		changedIDs[message.ID] = true
	}
	if len(changedIDs) != 6 {
		t.Fatalf("reconnect must recover six updated identities, got %d", len(changedIDs))
	}
	var deliveries int
	_ = a.store.db.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE message_id IN (SELECT id FROM messages WHERE conversation_id=?)`, chat.ID).Scan(&deliveries)
	if deliveries != 0 || len(platform.events) != 0 || len(platform.ensures) != 0 {
		t.Fatal("transcript revisions dispatched agent turns or deliveries")
	}
	text := a.voiceContextBefore(chat.ID, 100000)
	if strings.Count(text, "I'm good and you") != 1 || strings.Contains(text, "user: I'm\n") {
		t.Fatalf("typed context retained checkpoint duplicates: %s", text)
	}
}

func TestVoiceTranscriptStaleReplayTerminalAndScope(t *testing.T) {
	a, _, _ := newTestEnv(t)
	chat := mkConversation(t, a, 41)
	session := transcriptSession(t, a, chat.ID)
	event := transcriptEvent(session, 5, "assistant", "Complete sentence.", "same-id", "provider_final", 100, 600)
	if err := a.ingestVoiceEvent(event); err != nil {
		t.Fatal(err)
	}
	first, _ := a.store.MessagePage(chat.ID, 0, 100)
	for _, stale := range []sdk.TelemetryStreamEvent{event, transcriptEvent(session, 1, "assistant", "Com", "same-id", "application_grouped", 100, 200), transcriptEvent(session, 9, "assistant", "Complete sentence.", "same-id", "interrupted", 100, 600)} {
		if err := a.ingestVoiceEvent(stale); err != nil {
			t.Fatal(err)
		}
	}
	page, _ := a.store.MessagePage(chat.ID, 0, 100)
	if len(page.Messages) != 1 || page.Messages[0].Content != "Complete sentence." || page.Messages[0].Revision != first.Messages[0].Revision {
		t.Fatal("stale/replayed events changed final transcript")
	}
	blocked := transcriptEvent(session, 10, "assistant", "", "same-id", "blocked", 100, 600)
	if err := a.ingestVoiceEvent(blocked); err != nil {
		t.Fatal(err)
	}
	if err := a.ingestVoiceEvent(transcriptEvent(session, 11, "assistant", "Must stay hidden", "same-id", "provider_final", 100, 600)); err != nil {
		t.Fatal(err)
	}
	page, _ = a.store.MessagePage(chat.ID, 0, 100)
	if len(page.Messages) != 1 || page.Messages[0].Content != "" {
		t.Fatal("blocked transcript was not redacted")
	}
	other := mkConversation(t, a, 42)
	second := transcriptSession(t, a, other.ID)
	if err := a.ingestVoiceEvent(transcriptEvent(second, 1, "user", "Separate chat", "same-id", "provider_final", 100, 600)); err != nil {
		t.Fatal(err)
	}
	otherPage, _ := a.store.MessagePage(other.ID, 0, 100)
	if len(otherPage.Messages) != 1 || otherPage.Messages[0].Content != "Separate chat" {
		t.Fatal("transcript identity leaked across chats/sessions")
	}
	if _, err := a.store.db.Exec(`UPDATE conversation_voice_sessions SET status='ended',ended_at=datetime('now','-1 minute') WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.ingestVoiceEvent(transcriptEvent(second, 2, "user", "Late text", "late", "provider_final", 100, 600)); err != nil {
		t.Fatal(err)
	}
	otherPage, _ = a.store.MessagePage(other.ID, 0, 100)
	if len(otherPage.Messages) != 1 {
		t.Fatal("expired voice binding accepted late text")
	}
}

func TestVoiceTranscriptConcurrentCheckpoints(t *testing.T) {
	a, _, _ := newTestEnv(t)
	chat := mkConversation(t, a, 41)
	session := transcriptSession(t, a, chat.ID)
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errors <- a.ingestVoiceEvent(transcriptEvent(session, i, "user", fmt.Sprintf("Checkpoint %d", i), "same", "application_grouped", 100, 100+i))
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := a.store.MessagePage(chat.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Content != "Checkpoint 20" {
		t.Fatalf("concurrent updates lost the latest checkpoint: %+v", page.Messages)
	}
}
