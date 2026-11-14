package main

import (
	"archive/zip"
	"bytes"
	"context"
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
