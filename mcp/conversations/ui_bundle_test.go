package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// App UI entries are imported through project- and install-scoped URLs. Static
// relative ESM imports do not inherit the entry URL's query parameters, so a
// split chunk would be fetched without the authorization scope. Keep every
// declared Conversations entry self-contained until the platform exposes a
// path-scoped asset contract.
func TestUIModuleEntriesAreSelfContained(t *testing.T) {
	relativeImport := regexp.MustCompile(`(?m)\b(?:from|import)\s*["']\./`)
	for _, name := range []string{
		"ConversationsPanel.mjs",
		"AgentConversationsWidget.mjs",
		"InboxWidget.mjs",
	} {
		body, err := os.ReadFile(filepath.Join("ui", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if relativeImport.Match(body) {
			t.Fatalf("%s imports a relative module; project/install scope would be lost", name)
		}
	}

	chunks, err := filepath.Glob(filepath.Join("ui", "shared-*.mjs*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("shared UI chunks must not ship: %v", chunks)
	}
	if _, err := os.Stat(filepath.Join("ui", "split-bundle.json")); !os.IsNotExist(err) {
		t.Fatalf("split-bundle.json must remain absent; stat error=%v", err)
	}
}
