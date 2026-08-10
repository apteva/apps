package presentation

import (
	"context"
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
		`})("",321,654,true,"",360,520,true)`,
		`pulse.animate`,
		`data-apteva-presentation`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("pointer script missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`.click(`,
		`.focus(`,
		`dispatchEvent`,
		`scrollIntoView`,
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("presentation script must not produce page input; found %q", forbidden)
		}
	}
}

func TestTargetCueUsesResolvedSelectorAndEscapesCaption(t *testing.T) {
	script := targetCueScript(
		`input[name="odd'value"]`,
		12,
		34,
		true,
		`Text "updated"`,
		360,
		520,
		true,
	)
	for _, want := range []string{
		`document.querySelector(selector)`,
		`"input[name=\"odd'value\"]"`,
		`"Text \"updated\""`,
		`"__apteva_demo_caption"`,
		`pointerEvents = "none"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("target cue script missing %q", want)
		}
	}
}

func TestCueTargetFastModeIsStrictNoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	options, err := ForMode("fast")
	if err != nil {
		t.Fatal(err)
	}
	if err := CueTarget(ctx, "#control", 10, 20, true, "Updated", options); err != nil {
		t.Fatalf("fast presentation unexpectedly touched browser context: %v", err)
	}
	if err := MoveToTarget(ctx, "#control", 10, 20, true, options); err != nil {
		t.Fatalf("fast presentation move unexpectedly touched browser context: %v", err)
	}
}
