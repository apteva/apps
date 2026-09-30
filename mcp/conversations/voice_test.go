package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type voiceRuntimePlatform struct {
	*recordingPlatform
	sdk.RuntimeClient
	providers []sdk.RuntimeRealtimeProvider
	err error
}

func (p *voiceRuntimePlatform) ListRuntimeRealtimeProviders(projectID string) ([]sdk.RuntimeRealtimeProvider, error) {
	if projectID != testProject {
		return nil, errors.New("wrong project")
	}
	return p.providers, p.err
}

func TestVoiceModeUsesRealtimePoolNotTextProvider(t *testing.T) {
	platform := &voiceRuntimePlatform{recordingPlatform: &recordingPlatform{}}
	ctx := sdk.NewAppCtxForTest(nil, nil, nil, platform, nil)
	if got := voiceMode(ctx, testProject); got != "dictation" {
		t.Fatalf("empty realtime pool = %q", got)
	}
	platform.providers = []sdk.RuntimeRealtimeProvider{{Name: "openai-realtime"}}
	if got := voiceMode(ctx, testProject); got != "live" {
		t.Fatalf("separate realtime provider = %q", got)
	}
	platform.err = errors.New("catalog unavailable")
	if got := voiceMode(ctx, testProject); got != "dictation" {
		t.Fatalf("unknown realtime capability = %q", got)
	}
}

func voiceRequest(t *testing.T, a *App, method, chatID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/voice?chat_id="+chatID, nil)
	authorizeTestRequest(r)
	w := httptest.NewRecorder()
	a.handleVoice(w, r)
	return w
}

