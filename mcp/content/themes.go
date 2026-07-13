// Theme bundle loader. Bundled themes are parsed once at startup and
// selected per site at render time.
//
// Theme assets (CSS, fonts, images) are served by handleThemeAsset
// at /_theme/<version>/<path> with long-lived cache headers.

package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

//go:embed themes_default themes_magazine
var embeddedThemeFS embed.FS

// bundledThemes is the static catalog of themes shipped inside the
// binary. Each entry maps a slug to its embed-FS root directory.
// Storage-loaded themes (source != 'embedded') merge in alongside
// these once that path is wired (v2.3+).
var bundledThemes = map[string]bundledTheme{
	"default":  {Slug: "default", DisplayName: "Default", Version: "2", Root: "themes_default"},
	"magazine": {Slug: "magazine", DisplayName: "Magazine", Version: "1", Root: "themes_magazine"},
}

type bundledTheme struct {
	Slug        string
	DisplayName string
	Version     string
	Root        string // embed-FS subdir
}

// Theme holds the parsed templates + the asset filesystem for one
// theme.
//
// html/template forbids Clone() after the first Execute() — so we
// can't share one master set across requests and clone per call. Each
// per-route layout (single, list, plus any post-template overrides
// from templates/) gets its OWN pre-built template set built once at
// theme-load time. Renderers Execute against those directly without
// cloning, which makes them safe to call concurrently.
//
// Per-block partials are duplicated into each route set so renderBlock
// can look them up there. Feed XML lives in its own set because
// html/template would escape the <?xml ?> declaration; we build the
// feed body in Go and only the .xml file is reserved for future use.
type Theme struct {
	Name      string
	Version   string
	AssetFS   fs.FS
	BlockTpls map[string]*template.Template // type → cloned template for that block
	source    string                        // "embedded" | "storage:<slug>"

	// One per layout: each binds base.html + that layout's "main"
	// definition + all partials. Cloning these per request is unsafe
	// post-Execute; we Execute against the master directly (which is
	// concurrent-read-safe per the html/template docs).
	singleTpl     *template.Template
	listTpl       *template.Template
	pageTemplates map[string]*template.Template // posts.template name → built set
}

var (
	themeMu      sync.RWMutex
	themeCatalog = map[string]*Theme{}
)

func initializeThemes() error {
	themeMu.Lock()
	defer themeMu.Unlock()
	if len(themeCatalog) == len(bundledThemes) {
		return nil
	}
	catalog := make(map[string]*Theme, len(bundledThemes))
	for slug := range bundledThemes {
		t, err := loadEmbeddedTheme(slug)
		if err != nil {
			return fmt.Errorf("load theme %q: %w", slug, err)
		}
		catalog[slug] = t
	}
	themeCatalog = catalog
	return nil
}

func getTheme(slug string) *Theme {
	themeMu.RLock()
	defer themeMu.RUnlock()
	return themeCatalog[slug]
}

func activeThemeForSite(ctx *sdk.AppCtx, projectID string, siteID int64) *Theme {
	slug := "default"
	if ctx != nil && ctx.AppDB() != nil {
		if configured, err := dbGetSetting(ctx.AppDB(), projectID, siteID, "active_theme"); err == nil && configured != "" {
			slug = configured
		}
	}
	if t := getTheme(slug); t != nil {
		return t
	}
	return getTheme("default")
}

