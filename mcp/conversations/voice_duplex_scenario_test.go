//go:build scenario

package main

import (
	"os"
	"os/exec"
	"testing"
)

// Runner owns Server, Core, agent and sidecar. The browser uses the production
// player/worklet and real Web Audio; only the caller's microphone is synthesized.
func TestScenario_VoiceDuplexBrowser(t *testing.T) {
	_ = newScenarioClient(t)
	command := exec.Command("bun", "frontend/tests/voice-duplex.tier3.ts")
	command.Env = os.Environ()
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		t.Fatalf("live duplex browser workflow: %v", err)
	}
}
