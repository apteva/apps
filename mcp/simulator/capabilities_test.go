package main

import (
	"runtime"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// probeCapabilities must never panic and must populate both platform
// blocks. The Available flag depends on what's installed on the dev
// machine running the test, so we don't assert on it — just shape.
func TestProbeCapabilities_Shape(t *testing.T) {
	caps := probeCapabilities(nil)
	for _, name := range []string{"adb", "emulator", "avdmanager", "aapt", "gradle", "java"} {
		if _, ok := caps.Android.Tools[name]; !ok {
			t.Errorf("android probe missing tool entry %q", name)
		}
	}
	// On non-darwin hosts the iOS probe short-circuits with a single
	// "needs macOS" reason and no per-tool entries — that's the
	// expected behaviour and what the panel renders against.
	if runtime.GOOS != "darwin" {
		if caps.IOS.Available {
			t.Errorf("iOS reported available on %s — should be macOS-only", runtime.GOOS)
		}
		if len(caps.IOS.Reasons) == 0 {
			t.Errorf("iOS unavailable but no reasons given")
		}
		return
	}
	for _, name := range []string{"xcrun", "xcodebuild", "simctl", "idb", "idb_companion"} {
		if _, ok := caps.IOS.Tools[name]; !ok {
			t.Errorf("ios probe missing tool entry %q", name)
		}
	}
}

// capabilityCheckFor returns nil only when the platform is fully
// usable; otherwise an error that mentions every missing dep. The
// test exercises the error path — the success path needs the host
// to actually have the SDKs installed, which we can't assume.
func TestCapabilityCheckFor_Errors(t *testing.T) {
	if err := capabilityCheckFor(nil, "bogus"); err == nil {
		t.Error("expected error for unknown platform, got nil")
	}
}

// sims_capabilities tool must produce a JSON-shaped result when
// invoked through the SDK tool path.
func TestToolSimsCapabilities_Invokes(t *testing.T) {
	app := &App{}
	var tool sdk.Tool
	for _, candidate := range app.MCPTools() {
		if candidate.Name == "sims_capabilities" {
			tool = candidate
			break
		}
	}
	if tool.Handler == nil {
		t.Fatal("sims_capabilities not wired up")
	}
	out, err := tool.Handler(nil, map[string]any{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if _, ok := out.(Capabilities); !ok {
		t.Errorf("handler returned %T, want Capabilities", out)
	}
}
