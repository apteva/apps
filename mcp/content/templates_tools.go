// MCP tool handlers + REST endpoints for the templates catalog.
//
// Tools: templates_list, templates_get, templates_apply,
//        templates_preview, templates_register, templates_unregister.
//
// REST:  /admin/templates              GET (list), POST (register)
//        /admin/templates/:name        GET (full), DELETE (unregister)
//        /admin/templates/:name/apply  POST
//        /admin/templates/:name/preview GET

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
	"gopkg.in/yaml.v3"
)

// ── MCP tools ────────────────────────────────────────────────────

func (a *App) toolTemplatesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	// Lazy seed — cheap UPSERT, safe to call on every list.
	_ = seedBundledTemplates(ctx, pid)
	out, err := dbListTemplates(ctx.AppDB(), pid,
		asString(args["source"]), asString(args["tag"]))
	if err != nil {
		return nil, err
	}
	// List responses drop the body to keep things small; the agent
	// fetches the body via templates_get when it needs it.
	for i := range out {
		out[i].Body = ""
	}
	return map[string]any{"templates": out}, nil
}

func (a *App) toolTemplatesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := asString(args["name"])
	if name == "" {
		return nil, errors.New("name required")
	}
	_ = seedBundledTemplates(ctx, pid)
	t, err := dbGetTemplate(ctx.AppDB(), pid, name)
	if err != nil || t == nil {
		return nil, fmt.Errorf("template %q not found", name)
	}
	// Enrich with a parsed page+post index so the panel can show a
	// page picker without having to re-parse the YAML client-side.
	out := map[string]any{"template": t}
	var body TemplateBody
	if err := yaml.Unmarshal([]byte(t.Body), &body); err == nil {
		entries := make([]map[string]any, 0, len(body.Pages)+len(body.Posts))
		for _, p := range body.Pages {
			title := p.Title
			if title == "" {
				title = p.Slug
			}
			entries = append(entries, map[string]any{
				"kind": "page", "slug": p.Slug, "title": title,
				"blocks_count": len(p.Blocks),
			})
		}
		for _, p := range body.Posts {
			title := p.Title
			if title == "" {
				title = p.Slug
			}
			entries = append(entries, map[string]any{
				"kind": "post", "slug": p.Slug, "title": title,
				"blocks_count": len(p.Blocks),
			})
		}
		out["pages"] = entries
		out["homepage_slug"] = body.HomepageSlug
	}
	return out, nil
}

func (a *App) toolTemplatesPreview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := asString(args["name"])
	if name == "" {
		return nil, errors.New("name required")
	}
	_ = seedBundledTemplates(ctx, pid)
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	mode := ApplyMode(asStringDefault(args["mode"], string(ApplyEmptyOnly)))
	summary, err := applyTemplate(ctx, pid, siteID, name, mode, true /* dryRun */)
	if err != nil {
		return nil, err
	}
	return map[string]any{"summary": summary}, nil
}

func (a *App) toolTemplatesApply(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := asString(args["name"])
	if name == "" {
		return nil, errors.New("name required")
	}
	_ = seedBundledTemplates(ctx, pid)
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	mode := ApplyMode(asStringDefault(args["mode"], string(ApplyEmptyOnly)))
	summary, err := applyTemplate(ctx, pid, siteID, name, mode, false)
	if err != nil {
		return nil, err
	}
	ctx.Emit("template.applied", map[string]any{"name": name, "site_id": siteID, "mode": string(mode)})
	return map[string]any{"summary": summary}, nil
}

func (a *App) toolTemplatesRegister(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	body := asString(args["body"])
	if body == "" {
		return nil, errors.New("body (raw YAML) required")
	}
	// Parse the metadata header to populate catalog columns.
	var meta TemplateBody
	if err := yaml.Unmarshal([]byte(body), &meta); err != nil {
		return nil, fmt.Errorf("parse template body: %w", err)
	}
	if meta.Schema != TemplateSchemaCurrent {
		return nil, fmt.Errorf("schema %q not supported (expected %s)", meta.Schema, TemplateSchemaCurrent)
	}
	if meta.Name == "" {
		return nil, errors.New("template.name required")
	}
	if meta.Version == "" {
		meta.Version = "0.0.0"
	}
	t, err := dbUpsertTemplate(ctx.AppDB(), pid, Template{
		Name:         meta.Name,
		DisplayName:  firstNonEmpty(meta.DisplayName, meta.Name),
		Version:      meta.Version,
		Description:  strings.TrimSpace(meta.Description),
		Tags:         meta.Tags,
		PreviewImage: meta.PreviewImage,
		Source:       asStringDefault(args["source"], "imported"),
		Body:         body,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"template": t}, nil
}

func (a *App) toolTemplatesUnregister(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := asString(args["name"])
	if name == "" {
		return nil, errors.New("name required")
	}
	if err := dbDeleteTemplate(ctx.AppDB(), pid, name); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "name": name}, nil
}

