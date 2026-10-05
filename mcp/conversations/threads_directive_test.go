package main

import (
	"strings"
	"testing"
)

func TestConversationThreadDirectiveAcknowledgesEveryToolBackedRequest(t *testing.T) {
	text := conversationThreadDirective(&Conversation{ID: "conv-test", Title: "TodoTest", Origin: "web"})
	for _, want := range []string{
		"Before calling any work tool for a user request, including a single quick lookup",
		"phase=acknowledgement alone and briefly say what you are about to do",
		"For short flows, including several related tool calls, keep working through to the final outcome without a progress update",
		"complete a substantial batch before sending a concise phase=progress update",
		"normally leave about a minute between updates",
		"If completion is near, finish and send the final outcome instead",
		"The only exception is a response you can give without a tool, or a simple image question",
		"Distinguish file identification from content inspection",
		"Do not ask permission to look for a reader",
		"do not call pace or wait for a nonexistent tool",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("conversation directive missing %q: %s", want, text)
		}
	}
}