func TestVoiceSessionBindsExistingOperatorChat(t *testing.T) {
	a, _, platform := newTestEnv(t)
	chat := mkConversation(t, a, 41)
	start := voiceRequest(t, a, http.MethodPost, chat.ID)
	if start.Code != 200 {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	var session voiceSession
	if err := json.Unmarshal(start.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.ConversationID != chat.ID || !strings.HasPrefix(session.ThreadID, "voice-") || session.AudioBridgeURL == "" {
		t.Fatalf("unexpected session: %+v", session)
	}
	if len(platform.realtimeSpawns) != 1 || platform.realtimeSpawns[0].AgentID != 41 || platform.realtimeSpawns[0].CapabilityMode != sdk.RealtimeCapabilitiesInheritAgent || platform.realtimeSpawns[0].Ephemeral {
		t.Fatalf("bad spawn: %+v", platform.realtimeSpawns)
	}
	if !strings.Contains(platform.realtimeSpawns[0].Directive, "temporary") {
		t.Fatal("voice directive missing existing-chat contract")
	}
	if again := voiceRequest(t, a, http.MethodPost, chat.ID); again.Code != 409 {
		t.Fatalf("duplicate start: %d", again.Code)
	}
	if got := voiceRequest(t, a, http.MethodGet, chat.ID); got.Code != 200 || !strings.Contains(got.Body.String(), session.ThreadID) || strings.Contains(got.Body.String(), "token=") {
		t.Fatalf("status exposed token or failed: %s", got.Body.String())
	}
	if renewed := voiceRequest(t, a, http.MethodPatch, chat.ID); renewed.Code != 200 || !strings.Contains(renewed.Body.String(), "renewed") {
		t.Fatalf("renew: %d %s", renewed.Code, renewed.Body.String())
	}
	if ended := voiceRequest(t, a, http.MethodDelete, chat.ID); ended.Code != 200 {
		t.Fatalf("end: %d %s", ended.Code, ended.Body.String())
	}
	if len(platform.killed) != 1 || platform.killed[0].ThreadID != session.ThreadID {
		t.Fatalf("thread not killed: %+v", platform.killed)
	}
	if got := voiceRequest(t, a, http.MethodGet, chat.ID); got.Code != 200 || !strings.Contains(got.Body.String(), `"mode":"dictation"`) {
		t.Fatalf("ended status: %s", got.Body.String())
	}
}

func TestVoiceRejectsPublicArchivedAndUnownedChats(t *testing.T) {
	a, _, platform := newTestEnv(t)
	public, err := a.store.CreateConversation(CreateConversationInput{ProjectID: testProject, LeadAgentID: 41, Title: "Public", OwnerUserID: 1, Audience: "public"})
	if err != nil {
		t.Fatal(err)
	}
	if got := voiceRequest(t, a, http.MethodPost, public.ID); got.Code == 200 {
		t.Fatal("public chat started voice")
	}
	chat := mkConversation(t, a, 41)
	r := httptest.NewRequest(http.MethodPost, "/voice?chat_id="+chat.ID, nil)
	r.Header.Set("X-User-ID", "2")
	r.Header.Set("X-Apteva-Project-ID", testProject)
	w := httptest.NewRecorder()
	a.handleVoice(w, r)
	if w.Code == 200 {
		t.Fatal("unowned chat started voice")
	}
	if _, err := a.store.db.Exec(`UPDATE conversations SET archived_at=CURRENT_TIMESTAMP WHERE id=?`, chat.ID); err != nil {
		t.Fatal(err)
	}
	if got := voiceRequest(t, a, http.MethodPost, chat.ID); got.Code == 200 {
		t.Fatal("archived chat started voice")
	}
	if len(platform.realtimeSpawns) != 0 {
		t.Fatalf("unexpected spawn: %+v", platform.realtimeSpawns)
	}
}

func TestVoiceTelemetryIsIdempotentAndNotDeliveredAsNewAgentTurn(t *testing.T) {
	a, _, platform := newTestEnv(t)
	chat := mkConversation(t, a, 41)
	start := voiceRequest(t, a, http.MethodPost, chat.ID)
	var session voiceSession
	if err := json.Unmarshal(start.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	user := sdk.TelemetryStreamEvent{ID: "event-user-1", AgentID: 41, ThreadID: session.ThreadID, Type: "realtime.user", Time: time.Now(), Data: json.RawMessage(`{"text":"What is next?"}`)}
	if err := a.ingestVoiceEvent(user); err != nil {
		t.Fatal(err)
	}
	if err := a.ingestVoiceEvent(user); err != nil {
		t.Fatal(err)
	}
	answer := sdk.TelemetryStreamEvent{ID: "event-agent-1", AgentID: 41, ThreadID: session.ThreadID, Type: "realtime.assistant", Time: time.Now(), Data: json.RawMessage(`{"text":"Next step is review."}`)}
	if err := a.ingestVoiceEvent(answer); err != nil {
		t.Fatal(err)
	}
	page, err := a.store.MessagePage(chat.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].Role != "user" || page.Messages[1].Role != "agent" || page.Messages[0].Revision == 0 {
		t.Fatalf("unexpected voice history: %+v", page.Messages)
	}
	if len(platform.ensures) != 0 || len(platform.events) != 0 {
		t.Fatal("voice telemetry was re-sent as a new agent turn")
	}
	if got, err := a.store.ConversationForAgentThread(testProject, 41, session.ThreadID); err != nil || got == nil || got.ID != chat.ID {
		t.Fatalf("voice tool identity not bound: %+v %v", got, err)
	}
	typed, err := a.store.AppendMessage(&Message{ConversationID: chat.ID, Role: "user", UserID: 1, Content: "Continue in text"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.voiceContextBefore(chat.ID, typed.ID); !strings.Contains(got, "What is next?") || !strings.Contains(got, "Next step is review.") {
		t.Fatalf("typed continuation missing voice context: %s", got)
	}
	next, err := a.store.AppendMessage(&Message{ConversationID: chat.ID, Role: "user", UserID: 1, Content: "Another text turn"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.voiceContextBefore(chat.ID, next.ID); got != "" {
		t.Fatalf("voice context repeated after typed handoff: %s", got)
	}
	_ = voiceRequest(t, a, http.MethodDelete, chat.ID)
	if got, err := a.store.ConversationForAgentThread(testProject, 41, session.ThreadID); err != nil || got != nil {
		t.Fatalf("ended voice thread still bound: %+v %v", got, err)
	}
}

func TestArchiveStopsActiveVoiceBeforeChangingChat(t *testing.T) {
	a, _, platform := newTestEnv(t)
	chat := mkConversation(t, a, 41)
	if got := voiceRequest(t, a, http.MethodPost, chat.ID); got.Code != 200 {
		t.Fatalf("start: %s", got.Body.String())
	}
	r := httptest.NewRequest(http.MethodPatch, "/chats?id="+chat.ID, strings.NewReader(`{"archived":true}`))
	authorizeTestRequest(r)
	w := httptest.NewRecorder()
	a.handleUpdateChat(w, r)
	if w.Code != 200 || len(platform.killed) != 1 {
		t.Fatalf("archive did not stop voice: %d %+v", w.Code, platform.killed)
	}
	var active int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM conversation_voice_sessions WHERE conversation_id=? AND status='active'`, chat.ID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("voice binding survived archive: %d %v", active, err)
	}
}

func TestArchivingDuringSpawnKillsUnattachedVoiceChild(t *testing.T) {
	a, _, platform := newTestEnv(t)
	chat := mkConversation(t, a, 41)
	platform.realtimeSpawnHook = func(_ sdk.RealtimeSpawnRequest) {
		if _, err := a.store.SetConversationArchived(chat.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	got := voiceRequest(t, a, http.MethodPost, chat.ID)
	if got.Code != http.StatusConflict {
		t.Fatalf("spawn into archived chat: %d %s", got.Code, got.Body.String())
	}
	if len(platform.killed) != 1 || len(platform.realtimeSpawns) != 1 || platform.killed[0].ThreadID != platform.realtimeSpawns[0].ThreadID {
		t.Fatalf("spawned voice child not killed: %+v", platform.killed)
	}
	var active int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM conversation_voice_sessions WHERE conversation_id=? AND status IN ('starting','active')`, chat.ID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("voice binding survived archive race: %d %v", active, err)
	}
}

func TestEndedVoiceApprovalResultReturnsToDurableChatThread(t *testing.T) {
	a, ctx, platform := newTestEnv(t)
	chat := mkConversation(t, a, 41)
	start := voiceRequest(t, a, http.MethodPost, chat.ID)
	var session voiceSession
	if err := json.Unmarshal(start.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	msg, err := a.store.AppendMessage(&Message{ConversationID: chat.ID, Role: "agent", AgentID: 41, ThreadID: session.ThreadID, ComponentKind: kindApproval, Content: "May I proceed?", ActionStatus: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	if got := voiceRequest(t, a, http.MethodDelete, chat.ID); got.Code != 200 {
		t.Fatalf("end: %s", got.Body.String())
	}
	if err := (&agentAdapter{app: a}).Deliver(ctx, strings.TrimPrefix(approvalDeliveryTarget(msg), "agent:"), chat, msg); err != nil {
		t.Fatal(err)
	}
	if len(platform.trackedEvents) != 1 || platform.trackedEvents[0].ThreadID != conversationThreadID(chat.ID) {
		t.Fatalf("approval went to ended voice child: %+v", platform.trackedEvents)
	}
	if !strings.Contains(platform.trackedEvents[0].Message.(string), "voice session that has ended") {
		t.Fatalf("missing handoff context: %+v", platform.trackedEvents[0])
	}
}

func TestVoiceToolsStayBoundToSelectedChat(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	chat := mkConversation(t, a, 41)
	other := mkConversation(t, a, 41)
	start := voiceRequest(t, a, http.MethodPost, chat.ID)
	var session voiceSession
	if err := json.Unmarshal(start.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	caller := callerCtx(41, session.ThreadID)
	if _, err := a.toolAlert(caller, ctx, map[string]any{"conversation_id": other.ID, "text": "Wrong chat"}); err == nil {
		t.Fatal("voice child wrote to another chat")
	}
	if _, err := a.toolAlert(caller, ctx, map[string]any{"conversation_id": chat.ID, "text": "Local issue"}); err != nil {
		t.Fatalf("voice child cannot write own chat: %v", err)
	}
	_ = voiceRequest(t, a, http.MethodDelete, chat.ID)
	if _, err := a.toolAlert(caller, ctx, map[string]any{"conversation_id": chat.ID, "text": "Late issue"}); err == nil {
		t.Fatal("ended voice child retained chat authority")
	}
	if _, err := a.toolAlert(context.Background(), ctx, map[string]any{"conversation_id": chat.ID, "text": "No caller"}); err == nil {
		t.Fatal("missing caller gained chat authority")
	}
}
