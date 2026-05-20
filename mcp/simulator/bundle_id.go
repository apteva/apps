package main

// Bundle/package id extraction from built artifacts.
//
//   android: `aapt dump badging <apk>` prints a `package: name='...'`
//            line. aapt ships with the Android build-tools; aapt2 has
//            a different CLI, so we use aapt (still bundled).
//   ios:     `plutil -extract CFBundleIdentifier raw -o - <app>/Info.plist`
//            handles both XML and binary plists and ships with macOS,
//            so no Go plist dependency is needed.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var aaptPackageRE = regexp.MustCompile(`package: name='([^']+)'`)

// androidPackageID runs aapt against an APK and returns the package
// name (which is the id `adb shell am start` and `pm` use).
func androidPackageID(apkPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "aapt", "dump", "badging", apkPath).CombinedOutput()
	if err != nil {
		// aapt may be absent even when the rest of the SDK is present
		// (it lives in build-tools/<ver>/). Give an actionable hint.
		return "", fmt.Errorf("aapt dump badging: %w — ensure Android build-tools are on PATH (output: %s)",
			err, strings.TrimSpace(string(out)))
	}
	m := aaptPackageRE.FindStringSubmatch(string(out))
	if len(m) < 2 {
		return "", errors.New("could not find package name in aapt output")
	}
	return m[1], nil
}

// androidLaunchableActivity returns the activity aapt marks as the
// launcher entry point, qualified for `am start -n <pkg>/<activity>`.
// Falls back to the monkey-launch path (am start via the package's
// LAUNCHER intent) when aapt doesn't surface one.
func androidLaunchableActivity(apkPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "aapt", "dump", "badging", apkPath).CombinedOutput()
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`launchable-activity: name='([^']+)'`)
	m := re.FindStringSubmatch(string(out))
	if len(m) < 2 {
		return "", errors.New("no launchable-activity in aapt output")
	}
	return m[1], nil
}

// iosBundleID extracts CFBundleIdentifier from a built .app bundle's
// Info.plist via plutil. plutil is macOS-only, which is fine: iOS
// builds only ever run on macOS.
func iosBundleID(appPath string) (string, error) {
	plist := filepath.Join(appPath, "Info.plist")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "plutil", "-extract", "CFBundleIdentifier", "raw", "-o", "-", plist).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("plutil extract CFBundleIdentifier: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("CFBundleIdentifier empty in Info.plist")
	}
	return id, nil
}
