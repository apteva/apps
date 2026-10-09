package main

import (
	"encoding/json"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Only normalize representations known to be equivalent. Non-root slash
// variants are aliases only after an actual redirect has established it.
func canonicalCacheURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

func storeRedirectCacheAlias(ctx *sdk.AppCtx, p cachePolicy, out map[string]any) {
	if p.Kind != "extract" {
		return
	}
	final, _ := cacheResponseURLTitle(out)
	if final == "" {
		return
	}
	var args map[string]any
	if json.Unmarshal([]byte(p.RequestJSON), &args) != nil {
		return
	}
	requested, _ := args["url"].(string)
	if canonicalCacheURL(requested) == canonicalCacheURL(final) {
		return
	}
	// Recompute with every extraction option unchanged, including source mode.
	args["url"] = final
	request, key, err := cacheKey(p.Kind, args)
	if err != nil || key == p.Key {
		return
	}
	alias := p
	alias.RequestJSON, alias.Key = request, key
	if err := storeCachedResponse(ctx, alias, out); err != nil {
		ctx.Logger().Warn("redirect cache alias failed", "err", err.Error())
	}
}
