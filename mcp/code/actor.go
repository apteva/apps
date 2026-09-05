package main

import (
	"context"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strconv"
	"strings"
)

func callerActor(ctx context.Context) string {
	if c := sdk.CallerFrom(ctx); c != nil {
		if c.AgentID > 0 {
			return fmt.Sprintf("agent:%d", c.AgentID)
		}
		if c.SubjectType != "" && c.SubjectID != "" {
			return "user:" + c.SubjectType + ":" + c.SubjectID
		}
	}
	return "unknown"
}
func httpActor(r *http.Request) string {
	if actor := callerActor(r.Context()); actor != "unknown" {
		return actor
	}
	if typ, id := strings.TrimSpace(r.Header.Get("X-Apteva-Subject-Type")), strings.TrimSpace(r.Header.Get("X-Apteva-Subject-ID")); typ != "" && id != "" {
		return "user:" + typ + ":" + id
	}
	if id, err := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64); err == nil && id > 0 {
		return fmt.Sprintf("user:%d", id)
	}
	return "unknown"
}
func authenticatedTools(tools []sdk.Tool) []sdk.Tool {
	for i := range tools {
		plain, contextual := tools[i].Handler, tools[i].HandlerCtx
		tools[i].Handler = nil
		tools[i].HandlerCtx = func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			input := make(map[string]any, len(args)+3)
			for k, v := range args {
				input[k] = v
			}
			actor := callerActor(ctx)
			input["actor"] = actor
			input["author"] = actor
			input["created_by"] = actor
			if contextual != nil {
				return contextual(ctx, app, input)
			}
			return plain(app, input)
		}
	}
	return tools
}
