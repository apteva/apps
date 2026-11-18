package main

import (
	"context"
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

// Opt-in, on a real phone (no session engine needed). Put a screen with a radio
// list (exactly one row checked) inside a scrollable container in the
// foreground, then run:
//
//	RUN_ANDROID_SELECTION_E2E=1 ANDROID_TEST_UDID=<serial> go test -run TestAndroidSelectionStateE2E -v .
//
// It reads the source through the bundled fast collector only (never the
// uiautomator dump fallback), so a pass proves the new collector ran.
// To check that the checked row moves, tap another row and run it again.
func TestAndroidSelectionStateE2E(t *testing.T) {
	if os.Getenv("RUN_ANDROID_SELECTION_E2E") != "1" {
		t.Skip("set RUN_ANDROID_SELECTION_E2E=1 and ANDROID_TEST_UDID")
	}
	serial := os.Getenv("ANDROID_TEST_UDID")
	if serial == "" {
		t.Fatal("ANDROID_TEST_UDID is required")
	}
	src, err := newDrivers(runnerConfig{}).fastAndroidSource(context.Background(), serial)
	if err != nil {
		t.Fatal("fast collector failed:", err)
	}
	if strings.Contains(src[:min(len(src), 120)], "standalone") {
		t.Fatal("source came from uiautomator dump, not the bundled collector")
	}
	checks := checkSelectionSource(t, src)
	t.Logf("checkable=%d checked=%d scrollable=%d checked rows=%v", checks.checkable, len(checks.checked), checks.scrollable, checks.checked)
	if checks.checkable < 2 || len(checks.checked) != 1 || checks.scrollable == 0 {
		t.Fatalf("want ≥2 checkable rows, exactly 1 checked, a scrollable container; got %+v", checks)
	}
}

type selectionChecks struct {
	checkable, scrollable int
	checked               []string
}

func checkSelectionSource(t *testing.T, src string) selectionChecks {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(src))
	var out selectionChecks
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		el, ok := tok.(xml.StartElement)
		if !ok || el.Name.Local != "node" {
			continue
		}
		a := map[string]string{}
		for _, at := range el.Attr {
			a[at.Name.Local] = at.Value
		}
		for _, k := range []string{"checkable", "checked", "selected", "scrollable"} {
			if v := a[k]; v != "true" && v != "false" {
				t.Fatalf("node %s has %s=%q", a["resource-id"], k, v)
			}
		}
		if a["password"] == "true" && (a["text"] != "" || a["content-desc"] != "") {
			t.Fatal("password field text leaked into source")
		}
		if a["checkable"] == "true" {
			out.checkable++
		}
		if a["checked"] == "true" {
			out.checked = append(out.checked, a["text"]+"|"+a["resource-id"]+"|"+a["bounds"])
		}
		if a["scrollable"] == "true" {
			out.scrollable++
		}
	}
	return out
}
