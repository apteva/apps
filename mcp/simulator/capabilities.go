package main

// Host-capability probe. Run at OnMount (cached for the sidecar
// lifetime) and on every sims_capabilities tool call. Each backend
// reports availability + a per-tool breakdown so the panel can show
// "iOS is unavailable because idb_companion isn't on PATH" with an
// actionable hint, not just a boolean.
//
// Caching policy: we re-probe on every tool call. The probe is cheap
// (a handful of LookPath + version flag invocations, ~50ms total) and
// the user installing/uninstalling SDK components should be reflected
// without a sidecar restart. If profiling later shows this is hot,
// add a 30s in-memory cache here.

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Capabilities is the response shape returned by sims_capabilities.
type Capabilities struct {
	Android PlatformCapability `json:"android"`
	IOS     PlatformCapability `json:"ios"`
}

// PlatformCapability is the per-backend probe result.
//
//	Available: true → sims_boot for this platform should work
//	Reasons:   one human-readable line per missing dependency. Empty
//	           when Available is true. Always present so panels can
//	           render it unconditionally.
//	Tools:     map of tool-name → resolved info (path + version).
//	           Missing tools show up as {Found: false}.
type PlatformCapability struct {
	Available          bool                 `json:"available"`
	Reasons            []string             `json:"reasons"`
	BuildAvailable     bool                 `json:"build_available"`
	BuildReasons       []string             `json:"build_reasons"`
	StreamingAvailable bool                 `json:"streaming_available"`
	StreamingReasons   []string             `json:"streaming_reasons"`
	Tools              map[string]ToolProbe `json:"tools"`
}

// ToolProbe is the result of probing one external tool on PATH.
type ToolProbe struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Note    string `json:"note,omitempty"` // when found-but-degraded (e.g. wrong version line)
}

// probeCapabilities runs both backend probes and assembles the
// response. Each backend is independent — a missing adb doesn't
// affect the iOS probe.
//
// ctx isn't strictly needed today (no platform calls), but kept on
// the signature so future probes that need install config (e.g.
// "where is idb_companion configured?") can read ctx.Config().
func probeCapabilities(ctx *sdk.AppCtx) Capabilities {
	return Capabilities{
		Android: probeAndroid(ctx),
		IOS:     probeIOS(ctx),
	}
}

// ─── Android ────────────────────────────────────────────────────────

func probeAndroid(ctx *sdk.AppCtx) PlatformCapability {
	out := PlatformCapability{
		Reasons:      []string{},
		BuildReasons: []string{},
		Tools:        map[string]ToolProbe{},
	}
	probes := []struct {
		name string
		// versionArg is the flag that prints a version string in <2s.
		// "" means we just check presence on PATH.
		versionArg string
		hint       string
	}{
		{"adb", "--version", "Install Android Platform Tools (sdkmanager 'platform-tools' or `brew install --cask android-platform-tools`)."},
		{"emulator", "-version", "Install the Android Emulator (sdkmanager 'emulator')."},
		{"avdmanager", "", "Install Android Command-line Tools (sdkmanager 'cmdline-tools;latest')."},
		{"aapt", "version", "Install Android Build Tools and add build-tools/<version> to PATH (sdkmanager 'build-tools;35.0.0')."},
		{"gradle", "--version", "Optional when a repo has ./gradlew; otherwise install Gradle 8+ (`brew install gradle` / `sdk install gradle 8.7`)."},
		{"java", "-version", "Install a JDK 17 (`brew install openjdk@17` / `apt install openjdk-17-jdk`)."},
	}
	for _, p := range probes {
		tp := lookupAndVersion(p.name, p.versionArg)
		out.Tools[p.name] = tp
		if !tp.Found {
			switch p.name {
			case "adb", "emulator", "avdmanager":
				out.Reasons = append(out.Reasons, p.name+" not found on PATH. "+p.hint)
			case "aapt", "java":
				out.BuildReasons = append(out.BuildReasons, p.name+" not found on PATH. "+p.hint)
			}
		}
	}
	if !out.Tools["gradle"].Found {
		tp := out.Tools["gradle"]
		tp.Note = "Optional when the uploaded Android repo includes ./gradlew; builds without a wrapper need system gradle."
		out.Tools["gradle"] = tp
	}

	// android backend works wherever the four binaries above exist — no
	// OS-specific gating. (KVM acceleration on linux is recommended but
	// not strictly required; the emulator falls back to software
	// rendering, which is slow but functional.)
	out.Available = len(out.Reasons) == 0
	out.BuildReasons = append(out.BuildReasons, out.Reasons...)
	out.BuildAvailable = len(out.BuildReasons) == 0
	if !out.Tools["adb"].Found {
		out.StreamingReasons = append(out.StreamingReasons, "adb not found on PATH")
	}
	out.StreamingAvailable = len(out.StreamingReasons) == 0
	return out
}

