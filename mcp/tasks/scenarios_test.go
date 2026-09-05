package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScenariosDoNotDependOnDeprecatedConversationChannels(t *testing.T) {
	entries, err := os.ReadDir("scenarios")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"interaction: conversation",
		"chat_response_",
		"chat_final_messages",
		"APTEVA_TEST_CONVERSATION",
		"/api/apps/channel-chat",
		"channels_send",
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		body, err := os.ReadFile(filepath.Join("scenarios", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, marker := range forbidden {
			if strings.Contains(text, marker) {
				t.Errorf("%s contains deprecated scenario dependency %q", entry.Name(), marker)
			}
		}
	}
}
