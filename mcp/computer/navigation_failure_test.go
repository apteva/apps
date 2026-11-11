package main

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/navigation"
)

func TestNavigationExhaustionBlocksWritesAndKeepsMCPError(t *testing.T) {
	calls := 0
	fake := &fakeComp{url: "https://example.com/store", display: api.DisplaySize{Width: 800, Height: 600}}
	fake.executeHook = func(action api.Action) error {
		calls++
		if !action.ReadOnly {
			t.Fatal("read-only declaration lost")
		}
		return &navigation.Failure{Cause: errors.New("net::ERR_HTTP2_SERVER_REFUSED_STREAM"), Diagnostics: &api.NavigationDiagnostics{ReadOnly: true, RetryExhausted: true, Attempts: []api.NavigationAttempt{{Error: "net::ERR_HTTP2_SERVER_REFUSED_STREAM"}, {Error: "net::ERR_HTTP2_SERVER_REFUSED_STREAM"}}}}
	}
	app := appWithSession("br_nav", fake, "browserbase")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolComputerUseMCP(context.Background(), ctx, map[string]any{"session_id": "br_nav", "action": "navigate", "url": "https://example.com/performers", "read_only": true})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := out.(sdk.MCPResult)
	if !ok || !result.IsError || result.StructuredContent["coverage_incomplete"] != true || result.StructuredContent["retryable"] != false || result.StructuredContent["retry_exhausted"] != true {
		t.Fatalf("unsafe MCP result: %#v", out)
	}
	for _, action := range []string{"click", "upload_file", "type", "key", "batch", "prepare_upload", "set_text"} {
		out, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_nav", "action": action})
		if err != nil {
			t.Fatal(err)
		}
		r := out.(map[string]any)
		if r["action_dispatched"] != false || r["success"] != false {
			t.Fatalf("write %s not blocked: %v", action, r)
		}
	}
	if calls != 1 {
		t.Fatalf("exhausted navigation replayed or subsequent write dispatched: %d", calls)
	}
	fake.executeHook = func(action api.Action) error { fake.url = action.URL; return nil }
	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_nav", "action": "navigate", "url": "https://example.com/performers", "read_only": true}); err != nil {
		t.Fatal(err)
	}
	sess, _ := app.reg.get("br_nav")
	if sess.navigationBlocked {
		t.Fatal("verified navigation did not clear navigation block")
	}
}

func TestUncertainInputNeverReplaysOrUsesReadOnlyRecovery(t *testing.T) {
	for _, action := range []string{"key", "click", "upload_file"} {
		for _, cause := range []error{errors.New("net::ERR_HTTP2_SERVER_REFUSED_STREAM"), context.DeadlineExceeded} {
			t.Run(action+"/"+cause.Error(), func(t *testing.T) {
				calls := 0
				fake := &fakeComp{url: "https://example.com/edit"}
				app := appWithSession("br_write", fake, "browserbase")
				ctx := tk.NewAppCtx(t, "apteva.yaml")
				// Simulate a reply lost after a real normalized input dispatch.
				fake.executeHook = func(api.Action) error { calls++; return cause }
				args := map[string]any{"session_id": "br_write", "action": action}
				switch action {
				case "key":
					args["key"] = "Enter"
				case "click":
					args["selector"] = "button#save"
					args["expected_text"] = "Save"
				case "upload_file":
					args["selector"] = "input#upload"
					args["base64"] = base64.StdEncoding.EncodeToString([]byte("upload"))
					args["filename"] = "file.txt"
				}
				_, err := app.toolComputerUse(ctx, args)
				if cause == context.DeadlineExceeded && (err == nil || !strings.Contains(err.Error(), "action_timeout")) {
					t.Fatalf("expected uncertain timeout: %v", err)
				}
				if err == nil || calls != 1 {
					t.Fatalf("uncertain input calls=%d err=%v", calls, err)
				}
				out, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_write", "action": "navigate", "url": "https://example.com/performers", "read_only": true})
				if err != nil || out.(map[string]any)["error_code"] != "inspection_required" || calls != 1 {
					t.Fatalf("input replayed via read-only recovery: %v %v", out, err)
				}
			})
		}
	}
}
