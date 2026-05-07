package main

// MCP tool handlers — thin wrappers over store.go + render.go +
// storageclient.go. Tool surface mirrors the manifest's mcp_tools
// list; tests in tools_test.go hit these directly via testkit.
//
// Auth: every tool calls sdk.CallerFrom(ctx) when relevant. Tools
// declared with `requires:` in the manifest get gated by the SDK's
// pre-call check; nothing extra needed here.

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── templates CRUD ──────────────────────────────────────────────────

func (a *App) toolListTemplates(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	templates, err := listTemplates(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	// Strip body from list response — operators don't want a 50KB
	// HTML blob per row when they're just picking from a list.
	// Detail view (docs_get_template) keeps the body.
	stripped := make([]map[string]any, 0, len(templates))
	for _, t := range templates {
		stripped = append(stripped, map[string]any{
			"id":             t.ID,
			"slug":           t.Slug,
			"name":           t.Name,
			"description":    t.Description,
			"source_format":  t.SourceFormat,
			"output_format":  t.OutputFormat,
			"default_folder": t.DefaultFolder,
			"updated_at":     t.UpdatedAt,
		})
	}
	return map[string]any{"templates": stripped, "count": len(stripped)}, nil
}

func (a *App) toolGetTemplate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	slug := strArg(args, "slug")
	t, err := getTemplate(ctx.AppDB(), id, slug)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return map[string]any{"found": false}, nil
	}
	return map[string]any{"found": true, "template": t}, nil
}

func (a *App) toolCreateTemplate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t := &Template{
		Slug:          strArg(args, "slug"),
		Name:          strArg(args, "name"),
		Description:   strArg(args, "description"),
		Body:          strArg(args, "body"),
		SourceFormat:  strArg(args, "source_format"),
		OutputFormat:  strArg(args, "output_format"),
		DefaultFolder: strArg(args, "default_folder"),
	}
	id, err := createTemplate(ctx.AppDB(), t)
	if err != nil {
		// SQLite UNIQUE violation on slug — give a clean error to
		// the agent rather than a raw SQL message.
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil, fmt.Errorf("template slug %q already exists", t.Slug)
		}
		return nil, err
	}
	t.ID = id
	return map[string]any{"created": true, "template": t}, nil
}

func (a *App) toolUpdateTemplate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	fields := map[string]any{}
	for _, k := range []string{"name", "description", "body", "default_folder"} {
		if v, ok := args[k]; ok {
			fields[k] = v
		}
	}
	if err := updateTemplate(ctx.AppDB(), id, fields); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"updated": true, "id": id}, nil
}

