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
		"The only exception is a response you can give without a tool, or a simple image question",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("conversation directive missing %q: %s", want, text)
		}
	}
}
