//go:build scenario

package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestScenario_InstalledAppCardsBrowser(t *testing.T) {
	c := newScenarioClient(t)
	if c.installs["computer"] == 0 || c.installs["tasks"] == 0 {
		t.Fatal("scenario must install Computer and Tasks")
	}
	command := exec.Command("bun", "frontend/tests/app-components.tier3.ts")
	command.Env = os.Environ()
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		t.Fatalf("installed app cards browser workflow: %v", err)
	}
}
