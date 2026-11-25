package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Agent 77 has no main-routed conversation in newTestEnv, unlike 41–44.
const routedAgent = int64(77)

func mkAgentConversation(t *testing.T, app *App, in CreateConversationInput) *Conversation {
	t.Helper()
	if in.ProjectID == "" {
		in.ProjectID = testProject
	}
	if in.LeadAgentID == 0 {
		in.LeadAgentID = routedAgent
	}
	if in.OwnerUserID == 0 {
		in.OwnerUserID = 1
	}
	conv, err := app.store.CreateConversation(in)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	return conv
}

func bindingThread(t *testing.T, app *App, conv *Conversation, agentID int64) string {
	t.Helper()
	state, err := app.store.AgentThread(conv.ID, agentID)
	if err != nil {
		t.Fatalf("binding for %s: %v", conv.ID, err)
	}
	return state.ThreadID
}

func TestFirstOperatorChatRoutesToMainAndSecondGetsChatThread(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	first := mkAgentConversation(t, app, CreateConversationInput{Title: "Everyday"})
	second := mkAgentConversation(t, app, CreateConversationInput{Title: "Side task"})

	postUserMessage(t, app, first, "hello main")
	if got := bindingThread(t, app, first, routedAgent); got != mainThreadID {
		t.Fatalf("first conversation thread=%q, want main", got)
	}
	if len(platform.ensures) != 0 || len(platform.spawns) != 0 {
		t.Fatalf("main was ensured or spawned: ensures=%d spawns=%d", len(platform.ensures), len(platform.spawns))
	}
	if len(platform.trackedEvents) != 1 {
		t.Fatalf("tracked events=%d, want 1", len(platform.trackedEvents))
	}
	ev := platform.trackedEvents[0]
	if ev.ThreadID != mainThreadID || ev.AgentID != routedAgent || !strings.HasPrefix(ev.SourceEventID, "conversation:"+first.ID+":message:") {
		t.Fatalf("main event=%+v", ev)
	}
	text := fmt.Sprint(ev.Message)
	if !strings.Contains(text, "hello main") || !strings.Contains(text, "conversation_id="+first.ID) {
		t.Fatalf("main event lacks message or reply contract: %q", text)
	}

	postUserMessage(t, app, second, "side work")
	if got := bindingThread(t, app, second, routedAgent); got != conversationThreadID(second.ID) {
		t.Fatalf("second conversation thread=%q, want its own chat thread", got)
	}
	if len(platform.ensures) != 1 || platform.ensures[0].ThreadID != conversationThreadID(second.ID) {
		t.Fatalf("second conversation ensures=%+v", platform.ensures)
	}
	// Later messages keep their routes; the existing chat thread is never moved to main.
	postUserMessage(t, app, first, "again")
	if len(platform.trackedEvents) != 2 || platform.trackedEvents[1].ThreadID != mainThreadID {
		t.Fatalf("follow-up did not reach main: %+v", platform.trackedEvents)
	}
}

func TestMainRoutingSkipsPublicRoomAndBridgedConversations(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	public := mkAgentConversation(t, app, CreateConversationInput{Title: "Visitors", Audience: "public"})
	room := mkAgentConversation(t, app, CreateConversationInput{Title: "Room", AgentIDs: []int64{routedAgent, 78}})
	external := mkAgentConversation(t, app, CreateConversationInput{Title: "External", ExternalIdentity: "telegram:5", ExternalName: "Visitor"})

	for _, conv := range []*Conversation{public, room, external} {
		postUserMessage(t, app, conv, "hi")
		if got := bindingThread(t, app, conv, routedAgent); got != conversationThreadID(conv.ID) {
			t.Fatalf("%s routed to %q, want its chat thread", conv.Title, got)
		}
	}
	for _, ev := range platform.trackedEvents {
		if ev.ThreadID == mainThreadID {
			t.Fatalf("ineligible conversation reached main: %+v", ev)
		}
	}
	operator := mkAgentConversation(t, app, CreateConversationInput{Title: "Operator"})
	postUserMessage(t, app, operator, "hi")
	if got := bindingThread(t, app, operator, routedAgent); got != mainThreadID {
		t.Fatalf("operator chat thread=%q, want main", got)
	}
}

