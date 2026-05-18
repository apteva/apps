// Theme bundle loader. v1 ships an embedded default theme (via
// //go:embed). When a custom theme is installed in the bound storage
// app under /.themes/<slug>/, loadActiveTheme replaces the embedded
// templates with the storage-loaded ones at boot or on themes_set_active.
//
// Theme assets (CSS, fonts, images) are served by handleThemeAsset
// at /_theme/<version>/<path> with long-lived cache headers.

package main

import (
	"embed"
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

//go:embed themes_default
var embeddedThemeFS embed.FS

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
	singleTpl    *template.Template
	listTpl      *template.Template
	pageTemplates map[string]*template.Template // posts.template name → built set
}

var (
	themeMu       sync.RWMutex
	currentTheme  *Theme
	themeFuncMap  template.FuncMap
)

func currentThemeName() string {
	themeMu.RLock()
	defer themeMu.RUnlock()
	if currentTheme == nil {
		return ""
	}
	return currentTheme.Name
}

func getCurrentTheme() *Theme {
	themeMu.RLock()
	defer themeMu.RUnlock()
	return currentTheme
}

// loadActiveTheme reads `active_theme` from settings, attempts to load
// it from storage, falls back to the embedded default when missing.
// Called at OnMount and after any themes_set_active.
func loadActiveTheme(ctx *sdk.AppCtx) error {
	themeFuncMap = buildThemeFuncMap(ctx)
	// For v1, the storage-loaded theme path is sketched but not wired —
	// the loader always returns the embedded default. v1.1 will read
	// /.themes/<slug>/ from the bound storage app and unpack the
	// templates from there.
	t, err := loadEmbeddedDefaultTheme()
	if err != nil {
		return err
	}
	themeMu.Lock()
	currentTheme = t
	themeMu.Unlock()
	return nil
}

func loadEmbeddedDefaultTheme() (*Theme, error) {
	root, err := fs.Sub(embeddedThemeFS, "themes_default")
	if err != nil {
		return nil, err
	}

	// Read every file once.
	type fileBody struct{ name, body string }
	var (
		baseBody  string
		layouts   = map[string]string{}  // "single", "list" — without the .html suffix
		pageTpls  = map[string]string{}  // templates/<name>.html → name → body
		partials  []fileBody             // header/footer/post_card + blocks/*
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
		t := template.New("").Funcs(themeFuncMap)
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

	return &Theme{
		Name:          "default",
		Version:       "1",
		AssetFS:       assets,
		BlockTpls:     blockTpls,
		source:        "embedded",
		singleTpl:     singleSet,
		listTpl:       listSet,
		pageTemplates: pageSets,
	}, nil
}

// ── MCP tools ─────────────────────────────────────────────────────

func (a *App) toolThemesList(_ *sdk.AppCtx, _ map[string]any) (any, error) {
	t := getCurrentTheme()
	available := []map[string]any{
		{"slug": "default", "name": "Default", "version": "1", "source": "embedded", "active": true},
	}
	// v1.1: enumerate /.themes/* from storage and merge in.
	_ = t
	return map[string]any{"themes": available}, nil
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
	if slug != "default" {
		return nil, errors.New("custom themes from storage not yet supported in v1.0 — only 'default'")
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if err := dbSetSetting(ctx.AppDB(), pid, siteID, "active_theme", slug); err != nil {
		return nil, err
	}
	if err := loadActiveTheme(ctx); err != nil {
		return nil, err
	}
	invalidatePageCache()
	return map[string]any{"ok": true, "active_theme": slug}, nil
}

// ── REST handler (admin) ────────────────────────────────────────

func (a *App) handleHTTPThemes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, _ := a.toolThemesList(nil, nil)
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ── Asset serving ─────────────────────────────────────────────────

// handleThemeAsset serves /_theme/<version>/<path> from the active
// theme's asset filesystem. Long-lived cache headers since the version
// segment naturally invalidates on swap.
func (a *App) handleThemeAsset(w http.ResponseWriter, r *http.Request) {
	t := getCurrentTheme()
	if t == nil {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/_theme/")
	// rest looks like "<version>/<path>"; strip the version and verify
	// it matches the current theme to avoid stale-version serving from
	// a previous theme.
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	// Allow either an exact version match or "current" as a convenience
	// for the dashboard preview path.
	if parts[0] != t.Version && parts[0] != "current" {
		// Future versions live in storage; for v1 only "current" or
		// the active version are valid.
	}
	assetPath := path.Clean("/" + parts[1])[1:]
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
