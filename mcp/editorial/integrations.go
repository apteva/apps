package main

import (
	"encoding/json"
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
	s, e := getSettings(ctx.AppDB(), ctx.CurrentProject())
	if e != nil {
		return nil, e
	}
	brandID := str(args, "brand_id")
	b := findBrand(s, brandID)
	if brandID != "" && b == nil {
		return nil, invalid("unknown brand in this project")
	}
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
	e = ctx.WithProject(ctx.CurrentProject()).PlatformAPI().CallAppResult(app, tool, input, &out)
	if e != nil {
		return nil, e
	}
	key := "campaigns"
	if app == "social" {
		key = "posts"
	}
	records, _ := out[key].([]any)
	filtered := []any{}
	for _, v := range records {
		record, ok := v.(map[string]any)
		if ok && brandAllows(b, app, record) {
			filtered = append(filtered, record)
		}
	}
	out[key] = filtered
	return out, nil
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
	// Re-read brand mappings under the write lock before saving the snapshot.
	a.writes.Lock()
	defer a.writes.Unlock()
	parent, e := readItem(ctx.AppDB(), pid, r.ItemID)
	if e != nil {
		return nil, e
	}
	s, e := getSettings(ctx.AppDB(), pid)
	if e != nil {
		return nil, e
	}
	if !brandAllows(findBrand(s, parent.BrandID), r.App, record) {
		return nil, invalid("linked record is outside this brand's mappings; previous results preserved")
	}
	patch := map[string]any{"results": record, "synced_at": time.Now().UTC().Format(time.RFC3339)}
	// Remote status stays separate from the planner's intent.
	return saveRelease(ctx.AppDB(), pid, id, 0, r.Revision, patch, true)
}

// Mappings organize existing shared project connections; they are not permissions.
func brandAllows(b *Brand, app string, record map[string]any) bool {
	if b == nil {
		return true
	}
	contains := func(ids []int64, id int64) bool {
		for _, n := range ids {
			if n == id {
				return true
			}
		}
		return false
	}
	if app == "campaigns" {
		return len(b.CampaignIDs) == 0 || contains(b.CampaignIDs, number(record, "id"))
	}
	if len(b.SocialAccountIDs) == 0 {
		return true
	}
	// Normalize platform/stub array types, then require every post target to belong
	// to this brand so cross-brand multicast snapshots are not mixed together.
	raw, _ := json.Marshal(record["targets"])
	var targets []map[string]any
	if json.Unmarshal(raw, &targets) != nil || len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if !contains(b.SocialAccountIDs, number(target, "social_account_id")) {
			return false
		}
	}
	return true
}
