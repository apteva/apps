//go:build scenario

package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestScenario_PersistentResourceViews(t *testing.T) {
	c := newScenarioClient(t)
	if c.installs["view-fixture"] == 0 {
		t.Fatal("scenario must install the real resource producer")
	}
	command := exec.Command("bun", "frontend/tests/persistent-views.tier3.ts")
	command.Env = os.Environ()
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		t.Fatalf("persistent resource views: %v", err)
	}
}
