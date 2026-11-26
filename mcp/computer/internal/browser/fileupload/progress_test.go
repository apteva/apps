package fileupload

import "testing"

func TestDispatched(t *testing.T) {
	for phase, want := range map[string]string{PhaseStaging: "false", PhaseResolving: "false", PhaseAssigning: "unknown", PhaseAssigned: "true", "": ""} {
		if got := Dispatched(phase); got != want {
			t.Errorf("%q: %q, want %q", phase, got, want)
		}
	}
	var p *Progress
	p.Set(PhaseAssigned) // nil progress is a no-op
	if p.Phase() != "" {
		t.Fatal("nil progress has no phase")
	}
}