// ── REST handlers ────────────────────────────────────────────────

func (a *App) handleHTTPTemplates(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolTemplatesList(ctx, map[string]any{
			"_project_id": pid,
			"source":      r.URL.Query().Get("source"),
			"tag":         r.URL.Query().Get("tag"),
		})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolTemplatesRegister(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPTemplateItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/templates/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if name == "" {
		httpErr(w, http.StatusBadRequest, "template name required")
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "apply":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST only")
				return
			}
			siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body == nil {
				body = map[string]any{}
			}
			body["_project_id"] = pid
			body["_site_id"] = siteID
			body["name"] = name
			out, err := a.toolTemplatesApply(ctx, body)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "preview":
			siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			out, err := a.toolTemplatesPreview(ctx, map[string]any{
				"_project_id": pid,
				"_site_id":    siteID,
				"name":        name,
				"mode":        r.URL.Query().Get("mode"),
			})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "preview-render":
			a.handleTemplatePreviewRender(w, r, ctx, pid, name)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolTemplatesGet(ctx, map[string]any{"_project_id": pid, "name": name})
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodDelete:
		out, err := a.toolTemplatesUnregister(ctx, map[string]any{"_project_id": pid, "name": name})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTemplatePreviewRender renders one of a template's pages
// through the active theme without touching the database. The panel
// iframes this URL so users can see exactly what they'll get before
// hitting Apply.
//
// URL: GET /admin/templates/{name}/preview-render?page={slug}
//
// Defaults page to the template's homepage_slug, falling back to the
// first declared page if homepage_slug is empty. Returns text/html
// with X-Robots-Tag: noindex. Inline <script> blocks are stripped
// from the response — form blocks render visually but their progressive
// enhancement is disabled, so a stray Submit click in the preview
// can't fire actions against the real database.
func (a *App) handleTemplatePreviewRender(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid, name string) {
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = seedBundledTemplates(ctx, pid)
	t, err := dbGetTemplate(ctx.AppDB(), pid, name)
	if err != nil || t == nil {
		httpErr(w, http.StatusNotFound, "template not found")
		return
	}
	var body TemplateBody
	if err := yaml.Unmarshal([]byte(t.Body), &body); err != nil {
		httpErr(w, http.StatusInternalServerError, "parse template: "+err.Error())
		return
	}
	pageSlug := strings.TrimSpace(r.URL.Query().Get("page"))
	if pageSlug == "" {
		pageSlug = body.HomepageSlug
	}
	// Look across pages then posts.
	var (
		hit  *TemplatePost
		kind string
	)
	for i := range body.Pages {
		if pageSlug == "" || body.Pages[i].Slug == pageSlug {
			hit = &body.Pages[i]
			kind = "page"
			break
		}
	}
	if hit == nil {
		for i := range body.Posts {
			if body.Posts[i].Slug == pageSlug {
				hit = &body.Posts[i]
				kind = "post"
				break
			}
		}
	}
	if hit == nil {
		httpErr(w, http.StatusNotFound, "page slug not found in template")
		return
	}
	// Build a synthetic Post. Block ids are assigned on the fly so the
	// renderer validator doesn't reject the tree — the template's
	// YAML doesn't carry stable ids (they're minted at apply time).
	blocks := append([]Block(nil), hit.Blocks...)
	assignMissingIDs(blocks)
	title := hit.Title
	if title == "" {
		title = hit.Slug
	}
	post := &Post{
		ID:         0,
		ProjectID:  pid,
		SiteID:     siteID,
		Kind:       kind,
		Slug:       hit.Slug,
		Locale:     "en",
		Status:     "published",
		Title:      title,
		BodyBlocks: Document{Version: documentVersion, Blocks: blocks},
		Template:   hit.Template,
	}

	settings, _ := effectiveSettings(ctx, pid, siteID)
	data := basePageData(ctx, pid, siteID, settings, r)
	data.Post = post
	data.PageTitle = title + " (preview)"

	html, err := renderSingle(data)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	html = stripInlineScripts(html)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(html))
}

// stripInlineScripts removes every <script>…</script> pair from the
// rendered HTML so the preview iframe is inert. The form block's
// progressive-enhancement listener is the only thing currently
// emitted as inline script; killing all of them is the
// belt-and-braces version. If the renderer grows other legitimate
// inline scripts later, this helper has to become more surgical.
func stripInlineScripts(s string) string {
	for {
		i := strings.Index(s, "<script")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "</script>")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len("</script>"):]
	}
}
