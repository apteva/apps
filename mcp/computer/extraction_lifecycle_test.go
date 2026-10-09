package main

import (
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"testing"
)

func TestExtractionLifecycleSkipsFinalScreenshotButReleasesProvider(t *testing.T) {
	prev := newBackend
	t.Cleanup(func() { newBackend = prev })
	fake := &fakeComp{display: backends.DisplaySize{Width: 1024, Height: 768}, url: "https://example.com"}
	newBackend = func(cfg backends.Config) (backends.Computer, error) { return fake, nil }
	app := &App{reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	var open sdk.Tool
	for _, tool := range app.MCPTools() {
		if tool.Name == "browser_open" {
			open = tool
		}
	}
	out, err := open.Handler(ctx, map[string]any{"backend": "local", "url": "https://example.com", "extraction_only": true})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["session_id"].(string)
	_, err = app.toolBrowserSession(ctx, map[string]any{"action": "close", "session_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if fake.screenshotCalls != 0 || fake.closeCalls != 1 {
		t.Fatalf("screenshots=%d closes=%d", fake.screenshotCalls, fake.closeCalls)
	}
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM computer_sessions WHERE id=?`, id).Scan(&status); err != nil || status != "closed" {
		t.Fatalf("session cleanup record: %q %v", status, err)
	}
}