// ─── iOS ────────────────────────────────────────────────────────────

func probeIOS(ctx *sdk.AppCtx) PlatformCapability {
	out := PlatformCapability{
		Reasons:          []string{},
		BuildReasons:     []string{},
		StreamingReasons: []string{},
		Tools:            map[string]ToolProbe{},
	}
	// Hard gate: iOS Simulator is macOS-only. No partial-functionality
	// state — iOS either fully works or isn't available, per the v0.1
	// design call to keep error stories clean.
	if runtime.GOOS != "darwin" {
		out.Available = false
		out.Reasons = []string{"iOS Simulator requires macOS (this host is " + runtime.GOOS + ")."}
		out.BuildAvailable = false
		out.BuildReasons = append([]string{}, out.Reasons...)
		return out
	}

	probes := []struct {
		name       string
		versionArg string
		hint       string
	}{
		{"xcrun", "--version", "Install Xcode + Command Line Tools (`xcode-select --install`)."},
		{"xcodebuild", "-version", "Install Xcode from the Mac App Store, then run `sudo xcode-select -s /Applications/Xcode.app/Contents/Developer`."},
		{"simctl", "", "Bundled with Xcode CLI tools. If xcrun is present, this should be too."},
	}
	for _, p := range probes {
		var tp ToolProbe
		if p.name == "simctl" {
			// simctl is accessed via `xcrun simctl …` rather than a
			// direct binary. Probe with a real command (`simctl help`)
			// to confirm it's wired up.
			tp = xcrunSubProbe("simctl")
		} else {
			tp = lookupAndVersion(p.name, p.versionArg)
		}
		out.Tools[p.name] = tp
		if !tp.Found {
			out.Reasons = append(out.Reasons, p.name+" not found. "+p.hint)
		}
	}
	out.Available = len(out.Reasons) == 0
	out.BuildAvailable = out.Available
	out.BuildReasons = append([]string{}, out.Reasons...)
	out.StreamingAvailable = out.Available

	// iOS live view has a native fallback: a low-FPS screenshot stream
	// over `xcrun simctl io screenshot`. idb remains optional for the
	// high-FPS H.264 stream and for input injection.
	idb := lookupAndVersion("idb", "--help")
	out.Tools["idb"] = idb
	if !idb.Found {
		out.StreamingReasons = append(out.StreamingReasons,
			"idb not found; using native simctl screenshot streaming. Install idb only for high-FPS iOS live view and input.")
	}

	// idb_companion path can be overridden in install config. Empty =
	// fall back to PATH lookup.
	idbPath := ""
	if ctx != nil {
		idbPath = strings.TrimSpace(ctx.Config().Get("idb_companion_path"))
	}
	tp := probeIDBCompanion(idbPath)
	out.Tools["idb_companion"] = tp
	if !tp.Found {
		out.StreamingReasons = append(out.StreamingReasons,
			"idb_companion not found; using native simctl screenshot streaming. Install via `brew install idb-companion` for high-FPS iOS live view and input.")
	}

	return out
}

// probeIDBCompanion checks for idb_companion either at an explicit path
// (from config_schema.idb_companion_path) or on PATH.
func probeIDBCompanion(configPath string) ToolProbe {
	if configPath != "" {
		// Explicit override — run it with --help to confirm it's an
		// executable that responds. We don't try to extract a version
		// string because idb's version flag formatting changes
		// release-to-release.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, configPath, "--help")
		if err := cmd.Run(); err != nil {
			return ToolProbe{Name: "idb_companion", Found: false,
				Note: "configured path " + configPath + " not executable: " + err.Error()}
		}
		return ToolProbe{Name: "idb_companion", Found: true, Path: configPath}
	}
	return lookupAndVersion("idb_companion", "--help")
}

