package main

import (
	"os"
	"strings"
	"testing"
)

// Live prod reply: "1. 01. Define the goal" rendered as "1. 1." because the
// leading "01." starts a nested list. Agents learn the rule from the send
// tool's text field (always visible) and from the skill. Titles that carry
// their own sequence numbers go in a plain numbered list without the prefix.
func TestNumberedListRuleReachesAgents(t *testing.T) {
	var send map[string]any
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "send" {
			send, _ = tool.InputSchema["properties"].(map[string]any)["text"].(map[string]any)
		}
	}
	description, _ := send["description"].(string)
	if !strings.Contains(description, `never "1. 01. Define the goal"`) || !strings.Contains(description, "drop that prefix and use a plain numbered list") {
		t.Fatalf("send text description lacks the numbered-list rule: %q", description)
	}
	skill, err := os.ReadFile("skills/using-conversations.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "never `1. 01. Define the goal`") || !strings.Contains(string(skill), "drop that prefix and write a plain numbered list") {
		t.Fatal("skill lacks the numbered-list rule")
	}
}
