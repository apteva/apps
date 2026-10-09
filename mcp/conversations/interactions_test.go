package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func interactionArgs(id string, blocking bool) map[string]any {
	return map[string]any{"id": id, "version": 1, "app": "conversations", "name": "question-card", "props": map[string]any{"title": "Which tone?", "body": "I will use concise unless you answer."}, "interaction": map[string]any{"blocking": blocking, "fields": []any{map[string]any{"id": "tone", "label": "Tone", "type": "choice", "required": true, "allow_custom": true, "options": []any{map[string]any{"id": "concise", "label": "Concise"}, map[string]any{"id": "detailed", "label": "Detailed"}}}}}}
}
func sendInteraction(t *testing.T, a *App, ctx *sdk.AppCtx, conv *Conversation, blocking bool, cs ...map[string]any) *Message {
	t.Helper()
	if len(cs) == 0 {
		cs = []map[string]any{interactionArgs("tone", blocking)}
	}
	result, err := a.toolSend(callerCtxCall(41, conversationThreadID(conv.ID), fmt.Sprint(time.Now().UnixNano())), ctx, map[string]any{"conversation_id": conv.ID, "components": cs, "phase": "final"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.store.GetMessage(result.(map[string]any)["message_id"].(int64))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestInteractiveNonBlockingSurvivesFinalAndLateAnswer(t *testing.T) {
	a, ctx, p := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	a.streamer.emitAck(conv.ID, conversationThreadID(conv.ID), 41, 123)
	m := sendInteraction(t, a, ctx, conv, false)
	if m.Components[0].Interaction.Blocking || m.ActionStatus != "pending" {
		t.Fatalf("defaults %+v", m)
	}
	inbox, err := a.store.Inbox(testProject, 1, 100)
	if err != nil || len(inbox) != 1 || inbox[0].Message.ID != m.ID {
		t.Fatalf("inbox=%+v err=%v", inbox, err)
	}
	if frames := a.streamer.snapshot(conv.ID).Frames; len(frames) != 0 {
		t.Fatalf("unanswered optional card left Thinking: %+v", frames)
	}
	if _, err = a.toolSend(callerCtxCall(41, conversationThreadID(conv.ID), "work-done"), ctx, map[string]any{"conversation_id": conv.ID, "text": "Finished without an answer", "phase": "final"}); err != nil {
		t.Fatal(err)
	}
	got, err := a.resolveComponent(ctx, m, "tone", "submit", map[string]any{"tone": "detailed"}, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Components[0].Interaction.Values["tone"] != "detailed" || got.ActionStatus != "resolved" {
		t.Fatalf("answer %+v", got)
	}
	if len(p.trackedEvents) != 1 || p.trackedEvents[0].ThreadID != conversationThreadID(conv.ID) || !strings.Contains(p.trackedEvents[0].Message.(string), "component.result") {
		t.Fatalf("routing %+v", p.trackedEvents)
	}
	inbox, err = a.store.Inbox(testProject, 1, 100)
	if err != nil || len(inbox) != 0 {
		t.Fatalf("resolved inbox %+v %v", inbox, err)
	}
	if _, err = a.resolveComponent(ctx, m, "tone", "submit", map[string]any{"tone": "detailed"}, 1, false); err != nil {
		t.Fatal("identical retry", err)
	}
	if len(p.trackedEvents) != 1 {
		t.Fatal("duplicate delivery")
	}
	if _, err = a.resolveComponent(ctx, m, "tone", "submit", map[string]any{"tone": "concise"}, 1, false); err == nil {
		t.Fatal("answer overwritten")
	}
	fresh, _ := a.store.GetMessage(m.ID)
	if fresh.Components[0].Interaction.Values["tone"] != "detailed" {
		t.Fatal("lost answer")
	}
}
func TestInteractiveMultipleCardsValidationCancellationAndCAS(t *testing.T) {
	a, ctx, p := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	m := sendInteraction(t, a, ctx, conv, false, interactionArgs("one", false), interactionArgs("two", true))
	for _, v := range []map[string]any{{}, {"tone": 17}, {"tone": "concise", "forged": "x"}} {
		if _, err := a.resolveComponent(ctx, m, "one", "submit", v, 1, false); err == nil {
			t.Fatalf("accepted invalid fields %+v", v)
		}
	}
	if _, err := a.resolveComponent(ctx, m, "one", "unknown", nil, 1, false); err == nil {
		t.Fatal("unknown action")
	}
	first, err := a.resolveComponent(ctx, m, "one", "submit", map[string]any{"tone": "A custom answer"}, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.ActionStatus != "pending" {
		t.Fatal("other component dropped from inbox")
	}
	second, err := a.resolveComponent(ctx, m, "two", "cancel", nil, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Components[0].Interaction.Status != "answered" || second.Components[1].Interaction.Status != "cancelled" || second.ActionStatus != "resolved" {
		t.Fatalf("lost parallel component state %+v", second.Components)
	}
	if len(p.trackedEvents) != 2 || p.trackedEvents[0].SourceEventID == p.trackedEvents[1].SourceEventID {
		t.Fatalf("component identities %+v", p.trackedEvents)
	}
	if _, err = a.resolveComponent(ctx, m, "two", "cancel", nil, 1, false); err != nil {
		t.Fatal("cancel retry", err)
	}
}
func TestInteractiveDurableRetryAndExpiry(t *testing.T) {
	a, ctx, p := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	m := sendInteraction(t, a, ctx, conv, false)
	p.failSend = true
	got, err := a.resolveComponent(ctx, m, "tone", "submit", map[string]any{"tone": "concise"}, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	target := componentTarget(m, "tone")
	delivery, err := a.store.DeliveryFor(m.ID, target)
	if err != nil || delivery.Status == "delivered" {
		t.Fatalf("lost retry %+v %v", delivery, err)
	}
	// Simulate recovery using the persisted row and a fresh adapter; never rely
	// on the old caller's transient values or requester identity.
	p.failSend = false
	if err := (&componentAdapter{app: a}).Deliver(ctx, strings.TrimPrefix(target, "component:"), conv, got); err != nil {
		t.Fatal(err)
	}
	if len(p.trackedEvents) != 1 {
		t.Fatal("saved answer not delivered")
	}
	exp := sendInteraction(t, a, ctx, conv, true)
	exp.Components[0].Interaction.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	if _, err = a.store.UpdateMessageComponents(exp.ID, exp.Components); err != nil {
		t.Fatal(err)
	}
	if err = a.expireComponents(ctx); err != nil {
		t.Fatal(err)
	}
	saved, _ := a.store.GetMessage(exp.ID)
	if saved.Components[0].Interaction.Status != "expired" || saved.ActionStatus != "resolved" {
		t.Fatalf("expiry %+v", saved)
	}
	if _, err = a.resolveComponent(ctx, exp, "tone", "submit", map[string]any{"tone": "concise"}, 1, false); err == nil {
		t.Fatal("expired answer accepted")
	}
}
func TestInteractiveActionHTTPAuthorizationAndArchive(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	m := sendInteraction(t, a, ctx, conv, false)
	request := func(user int64, id string) int {
		body, _ := json.Marshal(map[string]any{"message_id": m.ID, "component_id": id, "action_id": "submit", "values": map[string]any{"tone": "concise"}})
		r := httptest.NewRequest(http.MethodPost, "/message-action", strings.NewReader(string(body)))
		r.Header.Set("X-Apteva-User-ID", fmt.Sprint(user))
		r.Header.Set("X-Apteva-Project-ID", testProject)
		w := httptest.NewRecorder()
		a.handleMessageAction(w, r)
		return w.Code
	}
	if code := request(999, "tone"); code != http.StatusNotFound {
		t.Fatalf("private answer code %d", code)
	}
	if code := request(1, "other"); code < 400 {
		t.Fatalf("wrong component code %d", code)
	}
	if _, err := a.store.db.Exec(`UPDATE conversations SET archived_at=CURRENT_TIMESTAMP WHERE id=?`, conv.ID); err != nil {
		t.Fatal(err)
	}
	if code := request(1, "tone"); code < 400 {
		t.Fatal("archived action accepted")
	}
}
func TestInteractiveSchemaValidationAndExistingCards(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(c map[string]any) { c["version"] = 2 }, func(c map[string]any) { c["interaction"].(map[string]any)["status"] = "answered" }, func(c map[string]any) { c["interaction"].(map[string]any)["expires_at"] = "yesterday" }, func(c map[string]any) { c["name"] = "approval-card" },
	} {
		c := interactionArgs("x", false)
		change(c)
		cs, err := componentsArg(map[string]any{"components": []any{c}}, "components")
		if err == nil {
			err = normalizeInteractions(cs, time.Now())
		}
		if err == nil {
			t.Fatalf("bad schema accepted %+v", c)
		}
	}
	card := approvalCard("Approve?", "Keep approval semantics", defaultApprovalActions())
	if card.Interaction == nil || !card.Interaction.Blocking {
		t.Fatal("approval lost blocking lifecycle")
	}
	m := &Message{ID: 1, ComponentKind: kindApproval, Components: []Component{card}}
	updated, _, err := applyInboxAction(m, "deny", "No", 1)
	if err != nil {
		t.Fatal(err)
	}
	if updated[0].Interaction.ActionID != "deny" || updated[0].Props["status"] != "deny" {
		t.Fatal("legacy verdict diverged from shared state")
	}
	a, _, _ := newTestEnv(t)
	names := []string{}
	for _, tool := range a.MCPTools() {
		names = append(names, tool.Name)
	}
	if len(names) != 9 {
		t.Fatalf("added tools: %v", names)
	}
}

func TestInteractiveHTTPRepeatedSkipAndEmptySubmit(t *testing.T) {
	a, ctx, p := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	optional := interactionArgs("optional", false)
	optional["interaction"].(map[string]any)["fields"] = []any{}
	for _, action := range []string{"cancel", "submit"} {
		m := sendInteraction(t, a, ctx, conv, false, optional)
		for i := 0; i < 2; i++ {
			body, _ := json.Marshal(map[string]any{"message_id": m.ID, "component_id": "optional", "action_id": action, "values": map[string]any{}})
			r := httptest.NewRequest(http.MethodPost, "/message-action", strings.NewReader(string(body)))
			r.Header.Set("X-Apteva-User-ID", "1")
			r.Header.Set("X-Apteva-Project-ID", testProject)
			w := httptest.NewRecorder()
			a.handleMessageAction(w, r)
			if w.Code != 200 {
				t.Fatalf("%s retry %d: %d %s", action, i, w.Code, w.Body.String())
			}
		}
	}
	if len(p.trackedEvents) != 2 {
		t.Fatalf("duplicate results: %+v", p.trackedEvents)
	}
}

func TestInteractiveConcurrentResultsPreserveBothCards(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	m := sendInteraction(t, a, ctx, conv, false, interactionArgs("one", false), interactionArgs("two", false))
	// Production queues results; don't concurrently mutate the synchronous mock.
	a.deliveryWorker = &deliveryWorker{wake: make(chan struct{}, 4), stop: make(chan struct{})}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			<-start
			_, err := a.resolveComponent(ctx, m, id, "submit", map[string]any{"tone": "detailed"}, 1, false)
			results <- err
		}(id)
	}
	close(start)
	for range 2 {
		err := <-results
		// A stale revision produces a conflict, never a lost update. The same
		// browser can refresh/retry safely after that conflict.
		if err != nil && !strings.Contains(err.Error(), "reload and retry") {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"one", "two"} {
		if _, err := a.resolveComponent(ctx, m, id, "submit", map[string]any{"tone": "detailed"}, 1, false); err != nil {
			t.Fatal(err)
		}
	}
	saved, _ := a.store.GetMessage(m.ID)
	for _, c := range saved.Components {
		if c.Interaction.Status != "answered" {
			t.Fatalf("lost result: %+v", c)
		}
	}
	var count int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE message_id=? AND target LIKE 'component:%'`, m.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("delivery count %d: %v", count, err)
	}
}

func TestInteractiveInboxOptOutAndBlockingPhase(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	c := interactionArgs("private", false)
	c["interaction"].(map[string]any)["inbox"] = false
	m := sendInteraction(t, a, ctx, conv, false, c)
	rows, err := a.store.Inbox(testProject, 1, 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("opted-out inbox: %+v %v", rows, err)
	}
	if m.Components[0].Interaction.Status != "pending" {
		t.Fatal("inbox opt-out removed pending question")
	}
	if _, err = a.resolveComponent(ctx, m, "private", "submit", map[string]any{"tone": "concise"}, 1, false); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"acknowledgement", "progress"} {
		_, err = a.toolSend(callerCtxCall(41, conversationThreadID(conv.ID), phase), ctx, map[string]any{"conversation_id": conv.ID, "phase": phase, "components": []any{interactionArgs("blocking", true)}})
		if err == nil {
			t.Fatal("blocking question kept a live response open")
		}
	}
}
