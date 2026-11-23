package main

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// The dashboard widgets parse and validate, and apteva.yaml says the same
// as the embedded manifest.
func TestManifestDeclaresWidgets(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"work-in-progress": "/ui/OperationsWorkWidget.mjs", "operations-stats": "/ui/OperationsStatsWidget.mjs",
		"operations-agenda": "/ui/OperationsAgendaWidget.mjs"}
	if len(m.Provides.UIComponents) != len(want) {
		t.Fatalf("components: %+v", m.Provides.UIComponents)
	}
	published := map[string]bool{}
	for _, p := range m.Provides.Publishes {
		published[p.Name] = true
	}
	for _, c := range m.Provides.UIComponents {
		if want[c.Name] != c.Entry || len(c.Slots) != 1 || c.Slots[0] != "dashboard.home" {
			t.Fatalf("component %+v", c)
		}
		if _, err := os.Stat("ui/" + c.Entry[len("/ui/"):]); err != nil {
			t.Fatalf("%s: bundle missing: %v", c.Name, err)
		}
		for _, topic := range c.RefreshTopics {
			if !published[topic] {
				t.Fatalf("%s refreshes on %s, which the app does not publish", c.Name, topic)
			}
		}
	}
	disk, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != manifestYAML+"\n" && string(disk) != manifestYAML {
		t.Fatal("apteva.yaml and the embedded manifest differ")
	}
}
