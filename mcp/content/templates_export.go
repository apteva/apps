package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	sdk "github.com/apteva/app-sdk"
	"gopkg.in/yaml.v3"
)

// templates_export_site turns an existing site into a reusable site kit. It
// preserves the block tree, settings, taxonomy, and navigation while leaving
// hostnames and site IDs behind.
func (a *App) toolTemplatesExportSite(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	name := slugify(asString(args["name"]))
	if name == "" {
		return nil, fmt.Errorf("name required")
	}
	display := asString(args["display_name"])
	if display == "" {
		display = name
	}
	body := TemplateBody{Schema: TemplateSchemaCurrent, Name: name, DisplayName: display, Version: "1", Description: asString(args["description"]), Settings: map[string]string{}}
	register, _ := args["register"].(bool)
	settings, _ := dbListSettings(ctx.AppDB(), pid, siteID)
	for k, v := range settings {
		if k != "homepage_page_id" {
			body.Settings[k] = v
		}
	}
	rows, err := ctx.AppDB().Query(`SELECT id,kind,slug,title,excerpt,body_blocks,parent_id,template FROM posts WHERE project_id=? AND site_id=? AND deleted_at IS NULL AND status='published' ORDER BY kind,id`, pid, siteID)
	if err != nil {
		return nil, err
	}
	parentSlugs := map[int64]string{}
	rawPosts := []struct {
		kind, slug, title, excerpt, blocks, template string
		id                                           int64
		parent                                       *int64
	}{}
	for rows.Next() {
		var id int64
		var kind, slug, title, excerpt, blocks, template string
		var parent sql.NullInt64
		if err := rows.Scan(&id, &kind, &slug, &title, &excerpt, &blocks, &parent, &template); err != nil {
			return nil, err
		}
		var parentID *int64
		if parent.Valid {
			v := parent.Int64
			parentID = &v
		}
		rawPosts = append(rawPosts, struct {
			kind, slug, title, excerpt, blocks, template string
			id                                           int64
			parent                                       *int64
		}{kind, slug, title, excerpt, blocks, template, id, parentID})
		parentSlugs[id] = slug
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, raw := range rawPosts {
		doc, _ := parseDocument(raw.blocks)
		stripTemplateMediaIDs(doc.Blocks)
		p := TemplatePost{Slug: raw.slug, Title: raw.title, Excerpt: raw.excerpt, Blocks: doc.Blocks, Template: raw.template}
		if raw.parent != nil {
			p.ParentSlug = parentSlugs[*raw.parent]
		}
		if raw.kind == "page" {
			body.Pages = append(body.Pages, p)
		} else {
			body.Posts = append(body.Posts, p)
		}
	}
	terms, _ := dbListTerms(ctx.AppDB(), pid, siteID, "", "")
	for _, t := range terms {
		body.Terms = append(body.Terms, TemplateTerm{Kind: t.Kind, Name: t.Name, Slug: t.Slug, Description: t.Description})
	}
	menus, _ := dbListMenus(ctx.AppDB(), pid, siteID)
	for _, m := range menus {
		body.Menus = append(body.Menus, exportTemplateMenu(m.Slug, m.Name, m.Items))
	}
	if home := settings["homepage_page_id"]; home != "" {
		for _, p := range body.Pages {
			var id int64
			if err := ctx.AppDB().QueryRow(`SELECT id FROM posts WHERE project_id=? AND site_id=? AND slug=? AND kind='page'`, pid, siteID, p.Slug).Scan(&id); err == nil && fmt.Sprint(id) == home {
				body.HomepageSlug = p.Slug
			}
		}
	}
	raw, err := yaml.Marshal(body)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"name": name, "body": string(raw)}
	if register {
		if _, err := dbUpsertTemplate(ctx.AppDB(), pid, Template{Name: name, DisplayName: display, Version: "1", Description: body.Description, Source: "exported", Body: string(raw)}); err != nil {
			return nil, err
		}
		out["registered"] = true
	}
	return out, nil
}

func exportTemplateMenu(slug, name string, items []MenuItem) TemplateMenu {
	out := TemplateMenu{Slug: slug, Name: name}
	for _, it := range items {
		out.Items = append(out.Items, TemplateMenuItem{Label: it.Label, TargetKind: it.TargetKind, TargetSlug: it.TargetSlug, TargetURL: it.TargetURL, Children: exportTemplateItems(it.Children)})
	}
	return out
}
func exportTemplateItems(items []MenuItem) []TemplateMenuItem {
	out := []TemplateMenuItem{}
	for _, it := range items {
		out = append(out, TemplateMenuItem{Label: it.Label, TargetKind: it.TargetKind, TargetSlug: it.TargetSlug, TargetURL: it.TargetURL, Children: exportTemplateItems(it.Children)})
	}
	return out
}

func stripTemplateMediaIDs(blocks []Block) {
	for i := range blocks {
		delete(blocks[i].Attrs, "media_id")
		delete(blocks[i].Attrs, "image_media_id")
		delete(blocks[i].Attrs, "media_ids")
		stripTemplateMediaIDs(blocks[i].Inner)
	}
}

func (a *App) handleHTTPTemplateExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	body["_project_id"] = pid
	out, err := a.toolTemplatesExportSite(ctx, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}
