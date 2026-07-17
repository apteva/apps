package main

import (
	"os"
	"strings"
	"testing"
)

func TestPanelLayoutDoesNotDependOnArbitraryTailwindUtilities(t *testing.T) {
	source, err := os.ReadFile("ui/CallsPanel.tsx")
	if err != nil {
		t.Fatalf("read telephony panel source: %v", err)
	}

	// App panels are loaded dynamically and can be newer than the dashboard CSS.
	// Exact runtime dimensions must use inline styles so older hosts render them.
	for _, utility := range []string{
		"grid-cols-[",
		"min-w-[",
		"max-w-[",
		"min-h-[",
		"max-h-[",
		"text-[",
	} {
		if strings.Contains(string(source), utility) {
			t.Errorf("panel uses host-build-dependent Tailwind utility %q", utility)
		}
	}
}
