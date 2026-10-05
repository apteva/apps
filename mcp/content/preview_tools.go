package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolPreviewCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["post_id"])
	if !ok || id <= 0 {
		return nil, fmt.Errorf("post_id required")
	}
	if _, err := dbGetPost(ctx.AppDB(), pid, siteID, id); err != nil {
		return nil, err
	}
	days := 7
	if n, ok := asInt64(args["ttl_days"]); ok && n > 0 {
		days = int(n)
	}
	if days > 30 {
		days = 30
	}
	token := signPreviewWithTTL(id, time.Duration(days)*24*time.Hour)
	if err := recordPreviewLink(ctx.AppDB(), pid, siteID, token, time.Now().Add(time.Duration(days)*24*time.Hour)); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("project_id", pid)
	q.Set("site", postSiteSlug(ctx, pid, siteID))
	return map[string]any{"url": "/preview/" + token + "?" + q.Encode(), "expires_at": time.Now().Add(time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339), "post_id": id}, nil
}

func (a *App) toolSitePreviewCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	days := 7
	if n, ok := asInt64(args["ttl_days"]); ok && n > 0 {
		days = int(n)
	}
	if days > 30 {
		days = 30
	}
	token := signSitePreview(siteID, time.Duration(days)*24*time.Hour)
	if err := recordPreviewLink(ctx.AppDB(), pid, siteID, token, time.Now().Add(time.Duration(days)*24*time.Hour)); err != nil {
		return nil, err
	}
	return map[string]any{"url": "/preview-site/" + token + "/?project_id=" + url.QueryEscape(pid) + "&site=" + url.QueryEscape(postSiteSlug(ctx, pid, siteID)), "expires_at": time.Now().Add(time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339), "site_id": siteID}, nil
}

func (a *App) toolPreviewRevoke(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(asString(args["token"]))
	if token == "" {
		return nil, fmt.Errorf("token required")
	}
	if err := revokePreviewLink(ctx.AppDB(), pid, token); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func postSiteSlug(ctx *sdk.AppCtx, pid string, siteID int64) string {
	if s, err := dbGetSite(ctx.AppDB(), pid, siteID); err == nil && s != nil {
		return s.Slug
	}
	return strconv.FormatInt(siteID, 10)
}
