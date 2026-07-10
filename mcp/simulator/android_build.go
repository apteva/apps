package main

// Android build backend: gradle assembleDebug. Given an extracted
// source tree, run the project's gradle wrapper (or system gradle) to
// produce a debug APK, then locate + hash + stash it.
//
// Module selection: callers may pass android_module ("app", "mobile",
// …); we run `:<module>:assembleDebug`. Empty defaults to the
// conventional ":app:assembleDebug". The build_cmd escape hatch (a
// full shell command) overrides everything when set, mirroring the
// dev runtime's run_cmd behavior in the code app.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// androidBuildResult is what a successful build produces.
type androidBuildResult struct {
	APKPath  string // path to the stashed APK in the artifacts dir
	BundleID string
}

// buildAndroid runs the gradle build in srcDir and returns the stashed
// APK + its package id. logW receives the combined gradle output so
// the panel's log tail shows progress live.
func (a *App) buildAndroid(ctx context.Context, srcDir, module, buildCmd string, extraArgs, allowedEnv []string, logW *os.File) (*androidBuildResult, error) {
	bin, args, err := resolveGradleCommand(srcDir, module, buildCmd, extraArgs)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logW, "+ %s %s (cwd=%s)\n", bin, strings.Join(args, " "), srcDir)

	if err := runBuildProcess(ctx, bin, args, srcDir, logW, allowedEnv); err != nil {
		return nil, fmt.Errorf("gradle build failed: %w (see build log)", err)
	}

	// Locate the debug APK. gradle writes it under
	// <module>/build/outputs/apk/debug/<...>-debug.apk. We search the
	// whole tree rather than hardcode the module dir, so multi-module
	// layouts and custom output dirs still resolve.
	apk, err := findFirst(srcDir, func(path string, info os.FileInfo) bool {
		if info.IsDir() {
			return false
		}
		base := filepath.Base(path)
		return strings.HasSuffix(base, ".apk") &&
			strings.Contains(path, filepath.Join("outputs", "apk", "debug"))
	})
	if err != nil {
		return nil, fmt.Errorf("build succeeded but no debug APK found under %s: %w", srcDir, err)
	}

	sha, err := hashFile(apk)
	if err != nil {
		return nil, err
	}
	stashed := filepath.Join(a.artifactsDir, sha+".apk")
	if !exists(stashed) {
		if err := copyFile(apk, stashed); err != nil {
			return nil, fmt.Errorf("stash apk: %w", err)
		}
	}

	pkg, err := androidPackageID(stashed)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logW, "=== built %s (package=%s) ===\n", filepath.Base(stashed), pkg)
	return &androidBuildResult{APKPath: stashed, BundleID: pkg}, nil
}

// resolveGradleCommand picks (bin, args). build_cmd override wins
// (run via sh -c). Otherwise prefer the project's ./gradlew wrapper
// over a system gradle — the wrapper pins the gradle version the
// project expects, avoiding "works on my machine" version drift.
func resolveGradleCommand(srcDir, module, buildCmd string, extraArgs []string) (string, []string, error) {
	if bc := strings.TrimSpace(buildCmd); bc != "" {
		return "sh", []string{"-c", bc}, nil
	}
	task := ":app:assembleDebug"
	if m := strings.TrimSpace(module); m != "" {
		m = strings.Trim(m, ":")
		task = ":" + m + ":assembleDebug"
	}
	args := []string{task, "--console=plain"}
	args = append(args, extraArgs...)

	wrapper := filepath.Join(srcDir, "gradlew")
	if exists(wrapper) {
		return wrapper, args, nil
	}
	if _, err := exec.LookPath("gradle"); err != nil {
		return "", nil, fmt.Errorf("no ./gradlew wrapper in repo and gradle not on PATH")
	}
	return "gradle", args, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
