package main

import (
	"context"
	"regexp"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type readRequestIDKey struct{}

var requestIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// _request_id is diagnostic metadata, never an authorization input. Functions
// supplies it through the normal app-call arguments because the platform's MCP
// bridge does not promise to forward arbitrary HTTP headers.
func traceTableCall(callCtx context.Context, appCtx *sdk.AppCtx, tool string, args map[string]any, handler sdk.ToolHandler) (any, error) {
	id, _ := args["_request_id"].(string)
	delete(args, "_request_id")
	if requestIDPattern.MatchString(id) {
		callCtx = context.WithValue(callCtx, readRequestIDKey{}, id)
		args["_request_context"] = callCtx
	}
	started := time.Now()
	result, err := handler(appCtx, args)
	if requestIDPattern.MatchString(id) {
		status := "ok"
		if err != nil {
			status = "error"
		}
		appCtx.Logger().Info("tables call completed", "request_id", id, "tool", tool, "project_id", appCtx.CurrentProject(), "duration_ms", time.Since(started).Milliseconds(), "status", status, "context_error", callCtx.Err())
	}
	return result, err
}
