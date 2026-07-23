package presentation

import (
	"strings"
	"testing"
)

func TestForModeDefaultsToFast(t *testing.T) {
	got, err := ForMode("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled() || got.ShowCursor || got.TypingDelayMS != 0 {
		t.Fatalf("fast defaults changed: %+v", got)
	}
}

func TestForModeDemoPreset(t *testing.T) {
	got, err := ForMode("demo")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled() || !got.ShowCursor || got.TypingDelayMS <= 0 ||
		got.PointerDurationMS <= 0 || got.ClickEffectMS <= 0 || got.PostActionDelayMS <= 0 {
		t.Fatalf("incomplete demo preset: %+v", got)
	}
}

func TestForModeRejectsUnknownMode(t *testing.T) {
	if _, err := ForMode("cinematic"); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestPointerScriptIsNonInteractiveAndUsesCoordinates(t *testing.T) {
	script := pointerScript(321, 654, 360, 520)
	for _, want := range []string{
		`"__apteva_demo_cursor"`,
		`pointerEvents = "none"`,
		`})(321,654,360,520)`,
		`pulse.animate`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("pointer script missing %q", want)
		}
	}
}
