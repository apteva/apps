package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sdk "github.com/apteva/app-sdk"
	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/navigation"
)

// Keep MCP's error bit as well as structured JSON. Older Actors versions rely
// on CallAppResult returning an error and do not inspect success=false maps.
func (a *App) toolComputerUseNative(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.toolComputerUseMCP(context.Background(), ctx, args)
}

func (a *App) toolComputerUseMCP(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out, err := a.toolComputerUseCaller(callCtx, ctx, args)
	if result, ok := out.(map[string]any); err == nil && ok && result["navigation_failure"] == true {
		body, _ := json.Marshal(result)
		return sdk.MCPResult{IsError: true, StructuredContent: result, Content: []sdk.MCPContent{{Type: "text", Text: stringArg(result, "error_code") + ": " + stringArg(result, "error") + "\n" + string(body)}}}, nil
	}
	return out, err
}

func navigationMayWrite(action string) bool {
	switch action {
	case "screenshot", "wait", "wait_for", "wait_for_stable", "scroll", "navigate", "back", "reload":
		return false
	}
	return true
}
func navigationActionGuard(id string, sess *session, args map[string]any) map[string]any {
	action := stringArg(args, "action")
	readOnly, has := args["read_only"]
	invalid := has && ((readOnly != true && readOnly != false) || (readOnly == true && action != "navigate"))
	blocked := (sess.navigationBlocked || sess.inputUncertain) && navigationMayWrite(action)
	unsafeRecovery := sess.inputUncertain && readOnly == true
	if !invalid && !blocked && !unsafeRecovery {
		return nil
	}
	code, reason := "inspection_required", "Prior navigation or input has an unresolved outcome. Inspect this session; do not infer absence or submit/create content. Reopen the saved context explicitly after inspection if needed."
	if invalid {
		code, reason = "invalid_navigation", "read_only must be a boolean and is supported only by action=navigate."
	}
	return map[string]any{"navigation_failure": true, "success": false, "failed": true, "error_code": code, "error": reason, "session_id": id, "action": action, "action_completed": false, "action_dispatched": false, "retryable": false, "coverage_incomplete": true, "inspection_required": true}
}

func (a *App) navigationFailure(ctx *sdk.AppCtx, id string, sess *session, action string, err error) map[string]any {
	var failed *navigation.Failure
	if !errors.As(err, &failed) {
		return nil
	}
	sess.navigationBlocked = true
	d := failed.Diagnostics
	retryable := d != nil && d.ReadOnly && navigation.Retryable(err) && !d.RetryExhausted
	if d != nil {
		for _, attempt := range d.Attempts {
			if attempt.UnsafeMethod || attempt.Canceled {
				retryable = false
			}
		}
	}
	out := map[string]any{
		"navigation_failure": true, "success": false, "failed": true, "error_code": "navigation_failed",
		"error":      "Navigation failed; read-only coverage is incomplete. Do not infer absence or create/submit content.",
		"session_id": id, "backend": sess.backend, "action": action, "action_completed": false, "action_dispatched": true,
		"navigation_verified": false, "coverage_incomplete": true, "inspection_complete": false, "more_results_remaining": true,
		"retryable": retryable, "transient": navigation.Retryable(err), "inspection_required": true,
		"next_step":  "Inspect the navigation diagnostics and a fresh screenshot. Do not replay an upload, Save or submission. Only resume after read-only coverage has been verified; a fresh session must explicitly reopen the same saved context.",
		"navigation": d, "app_context_id": sess.appContextID, "backend_session_id": sess.backendSessionID,
		"session_age_seconds": max(0, int(time.Since(sess.openedAt).Seconds())), "proxy": sess.proxy,
	}
	if d != nil {
		out["retry_exhausted"] = d.RetryExhausted
	}
	if ctx != nil {
		ctx.Logger().Warn("navigation_failed", "diagnostics", out)
	}
	emitEvent(ctx, "session.action", out)
	return out
}

func mergeNavigationDiagnostics(out map[string]any, sess *session, action string) {
	if action != "navigate" && action != "back" && action != "reload" {
		return
	}
	if reporter, ok := sess.comp.(api.NavigationReporter); ok {
		out["navigation"] = reporter.LastNavigation()
		out["session_age_seconds"] = max(0, int(time.Since(sess.openedAt).Seconds()))
		out["backend_session_id"] = sess.backendSessionID
		out["app_context_id"] = sess.appContextID
		out["proxy"] = sess.proxy
	}
}
