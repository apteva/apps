package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
		// Source maps are part of the release. Verify they describe today's
		// source, not stale compiled widgets left behind by a frontend build.
		mapped, err := os.ReadFile(filepath.Join("ui", name+".map"))
		if err != nil {
			t.Fatal(err)
		}
		var sourceMap struct {
			Sources  []string `json:"sources"`
			Contents []string `json:"sourcesContent"`
		}
		if err := json.Unmarshal(mapped, &sourceMap); err != nil {
			t.Fatal(err)
		}
		if len(sourceMap.Sources) != len(sourceMap.Contents) {
			t.Fatal("missing embedded panel sources")
		}
		for i, source := range sourceMap.Sources {
			if strings.Contains(source, "node_modules/") {
				continue
			}
			current, err := os.ReadFile(filepath.Join("ui", source))
			if err != nil {
				t.Fatal(err)
			}
			if string(current) != sourceMap.Contents[i] {
				t.Fatalf("%s has stale %s; run scripts/build-panels.ts --app conversations", name, source)
			}
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
