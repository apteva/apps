// Rendering pipeline.
//
// Input: a Post + active Theme + site settings. Output: a complete
// HTML document (or RSS XML for /feed.xml, etc.).
//
// The renderer walks the block tree and dispatches each block to its
// per-type partial. Markdown is rendered with goldmark and run
// through a bluemonday strict allow-list; raw HTML blocks go through
// the same sanitizer.
//
// A coarse in-memory page cache stores composed responses keyed by
// (host + path + theme version). The cache is invalidated on any
// write — invalidatePageCache() is called from every mutating
// handler. The trade-off: simple correctness over fine-grained
// invalidation; v1.x sites are low-write enough that a flush per
// edit is cheaper to reason about than per-page invalidation.

package main

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"sync"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	sdk "github.com/apteva/app-sdk"
)

// ── sanitizers ───────────────────────────────────────────────────

var (
	mdEngine    goldmark.Markdown
	htmlPolicy  *bluemonday.Policy
	inlinePolicy *bluemonday.Policy
)

func init() {
	mdEngine = goldmark.New()
	// Strict allow-list for the html block + markdown output. Mirrors
	// the WordPress kses defaults: text formatting, links, images,
	// lists, code, blockquote, hr. No <script>, no iframes (those
	// live in the embed block which uses safeHTML on cached_html
	// only — cached_html comes from oEmbed providers that already
	// produced sanitized markup).
	htmlPolicy = bluemonday.UGCPolicy()
	htmlPolicy.AllowAttrs("class").Globally()
	htmlPolicy.AllowAttrs("style").Globally()
	htmlPolicy.AllowURLSchemes("http", "https", "mailto", "tel")

	// Inline policy for paragraph-level markdown — strips block-level
	// tags so a paragraph block stays a paragraph.
	inlinePolicy = bluemonday.StrictPolicy().
		AllowElements("b", "strong", "i", "em", "u", "code", "a", "br", "span")
	inlinePolicy.AllowAttrs("href", "title").OnElements("a")
}

func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := mdEngine.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	safe := htmlPolicy.SanitizeBytes(buf.Bytes())
	return template.HTML(safe)
}

func renderInlineMarkdown(src string) template.HTML {
	html := renderMarkdown(src)
	// Strip the outer <p>…</p> if the input was a single block, so
	// callers (paragraph block) can wrap themselves.
	s := strings.TrimSpace(string(html))
	if strings.HasPrefix(s, "<p>") && strings.HasSuffix(s, "</p>") && strings.Count(s, "<p>") == 1 {
		s = strings.TrimPrefix(strings.TrimSuffix(s, "</p>"), "<p>")
	}
	return template.HTML(inlinePolicy.Sanitize(s))
}

func sanitizeHTML(src string) template.HTML {
	return template.HTML(htmlPolicy.Sanitize(src))
}

// ── theme funcs (injected at theme load time) ────────────────────

func buildThemeFuncMap(_ *sdk.AppCtx) template.FuncMap {
	return template.FuncMap{
		// int coerces JSON-derived numbers (float64) into int so
		// per-block templates can `eq (int .Attrs.level) 1` cleanly.
		"int": func(v any) int {
			if v == nil {
				return 0
			}
			n, _ := asInt64(v)
			return int(n)
		},
		"asset": func(path string) string {
			t := getCurrentTheme()
			v := "current"
			if t != nil {
				v = t.Version
			}
			return "/_theme/" + v + "/" + strings.TrimPrefix(path, "/")
		},
		"media": func(mediaID any) string {
			id, _ := asInt64(mediaID)
			if id == 0 {
				return ""
			}
			// Look up the media row to get the storage_path; serve
			// via /_media/<storage_path>.
			ctx := globalCtx
			if ctx == nil {
				return ""
			}
			pid := ""
			if env := strings.TrimSpace(getEnvFunc("APTEVA_PROJECT_ID")); env != "" {
				pid = env
			}
			if pid == "" {
				return ""
			}
			m, err := dbGetMedia(ctx.AppDB(), pid, id)
			if err != nil || m == nil {
				return ""
			}
			return "/_media" + strings.TrimPrefix(m.StoragePath, "/.media")
		},
		"markdown": renderInlineMarkdown,
		"safeHTML": sanitizeHTML,
		"formatDate": func(s, layout string) string {
			t, err := parseFlexibleTime(s)
			if err != nil {
				return s
			}
			return t.Format(layout)
		},
		"renderBlock": func(b Block) template.HTML {
			return renderBlock(b)
		},
		"renderBlocks": func(bs []Block) template.HTML {
			var out bytes.Buffer
			for _, b := range bs {
				out.WriteString(string(renderBlock(b)))
			}
			return template.HTML(out.String())
		},
	}
}

// getEnvFunc is a tiny indirection so tests can override.
var getEnvFunc = func(k string) string {
	return strings.TrimSpace(envLookup(k))
}

func envLookup(k string) string {
	// Light wrapper so the import surface is small.
	return getEnv(k)
}

// renderBlock dispatches to the per-type template; falls back to the
// fallback partial when no template exists.
func renderBlock(b Block) template.HTML {
	t := getCurrentTheme()
	if t == nil {
		return template.HTML("<!-- no theme loaded -->")
	}
	tpl := t.BlockTpls[b.Type]
	if tpl == nil {
		tpl = t.BlockTpls["fallback"]
	}
	if tpl == nil {
		return template.HTML("<!-- no template for " + template.HTMLEscapeString(b.Type) + " -->")
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, b); err != nil {
		return template.HTML("<!-- render error: " + template.HTMLEscapeString(err.Error()) + " -->")
	}
	return template.HTML(buf.String())
}

