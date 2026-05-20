package main

// AVD (Android Virtual Device) management. AVDs are JSON-described
// virtual devices created via `avdmanager create avd`; once created
// they live under $ANDROID_AVD_HOME (default ~/.android/avd) and are
// reusable across boots. Auto-create policy:
//
//   1. List existing AVDs (`emulator -list-avds`).
//   2. If a sim row already exists for this project with status≠crashed
//      reuse its name.
//   3. Else look for an AVD matching the requested device_type. If
//      found, reuse it. Otherwise create a fresh one named
//      "apteva-<device_type>-<sha8>".
//
// The system image declared in config_schema.android_image must already
// be installed (`sdkmanager --install …`). We do NOT auto-install
// system images — they're hundreds of megabytes and the first install
// takes minutes; we leave that to the operator and surface a clear
// error when the image is missing.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// listAVDs returns the AVD names known to the emulator binary. The
// `emulator -list-avds` invocation completes in <500ms on a warm
// machine and prints one name per line.
func listAVDs(ctx context.Context) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "emulator", "-list-avds").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("emulator -list-avds: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	names := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// avdmanager sometimes prints `INFO: …` headers — skip anything
		// that looks like a tag.
		if line == "" || strings.HasPrefix(line, "INFO:") || strings.HasPrefix(line, "WARNING:") {
			continue
		}
		names = append(names, line)
	}
	return names, nil
}

// ensureAVD finds or creates an AVD usable for the requested
// device_type + system image. Returns the AVD name (which is also the
// sims.id for android rows).
//
// Naming rule: AVDs we create are named "apteva-<device_type>-<sha8>"
// where sha8 is a hash of the system image string. That keeps multiple
// images for the same device profile from colliding, and lets us
// safely list-and-reuse on subsequent boots.
func ensureAVD(ctx context.Context, deviceType, systemImage string) (string, error) {
	deviceType = strings.TrimSpace(deviceType)
	systemImage = strings.TrimSpace(systemImage)
	if deviceType == "" {
		return "", errors.New("android device_type required")
	}
	if systemImage == "" {
		return "", errors.New("android system image required")
	}

	avdName := canonicalAVDName(deviceType, systemImage)

	existing, err := listAVDs(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range existing {
		if n == avdName {
			return avdName, nil
		}
	}

	// Need to create it. `avdmanager create avd -n <name> -k <image> -d <device>`
	// reads the device profile and system image and writes the AVD
	// config. The interactive "Do you wish to create a custom hardware
	// profile?" prompt is suppressed by piping "no\n" on stdin.
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "avdmanager", "create", "avd",
		"-n", avdName,
		"-k", systemImage,
		"-d", deviceType,
		"--force",
	)
	cmd.Stdin = strings.NewReader("no\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if strings.Contains(text, "Package path is not valid") || strings.Contains(text, "could not find package") {
			return "", fmt.Errorf("system image %q not installed — run `sdkmanager --install %q` first (output: %s)",
				systemImage, systemImage, text)
		}
		if strings.Contains(text, "No device found with id") {
			return "", fmt.Errorf("device profile %q unknown to avdmanager (run `avdmanager list device` to see options): %s",
				deviceType, text)
		}
		return "", fmt.Errorf("avdmanager create avd: %w (output: %s)", err, text)
	}
	return avdName, nil
}

// canonicalAVDName produces a stable name from the (device, image)
// pair. Used so listAVDs can identify AVDs we created previously.
func canonicalAVDName(deviceType, systemImage string) string {
	h := sha1.Sum([]byte(deviceType + "|" + systemImage))
	return fmt.Sprintf("apteva-%s-%s", slugForAVD(deviceType), hex.EncodeToString(h[:4]))
}

// slugForAVD reduces an arbitrary device-profile string to the
// [a-zA-Z0-9_-] subset avdmanager accepts in -n. avdmanager's parser
// is tighter than slug rules elsewhere in the codebase, so we keep
// this local rather than reusing the repo slugify.
func slugForAVD(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out == "" {
		return "device"
	}
	return out
}
