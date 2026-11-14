package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// This read-only shell helper connects to Android's UI automation API without
// waiting for animations to stop. It installs no APK and does not suppress the
// user's accessibility services. Source and its bounded collector ship together.
//
//go:embed android-source/source.dex.jar
var androidSourceJar []byte

func (d *drivers) androidSource(ctx context.Context, s Session) (string, error) {
	if source, err := d.fastAndroidSource(ctx, s.Device.UDID); err == nil {
		return source, nil
	}
	// Older devices can still use the platform collector. A missing dump is an
	// observation failure, never permission to proceed without screen guards.
	remote := "/data/local/tmp/apteva-" + s.ID + ".xml"
	defer d.shell(ctx, s.Device.UDID, "rm", "-f", remote)
	if _, err := d.shell(ctx, s.Device.UDID, "uiautomator", "dump", remote); err != nil {
		return "", err
	}
	b, err := d.shell(ctx, s.Device.UDID, "cat", remote)
	if err != nil {
		return "", problem("observation_unavailable", "Android UI source is unavailable; no guarded input can proceed")
	}
	return checkedAndroidSource(b)
}

func checkedAndroidSource(b []byte) (string, error) {
	if len(b) == 0 || len(b) > maxSource {
		return "", problem("observation_unavailable", "Android UI source is missing or exceeds its limit")
	}
	text := strings.TrimSpace(string(b))
	var tree struct {
		XMLName xml.Name
		Nodes   []struct{} `xml:"node"`
	}
	if xml.Unmarshal([]byte(text), &tree) != nil || tree.XMLName.Local != "hierarchy" || len(tree.Nodes) == 0 {
		return "", problem("observation_unavailable", "Android returned an incomplete UI tree")
	}
	return text, nil
}

func (d *drivers) fastAndroidSource(ctx context.Context, serial string) (string, error) {
	f, err := os.CreateTemp("", "apteva-ui-source-*.jar")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(androidSourceJar); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	digest := sha256.Sum256(androidSourceJar)
	remote := fmt.Sprintf("/data/local/tmp/apteva-ui-source-%x.jar", digest[:8])
	if _, err = d.adb(ctx, serial, "push", f.Name(), remote); err != nil {
		return "", err
	}
	// The script contains only our fixed executable and a digest-derived path.
	b, err := d.adb(ctx, serial, "shell", "CLASSPATH="+shellQuote(remote)+" app_process /system/bin AptevaUiSource")
	if err != nil {
		return "", err
	}
	return checkedAndroidSource(b)
}