// ── rendering entry points ──────────────────────────────────────

// PageData is the typed context passed to every layout template.
// Fields are populated by the callers in http.go; per-route extras
// (Posts, Pagination, ListTitle) ride along on the same struct via
// concrete fields rather than a map so templates can access them
// without index calls.
type PageData struct {
	SiteTitle        string
	SiteTagline      string
	Locale           string
	PublicBaseURL    string
	PageTitle        string
	MetaDescription  string
	Canonical        string
	OpenGraph        *OpenGraph
	PrimaryMenu      []RenderedMenuItem
	Post             *Post
	Posts            []Post
	Terms            []Term
	ListTitle        string
	Pagination       *Pagination
	FeaturedMediaURL string
	FeaturedMediaAlt string
	Now              string
}

type OpenGraph struct {
	Title, Description, Image, Type string
}

type RenderedMenuItem struct {
	Label    string
	URL      string
	Children []RenderedMenuItem
}

type Pagination struct {
	Prev, Next string
	Page, Total int
}

// renderSingle composes base.html + single.html (or a per-page template
// if Post.Template is set and exists) and returns the HTML.
//
// Each layout has its own pre-built template set (see themes.go) so
// rendering is just an Execute — no cloning, no Race conditions.
func renderSingle(data PageData) (string, error) {
	t := getCurrentTheme()
	if t == nil {
		return "", fmt.Errorf("no theme loaded")
	}
	set := t.singleTpl
	if data.Post != nil && data.Post.Template != "" {
		if alt, ok := t.pageTemplates[data.Post.Template]; ok {
			set = alt
		}
	}
	return executeBase(set, data)
}

func renderList(data PageData) (string, error) {
	t := getCurrentTheme()
	if t == nil {
		return "", fmt.Errorf("no theme loaded")
	}
	return executeBase(t.listTpl, data)
}

// executeBase runs base.html against a pre-built layout set. Each set
// owns its own "main" block (single.html or list.html or templates/<x>),
// so the same base.html composes differently per route without us
// having to Clone — which html/template forbids post-Execute.
//
// Calls are concurrency-safe: html/template ExecuteTemplate is safe to
// call from multiple goroutines on the same set.
func executeBase(set *template.Template, data PageData) (string, error) {
	if set == nil {
		return "", fmt.Errorf("layout template set missing")
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, "layouts/base.html", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// renderFeed builds the RSS XML in Go rather than via html/template,
// which would HTML-escape the XML declaration. Trivial enough that a
// template buys nothing.
func renderFeed(data PageData) (string, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0"><channel>` + "\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", xmlEscape(data.SiteTitle))
	fmt.Fprintf(&b, "<link>%s/</link>\n", xmlEscape(data.PublicBaseURL))
	fmt.Fprintf(&b, "<description>%s</description>\n", xmlEscape(data.SiteTagline))
	for _, p := range data.Posts {
		link := data.PublicBaseURL + "/posts/" + p.Slug
		b.WriteString("<item>\n")
		fmt.Fprintf(&b, "<title>%s</title>\n", xmlEscape(p.Title))
		fmt.Fprintf(&b, "<link>%s</link>\n", xmlEscape(link))
		fmt.Fprintf(&b, "<guid>%s</guid>\n", xmlEscape(link))
		if t, err := parseFlexibleTime(p.PublishedAt); err == nil {
			fmt.Fprintf(&b, "<pubDate>%s</pubDate>\n", t.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
		}
		if p.Excerpt != "" {
			fmt.Fprintf(&b, "<description>%s</description>\n", xmlEscape(p.Excerpt))
		}
		b.WriteString("</item>\n")
	}
	b.WriteString("</channel></rss>\n")
	return b.String(), nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// ── page cache ───────────────────────────────────────────────────

type cacheEntry struct {
	body        string
	contentType string
	etag        string
	storedAt    time.Time
}

var (
	pageCache   = map[string]cacheEntry{}
	pageCacheMu sync.RWMutex
)

func cacheKey(host, path, locale string) string {
	t := getCurrentTheme()
	ver := ""
	if t != nil {
		ver = t.Version
	}
	return host + "|" + path + "|" + locale + "|" + ver
}

func cacheGet(key string) (cacheEntry, bool) {
	pageCacheMu.RLock()
	defer pageCacheMu.RUnlock()
	e, ok := pageCache[key]
	if !ok {
		return e, false
	}
	// Soft TTL — entries older than 1h are dropped to bound memory
	// growth on long-lived sidecars. Real invalidation is push-based
	// via invalidatePageCache().
	if time.Since(e.storedAt) > time.Hour {
		return e, false
	}
	return e, true
}

func cacheSet(key, body, contentType, etag string) {
	pageCacheMu.Lock()
	defer pageCacheMu.Unlock()
	pageCache[key] = cacheEntry{body: body, contentType: contentType, etag: etag, storedAt: time.Now()}
}

func invalidatePageCache() {
	pageCacheMu.Lock()
	defer pageCacheMu.Unlock()
	pageCache = map[string]cacheEntry{}
}

// ── time helper ──────────────────────────────────────────────────

func parseFlexibleTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable time %q", s)
}
