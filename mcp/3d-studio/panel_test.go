package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Keep this release artifact contract in the normal Go test suite: the SDK
// serves the checked-in MJS, not the TSX source or standalone development build.
func TestReleasedPanelUsesProductionJSX(t *testing.T) {
	data, err := os.ReadFile("ui/StudioPanel.mjs")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if strings.Contains(source, "react/jsx-dev-runtime") || strings.Contains(source, "jsxDEV") {
		t.Fatal("released panel imports development JSX; rebuild with scripts/build-panel.ts")
	}
	re := regexp.MustCompile(`import\s*\{([^}]+)\}\s*from\s*["']react/jsx-runtime["']`)
	imports := re.FindStringSubmatch(source)
	if len(imports) != 2 {
		t.Fatal("panel must use the host production JSX runtime")
	}
	for _, spec := range strings.Split(imports[1], ",") {
		name := strings.Fields(strings.TrimSpace(spec))[0]
		if name != "jsx" && name != "jsxs" && name != "Fragment" {
			t.Fatalf("unsupported JSX runtime import %s", name)
		}
	}
}
