package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestAndroidSourceAvoidsIdleCollectorAndBoundsFailures(t *testing.T) {
	valid := `<hierarchy><node package="com.example"/></hierarchy>`
	for _, mode := range []string{"fast", "fallback", "missing", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			d := newDrivers(runnerConfig{})
			idleCalls := 0
			d.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				cmd := args[len(args)-1]
				if strings.Contains(cmd, "AptevaUiSource") && mode == "fast" {
					return []byte(valid), nil
				}
				if strings.Contains(cmd, "uiautomator") {
					idleCalls++
				}
				if strings.Contains(cmd, "'cat'") {
					if mode == "fallback" {
						return []byte(valid), nil
					}
					if mode == "malformed" {
						return []byte(`<hierarchy><node>`), nil
					}
				}
				return nil, nil
			}
			got, err := d.androidSource(context.Background(), Session{ID: "s", Device: Device{UDID: "phone"}})
			if (err == nil) != (mode == "fast" || mode == "fallback") {
				t.Fatal(got, err)
			}
			if mode == "fast" && idleCalls != 0 {
				t.Fatal("fast source waited for an idle screen")
			}
		})
	}
	if _, err := checkedAndroidSource(bytes.Repeat([]byte("x"), maxSource+1)); err == nil {
		t.Fatal("oversized source accepted")
	}
	z, err := zip.NewReader(bytes.NewReader(androidSourceJar), int64(len(androidSourceJar)))
	if err != nil || len(z.File) != 1 || z.File[0].Name != "classes.dex" {
		t.Fatal("invalid source collector artifact", err)
	}
}

// selection_state is driver-scoped: only the ADB driver's source carries it.
// Appium-driven Android phones and native iOS must not advertise it.
func TestSelectionStateCapabilityIsADBOnly(t *testing.T) {
	if !contains(capabilities("adb"), "selection_state") {
		t.Fatal("adb driver missing selection_state")
	}
	for _, driver := range []string{"appium", driverXCTest} {
		if contains(capabilities(driver), "selection_state") {
			t.Fatalf("%s advertises selection_state", driver)
		}
	}
}

// The collector runs on the device, so its output cannot be unit-tested here.
// This guards that the shipped jar was rebuilt from the source that writes the
// selection attributes (a stale jar would silently omit them).
func TestAndroidSourceJarCarriesSelectionAttributes(t *testing.T) {
	z, err := zip.NewReader(bytes.NewReader(androidSourceJar), int64(len(androidSourceJar)))
	if err != nil {
		t.Fatal(err)
	}
	r, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dex, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checkable", "checked", "selected", "scrollable"} {
		// dex string data: ULEB128 length byte, then the bytes, then NUL.
		if !bytes.Contains(dex, append(append([]byte{byte(len(name))}, name...), 0)) {
			t.Fatalf("source.dex.jar lacks %q; rebuild with android-source/build.py", name)
		}
	}
}
