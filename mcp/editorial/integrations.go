package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"time"
)

func boundApp(ctx *sdk.AppCtx, app string) error {
	if app != "social" && app != "campaigns" {
		return invalid("choose social or campaigns")
	}
	b := ctx.IntegrationFor(app)
	if b == nil || b.Kind != "app" {
		return invalid(app + " is not connected; standalone planning remains available")
	}
	return nil
}
func integrations(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	app := str(args, "app")
	if app == "" {
		return map[string]any{"social": boundApp(ctx, "social") == nil, "campaigns": boundApp(ctx, "campaigns") == nil}, nil
	}
	if e := boundApp(ctx, app); e != nil {
		return nil, e
	}
	tool := "campaigns_list"
	input := map[string]any{}
	if app == "social" {
		tool = "post_list"
		input["limit"] = 200
	}
	var out map[string]any
	e := ctx.WithProject(ctx.CurrentProject()).PlatformAPI().CallAppResult(app, tool, input, &out)
	return out, e
}
func fetchRecord(ctx *sdk.AppCtx, app string, id int64) (map[string]any, error) {
	if e := boundApp(ctx, app); e != nil {
		return nil, e
	}
	var out map[string]any
	scoped := ctx.WithProject(ctx.CurrentProject())
	if app == "campaigns" {
		if e := scoped.PlatformAPI().CallAppResult(app, "campaigns_get", map[string]any{"id": id}, &out); e != nil {
			return nil, e
		}
		record, ok := out["campaign"].(map[string]any)
		if !ok {
			return nil, invalid("linked campaign was not found")
		}
		return record, nil
	}
	if e := scoped.PlatformAPI().CallAppResult(app, "post_list", map[string]any{"limit": 200}, &out); e != nil {
		return nil, e
	}
	posts, _ := out["posts"].([]any)
	for _, p := range posts {
		record, ok := p.(map[string]any)
		if ok && number(record, "id") == id {
			return record, nil
		}
	}
	return nil, invalid("linked post is not in Social's latest 200 posts; previous results have been preserved")
}
func (a *App) refresh(ctx *sdk.AppCtx, id int64) (any, error) {
	pid := ctx.CurrentProject()
	r, e := readRelease(ctx.AppDB(), pid, id)
	if e != nil {
		return nil, e
	}
	if r.App == "" {
		return nil, invalid("link a Social or Campaigns record first")
	}
	record, e := fetchRecord(ctx, r.App, r.ExternalID)
	if e != nil {
		return nil, fmt.Errorf("refresh: %w", e)
	}
	patch := map[string]any{"results": record, "synced_at": time.Now().UTC().Format(time.RFC3339)}
	// Remote status stays separate from the planner's intent.
	a.writes.Lock()
	defer a.writes.Unlock()
	return saveRelease(ctx.AppDB(), pid, id, 0, r.Revision, patch, true)
}