// The Apteva Helper is listed in every project it serves but has no project
// of its own, and its main thread is not a chat thread. Chats it leads are
// marked at creation and always get their own chat-* thread; agents with a
// project still take main for their first operator chat.
func TestProjectlessLeadNeverTakesMainRouting(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	const helper = int64(3)
	platform.extraAgents = []sdk.PlatformAgent{
		{ID: helper, Name: "Apteva Helper", Status: "running"},
		{ID: routedAgent, Name: "Routed", Status: "running", ProjectID: testProject},
	}
	create := func(agentID int64) *Conversation {
		t.Helper()
		rec := doChats(t, app, "POST", "/chats", fmt.Sprintf(`{"agent_id":%d}`, agentID))
		if rec.Code != 200 {
			t.Fatalf("create for agent %d status=%d body=%s", agentID, rec.Code, rec.Body.String())
		}
		var got struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.ID == "" {
			t.Fatalf("decode create: %v body=%s", err, rec.Body.String())
		}
		conv, err := app.store.GetConversation(got.ID)
		if err != nil {
			t.Fatalf("GetConversation: %v", err)
		}
		return conv
	}
	mainRoutingColumn := func(conv *Conversation) int {
		t.Helper()
		var v int
		if err := app.store.db.QueryRow(`SELECT main_routing FROM conversations WHERE id=?`, conv.ID).Scan(&v); err != nil {
			t.Fatalf("read main_routing: %v", err)
		}
		return v
	}

	helperChat := create(helper)
	if got := mainRoutingColumn(helperChat); got != 0 {
		t.Fatalf("helper chat main_routing=%d, want 0", got)
	}
	postUserMessage(t, app, helperChat, "hello helper")
	if got := bindingThread(t, app, helperChat, helper); got != conversationThreadID(helperChat.ID) {
		t.Fatalf("helper chat thread=%q, want its chat thread", got)
	}
	if len(platform.ensures) != 1 || platform.ensures[0].ThreadID != conversationThreadID(helperChat.ID) || platform.ensures[0].AgentID != helper {
		t.Fatalf("helper chat ensures=%+v", platform.ensures)
	}
	for _, ev := range platform.trackedEvents {
		if ev.ThreadID == mainThreadID {
			t.Fatalf("helper chat reached main: %+v", ev)
		}
	}
	// A second Helper chat behaves the same; nothing ever holds Helper's main.
	second := create(helper)
	postUserMessage(t, app, second, "again")
	if got := bindingThread(t, app, second, helper); got != conversationThreadID(second.ID) {
		t.Fatalf("second helper chat thread=%q, want its chat thread", got)
	}
	var held int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM conversation_agent_threads WHERE agent_id=? AND thread_id=?`, helper, mainThreadID).Scan(&held); err != nil || held != 0 {
		t.Fatalf("helper main bindings=%d err=%v, want 0", held, err)
	}

	routed := create(routedAgent)
	if got := mainRoutingColumn(routed); got != 1 {
		t.Fatalf("project agent chat main_routing=%d, want 1", got)
	}
	postUserMessage(t, app, routed, "hello main")
	if got := bindingThread(t, app, routed, routedAgent); got != mainThreadID {
		t.Fatalf("project agent chat thread=%q, want main", got)
	}
}

func TestMainStaysGlobalCallerAndRepliesOnlyInItsConversation(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Everyday"})
	other := mkAgentConversation(t, app, CreateConversationInput{Title: "Other"})
	postUserMessage(t, app, routed, "hello")
	postUserMessage(t, app, other, "side")

	main := callerCtxCall(routedAgent, mainThreadID, "call-1")
	listed, err := app.toolList(main, ctx, map[string]any{})
	if err != nil {
		t.Fatalf("main lost conversations_list: %v", err)
	}
	if listed.(map[string]any)["main_conversation_id"] != routed.ID {
		t.Fatalf("main_conversation_id=%v, want %s", listed.(map[string]any)["main_conversation_id"], routed.ID)
	}
	if _, err := app.toolCreate(callerCtxCall(routedAgent, mainThreadID, "call-2"), ctx, map[string]any{"title": "New topic"}); err != nil {
		t.Fatalf("main lost conversations_create: %v", err)
	}
	if _, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "call-3"), ctx, map[string]any{
		"conversation_id": routed.ID, "text": "Done.", "phase": "final"}); err != nil {
		t.Fatalf("main reply in its conversation: %v", err)
	}
	if _, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "call-4"), ctx, map[string]any{
		"conversation_id": other.ID, "text": "Not mine", "phase": "final"}); err == nil {
		t.Fatal("main replied ordinarily in a conversation answered by its own chat thread")
	}
	// The chat thread stays confined to its own conversation.
	if _, err := app.toolSend(callerCtxCall(routedAgent, conversationThreadID(other.ID), "call-5"), ctx, map[string]any{
		"conversation_id": routed.ID, "text": "cross", "phase": "final"}); err == nil {
		t.Fatal("chat thread wrote into the conversation main answers")
	}
}

func TestEnsureNeverReconfiguresMain(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Everyday"})
	postUserMessage(t, app, routed, "hello")
	threadID, _, err := app.ensureConversationThreadForAgent(ctx, routed, routedAgent, nil, true)
	if err != nil || threadID != mainThreadID {
		t.Fatalf("ensure on main-routed conversation thread=%q err=%v", threadID, err)
	}
	if len(platform.ensures) != 0 || len(platform.spawns) != 0 {
		t.Fatalf("main received a profile: ensures=%+v spawns=%+v", platform.ensures, platform.spawns)
	}
}

func TestDeletingMainConversationReleasesRoutingWithoutRetiringMain(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Everyday"})
	postUserMessage(t, app, routed, "hello")
	if err := app.store.DeleteConversation(routed.ID); err != nil {
		t.Fatal(err)
	}
	var retired int
	_ = app.store.db.QueryRow(`SELECT COUNT(*) FROM retired_conversation_threads WHERE thread_id='main'`).Scan(&retired)
	if retired != 0 {
		t.Fatal("deleting the main-routed conversation retired main")
	}
	if err := app.killRetiredThreads(ctx); err != nil {
		t.Fatal(err)
	}
	for _, k := range platform.killed {
		if k.ThreadID == mainThreadID {
			t.Fatal("main was killed")
		}
	}
	if _, err := app.toolList(callerCtxCall(routedAgent, mainThreadID, "call-1"), ctx, map[string]any{}); err != nil {
		t.Fatalf("main refused after delete: %v", err)
	}
	next := mkAgentConversation(t, app, CreateConversationInput{Title: "Next"})
	postUserMessage(t, app, next, "hello again")
	if got := bindingThread(t, app, next, routedAgent); got != mainThreadID {
		t.Fatalf("next conversation thread=%q, want main", got)
	}
}

func TestArchiveAndTransportBindingReleaseMain(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Everyday"})
	postUserMessage(t, app, routed, "hello")
	if _, err := app.store.SetConversationArchived(routed.ID, true); err != nil {
		t.Fatal(err)
	}
	if app.store.mainRouted(routed.ID, routedAgent) {
		t.Fatal("archived conversation kept main")
	}
	next := mkAgentConversation(t, app, CreateConversationInput{Title: "Next"})
	postUserMessage(t, app, next, "hello")
	if !app.store.mainRouted(next.ID, routedAgent) {
		t.Fatal("next conversation did not take main after archive")
	}
	if _, err := app.store.db.Exec(`INSERT INTO participants(conversation_id,external_identity,display_name) VALUES(?,?,?)`,
		next.ID, "telegram:9", "Guest"); err != nil {
		t.Fatal(err)
	}
	if app.store.mainRouted(next.ID, routedAgent) {
		t.Fatal("bridged conversation kept main")
	}
}

func TestDeleteKillsChatThreadAndRetriesFailures(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	postUserMessage(t, app, conv, "hello")
	platform.killErr = errors.New("platform unavailable")
	if err := app.store.DeleteConversation(conv.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.killRetiredThreads(ctx); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var killed *string
	if err := app.store.db.QueryRow(`SELECT kill_attempts,killed_at FROM retired_conversation_threads WHERE thread_id=?`,
		conversationThreadID(conv.ID)).Scan(&attempts, &killed); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || killed != nil {
		t.Fatalf("failed kill attempts=%d killed=%v", attempts, killed)
	}
	platform.killErr = nil
	if _, err := app.store.db.Exec(`UPDATE retired_conversation_threads SET next_kill_at=NULL`); err != nil {
		t.Fatal(err)
	}
	if err := app.killRetiredThreads(ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.killed) != 1 || platform.killed[0] != (sdk.ThreadRef{AgentID: 41, ThreadID: conversationThreadID(conv.ID)}) {
		t.Fatalf("killed=%+v", platform.killed)
	}
	// A thread Core already lost counts as stopped.
	other := mkConversation(t, app, 42)
	postUserMessage(t, app, other, "hello")
	platform.killErr = errors.New("HTTP 404: thread not found")
	_ = app.store.DeleteConversation(other.ID)
	_ = app.killRetiredThreads(ctx)
	var pending int
	_ = app.store.db.QueryRow(`SELECT COUNT(*) FROM retired_conversation_threads WHERE killed_at IS NULL`).Scan(&pending)
	if pending != 0 {
		t.Fatalf("pending kills=%d, want 0", pending)
	}
}

// A backlog of threads retired by earlier releases must not delay stopping the
// thread of a conversation deleted just now.
func TestDeleteStopsNewThreadBeforeRetiredBacklog(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	for i := 0; i < lifecycleBatch+10; i++ {
		if _, err := app.store.db.Exec(`INSERT INTO retired_conversation_threads(project_id,agent_id,thread_id) VALUES(?,?,?)`,
			testProject, 999, fmt.Sprintf("chat-old-%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	conv := mkConversation(t, app, 41)
	postUserMessage(t, app, conv, "hello")
	if err := app.store.DeleteConversation(conv.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.killRetiredThreads(ctx); err != nil {
		t.Fatal(err)
	}
	want := sdk.ThreadRef{AgentID: 41, ThreadID: conversationThreadID(conv.ID)}
	if len(platform.killed) != lifecycleBatch || platform.killed[0] != want {
		first := sdk.ThreadRef{}
		if len(platform.killed) > 0 {
			first = platform.killed[0]
		}
		t.Fatalf("killed %d threads, first %+v; want %d starting with %+v", len(platform.killed), first, lifecycleBatch, want)
	}
	// The backlog still drains on later passes.
	for i := 0; i < 3; i++ {
		_ = app.killRetiredThreads(ctx)
	}
	var pending int
	_ = app.store.db.QueryRow(`SELECT COUNT(*) FROM retired_conversation_threads WHERE killed_at IS NULL`).Scan(&pending)
	if pending != 0 {
		t.Fatalf("pending kills=%d, want 0", pending)
	}
}

func ageConversation(t *testing.T, app *App, conv *Conversation, days int) {
	t.Helper()
	old := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := app.store.db.Exec(`UPDATE messages SET created_at=? WHERE conversation_id=?`, old, conv.ID); err != nil {
		t.Fatal(err)
	}
}

func eventsTo(platform *recordingPlatform, thread, marker string) []sdk.AgentEventRequest {
	var out []sdk.AgentEventRequest
	for _, ev := range platform.trackedEvents {
		if ev.ThreadID == thread && strings.Contains(fmt.Sprint(ev.Message), marker) {
			out = append(out, ev)
		}
	}
	return out
}

func TestIdleThreadIsCheckedThenSleptThenResumed(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	postUserMessage(t, app, conv, "hello")
	chat := conversationThreadID(conv.ID)
	if _, err := app.toolSend(callerCtxCall(41, chat, "final-1"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "All set.", "phase": "final"}); err != nil {
		t.Fatal(err)
	}

	// Recent conversations are left alone.
	if err := app.runThreadLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(eventsTo(platform, chat, "[conversation idle check]")) != 0 {
		t.Fatal("recent conversation got an idle check")
	}

	ageConversation(t, app, conv, 20)
	if err := app.runThreadLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(eventsTo(platform, chat, "[conversation idle check]")); n != 1 {
		t.Fatalf("idle checks=%d, want 1", n)
	}
	// Within the grace period the thread is not stopped and not checked twice.
	if err := app.runThreadLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.killed) != 0 || len(eventsTo(platform, chat, "[conversation idle check]")) != 1 {
		t.Fatalf("acted inside grace: killed=%+v", platform.killed)
	}

	past := sqliteTime(time.Now().Add(-2 * time.Hour))
	if _, err := app.store.db.Exec(`UPDATE conversation_agent_threads SET idle_check_at=? WHERE conversation_id=?`, past, conv.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runThreadLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	notes := eventsTo(platform, mainThreadID, "[conversation dormant]")
	if len(notes) != 1 || !strings.Contains(fmt.Sprint(notes[0].Message), "All set.") || !strings.Contains(fmt.Sprint(notes[0].Message), conv.ID) {
		t.Fatalf("dormancy note to main=%+v", notes)
	}
	if len(platform.killed) != 1 || platform.killed[0].ThreadID != chat {
		t.Fatalf("killed=%+v, want the chat thread", platform.killed)
	}
	if !app.store.threadDormant(conv.ID, 41) {
		t.Fatal("binding not dormant")
	}

	ensuresBefore := len(platform.ensures)
	postUserMessage(t, app, conv, "back again")
	if len(platform.ensures) != ensuresBefore+1 {
		t.Fatal("dormant thread was not recreated")
	}
	resumed := platform.ensures[len(platform.ensures)-1]
	// The notice follows the message so Core's preview still starts with it.
	if resumed.ThreadID != chat || len(resumed.Events) != 1 ||
		!strings.Contains(fmt.Sprint(resumed.Events[0].Message), "[conversation resumed]") ||
		!strings.HasPrefix(fmt.Sprint(resumed.Events[0].Message), "[chat] back again") {
		t.Fatalf("resume event=%+v", resumed.Events)
	}
	if app.store.threadDormant(conv.ID, 41) {
		t.Fatal("binding still dormant after resume")
	}
}

func TestKeepThreadSurvivesIdleCheck(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	postUserMessage(t, app, conv, "watch the price")
	chat := conversationThreadID(conv.ID)
	ageConversation(t, app, conv, 20)
	if err := app.runThreadLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolKeepThread(callerCtxCall(41, mainThreadID, "keep-main"), ctx, map[string]any{
		"conversation_id": conv.ID, "reason": "x"}); err == nil {
		t.Fatal("main kept a chat thread awake")
	}
	if _, err := app.toolKeepThread(callerCtxCall(41, chat, "keep-1"), ctx, map[string]any{
		"conversation_id": conv.ID, "reason": "Watching the price until it drops below 100"}); err != nil {
		t.Fatal(err)
	}
	past := sqliteTime(time.Now().Add(-2 * time.Hour))
	_, _ = app.store.db.Exec(`UPDATE conversation_agent_threads SET idle_check_at=? WHERE conversation_id=?`, past, conv.ID)
	if err := app.runThreadLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.killed) != 0 || app.store.threadDormant(conv.ID, 41) {
		t.Fatalf("kept thread was stopped: killed=%+v", platform.killed)
	}
}

func TestIdleSweepSkipsPendingApprovalsAndMain(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	postUserMessage(t, app, conv, "hello")
	if _, err := app.store.db.Exec(`INSERT INTO messages(conversation_id,role,content,agent_id,component_kind,action_status,created_at)
		VALUES(?,?,?,?,?,?,?)`, conv.ID, "agent", "Approve?", 41, kindApproval, "pending", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Everyday"})
	postUserMessage(t, app, routed, "hello")
	ageConversation(t, app, conv, 30)
	ageConversation(t, app, routed, 30)
	if err := app.runThreadLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(eventsTo(platform, conversationThreadID(conv.ID), "[conversation idle check]")) != 0 {
		t.Fatal("thread with a pending approval got an idle check")
	}
	if len(eventsTo(platform, mainThreadID, "[conversation idle check]")) != 0 {
		t.Fatal("main got an idle check")
	}
}

func TestStreamerIgnoresMainBackgroundTurns(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Everyday"})
	postUserMessage(t, app, routed, "hello")
	app.streamer.finishResponse(routed.ID, routedAgent)
	if app.streamer.hasActiveResponse(routed.ID, routedAgent) {
		t.Fatal("main's reply did not settle the response")
	}

	app.streamer.Ingest("llm.start", routedAgent, mainThreadID, `{}`, time.Now())
	if app.streamer.hasActiveResponse(routed.ID, routedAgent) {
		t.Fatal("a background main turn claimed the conversation")
	}
}

// coreEventPreview mirrors Core's event.received telemetry for a tracked
// event to main: "[console] " plus the event text, cut to 100 bytes and "...".
func coreEventPreview(text string) string {
	text = "[console] " + text
	if len(text) > 100 {
		return text[:100] + "..."
	}
	return text
}

// Live 0.27.0 bug: main receives "[console] [chat] ...", so the inbound
// preview never matched and Thinking stayed on after main's final reply.
func TestMainRoutedReplySettlesThinking(t *testing.T) {
	cases := map[string]string{
		"short":   "Hey",
		"long":    strings.Repeat("Please check the invoice backlog. ", 5),
		"unicode": strings.Repeat("a", 82) + "你好, check the invoices",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			app, ctx, platform := newTestEnv(t)
			mountedCtx = ctx
			app.streamer.telemetryConnected = true
			app.streamer.mainTelemetryConnected = true
			routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Chat"})
			postUserMessage(t, app, routed, content)
			if len(platform.trackedEvents) != 1 || platform.trackedEvents[0].ThreadID != mainThreadID {
				t.Fatalf("main deliveries=%+v", platform.trackedEvents)
			}
			text, _ := platform.trackedEvents[0].Message.(string)
			received := func(text string) string {
				raw, _ := json.Marshal(map[string]string{"source": "console", "message": coreEventPreview(text)})
				return string(raw)
			}
			now := time.Now()
			// Another console event cannot take over this response.
			app.streamer.Ingest("event.received", routedAgent, mainThreadID, received("[chat] something else"), now)
			app.streamer.Ingest("llm.start", routedAgent, mainThreadID, `{}`, now.Add(time.Millisecond))
			app.streamer.Ingest("tool.call", routedAgent, mainThreadID, `{"name":"pace"}`, now.Add(2*time.Millisecond))
			if !app.streamer.hasActiveResponse(routed.ID, routedAgent) {
				t.Fatal("an unrelated main event settled the response")
			}
			app.streamer.Ingest("event.received", routedAgent, mainThreadID, received(text), now.Add(3*time.Millisecond))
			app.streamer.Ingest("llm.start", routedAgent, mainThreadID, `{}`, now.Add(4*time.Millisecond))
			if _, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "final"), ctx, map[string]any{
				"conversation_id": routed.ID, "text": "Hey! How can I help?", "phase": "final"}); err != nil {
				t.Fatal(err)
			}
			if ids := app.streamer.activeConversationIDs(); len(ids) != 0 {
				t.Fatalf("Thinking stayed on after main's final reply: %v", ids)
			}
		})
	}
}

// The live 0.27.0 condition: the chat-thread feed is connected, main's is not,
// so Core's receipt for main never arrives. Main's reply must still settle.
func TestMainReplySettlesWithoutMainTelemetry(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	app.streamer.telemetryConnected = true
	routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Chat"})
	postUserMessage(t, app, routed, "Hey")
	if !app.streamer.hasActiveResponse(routed.ID, routedAgent) {
		t.Fatal("no Thinking while main answers")
	}
	if _, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "final"), ctx, map[string]any{
		"conversation_id": routed.ID, "text": "Hey! How can I help?", "phase": "final"}); err != nil {
		t.Fatal(err)
	}
	if ids := app.streamer.activeConversationIDs(); len(ids) != 0 {
		t.Fatalf("Thinking stayed on after main's final reply: %v", ids)
	}
}

// Main's feed carries all of main's work. Only tool calls inside the response
// main owns appear in the conversation it answers.
func TestMainToolActivityOnlyInsideOwnedResponse(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	app.streamer.telemetryConnected = true
	app.streamer.mainTelemetryConnected = true
	routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Chat"})
	at := time.Now()
	step := func(typ, data string) {
		t.Helper()
		at = at.Add(time.Millisecond)
		if err := app.ingestToolActivity(typ, routedAgent, mainThreadID, data, at); err != nil {
			t.Fatal(err)
		}
		app.streamer.Ingest(typ, routedAgent, mainThreadID, data, at)
	}
	// Background work before anyone writes.
	step("tool.call", `{"id":"background-1","name":"code_repos_list"}`)
	postUserMessage(t, app, routed, "List the repositories")
	// Core's events follow the acknowledgement; earlier ones are ignored.
	at = time.Now()
	// Background work while the message waits in Core's queue.
	step("tool.call", `{"id":"background-2","name":"code_repos_list"}`)
	text, _ := platform.trackedEvents[0].Message.(string)
	received, _ := json.Marshal(map[string]string{"source": "console", "message": coreEventPreview(text)})
	step("event.received", string(received))
	step("llm.start", `{}`)
	step("tool.call", `{"id":"owned-1","name":"code_repos_list"}`)
	if !app.streamer.hasActiveResponse(routed.ID, routedAgent) {
		t.Fatal("main's work did not show as active")
	}
	if _, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "final"), ctx, map[string]any{
		"conversation_id": routed.ID, "text": "Two repositories.", "phase": "final"}); err != nil {
		t.Fatal(err)
	}
	// A parallel call's result lands after the reply; later work is background.
	step("tool.result", `{"id":"owned-1","name":"code_repos_list","success":true}`)
	step("tool.call", `{"id":"background-3","name":"code_repos_list"}`)
	step("tool.result", `{"id":"background-3","name":"code_repos_list","success":true}`)

	rows, err := app.store.toolActivities(routed.ID)
	if err != nil || len(rows) != 1 || rows[0].CallID != "owned-1" || rows[0].Status != "completed" {
		t.Fatalf("activity rows=%+v err=%v, want only owned-1 completed", rows, err)
	}
	if ids := app.streamer.activeConversationIDs(); len(ids) != 0 {
		t.Fatalf("background work reopened the conversation: %v", ids)
	}
}

func TestMainFeedEndFallsBackWithoutTouchingChatFeed(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	app.streamer.telemetryConnected = true
	app.streamer.mainTelemetryConnected = true
	ch := make(chan sdk.TelemetryStreamEvent, 1)
	// The prefix also matches other thread names; they are not main.
	ch <- sdk.TelemetryStreamEvent{AgentID: routedAgent, ThreadID: "maintenance", Type: "llm.start", Time: time.Now(), Data: json.RawMessage(`{}`)}
	close(ch)
	app.consumeChatTelemetry(ctx, ch, mainFeed)
	if app.streamer.mainTelemetryConnected || !app.streamer.telemetryConnected {
		t.Fatalf("main=%v chat=%v after main feed ended", app.streamer.mainTelemetryConnected, app.streamer.telemetryConnected)
	}
}

func TestChatThreadDirectiveCarriesSelfPacingAndReportBack(t *testing.T) {
	conv := &Conversation{ID: "conv-1", Title: "Chat", Audience: "operator", Origin: "web"}
	directive := conversationThreadDirective(conv)
	for _, want := range []string{"pace clear_wake=true", "pace sleep", "Never leave a turn without a timing decision",
		"send id=main", "conversations_keep_thread"} {
		if !strings.Contains(directive, want) {
			t.Fatalf("directive missing %q", want)
		}
	}
}

// Main answering a chat must do the requested work itself: Core tells main to
// hand independent units to helper threads, which turned a quick inbox triage
// into four spawned threads on prod (2026-11-24).
func TestMainReplyContractKeepsRequestedWorkOnMain(t *testing.T) {
	contract := mainReplyContract(&Conversation{ID: "conv-1", Title: "Messaging"})
	for _, fragment := range []string{
		"very small work you do yourself in this turn",
		"do not spawn a thread for them",
		"Spawn only for work that must continue after your reply",
	} {
		if !strings.Contains(contract, fragment) {
			t.Errorf("main reply contract missing %q:\n%s", fragment, contract)
		}
	}
}

// Prod 2026-11-24 (Messaging Agent): main created "Email triage" for its
// daily summaries. The person's first reply ("Thanks") gave it a chat thread,
// which read the agent's standing instructions and started its own hourly
// inbox job, so two summaries went out at 17:00. A conversation the agent
// created must be answered by main, which knows the job already runs.
func TestAgentCreatedConversationRoutesToMain(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	created, err := app.toolCreate(callerCtx(routedAgent, mainThreadID), ctx, map[string]any{"title": "Email triage"})
	if err != nil {
		t.Fatalf("toolCreate: %v", err)
	}
	id := created.(map[string]any)["conversation_id"].(string)
	conv, err := app.store.GetConversation(id)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if conv.Origin != "agent" {
		t.Fatalf("origin=%q, want agent", conv.Origin)
	}

	postUserMessage(t, app, conv, "Thanks")
	if got := bindingThread(t, app, conv, routedAgent); got != mainThreadID {
		t.Fatalf("agent-created conversation thread=%q, want main", got)
	}
	if len(platform.ensures) != 0 || len(platform.spawns) != 0 {
		t.Fatalf("a chat thread was created: ensures=%d spawns=%d", len(platform.ensures), len(platform.spawns))
	}
	if len(platform.trackedEvents) != 1 || platform.trackedEvents[0].ThreadID != mainThreadID {
		t.Fatalf("reply did not reach main: %+v", platform.trackedEvents)
	}
	text := fmt.Sprint(platform.trackedEvents[0].Message)
	for _, want := range []string{"Thanks", "conversation_id=" + conv.ID, "You created this conversation yourself", "standing jobs keep running from main"} {
		if !strings.Contains(text, want) {
			t.Fatalf("main event missing %q: %q", want, text)
		}
	}

	// Main answers one conversation per agent. A second agent-created
	// conversation falls back to a chat thread, whose directive keeps it off
	// main's standing duties.
	again, err := app.toolCreate(callerCtx(routedAgent, mainThreadID), ctx, map[string]any{"title": "Weekly digest"})
	if err != nil {
		t.Fatalf("toolCreate: %v", err)
	}
	second, _ := app.store.GetConversation(again.(map[string]any)["conversation_id"].(string))
	postUserMessage(t, app, second, "ok")
	if got := bindingThread(t, app, second, routedAgent); got != conversationThreadID(second.ID) {
		t.Fatalf("second agent-created conversation thread=%q, want its chat thread", got)
	}
	if len(platform.ensures) != 1 || !strings.Contains(platform.ensures[0].DirectiveSuffix, "belong to main, which already runs them") {
		t.Fatalf("chat thread ensure missing standing-duty rule: %+v", platform.ensures)
	}
}

// The web-started chat's contract does not claim main created it.
func TestMainReplyContractNotesOnlyAgentCreatedConversations(t *testing.T) {
	if strings.Contains(mainReplyContract(&Conversation{ID: "c", Title: "Chat", Origin: "web"}), "You created this conversation") {
		t.Fatal("web conversation contract claims the agent created it")
	}
	if !strings.Contains(mainReplyContract(&Conversation{ID: "c", Title: "Report", Origin: "agent"}), "You created this conversation") {
		t.Fatal("agent-created conversation contract lacks the note")
	}
}

// A chat thread reads the agent's full instructions. It must not adopt the
// schedules and recurring reports in them; it takes on recurring work only
// when someone asks for it in the conversation.
func TestChatThreadDirectiveLeavesStandingDutiesToMain(t *testing.T) {
	directive := conversationThreadDirective(&Conversation{ID: "conv-1", Title: "Email triage", Audience: "operator", Origin: "agent"})
	for _, want := range []string{
		"belong to main, which already runs them",
		"Do not start, repeat or take over any of them here",
		"Start recurring work here only when a person asks for it in this conversation",
	} {
		if !strings.Contains(directive, want) {
			t.Errorf("directive missing %q", want)
		}
	}
	if strings.Contains(directive, "Keep recurring work that belongs to this conversation here") {
		t.Error("directive still tells the thread to keep recurring work that belongs to the conversation")
	}
}
