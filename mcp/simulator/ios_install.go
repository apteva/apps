package main

// iOS install + launch via simctl. install copies the .app into the
// device's container; launch starts it by bundle id. Both target a
// specific UDID rather than "booted" so concurrent sims don't collide.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// installIOSApp installs a built .app bundle onto the device.
func installIOSApp(udid, appPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "install", udid, appPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("simctl install: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// launchIOS starts the installed app by bundle id. Returns the app's
// pid (simctl prints "<bundle>: <pid>") on success — useful for log
// predicate filtering, though we don't require it.
func launchIOS(udid, bundleID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "launch", udid, bundleID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("simctl launch: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
