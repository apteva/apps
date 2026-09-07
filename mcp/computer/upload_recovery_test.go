package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

func TestUploadRejectionsAndFreshTargetRecovery(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	fake := &fakeComp{
		display:    backends.DisplaySize{Width: 1000, Height: 600},
		png:        []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		url:        "https://example.com/image-composer",
		somTargets: []backends.SetOfMarkTarget{{ID: "som_browse", Label: 17, Role: "button", AccessibleName: "Browse"}},
	}
	newBackend = func(backends.Config) (backends.Computer, error) { return fake, nil }
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	opened, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatal(err)
	}
	id := opened.(map[string]any)["session_id"].(string)
	t.Cleanup(func() { _, _ = app.toolBrowserSession(ctx, map[string]any{"action": "close", "session_id": id}) })
	// Reproduce the report's revision numbers and string-encoded arguments.
	app.reg.m[id].somRevision = 16
	var downloads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fake.png)
	}))
	defer source.Close()
	dispatches := 0
	fake.executeHook = func(action backends.Action) error {
		if action.Type == "upload_file" {
			dispatches++
		}
		return nil
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"stale_revision", map[string]any{"label": "17", "som_revision": "15"}, "current revision is 16"},
		{"click_only_argument", map[string]any{"selector": "button", "expected_text": "Browse"}, "expected_text is only valid for click or double_click"},
		{"unknown_label", map[string]any{"label": 99, "som_revision": 16}, "not present"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.args["session_id"], tc.args["action"] = id, "upload_file"
			tc.args["source_url"], tc.args["filename"] = source.URL, "image.png"
			_, err := app.toolComputerUse(ctx, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("rejection: %v", err)
			}
			for _, want := range []string{"Upload was not dispatched", "action=\"screenshot\"", "include_som=true", "retry action=upload_file", "fresh label or target_id and som_revision", "original source_url/base64/file_path", "omit expected_text"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("recovery missing %q: %v", want, err)
				}
			}
			if downloads.Load() != 0 || dispatches != 0 {
				t.Fatalf("rejected call downloaded or dispatched: %d/%d", downloads.Load(), dispatches)
			}
		})
	}
	// The refreshed screenshot can renumber Browse; do not reuse label 17.
	fake.somTargets[0].Label = 23
	shot, err := app.toolComputerUse(ctx, map[string]any{"session_id": id, "action": "screenshot", "include_som": true})
	if err != nil {
		t.Fatal(err)
	}
	revision := shot.(map[string]any)["som_revision"]
	args := map[string]any{"session_id": id, "action": "upload_file", "label": 23, "som_revision": revision, "expected_name": "Browse", "expected_role": "button", "source_url": source.URL, "filename": "image.png"}
	// The supported upload identity guard rejects contradictory names too.
	args["expected_name"] = "Delete"
	rejected, err := app.toolComputerUse(ctx, args)
	if err != nil || rejected.(map[string]any)["error_code"] != "expected_name_mismatch" {
		t.Fatalf("name guard: %v / %v", rejected, err)
	}
	if downloads.Load() != 0 || dispatches != 0 {
		t.Fatal("name mismatch reached upload")
	}
	args["expected_name"] = "Browse"
	result, err := app.toolComputerUse(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["uploaded"] != true || downloads.Load() != 1 || dispatches != 1 {
		t.Fatalf("retry did not upload once: %v downloads=%d dispatches=%d", result, downloads.Load(), dispatches)
	}
	if fake.lastAction.TargetID != "som_browse" || fake.lastAction.Label != 23 || fake.lastAction.ExpectedText != "" || fmt.Sprint(fake.lastAction.SOMRevision) != fmt.Sprint(revision) {
		t.Fatalf("wrong recovered target: %+v", fake.lastAction)
	}
}
