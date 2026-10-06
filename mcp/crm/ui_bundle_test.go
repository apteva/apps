package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Dashboard entries are loaded through project- and install-scoped URLs.
// Relative imports in a built bundle would lose that authorization scope.
func TestCRMUIEntriesAreSelfContained(t *testing.T) {
	relativeImport := regexp.MustCompile(`(?m)\b(?:from|import)\s*["']\./`)
	for _, name := range []string{"CrmPanel.mjs", "CrmInboxWidget.mjs"} {
		body, err := os.ReadFile(filepath.Join("ui", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if relativeImport.Match(body) {
			t.Fatalf("%s imports a relative module; project/install scope would be lost", name)
		}
	}
}
