package main

import (
	"strings"
	"testing"
	"time"
)

func suggestionArgs(text string) map[string]any {
	return map[string]any{"app": appName, "name": replySuggestionComponent, "props": map[string]any{"items": []any{map[string]any{"id": "next", "text": text}}}}
}
func TestReplySuggestionStoredWithFinalReplyWithoutInteraction(t *testing.T) {
	a, ctx, p := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	args := map[string]any{"conversation_id": conv.ID, "text": "The analysis is complete.", "phase": "final", "components": []any{suggestionArgs("  Implement the fix and run tests.  ")}}
	caller := callerCtxCall(41, conversationThreadID(conv.ID), "suggestion-final")
	first, err := a.toolSend(caller, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.toolSend(caller, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	id := first.(map[string]any)["message_id"].(int64)
	if id != second.(map[string]any)["message_id"] || second.(map[string]any)["inserted"] != false {
		t.Fatal("retry duplicated final")
	}
	m, err := a.store.GetMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.ComponentKind != "" || m.ActionStatus != "" || len(m.Components) != 1 || m.Components[0].Interaction != nil {
		t.Fatalf("unexpected interaction %+v", m)
	}
	items := m.Components[0].Props["items"].([]any)
	if items[0].(map[string]any)["text"] != "Implement the fix and run tests." {
		t.Fatalf("suggestion lost: %+v", items)
	}
	inbox, err := a.store.Inbox(testProject, 1, 100)
	if err != nil || len(inbox) != 0 {
		t.Fatalf("suggestion entered inbox %+v %v", inbox, err)
	}
	if len(p.trackedEvents) != 0 {
		t.Fatal("suggestion woke agent without user input")
	}
}
func TestReplySuggestionRejectsMultipleProgressAndInteractions(t *testing.T) {
	cases := []struct {
		name, phase, text string
		components        []map[string]any
	}{
		{"progress", "progress", "Working", []map[string]any{suggestionArgs("Continue.")}},
		{"empty-final", "final", "", []map[string]any{suggestionArgs("Continue.")}},
		{"multiple-components", "final", "Done", []map[string]any{suggestionArgs("One."), suggestionArgs("Two.")}},
		{"with-question", "final", "Done", []map[string]any{suggestionArgs("Continue."), interactionArgs("question", false)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs, err := componentsArg(map[string]any{"components": tc.components}, "components")
			if err != nil {
				t.Fatal(err)
			}
			if err = normalizeInteractions(cs, time.Now()); err == nil {
				err = normalizeReplySuggestions(cs, tc.phase, tc.text)
			}
			if err == nil {
				t.Fatal("accepted invalid suggestion")
			}
		})
	}
	for _, text := range []string{"", strings.Repeat("x", 501)} {
		cs, _ := componentsArg(map[string]any{"components": []any{suggestionArgs(text)}}, "components")
		if err := normalizeReplySuggestions(cs, "final", "Done"); err == nil {
			t.Fatal("accepted invalid text")
		}
	}
	c := suggestionArgs("Continue.")
	c["props"].(map[string]any)["items"] = []any{map[string]any{"text": "One."}, map[string]any{"text": "Two."}}
	cs, _ := componentsArg(map[string]any{"components": []any{c}}, "components")
	if err := normalizeReplySuggestions(cs, "final", "Done"); err == nil {
		t.Fatal("accepted multiple options")
	}
}