func loadEmbeddedTheme(slug string) (*Theme, error) {
	bt, ok := bundledThemes[slug]
	if !ok {
		return nil, fmt.Errorf("theme %q not found in bundle", slug)
	}
	root, err := fs.Sub(embeddedThemeFS, bt.Root)
	if err != nil {
		return nil, err
	}
	theme := &Theme{Name: bt.Slug, Version: bt.Version, source: "embedded"}
	funcs := buildThemeFuncMap(theme)

	// Read every file once.
	type fileBody struct{ name, body string }
	var (
		baseBody string
		layouts  = map[string]string{} // "single", "list" — without the .html suffix
		pageTpls = map[string]string{} // templates/<name>.html → name → body
		partials []fileBody            // header/footer/post_card + blocks/*
	)
	err = fs.WalkDir(root, ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".html") && !strings.HasSuffix(p, ".xml") {
			return nil
		}
		raw, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		body := string(raw)
		switch {
		case p == "layouts/base.html":
			baseBody = body
		case strings.HasPrefix(p, "layouts/") && strings.HasSuffix(p, ".html"):
			name := strings.TrimSuffix(strings.TrimPrefix(p, "layouts/"), ".html")
			layouts[name] = body
		case strings.HasPrefix(p, "templates/") && strings.HasSuffix(p, ".html"):
			name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
			pageTpls[name] = body
		case strings.HasPrefix(p, "partials/"):
			partials = append(partials, fileBody{name: p, body: body})
		case p == "layouts/feed.xml":
			// reserved for future use; the feed is built in Go (see
			// renderFeed) to avoid html/template's XML escaping.
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if baseBody == "" {
		return nil, fmt.Errorf("theme: layouts/base.html missing")
	}

	// buildSet stitches base + a layout's body + every partial into one
	// fresh template set. Built once per layout at theme-load time;
	// rendered against directly thereafter (no per-request Clone, which
	// html/template forbids after Execute).
	buildSet := func(layoutName, layoutBody string) (*template.Template, error) {
		t := template.New("").Funcs(funcs)
		if _, err := t.New("layouts/base.html").Parse(baseBody); err != nil {
			return nil, fmt.Errorf("parse base.html: %w", err)
		}
		if _, err := t.New("layouts/" + layoutName + ".html").Parse(layoutBody); err != nil {
			return nil, fmt.Errorf("parse %s: %w", layoutName, err)
		}
		for _, p := range partials {
			if _, err := t.New(p.name).Parse(p.body); err != nil {
				return nil, fmt.Errorf("parse %s: %w", p.name, err)
			}
		}
		return t, nil
	}

	singleSet, err := buildSet("single", layouts["single"])
	if err != nil {
		return nil, err
	}
	listSet, err := buildSet("list", layouts["list"])
	if err != nil {
		return nil, err
	}
	pageSets := map[string]*template.Template{}
	for name, body := range pageTpls {
		// A per-page template is structured like single.html (defines
		// "main"); it's parsed in place of layouts/single.html.
		s, err := buildSet(name, body)
		if err != nil {
			return nil, err
		}
		pageSets[name] = s
	}

	// Index block partials by type ("core/heading") for fast lookup.
	// Build from singleSet — partials are identical across sets so any
	// set will do for the lookup table.
	blockTpls := map[string]*template.Template{}
	for _, t := range singleSet.Templates() {
		n := t.Name()
		if strings.HasPrefix(n, "partials/blocks/") {
			typ := strings.TrimSuffix(strings.TrimPrefix(n, "partials/blocks/"), ".html")
			blockTpls[typ] = t
		}
	}

	assets, err := fs.Sub(root, "assets")
	if err != nil {
		return nil, err
	}

	theme.AssetFS = assets
	theme.BlockTpls = blockTpls
	theme.singleTpl = singleSet
	theme.listTpl = listSet
	theme.pageTemplates = pageSets
	return theme, nil
}

// ── MCP tools ─────────────────────────────────────────────────────

func (a *App) toolThemesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	// The "active" flag is per-site; resolve via args if a site
	// selector was supplied, otherwise read the current theme.
	activeSlug := ""
	if ctx != nil && ctx.AppDB() != nil {
		if pid, err := resolveProjectFromArgs(args); err == nil {
			if siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args); err == nil {
				activeSlug, _ = dbGetSetting(ctx.AppDB(), pid, siteID, "active_theme")
			}
		}
	}
	if activeSlug == "" {
		activeSlug = "default"
	}

	available := make([]map[string]any, 0, len(bundledThemes))
	// Stable iteration: default first, then alphabetical.
	order := []string{"default", "magazine"}
	for slug, bt := range bundledThemes {
		if !contains(order, slug) {
			order = append(order, slug)
		}
		_ = bt
	}
	for _, slug := range order {
		bt, ok := bundledThemes[slug]
		if !ok {
			continue
		}
		available = append(available, map[string]any{
			"slug":    bt.Slug,
			"name":    bt.DisplayName,
			"version": bt.Version,
			"source":  "embedded",
			"active":  slug == activeSlug,
		})
	}
	return map[string]any{"themes": available}, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (a *App) toolThemesSetActive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug := asString(args["slug"])
	if slug == "" {
		return nil, errors.New("slug required")
	}
	if _, ok := bundledThemes[slug]; !ok {
		return nil, fmt.Errorf("theme %q not found (available: default, magazine)", slug)
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if err := dbSetSetting(ctx.AppDB(), pid, siteID, "active_theme", slug); err != nil {
		return nil, err
	}
	invalidatePageCacheForSite(siteID)
	ctx.Emit("theme.changed", map[string]any{"slug": slug, "site_id": siteID})
	return map[string]any{"ok": true, "active_theme": slug}, nil
}

// ── REST handler (admin) ────────────────────────────────────────

func (a *App) handleHTTPThemes(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, _ := resolveProjectFromRequest(r)
	switch r.Method {
	case http.MethodGet:
		out, _ := a.toolThemesList(ctx, map[string]any{"_project_id": pid})
		httpJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolThemesSetActive(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ── Asset serving ─────────────────────────────────────────────────

// handleThemeAsset serves /_theme/<slug>/<version>/<path>. Including
// the slug prevents one site's active theme from changing another
// site's immutable asset URL.
func (a *App) handleThemeAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/_theme/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	t := getTheme(parts[0])
	if t == nil || parts[1] != t.Version {
		http.NotFound(w, r)
		return
	}
	assetPath := path.Clean("/" + parts[2])[1:]
	f, err := t.AssetFS.Open(assetPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if rs, ok := f.(io.ReadSeeker); ok {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Content-Type", mimeForPath(assetPath))
		http.ServeContent(w, r, assetPath, stat.ModTime(), rs)
		return
	}
	// Generic byte read for the rare fs.File that isn't a ReadSeeker.
	buf := make([]byte, stat.Size())
	if _, err := io.ReadFull(f, buf); err != nil && err != io.ErrUnexpectedEOF {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", mimeForPath(assetPath))
	_, _ = w.Write(buf)
}

func mimeForPath(p string) string {
	switch {
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".woff2"):
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}
