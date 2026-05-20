package main

// iOS Simulator device discovery + creation, via `xcrun simctl`. The
// host-managed Simulator service owns the device lifecycle — we don't
// spawn child processes the way the android emulator path does. Each
// device is identified by its UDID (a UUID-shaped string simctl mints
// on create); that UDID becomes the sim_id for ios sims rows.
//
// `simctl list devices --json` is the source of truth for what's
// available; `simctl list runtimes --json` enumerates installed iOS
// runtimes; `simctl list devicetypes --json` enumerates device-type
// identifiers. The relevant identifier shapes:
//
//   runtime    "com.apple.CoreSimulator.SimRuntime.iOS-17-5"
//   devicetype "com.apple.CoreSimulator.SimDeviceType.iPhone-15-Pro"
//
// Config + tool args accept the trailing segment ("iOS-17-5",
// "iPhone-15-Pro") and we prefix the canonical bundle id ourselves.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	iosRuntimePrefix    = "com.apple.CoreSimulator.SimRuntime."
	iosDeviceTypePrefix = "com.apple.CoreSimulator.SimDeviceType."
)

// simctlDevice mirrors the relevant fields of an entry in
// `simctl list devices --json`. Other fields (logPath, dataPath, …)
// are present but uninteresting at this layer.
type simctlDevice struct {
	UDID    string `json:"udid"`
	Name    string `json:"name"`
	State   string `json:"state"`     // "Booted" | "Shutdown" | …
	Runtime string `json:"-"`         // populated from the map key, see listIOSDevices
	DeviceType string `json:"deviceTypeIdentifier,omitempty"`
}

type simctlRuntime struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	IsAvailable bool  `json:"isAvailable"`
}

type simctlDeviceType struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
}

// listIOSDevices flattens `simctl list devices --json`'s
// runtime-keyed map into a single slice with Runtime populated per
// entry. Skips unavailable runtimes — devices under those can't boot
// anyway and listing them confuses the picker.
func listIOSDevices(ctx context.Context) ([]simctlDevice, error) {
	out, err := runSimctlJSON(ctx, "list", "devices", "--json")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Devices map[string][]simctlDevice `json:"devices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("decode simctl devices: %w", err)
	}
	flat := []simctlDevice{}
	for runtime, devs := range parsed.Devices {
		// Runtime key is the full SimRuntime identifier. Strip the
		// prefix so callers see the readable short form.
		shortRuntime := strings.TrimPrefix(runtime, iosRuntimePrefix)
		for _, d := range devs {
			d.Runtime = shortRuntime
			flat = append(flat, d)
		}
	}
	sort.Slice(flat, func(i, j int) bool { return flat[i].Runtime < flat[j].Runtime })
	return flat, nil
}

// listIOSRuntimes returns installed runtimes, sorted newest first.
// "Newest" = highest version string, naive lexical compare; iOS
// version strings have always sorted correctly that way (17.5 > 17.4 > 16.4)
// and we don't need a real semver compare here.
func listIOSRuntimes(ctx context.Context) ([]simctlRuntime, error) {
	out, err := runSimctlJSON(ctx, "list", "runtimes", "--json")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Runtimes []simctlRuntime `json:"runtimes"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("decode simctl runtimes: %w", err)
	}
	avail := parsed.Runtimes[:0]
	for _, r := range parsed.Runtimes {
		if r.IsAvailable {
			avail = append(avail, r)
		}
	}
	sort.Slice(avail, func(i, j int) bool { return avail[i].Version > avail[j].Version })
	return avail, nil
}

// resolveIOSRuntime turns a caller's short-form runtime ("iOS-17-5") —
// or empty, meaning "newest installed" — into the full identifier
// simctl wants. Errors when the named runtime isn't installed.
func resolveIOSRuntime(ctx context.Context, short string) (string, error) {
	short = strings.TrimSpace(short)
	runtimes, err := listIOSRuntimes(ctx)
	if err != nil {
		return "", err
	}
	if len(runtimes) == 0 {
		return "", errors.New("no iOS runtimes installed — open Xcode → Settings → Platforms")
	}
	if short == "" {
		return runtimes[0].Identifier, nil
	}
	full := short
	if !strings.HasPrefix(full, iosRuntimePrefix) {
		full = iosRuntimePrefix + short
	}
	for _, r := range runtimes {
		if r.Identifier == full {
			return r.Identifier, nil
		}
	}
	names := make([]string, 0, len(runtimes))
	for _, r := range runtimes {
		names = append(names, strings.TrimPrefix(r.Identifier, iosRuntimePrefix))
	}
	return "", fmt.Errorf("ios runtime %q not installed; available: %s", short, strings.Join(names, ", "))
}

// resolveIOSDeviceType maps a short name ("iPhone-15-Pro") to the full
// SimDeviceType identifier. Errors when simctl doesn't recognize it.
func resolveIOSDeviceType(ctx context.Context, short string) (string, error) {
	short = strings.TrimSpace(short)
	if short == "" {
		return "", errors.New("ios device_type required")
	}
	out, err := runSimctlJSON(ctx, "list", "devicetypes", "--json")
	if err != nil {
		return "", err
	}
	var parsed struct {
		DeviceTypes []simctlDeviceType `json:"devicetypes"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", fmt.Errorf("decode simctl devicetypes: %w", err)
	}
	full := short
	if !strings.HasPrefix(full, iosDeviceTypePrefix) {
		full = iosDeviceTypePrefix + short
	}
	for _, d := range parsed.DeviceTypes {
		if d.Identifier == full {
			return d.Identifier, nil
		}
	}
	return "", fmt.Errorf("ios device_type %q unknown; run `xcrun simctl list devicetypes` to see options", short)
}

// ensureIOSDevice finds a device matching (device_type, runtime) on
// this host, or creates one. Returns its UDID.
//
// Naming rule: simulator devices created by us are named
// "apteva-<device>-<runtime>" so listIOSDevices can identify and
// reuse them on the next boot. Existing devices with that same name
// match-and-reuse before we create another.
func ensureIOSDevice(ctx context.Context, deviceType, runtime string) (string, error) {
	fullRuntime, err := resolveIOSRuntime(ctx, runtime)
	if err != nil {
		return "", err
	}
	fullDeviceType, err := resolveIOSDeviceType(ctx, deviceType)
	if err != nil {
		return "", err
	}

	desiredName := "apteva-" + slugForAVD(deviceType) + "-" + slugForAVD(strings.TrimPrefix(fullRuntime, iosRuntimePrefix))

	devices, err := listIOSDevices(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range devices {
		if d.Name == desiredName && strings.HasSuffix(fullRuntime, d.Runtime) && d.DeviceType == fullDeviceType {
			return d.UDID, nil
		}
	}

	// Need to create. `simctl create <name> <devicetype> <runtime>`
	// prints the new UDID on stdout.
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "xcrun", "simctl", "create", desiredName, fullDeviceType, fullRuntime).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("simctl create: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	udid := strings.TrimSpace(string(out))
	if udid == "" {
		return "", errors.New("simctl create returned empty UDID")
	}
	return udid, nil
}

// runSimctlJSON runs an `xcrun simctl ...` invocation expected to
// produce JSON on stdout, with a 10s timeout. Used by every list
// helper above.
func runSimctlJSON(ctx context.Context, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	full := append([]string{"simctl"}, args...)
	cmd := exec.CommandContext(cctx, "xcrun", full...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("xcrun simctl %s: %w (stderr: %s)",
				strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("xcrun simctl %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