func (a *App) toolDeleteTemplate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if err := deleteTemplate(ctx.AppDB(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

// ─── render ───────────────────────────────────────────────────────────

func (a *App) toolRender(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := lookupTemplateForRender(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	data, _ := args["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	body, err := renderTemplateToPDF(ctx, t, data)
	if err != nil {
		return nil, err
	}
	// Resolve output folder/filename — tool args > template default
	// > install config default ("/docs/" by default).
	folder := strArg(args, "output_folder")
	if folder == "" {
		folder = t.DefaultFolder
	}
	if folder == "" {
		folder = ctx.Config().Get("default_output_folder")
	}
	if folder == "" {
		folder = "/docs/"
	}
	if !strings.HasPrefix(folder, "/") {
		folder = "/" + folder
	}
	if !strings.HasSuffix(folder, "/") {
		folder = folder + "/"
	}
	name := strArg(args, "output_name")
	if name == "" {
		name = defaultOutputName(t.Slug)
	}
	if !strings.HasSuffix(name, ".pdf") {
		name += ".pdf"
	}

	uploaded, err := uploadToStorage(ctx, name, folder, "application/pdf", body)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}

	// Audit row. Failure here doesn't fail the render — the file is
	// already in storage. Log so ops can see it, then return.
	dataJSON, _ := json.Marshal(data)
	renderID, _ := insertRender(ctx.AppDB(), &Render{
		TemplateID:   t.ID,
		TemplateSlug: t.Slug,
		OutputFileID: strconv.FormatInt(uploaded.ID, 10),
		OutputName:   name,
		OutputFolder: folder,
		DataSnapshot: dataJSON,
		RenderedBy:   strArg(args, "_requested_by"),
		Bytes:        int64(len(body)),
	})

	return map[string]any{
		"file_id":   uploaded.ID,
		"url":       uploaded.URL,
		"name":      uploaded.Name,
		"folder":    uploaded.Folder,
		"sha256":    uploaded.SHA256,
		"render_id": renderID,
	}, nil
}

// toolPreview renders without persisting — for the panel's editor
// preview pane. Returns base64 so the dashboard can render with
// data:application/pdf;base64,...
//
// Two modes:
//
//	body=<inline body>   — preview an unsaved draft (panel scratch)
//	template_id|template_slug — preview an existing saved template
//
// The first mode is what the editor uses while the operator is
// typing. Lets them iterate without saving N versions.
func (a *App) toolPreview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	body := strArg(args, "body")
	if body == "" {
		t, err := lookupTemplateForRender(ctx.AppDB(), args)
		if err != nil {
			return nil, err
		}
		body = t.Body
	}
	data, _ := args["data"].(map[string]any)
	pageSize := ctx.Config().Get("page_size")
	pdf, err := renderPDF(body, data, RenderOptions{PageSize: pageSize})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content_type": "application/pdf",
		"size_bytes":   len(pdf),
		"base64":       base64.StdEncoding.EncodeToString(pdf),
	}, nil
}

// renderTemplateToPDF wraps renderPDF with the install's page-size
// config. Pulled out for testability — preview uses the same path.
func renderTemplateToPDF(ctx *sdk.AppCtx, t *Template, data map[string]any) ([]byte, error) {
	pageSize := ctx.Config().Get("page_size")
	return renderPDF(t.Body, data, RenderOptions{PageSize: pageSize})
}

// lookupTemplateForRender — accept template_id (int) or
// template_slug (string), enforce one of them present, return the
// row. Used by toolRender + toolPreview.
func lookupTemplateForRender(db *sql.DB, args map[string]any) (*Template, error) {
	id, _ := int64Arg(args, "template_id")
	slug := strArg(args, "template_slug")
	if id == 0 && slug == "" {
		return nil, errors.New("template_id or template_slug required")
	}
	t, err := getTemplate(db, id, slug)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("template not found")
	}
	return t, nil
}

// defaultOutputName generates "<slug>-YYYY-MM-DD-HHMMSS.pdf" so
// renders don't collide on the same folder + storage's name dedup.
func defaultOutputName(slug string) string {
	return fmt.Sprintf("%s-%s.pdf", slug, time.Now().UTC().Format("2006-01-02-150405"))
}

// ─── audit ────────────────────────────────────────────────────────────

func (a *App) toolListRenders(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	templateID, _ := int64Arg(args, "template_id")
	since := strArg(args, "since")
	limit := 50
	if v, ok := int64Arg(args, "limit"); ok && v > 0 && v <= 500 {
		limit = int(v)
	}
	rows, err := listRenders(ctx.AppDB(), RenderFilters{
		TemplateID: templateID,
		Since:      since,
		Limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"renders": rows, "count": len(rows)}, nil
}

func (a *App) toolGetRender(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "render_id")
	if id == 0 {
		return nil, errors.New("render_id required")
	}
	r, err := getRender(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return map[string]any{"found": false}, nil
	}
	return map[string]any{"found": true, "render": r}, nil
}

// ─── arg helpers ──────────────────────────────────────────────────────

func strArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func int64Arg(args map[string]any, key string) (int64, bool) {
	switch v := args[key].(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, true
		}
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}
