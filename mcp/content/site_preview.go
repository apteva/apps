package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func signSitePreview(siteID int64, ttl time.Duration) string {
	if ttl <= 0 || ttl > 30*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	payload := fmt.Sprintf("%d.%d", siteID, time.Now().Add(ttl).Unix())
	mac := hmac.New(sha256.New, previewSecret())
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "." + hex.EncodeToString(mac.Sum(nil))))
}

func verifySitePreview(token string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, fmt.Errorf("invalid token")
	}
	parts := strings.SplitN(string(raw), ".", 3)
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid token")
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, previewSecret())
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[2])) {
		return 0, fmt.Errorf("signature mismatch")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, fmt.Errorf("token expired")
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid site")
	}
	return id, nil
}

func (a *App) handleSitePreview(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := publicProject(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/preview-site/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "preview token required")
		return
	}
	siteID, err := verifySitePreview(parts[0])
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !previewLinkActive(ctx.AppDB(), pid, siteID, parts[0]) {
		http.Error(w, "preview link revoked or expired", http.StatusUnauthorized)
		return
	}
	site, err := dbGetSite(ctx.AppDB(), pid, siteID)
	if err != nil || site == nil {
		http.NotFound(w, r)
		return
	}
	settings, _ := effectiveSettings(ctx, pid, siteID)
	slug := ""
	if len(parts) > 1 {
		slug = strings.TrimSpace(strings.Join(parts[1:], "/"))
	}
	var post *Post
	if strings.HasPrefix(slug, "posts/") {
		post, _ = dbGetPostBySlug(ctx.AppDB(), pid, siteID, "post", "en", strings.TrimPrefix(slug, "posts/"))
	} else if slug != "" {
		post, _ = dbGetPostBySlug(ctx.AppDB(), pid, siteID, "page", "en", strings.TrimPrefix(slug, "/"))
	}
	if post == nil {
		if home, _ := strconv.ParseInt(settings["homepage_page_id"], 10, 64); home > 0 {
			post, _ = dbGetPost(ctx.AppDB(), pid, siteID, home)
		}
	}
	if post == nil {
		rows, _, _ := dbSearchPosts(ctx.AppDB(), pid, siteID, PostSearch{Kind: "page", Limit: 1})
		if len(rows) > 0 {
			post = &rows[0]
		}
	}
	if post == nil {
		http.NotFound(w, r)
		return
	}
	data := basePageData(ctx, pid, siteID, settings, r)
	previewPrefix := strings.TrimSuffix(computeURLPrefix(r), "/") + "/preview-site/" + parts[0] + "/"
	data.URLPrefix = previewPrefix
	if menu, err := dbGetMenuBySlug(ctx.AppDB(), pid, siteID, "primary"); err == nil && menu != nil {
		data.PrimaryMenu = renderMenuItems(menu.Items, previewPrefix, data.ResourceQuery)
	}
	data.Post = hydratePostMediaPaths(ctx.AppDB(), pid, siteID, post, data.ResourceQuery)
	data.PageTitle = post.Title + " (preview)"
	data.NoIndex = true
	body, policy, err := renderSingleForSite(ctx, pid, siteID, data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	applyExtensionBrowserPolicy(w, policy)
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}
