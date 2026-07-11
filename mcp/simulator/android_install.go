package main

// Android install + launch via adb. install -r replaces an existing
// install in place (so re-running a dev cycle is fast); am start with
// the LAUNCHER intent boots the app's entry activity.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// installAndroidAPK pushes + installs an APK onto the device. -r
// reinstalls keeping data; -t allows test/debug APKs; -g grants
// runtime permissions up front so the app doesn't stall on a
// permission dialog the first launch.
func installAndroidAPK(serial, apkPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "adb", "-s", serial, "install", "-r", "-t", "-g", apkPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb install: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "Success") {
		return fmt.Errorf("adb install did not report Success: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// launchAndroid starts the app. Prefer an explicit component when we
// resolved a launchable activity from aapt. Otherwise ask Android's
// package manager for the LAUNCHER activity before falling back to
// monkey for compatibility with older devices.
func launchAndroid(serial, pkg, activity string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "adb", append([]string{"-s", serial}, args...)...).CombinedOutput()
	}
	return launchAndroidWithADB(pkg, activity, run)
}

type adbCommand func(args ...string) ([]byte, error)

func launchAndroidWithADB(pkg, activity string, run adbCommand) error {
	start := func(component string) bool {
		out, err := run("shell", "am", "start", "-n", component,
			"-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER")
		return err == nil && !strings.Contains(string(out), "Error")
	}

	if activity != "" {
		if start(pkg + "/" + activity) {
			return nil
		}
		// The APK metadata can occasionally name an activity that is not
		// directly startable, so resolve the device's actual launcher next.
	}

	resolved, resolveErr := run("shell", "cmd", "package", "resolve-activity", "--brief",
		"-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER", pkg)
	if resolveErr == nil {
		if component := resolvedActivity(string(resolved)); component != "" && start(component) {
			return nil
		}
	}

	out, err := run("shell", "monkey", "-p", pkg, "-c", "android.intent.category.LAUNCHER", "1")
	if err != nil {
		return fmt.Errorf("adb monkey launch: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if strings.Contains(string(out), "No activities found") {
		return fmt.Errorf("no launchable activity for package %s", pkg)
	}
	return nil
}

func resolvedActivity(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(lines[i])
		if !strings.ContainsAny(candidate, " \t") && strings.Contains(candidate, "/") {
			return candidate
		}
	}
	return ""
}
