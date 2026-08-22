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

func TestRoutingPanelUsesGuidedSetupAndRetainsAdvancedEditor(t *testing.T) {
	source, err := os.ReadFile("ui/CallsPanel.tsx")
	if err != nil {
		t.Fatalf("read telephony panel source: %v", err)
	}
	panel := string(source)
	for _, label := range []string{
		"Incoming call routing",
		"Which numbers?",
		"What should happen when someone calls?",
		"Activate for",
		"Reusable flows",
		"Select all",
		"Manage numbers",
		"Duplicate",
		"Save number assignments",
		"Advanced flows",
		"Back to guided setup",
	} {
		if !strings.Contains(panel, label) {
			t.Errorf("routing panel is missing guided entry point %q", label)
		}
	}
	if !strings.Contains(panel, "Object.keys(NODE_LABELS)") {
		t.Error("advanced editor does not fall back to its local node catalog")
	}
}
