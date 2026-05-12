// Public HTTP surface: rendered pages, term archives, feed, sitemap,
// media streaming, draft previews. The headless REST handlers live in
// the per-domain files (posts.go, terms.go, etc.) — this file is the
// catch-all renderer + the asset/preview/feed surfaces.

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

// handlePublic is the catch-all rendered-HTML handler. It resolves the
// requested path against redirects → posts → pages → term archives →
// paginated blog index. Misses return 404.
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

	if r.URL.Path == "/health" || r.URL.Path == "/healthz" {
		w.Write([]byte("ok"))
		return
	}

	// Redirects come first — content migrations rely on this.
	if red, _ := dbLookupRedirect(ctx.AppDB(), pid, r.URL.Path); red != nil {
		http.Redirect(w, r, red.To, red.Code)
		return
	}

	// Try the page cache.
	key := cacheKey(r.Host, r.URL.Path, "")
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

	// Dispatch by path shape.
	switch {
	case r.URL.Path == "/":
		a.renderHomepage(w, r, ctx, pid)
	case strings.HasPrefix(r.URL.Path, "/posts/"):
		slug := strings.TrimPrefix(r.URL.Path, "/posts/")
		a.renderPost(w, r, ctx, pid, slug)
	case strings.HasPrefix(r.URL.Path, "/category/"):
		slug := strings.TrimPrefix(r.URL.Path, "/category/")
		a.renderTermArchive(w, r, ctx, pid, "category", slug)
	case strings.HasPrefix(r.URL.Path, "/tag/"):
		slug := strings.TrimPrefix(r.URL.Path, "/tag/")
		a.renderTermArchive(w, r, ctx, pid, "tag", slug)
	case strings.HasPrefix(r.URL.Path, "/page/"):
		nStr := strings.TrimPrefix(r.URL.Path, "/page/")
		n, _ := strconv.Atoi(nStr)
		if n <= 0 {
			http.NotFound(w, r)
			return
		}
		a.renderBlogIndex(w, r, ctx, pid, n)
	default:
		// Page slug (possibly nested). Strip leading slash.
		slug := strings.TrimPrefix(r.URL.Path, "/")
		a.renderPage(w, r, ctx, pid, slug)
	}
}

// publicProject resolves a project id for public requests. For
// project-scoped installs APTEVA_PROJECT_ID is set; for global installs
// the platform's domain_link table routes the host to a specific
// project that gets forwarded via the X-Apteva-Project-ID header.
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

func (a *App) renderHomepage(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string) {
	settings, _ := effectiveSettings(ctx, pid)
	// If homepage_page_id is set, render that page.
	if id := settings["homepage_page_id"]; id != "" {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			if post, err := dbGetPost(ctx.AppDB(), pid, n); err == nil && post.Status == "published" {
				a.servePost(w, r, ctx, pid, post, settings)
				return
			}
		}
	}
	a.renderBlogIndex(w, r, ctx, pid, 1)
}

