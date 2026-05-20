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
// resolved a launchable activity from aapt; otherwise use monkey to
// fire the package's LAUNCHER intent (works without knowing the
// activity name).
func launchAndroid(serial, pkg, activity string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if activity != "" {
		component := pkg + "/" + activity
		out, err := exec.CommandContext(ctx, "adb", "-s", serial, "shell", "am", "start",
			"-n", component, "-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER").CombinedOutput()
		if err == nil && !strings.Contains(string(out), "Error") {
			return nil
		}
		// Fall through to monkey on error — some activities aren't
		// directly start-able via am start with these flags.
	}
	out, err := exec.CommandContext(ctx, "adb", "-s", serial, "shell", "monkey",
		"-p", pkg, "-c", "android.intent.category.LAUNCHER", "1").CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb monkey launch: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	if strings.Contains(string(out), "No activities found") {
		return fmt.Errorf("no launchable activity for package %s", pkg)
	}
	return nil
}
