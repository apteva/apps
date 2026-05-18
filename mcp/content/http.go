// Public HTTP surface: rendered pages, term archives, feed, sitemap,
// media streaming, draft previews. Multi-site (v2.0): every public
// route resolves the target site via resolveSiteIDFromRequest, which
// considers hostname → sites.hostname lookup first for domain-linked
// public traffic, then query/header overrides, then falls back to
// the project's default site.

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	if ctx == nil {
		http.Error(w, "not mounted", 500)
		return
	}
	pid, err := publicProject(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if r.URL.Path == "/health" || r.URL.Path == "/healthz" {
		w.Write([]byte("ok"))
		return
	}

	if red, _ := dbLookupRedirect(ctx.AppDB(), pid, siteID, r.URL.Path); red != nil {
		http.Redirect(w, r, red.To, red.Code)
		return
	}

	// Page cache. Site segments the key alongside prefix/host/path so
	// no two sites can return each other's cached body.
	prefix := computeURLPrefix(r)
	key := cacheKeyMulti(r.Host, r.URL.Path, "", prefix, siteID)
	if e, ok := cacheGet(key); ok {
		w.Header().Set("Content-Type", e.contentType)
		w.Header().Set("ETag", e.etag)
		if r.Header.Get("If-None-Match") == e.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, e.body)
		return
	}

	switch {
	case r.URL.Path == "/":
		a.renderHomepage(w, r, ctx, pid, siteID)
	case strings.HasPrefix(r.URL.Path, "/posts/"):
		slug := strings.TrimPrefix(r.URL.Path, "/posts/")
		a.renderPost(w, r, ctx, pid, siteID, slug)
	case strings.HasPrefix(r.URL.Path, "/category/"):
		slug := strings.TrimPrefix(r.URL.Path, "/category/")
		a.renderTermArchive(w, r, ctx, pid, siteID, "category", slug)
	case strings.HasPrefix(r.URL.Path, "/tag/"):
		slug := strings.TrimPrefix(r.URL.Path, "/tag/")
		a.renderTermArchive(w, r, ctx, pid, siteID, "tag", slug)
	case strings.HasPrefix(r.URL.Path, "/page/"):
		nStr := strings.TrimPrefix(r.URL.Path, "/page/")
		n, _ := strconv.Atoi(nStr)
		if n <= 0 {
			http.NotFound(w, r)
			return
		}
		a.renderBlogIndex(w, r, ctx, pid, siteID, n)
	default:
		slug := strings.TrimPrefix(r.URL.Path, "/")
		a.renderPage(w, r, ctx, pid, siteID, slug)
	}
}

func publicProject(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.Header.Get("X-Apteva-Project-ID"); v != "" {
		return v, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("project_id required (host not bound to a project)")
}

func (a *App) renderHomepage(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, siteID int64) {
	settings, _ := effectiveSettings(ctx, pid, siteID)
	if id := settings["homepage_page_id"]; id != "" {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if post, err := dbGetPost(ctx.AppDB(), pid, siteID, n); err == nil && post.Status == "published" {
				a.servePost(w, r, ctx, pid, siteID, post, settings)
				return
			}
		}
	}
	a.renderBlogIndex(w, r, ctx, pid, siteID, 1)
}

