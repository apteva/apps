package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func linkedRelease(t *testing.T, a *App, ctx *sdk.AppCtx, item int64, channel string, post int64, results map[string]any) Release {
	t.Helper()
	value, err := a.dispatch(ctx, "releases_create", map[string]any{"item_id": item, "channel": channel, "app": "social", "external_id": post, "status": "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	r := value.(Release)
	if results != nil {
		r, err = mergeReleaseResults(ctx.AppDB(), ctx.CurrentProject(), r.ID, r.Revision, results)
		if err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestSocialWorkerTracksTargetsPreservesReviewAndRetries(t *testing.T) {
	p := &platform{bindings: map[string]any{"social": float64(7)}}
	a, ctx := testApp(t, p)
	i := create(t, a, ctx, map[string]any{"title": "Multicast", "approval": "approved", "reviewer": "Marketing", "fields": map[string]any{"source": "unchanged"}})
	li := linkedRelease(t, a, ctx, i.ID, "LinkedIn", 21, map[string]any{"social_account_id": 1, "qa": map[string]any{"pass": true}})
	fb := linkedRelease(t, a, ctx, i.ID, "Facebook", 21, nil)
	manual := create(t, a, ctx, map[string]any{"title": "Other project"})
	linkedRelease(t, a, ctx.WithProject("beta"), create(t, a, ctx.WithProject("beta"), map[string]any{"title": "Beta"}).ID, "LinkedIn", 21, nil)
	targetLI := map[string]any{"id": 11, "platform": "linkedin", "social_account_id": 1, "status": "published", "platform_post_id": "native-li", "platform_url": "https://example.com/li", "publication_facts": map[string]any{"provider_reported_published_at": "2026-11-11T19:30:07Z"}}
	targetFB := map[string]any{"id": 12, "platform": "facebook", "social_account_id": 2, "status": "pending"}
	p.result = map[string]any{"posts": []any{map[string]any{"id": 21, "status": "partial", "targets": []any{targetLI, targetFB}}}}
	run := func() {
		t.Helper()
		if err := a.Workers()[0].Run(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
	}
	run()
	gotLI, _ := readRelease(ctx.AppDB(), "alpha", li.ID)
	gotFB, _ := readRelease(ctx.AppDB(), "alpha", fb.ID)
	parent, _ := readItem(ctx.AppDB(), "alpha", i.ID)
	if gotLI.Status != "published" || gotLI.URL != "https://example.com/li" || gotLI.PublishedAt != "2026-11-11T19:30:07Z" || gotFB.Status != "scheduled" || parent.Status == "published" {
		t.Fatal(gotLI, gotFB, parent)
	}
	if !reflect.DeepEqual(gotLI.Results["qa"], map[string]any{"pass": true}) {
		t.Fatal("QA overwritten", gotLI.Results)
	}
	if parent.Approval != "approved" || !reflect.DeepEqual(parent.Fields, i.Fields) {
		t.Fatal("approval or content changed", parent)
	}
	before := gotLI.Revision
	run()
	gotLI, _ = readRelease(ctx.AppDB(), "alpha", li.ID)
	if gotLI.Revision != before {
		t.Fatal("unchanged scan appended history")
	}
	targetFB["status"] = "published"
	targetFB["publication_facts"] = map[string]any{"submitted_at": "2026-11-11T19:30:00Z"}
	run()
	parent, _ = readItem(ctx.AppDB(), "alpha", i.ID)
	gotFB, _ = readRelease(ctx.AppDB(), "alpha", fb.ID)
	if parent.Status != "published" || parent.Approval != "approved" || gotFB.PublishedAt != "" {
		t.Fatal("aggregate or publication-time inference", parent, gotFB)
	}
	// A failed read never erases state; the next healthy tick still works.
	gotLI, _ = readRelease(ctx.AppDB(), "alpha", li.ID)
	p.fail = true
	if err := a.runSocialSync(context.Background(), ctx); err == nil {
		t.Fatal("expected upstream error")
	}
	saved, _ := readRelease(ctx.AppDB(), "alpha", li.ID)
	if saved.Revision != gotLI.Revision {
		t.Fatal("failure changed results")
	}
	p.fail = false
	targetLI["status"] = "pending"
	run()
	saved, _ = readRelease(ctx.AppDB(), "alpha", li.ID)
	if saved.Status != "published" {
		t.Fatal("late pending downgraded publication")
	}
	other, _ := readItem(ctx.AppDB(), "alpha", manual.ID)
	if other.Revision != manual.Revision {
		t.Fatal("unrelated item changed")
	}
	for _, name := range p.calls {
		if name != "social/post_list" {
			t.Fatal("unexpected external mutation", p.calls)
		}
	}
	if p.project != "alpha" {
		t.Fatal("unscoped read", p.project)
	}
}

func TestSocialMatchingAmbiguityCancellationAndRebind(t *testing.T) {
	p := &platform{bindings: map[string]any{"social": float64(7)}}
	a, ctx := testApp(t, p)
	i := create(t, a, ctx, map[string]any{"title": "X"})
	r := linkedRelease(t, a, ctx, i.ID, "X", 21, nil)
	post := map[string]any{"id": 21, "status": "published", "targets": []any{map[string]any{"platform": "twitter", "social_account_id": 1, "status": "published"}, map[string]any{"platform": "twitter", "social_account_id": 2, "status": "published"}}}
	if _, _, err := a.syncSocialRelease(ctx, r, post); err == nil {
		t.Fatal("ambiguous target accepted")
	}
	current, _ := readRelease(ctx.AppDB(), "alpha", r.ID)
	if current.Revision != r.Revision {
		t.Fatal("ambiguous state changed")
	}
	r, _ = mergeReleaseResults(ctx.AppDB(), "alpha", r.ID, r.Revision, map[string]any{"social_account_id": 2})
	if target, err := socialReleaseTarget(r, post); err != nil || number(target, "social_account_id") != 2 {
		t.Fatal(target, err)
	}
	changed, err := saveRelease(ctx.AppDB(), "alpha", r.ID, 0, r.Revision, map[string]any{"external_id": 22}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.syncSocialRelease(ctx, r, post); err != errConflict {
		t.Fatal("concurrent rebind accepted", err)
	}
	changed, err = saveRelease(ctx.AppDB(), "alpha", changed.ID, 0, changed.Revision, map[string]any{"status": "cancelled"}, false)
	if err != nil {
		t.Fatal(err)
	}
	post["id"] = 22
	if _, wrote, err := a.syncSocialRelease(ctx, changed, post); err != nil || wrote {
		t.Fatal("cancelled release changed", err)
	}
}

type pagedSocial struct {
	platform
	offsets []int64
}

func (p *pagedSocial) CallAppResult(app, tool string, args map[string]any, out any) error {
	offset := number(args, "offset")
	p.offsets = append(p.offsets, offset)
	page := map[string]any{"posts": []any{map[string]any{"id": 99}}, "has_more": true, "next_offset": 200}
	if offset == 200 {
		page = map[string]any{"posts": []any{map[string]any{"id": 21, "targets": []any{map[string]any{"id": 5, "platform": "twitter", "social_account_id": 39, "status": "published"}}}}, "has_more": false}
	}
	raw, _ := json.Marshal(page)
	return json.Unmarshal(raw, out)
}
func TestSocialRefreshFindsOlderPostsAndKeepsMetadata(t *testing.T) {
	p := &pagedSocial{platform: platform{bindings: map[string]any{"social": float64(7)}}}
	a, ctx := testApp(t, p)
	i := create(t, a, ctx, map[string]any{"title": "Older"})
	r := linkedRelease(t, a, ctx, i.ID, "X", 21, map[string]any{"review": "retain"})
	v, err := a.dispatch(ctx, "releases_refresh", map[string]any{"id": r.ID})
	if err != nil {
		t.Fatal(err)
	}
	saved := v.(Release)
	if saved.Status != "published" || saved.Results["review"] != "retain" || !reflect.DeepEqual(p.offsets, []int64{0, 200}) {
		t.Fatal(saved, p.offsets)
	}
}