func (a *App) renderBlogIndex(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, page int) {
	settings, _ := effectiveSettings(ctx, pid)
	per := 10
	if v := settings["posts_per_page"]; v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			per = n
		}
	}
	posts, total, err := dbSearchPosts(ctx.AppDB(), pid, PostSearch{
		Status: "published",
		Kind:   "post",
		Limit:  per,
		Offset: (page - 1) * per,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	data := basePageData(ctx, pid, settings, r)
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
	cacheAndWrite(w, r, body, "text/html; charset=utf-8")
}

func (a *App) renderPost(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid, slug string) {
	post, err := dbGetPostBySlug(ctx.AppDB(), pid, "post", "en", slug)
	if err != nil || post.Status != "published" {
		http.NotFound(w, r)
		return
	}
	settings, _ := effectiveSettings(ctx, pid)
	a.servePost(w, r, ctx, pid, post, settings)
}

func (a *App) renderPage(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid, slug string) {
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(slug, "/")
	leaf := parts[len(parts)-1]
	post, err := dbGetPostBySlug(ctx.AppDB(), pid, "page", "en", leaf)
	if err != nil || post.Status != "published" {
		http.NotFound(w, r)
		return
	}
	// Validate ancestry — if the slug is nested, walk parent_id chain.
	if len(parts) > 1 {
		curr := post
		for i := len(parts) - 2; i >= 0; i-- {
			if curr.ParentID == nil {
				http.NotFound(w, r)
				return
			}
			parent, err := dbGetPost(ctx.AppDB(), pid, *curr.ParentID)
			if err != nil || parent.Slug != parts[i] {
				http.NotFound(w, r)
				return
			}
			curr = parent
		}
	}
	settings, _ := effectiveSettings(ctx, pid)
	a.servePost(w, r, ctx, pid, post, settings)
}

func (a *App) servePost(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, post *Post, settings map[string]string) {
	terms, _ := dbListPostTerms(ctx.AppDB(), post.ID)
	data := basePageData(ctx, pid, settings, r)
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
		if m, err := dbGetMedia(ctx.AppDB(), pid, *post.FeaturedMediaID); err == nil {
			data.FeaturedMediaURL = "/_media" + strings.TrimPrefix(m.StoragePath, "/.media")
			data.FeaturedMediaAlt = m.Alt
		}
	}
	og := &OpenGraph{
		Title:       firstNonEmpty(post.SEOTitle, post.Title),
		Description: data.MetaDescription,
		Type:        "article",
		Image:       data.FeaturedMediaURL,
	}
	data.OpenGraph = og
	body, err := renderSingle(data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cacheAndWrite(w, r, body, "text/html; charset=utf-8")
}

func (a *App) renderTermArchive(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid, kind, slug string) {
	term, err := dbGetTermBySlug(ctx.AppDB(), pid, kind, slug)
	if err != nil || term == nil {
		http.NotFound(w, r)
		return
	}
	posts, _, err := dbSearchPosts(ctx.AppDB(), pid, PostSearch{
		Status:   "published",
		Kind:     "post",
		TermSlug: slug,
		Limit:    50,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings, _ := effectiveSettings(ctx, pid)
	data := basePageData(ctx, pid, settings, r)
	data.Posts = posts
	data.ListTitle = term.Name
	data.PageTitle = term.Name
	body, err := renderList(data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cacheAndWrite(w, r, body, "text/html; charset=utf-8")
}

func (a *App) handleFeed(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := publicProject(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	posts, _, err := dbSearchPosts(ctx.AppDB(), pid, PostSearch{Status: "published", Kind: "post", Limit: 20})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings, _ := effectiveSettings(ctx, pid)
	data := basePageData(ctx, pid, settings, r)
	data.Posts = posts
	body, err := renderFeed(data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cacheAndWrite(w, r, body, "application/rss+xml; charset=utf-8")
}

func (a *App) handleSitemap(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := publicProject(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	posts, _, err := dbSearchPosts(ctx.AppDB(), pid, PostSearch{Status: "published", Limit: 500})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings, _ := effectiveSettings(ctx, pid)
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
	cacheAndWrite(w, r, b.String(), "application/xml; charset=utf-8")
}

// handleMediaAsset streams a byte body from the bound storage app.
// Path shape: /_media/<rest> — translated to /.media/<rest> for the
// storage lookup. NoAuth (declared in main.go) since this is the
// public-facing media URL.
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

// handlePreview serves a draft via a signed short-lived token.
//   /preview/<base64(post_id|exp|hmac)>
func (a *App) handlePreview(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, _ := publicProject(r)
	rest := strings.TrimPrefix(r.URL.Path, "/preview/")
	postID, err := verifyPreviewToken(rest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	post, err := dbGetPost(ctx.AppDB(), pid, postID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	settings, _ := effectiveSettings(ctx, pid)
	terms, _ := dbListPostTerms(ctx.AppDB(), post.ID)
	data := basePageData(ctx, pid, settings, r)
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

// previewSecret derives the HMAC key from the install's app token; it
// rotates whenever the platform reissues the token, which is fine —
// previews are short-lived (15 min).
func previewSecret() []byte {
	tok := os.Getenv("APTEVA_APP_TOKEN")
	if tok == "" {
		tok = "dev-preview-secret"
	}
	sum := sha256.Sum256([]byte(tok + ":content-preview"))
	return sum[:]
}

// SignPreview returns a token that grants read access to one post for
// 15 minutes. Exposed so the dashboard can mint a preview URL via the
// REST surface (POST /api/posts/:id/preview-token) — not wired in v1.
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

func basePageData(ctx *sdk.AppCtx, pid string, settings map[string]string, r *http.Request) PageData {
	mainMenu, _ := dbGetMenuBySlug(ctx.AppDB(), pid, "primary")
	var rendered []RenderedMenuItem
	if mainMenu != nil {
		rendered = renderMenuItems(mainMenu.Items)
	}
	return PageData{
		SiteTitle:     firstNonEmpty(settings["site_title"], "My Site"),
		SiteTagline:   settings["site_tagline"],
		Locale:        firstNonEmpty(settings["default_locale"], "en"),
		PublicBaseURL: settings["public_base_url"],
		PrimaryMenu:   rendered,
		Now:           time.Now().UTC().Format(time.RFC3339),
	}
}

func renderMenuItems(items []MenuItem) []RenderedMenuItem {
	out := make([]RenderedMenuItem, 0, len(items))
	for _, it := range items {
		out = append(out, RenderedMenuItem{
			Label:    it.Label,
			URL:      resolveMenuURL(it),
			Children: renderMenuItems(it.Children),
		})
	}
	return out
}

func resolveMenuURL(it MenuItem) string {
	if it.TargetURL != "" {
		return it.TargetURL
	}
	if it.TargetID == nil {
		return "#"
	}
	switch it.TargetKind {
	case "post":
		return "/posts/" + strconv.FormatInt(*it.TargetID, 10)
	case "page":
		return "/" + strconv.FormatInt(*it.TargetID, 10)
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

func cacheAndWrite(w http.ResponseWriter, r *http.Request, body, contentType string) {
	sum := sha256.Sum256([]byte(body))
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	key := cacheKey(r.Host, r.URL.Path, "")
	cacheSet(key, body, contentType, etag)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.WriteString(w, body)
}