func (a *App) renderBlogIndex(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, siteID int64, page int) {
	settings, _ := effectiveSettings(ctx, pid, siteID)
	per := 10
	if v := settings["posts_per_page"]; v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			per = n
		}
	}
	posts, total, err := dbSearchPosts(ctx.AppDB(), pid, siteID, PostSearch{
		Status: "published",
		Kind:   "post",
		Limit:  per,
		Offset: (page - 1) * per,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	data := basePageData(ctx, pid, siteID, settings, r)
	data.Posts = posts
	data.ListTitle = ""
	if total > per {
		var pag Pagination
		pag.Page = page
		pag.Total = total
		if page > 1 {
			if page == 2 {
				pag.Prev = "/"
			} else {
				pag.Prev = fmt.Sprintf("/page/%d", page-1)
			}
		}
		if page*per < total {
			pag.Next = fmt.Sprintf("/page/%d", page+1)
		}
		data.Pagination = &pag
	}
	body, err := renderList(data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cacheAndWrite(w, r, body, "text/html; charset=utf-8", siteID)
}

func (a *App) renderPost(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, siteID int64, slug string) {
	post, err := dbGetPostBySlug(ctx.AppDB(), pid, siteID, "post", "en", slug)
	if err != nil || post.Status != "published" {
		http.NotFound(w, r)
		return
	}
	settings, _ := effectiveSettings(ctx, pid, siteID)
	a.servePost(w, r, ctx, pid, siteID, post, settings)
}

func (a *App) renderPage(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, siteID int64, slug string) {
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(slug, "/")
	leaf := parts[len(parts)-1]
	post, err := dbGetPostBySlug(ctx.AppDB(), pid, siteID, "page", "en", leaf)
	if err != nil || post.Status != "published" {
		http.NotFound(w, r)
		return
	}
	if len(parts) > 1 {
		curr := post
		for i := len(parts) - 2; i >= 0; i-- {
			if curr.ParentID == nil {
				http.NotFound(w, r)
				return
			}
			parent, err := dbGetPost(ctx.AppDB(), pid, siteID, *curr.ParentID)
			if err != nil || parent.Slug != parts[i] {
				http.NotFound(w, r)
				return
			}
			curr = parent
		}
	}
	settings, _ := effectiveSettings(ctx, pid, siteID)
	a.servePost(w, r, ctx, pid, siteID, post, settings)
}

func (a *App) servePost(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, siteID int64, post *Post, settings map[string]string) {
	terms, _ := dbListPostTerms(ctx.AppDB(), post.ID)
	data := basePageData(ctx, pid, siteID, settings, r)
	data.Post = post
	data.Terms = terms
	data.PageTitle = post.Title
	if post.SEODescription != "" {
		data.MetaDescription = post.SEODescription
	} else {
		data.MetaDescription = post.Excerpt
	}
	if post.SEOCanonical != "" {
		data.Canonical = post.SEOCanonical
	}
	if post.FeaturedMediaID != nil {
		if m, err := dbGetMedia(ctx.AppDB(), pid, siteID, *post.FeaturedMediaID); err == nil {
			data.FeaturedMediaURL = "/_media" + strings.TrimPrefix(m.StoragePath, "/.media")
			data.FeaturedMediaAlt = m.Alt
		}
	}
	data.OpenGraph = &OpenGraph{
		Title:       firstNonEmpty(post.SEOTitle, post.Title),
		Description: data.MetaDescription,
		Type:        "article",
		Image:       data.FeaturedMediaURL,
	}
	body, err := renderSingle(data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cacheAndWrite(w, r, body, "text/html; charset=utf-8", siteID)
}

func (a *App) renderTermArchive(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, siteID int64, kind, slug string) {
	term, err := dbGetTermBySlug(ctx.AppDB(), pid, siteID, kind, slug)
	if err != nil || term == nil {
		http.NotFound(w, r)
		return
	}
	posts, _, err := dbSearchPosts(ctx.AppDB(), pid, siteID, PostSearch{
		Status:   "published",
		Kind:     "post",
		TermSlug: slug,
		Limit:    50,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings, _ := effectiveSettings(ctx, pid, siteID)
	data := basePageData(ctx, pid, siteID, settings, r)
	data.Posts = posts
	data.ListTitle = term.Name
	data.PageTitle = term.Name
	body, err := renderList(data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cacheAndWrite(w, r, body, "text/html; charset=utf-8", siteID)
}

func (a *App) handleFeed(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := publicProject(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	posts, _, err := dbSearchPosts(ctx.AppDB(), pid, siteID, PostSearch{Status: "published", Kind: "post", Limit: 20})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings, _ := effectiveSettings(ctx, pid, siteID)
	data := basePageData(ctx, pid, siteID, settings, r)
	data.Posts = posts
	body, err := renderFeed(data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cacheAndWrite(w, r, body, "application/rss+xml; charset=utf-8", siteID)
}

func (a *App) handleSitemap(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := publicProject(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	posts, _, err := dbSearchPosts(ctx.AppDB(), pid, siteID, PostSearch{Status: "published", Limit: 500})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings, _ := effectiveSettings(ctx, pid, siteID)
	base := settings["public_base_url"]
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range posts {
		loc := base
		switch p.Kind {
		case "post":
			loc += "/posts/" + p.Slug
		case "page":
			loc += "/" + p.Slug
		}
		mod := p.UpdatedAt
		if mod == "" {
			mod = p.PublishedAt
		}
		b.WriteString("<url><loc>" + loc + "</loc>")
		if mod != "" {
			b.WriteString("<lastmod>" + mod + "</lastmod>")
		}
		b.WriteString("</url>\n")
	}
	b.WriteString("</urlset>")
	cacheAndWrite(w, r, b.String(), "application/xml; charset=utf-8", siteID)
}

func (a *App) handleMediaAsset(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	rest := strings.TrimPrefix(r.URL.Path, "/_media/")
	path := "/.media/" + rest
	data, mime, err := storageRead(ctx, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

func (a *App) handlePreview(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, _ := publicProject(r)
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/preview/")
	postID, err := verifyPreviewToken(rest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	post, err := dbGetPost(ctx.AppDB(), pid, siteID, postID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	settings, _ := effectiveSettings(ctx, pid, siteID)
	terms, _ := dbListPostTerms(ctx.AppDB(), post.ID)
	data := basePageData(ctx, pid, siteID, settings, r)
	data.Post = post
	data.Terms = terms
	data.PageTitle = post.Title + " (preview)"
	w.Header().Set("X-Robots-Tag", "noindex")
	body, err := renderSingle(data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

// ── preview token signing ────────────────────────────────────────

func previewSecret() []byte {
	tok := os.Getenv("APTEVA_APP_TOKEN")
	if tok == "" {
		tok = "dev-preview-secret"
	}
	sum := sha256.Sum256([]byte(tok + ":content-preview"))
	return sum[:]
}

func SignPreview(postID int64) string {
	exp := time.Now().Add(15 * time.Minute).Unix()
	payload := fmt.Sprintf("%d.%d", postID, exp)
	mac := hmac.New(sha256.New, previewSecret())
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "." + sig))
}

func verifyPreviewToken(token string) (int64, error) {
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
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return 0, fmt.Errorf("signature mismatch")
	}
	exp, _ := strconv.ParseInt(parts[1], 10, 64)
	if time.Now().Unix() > exp {
		return 0, fmt.Errorf("token expired")
	}
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	return id, nil
}

// ── shared helpers ──────────────────────────────────────────────

func basePageData(ctx *sdk.AppCtx, pid string, siteID int64, settings map[string]string, r *http.Request) PageData {
	prefix := computeURLPrefix(r)
	mainMenu, _ := dbGetMenuBySlug(ctx.AppDB(), pid, siteID, "primary")
	var rendered []RenderedMenuItem
	if mainMenu != nil {
		rendered = renderMenuItems(mainMenu.Items, prefix)
	}
	return PageData{
		SiteTitle:     firstNonEmpty(settings["site_title"], "My Site"),
		SiteTagline:   settings["site_tagline"],
		Locale:        firstNonEmpty(settings["default_locale"], "en"),
		PublicBaseURL: settings["public_base_url"],
		URLPrefix:     prefix,
		SiteID:        siteID,
		PrimaryMenu:   rendered,
		Now:           time.Now().UTC().Format(time.RFC3339),
	}
}

func computeURLPrefix(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-Prefix")); v != "" {
		if !strings.HasSuffix(v, "/") {
			v += "/"
		}
		return v
	}
	throughDashboardProxy := r.Header.Get("X-Apteva-App-Install-ID") != "" &&
		r.Header.Get("X-Forwarded-Host") == ""
	if throughDashboardProxy {
		name := "content"
		if globalCtx != nil && globalCtx.Manifest() != nil && globalCtx.Manifest().Name != "" {
			name = globalCtx.Manifest().Name
		}
		return "/api/apps/" + name + "/"
	}
	return "/"
}

func renderMenuItems(items []MenuItem, prefix string) []RenderedMenuItem {
	out := make([]RenderedMenuItem, 0, len(items))
	for _, it := range items {
		out = append(out, RenderedMenuItem{
			Label:    it.Label,
			URL:      resolveMenuURL(it, prefix),
			Children: renderMenuItems(it.Children, prefix),
		})
	}
	return out
}

func resolveMenuURL(it MenuItem, prefix string) string {
	if it.TargetURL != "" {
		return it.TargetURL
	}
	if it.TargetID == nil {
		return "#"
	}
	id := strconv.FormatInt(*it.TargetID, 10)
	switch it.TargetKind {
	case "post":
		return prefix + "posts/" + id
	case "page":
		return prefix + id
	case "term":
		return prefix + "category/" + id
	default:
		return "#"
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// cacheKeyMulti is the v2.0 cache key — same as cacheKey but with
// siteID segmented in so two sites in the same project (and even
// sharing a hostname accidentally) can't return each other's pages.
func cacheKeyMulti(host, path, locale, prefix string, siteID int64) string {
	return cacheKey(host, path, locale, prefix) + "|s:" + strconv.FormatInt(siteID, 10)
}

func cacheAndWrite(w http.ResponseWriter, r *http.Request, body, contentType string, siteID int64) {
	sum := sha256.Sum256([]byte(body))
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	key := cacheKeyMulti(r.Host, r.URL.Path, "", computeURLPrefix(r), siteID)
	cacheSet(key, body, contentType, etag)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.WriteString(w, body)
}
