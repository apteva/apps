package main

// iOS build backend: xcodebuild against the iOS Simulator SDK. Given
// an extracted source tree, locate the project/workspace, pick a
// scheme, build for the booted simulator, then locate + stash the
// produced .app bundle.
//
// Project discovery order (matches what an Xcode user expects):
//   1. *.xcworkspace at root  → -workspace
//   2. *.xcodeproj at root    → -project
//   3. Package.swift          → SwiftPM; built via a generated scheme
//      (xcodebuild understands SwiftPM packages directly since Xcode 11)
//
// Scheme selection: ios_scheme arg wins; else the first scheme from
// `xcodebuild -list -json`. build_cmd escape hatch (full shell command)
// overrides everything.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type iosBuildResult struct {
	AppPath  string // stashed .app bundle dir in the artifacts dir
	BundleID string
}

// buildIOS runs xcodebuild for the booted simulator (identified by
// udid) and returns the stashed .app + its bundle id.
func (a *App) buildIOS(srcDir, scheme, buildCmd, udid string, extraArgs []string, logW *os.File) (*iosBuildResult, error) {
	derived := filepath.Join(os.TempDir(), "apteva-sim-derived-"+randHex(8))
	defer os.RemoveAll(derived)

	bin, args, err := resolveXcodebuildCommand(srcDir, scheme, buildCmd, udid, derived, extraArgs)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logW, "+ %s %s (cwd=%s)\n", bin, strings.Join(args, " "), srcDir)

	cmd := exec.Command(bin, args...)
	cmd.Dir = srcDir
	cmd.Stdout = logW
	cmd.Stderr = logW
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("xcodebuild failed: %w (see build log)", err)
	}

	// Locate the .app under DerivedData's
	// Build/Products/Debug-iphonesimulator/<Scheme>.app. Search broadly
	// to tolerate custom CONFIGURATION_BUILD_DIR overrides.
	app, err := findFirst(derived, func(path string, info os.FileInfo) bool {
		return info.IsDir() &&
			strings.HasSuffix(path, ".app") &&
			strings.Contains(path, "iphonesimulator")
	})
	if err != nil {
		return nil, fmt.Errorf("build succeeded but no .app found under DerivedData: %w", err)
	}

	sha, err := hashDir(app)
	if err != nil {
		return nil, err
	}
	stashed := filepath.Join(a.artifactsDir, sha+".app")
	if !exists(stashed) {
		if err := copyTree(app, stashed); err != nil {
			return nil, fmt.Errorf("stash .app: %w", err)
		}
	}

	bundleID, err := iosBundleID(stashed)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logW, "=== built %s (bundle=%s) ===\n", filepath.Base(stashed), bundleID)
	return &iosBuildResult{AppPath: stashed, BundleID: bundleID}, nil
}

// resolveXcodebuildCommand assembles the xcodebuild invocation. The
// destination targets the specific booted simulator by udid so the
// product lands in Debug-iphonesimulator with the right arch.
func resolveXcodebuildCommand(srcDir, scheme, buildCmd, udid, derived string, extraArgs []string) (string, []string, error) {
	if bc := strings.TrimSpace(buildCmd); bc != "" {
		return "sh", []string{"-c", bc}, nil
	}

	projFlag, projValue, err := discoverXcodeProject(srcDir)
	if err != nil {
		return "", nil, err
	}

	if scheme == "" {
		scheme, err = firstScheme(srcDir, projFlag, projValue)
		if err != nil {
			return "", nil, err
		}
	}

	args := []string{}
	if projFlag != "" {
		args = append(args, projFlag, projValue)
	}
	args = append(args,
		"-scheme", scheme,
		"-configuration", "Debug",
		"-destination", "platform=iOS Simulator,id="+udid,
		"-derivedDataPath", derived,
		"-skipPackagePluginValidation",
		"-skipMacroValidation",
		"CODE_SIGNING_ALLOWED=NO", // simulator builds don't need signing
		"build",
	)
	args = append(args, extraArgs...)
	return "xcodebuild", args, nil
}

// discoverXcodeProject returns the xcodebuild flag (-workspace /
// -project) and its value, or ("","") for a SwiftPM package (where
// xcodebuild auto-discovers Package.swift).
func discoverXcodeProject(srcDir string) (flag, value string, err error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return "", "", err
	}
	var workspace, project string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".xcworkspace") && workspace == "" {
			workspace = name
		}
		if strings.HasSuffix(name, ".xcodeproj") && project == "" {
			project = name
		}
	}
	switch {
	case workspace != "":
		return "-workspace", filepath.Join(srcDir, workspace), nil
	case project != "":
		return "-project", filepath.Join(srcDir, project), nil
	case exists(filepath.Join(srcDir, "Package.swift")):
		return "", "", nil // SwiftPM — xcodebuild discovers it
	}
	return "", "", fmt.Errorf("no .xcworkspace, .xcodeproj, or Package.swift in %s", srcDir)
}

// firstScheme runs `xcodebuild -list -json` and returns the first
// scheme. Errors with the available list when none exist.
func firstScheme(srcDir, projFlag, projValue string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"-list", "-json"}
	if projFlag != "" {
		args = append([]string{projFlag, projValue}, args...)
	}
	cmd := exec.CommandContext(ctx, "xcodebuild", args...)
	cmd.Dir = srcDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("xcodebuild -list: %w", err)
	}
	var parsed struct {
		Project   struct{ Schemes []string `json:"schemes"` } `json:"project"`
		Workspace struct{ Schemes []string `json:"schemes"` } `json:"workspace"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", fmt.Errorf("decode xcodebuild -list json: %w", err)
	}
	schemes := parsed.Project.Schemes
	if len(schemes) == 0 {
		schemes = parsed.Workspace.Schemes
	}
	if len(schemes) == 0 {
		return "", fmt.Errorf("no schemes found — set ios_scheme explicitly")
	}
	return schemes[0], nil
}

func randHex(n int) string {
	tok, _ := randomToken(n)
	if tok == "" {
		// randomToken only fails if crypto/rand fails, which is
		// effectively never; fall back to a timestamp so the build dir
		// is still unique-ish.
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return tok
}