// ─── Shared probe helpers ───────────────────────────────────────────

// lookupAndVersion finds a tool on PATH, then captures the first line
// of its version output. Returns Found=false if the binary isn't on
// PATH or doesn't respond within 2s.
func lookupAndVersion(name, versionArg string) ToolProbe {
	path, err := exec.LookPath(name)
	if err != nil {
		return ToolProbe{Name: name, Found: false}
	}
	tp := ToolProbe{Name: name, Found: true, Path: path}
	if versionArg == "" {
		return tp
	}
	tp.Version = firstLine(runWithTimeout(name, versionArg))
	return tp
}

// xcrunSubProbe verifies that an `xcrun <subcmd>` invocation works.
// Used for tools that don't live as a top-level binary on PATH
// (simctl is the canonical example: `xcrun --find simctl` resolves to
// /Applications/Xcode.app/.../usr/bin/simctl but invocation goes
// through xcrun).
func xcrunSubProbe(subcmd string) ToolProbe {
	if _, err := exec.LookPath("xcrun"); err != nil {
		return ToolProbe{Name: subcmd, Found: false, Note: "xcrun not on PATH"}
	}
	out := runWithTimeout("xcrun", "--find", subcmd)
	out = strings.TrimSpace(out)
	if out == "" {
		return ToolProbe{Name: subcmd, Found: false, Note: "xcrun could not resolve " + subcmd}
	}
	return ToolProbe{Name: subcmd, Found: true, Path: out}
}

// runWithTimeout runs a command and captures both stdout + stderr,
// time-boxed to 2s. The two streams are concatenated; most version
// flags print to either depending on the tool (java -version goes to
// stderr, for instance).
func runWithTimeout(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	combined, err := cmd.CombinedOutput()
	if err != nil && len(combined) == 0 {
		return ""
	}
	return string(combined)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// capabilityCheckFor returns nil if the named platform is fully usable
// on this host, otherwise an error whose message lists every missing
// dependency. sims_run / sims_boot use this as the front-door guard so
// callers see a clear "host_unsupported" reason rather than a confusing
// downstream failure.
func capabilityCheckFor(ctx *sdk.AppCtx, platform string) error {
	caps := probeCapabilities(ctx)
	var pc PlatformCapability
	switch platform {
	case "android":
		pc = caps.Android
	case "ios":
		pc = caps.IOS
	default:
		return errors.New("unknown platform: " + platform)
	}
	if pc.Available {
		return nil
	}
	return errors.New("host_unsupported: " + strings.Join(pc.Reasons, "; "))
}

func capabilityCheckForNeeds(ctx *sdk.AppCtx, platform string, needBuild, needStream bool) error {
	caps := probeCapabilities(ctx)
	var pc PlatformCapability
	switch platform {
	case "android":
		pc = caps.Android
	case "ios":
		pc = caps.IOS
	default:
		return errors.New("unknown platform: " + platform)
	}
	if !pc.Available {
		return errors.New("host_unsupported: " + strings.Join(pc.Reasons, "; "))
	}
	if needBuild && !pc.BuildAvailable {
		return errors.New("build_unsupported: " + strings.Join(pc.BuildReasons, "; "))
	}
	if needStream && !pc.StreamingAvailable {
		return errors.New("streaming_unsupported: " + strings.Join(pc.StreamingReasons, "; "))
	}
	return nil
}

func streamingCapabilityCheckFor(ctx *sdk.AppCtx, platform string) error {
	caps := probeCapabilities(ctx)
	var pc PlatformCapability
	switch platform {
	case "android":
		pc = caps.Android
	case "ios":
		pc = caps.IOS
	default:
		return errors.New("unknown platform: " + platform)
	}
	if pc.StreamingAvailable {
		return nil
	}
	return errors.New("streaming_unsupported: " + strings.Join(pc.StreamingReasons, "; "))
}
