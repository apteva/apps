package main

// MCP tool handlers — thin wrappers over store.go + render.go +
// storageclient.go. Tool surface mirrors the manifest's mcp_tools
// list; tests in tools_test.go hit these directly via testkit.
//
// Auth: every tool resolves the target template first, then checks the
// caller's resource-scoped grant. The manifest cannot express an
// id-or-slug resource selector, so authorization intentionally lives
// here rather than in the SDK's pre-call gate.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── templates CRUD ──────────────────────────────────────────────────

func (a *App) toolListTemplatesCtx(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	templates, err := listTemplateSummaries(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	templates = sdk.Filter(sdk.CallerFrom(callCtx), "docs.read", templates, func(t Template) string {
		return templateResource(t.ID)
	})
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

func (a *App) toolGetTemplateCtx(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	slug := strArg(args, "slug")
	t, err := getTemplate(ctx.AppDB(), id, slug)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return map[string]any{"found": false}, nil
	}
	if err := authorizeTemplate(callCtx, "docs.read", t.ID); err != nil {
		return nil, err
	}
	return map[string]any{"found": true, "template": t}, nil
}

func (a *App) toolCreateTemplateCtx(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := authorizeBroad(callCtx, "docs.write"); err != nil {
		return nil, err
	}
	t := &Template{
		Slug:          strArg(args, "slug"),
		Name:          strArg(args, "name"),
		Description:   strArg(args, "description"),
		Body:          strArg(args, "body"),
		SourceFormat:  "markdown",
		OutputFormat:  "pdf",
		DefaultFolder: strArg(args, "default_folder"),
	}
	if err := validateTemplateSize(ctx, t.Body); err != nil {
		return nil, err
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

func (a *App) toolUpdateTemplateCtx(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := authorizeTemplate(callCtx, "docs.write", id); err != nil {
		return nil, err
	}
	fields := map[string]any{}
	for _, k := range []string{"name", "description", "body", "default_folder"} {
		if v, ok := args[k]; ok {
			fields[k] = v
		}
	}
	if body, ok := fields["body"].(string); ok {
		if err := validateTemplateSize(ctx, body); err != nil {
			return nil, err
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

func (a *App) toolDeleteTemplateCtx(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, _ := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	if err := authorizeTemplate(callCtx, "docs.write", id); err != nil {
		return nil, err
	}
	if err := deleteTemplate(ctx.AppDB(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

// ─── render ───────────────────────────────────────────────────────────

func (a *App) toolRenderCtx(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := lookupTemplateForRender(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	if err := authorizeTemplate(callCtx, "docs.render", t.ID); err != nil {
		return nil, err
	}
	return a.renderDocument(ctx, args, renderedBy(callCtx))
}

func (a *App) renderDocument(ctx *sdk.AppCtx, args map[string]any, actor string) (any, error) {
	t, err := lookupTemplateForRender(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	data, _ := args["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	if err := validateRenderData(ctx, data); err != nil {
		return nil, err
	}
	warnings := []string{}
	pageSize, err := resolvePageSize(strArg(args, "page_size"), ctx.Config().Get("page_size"))
	if err != nil {
		return nil, err
	}
	body, err := renderTemplateToPDF(ctx, t, data, pageSize, &warnings)
	if err != nil {
		return nil, err
	}
	if max := configIntDefault(ctx.Config().Get("max_pdf_bytes"), 20<<20); max > 0 && len(body) > max {
		return nil, fmt.Errorf("generated PDF is %d bytes; maximum is %d", len(body), max)
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
	if len(name) > 240 || strings.ContainsRune(name, '\x00') || strings.ContainsRune(folder, '\x00') {
		return nil, errors.New("invalid output name or folder")
	}
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}

	uploaded, err := uploadToStorage(ctx, name, folder, "application/pdf", body)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}

	// Audit row. The storage write cannot be rolled back, so surface a
	// precise partial-success error if audit persistence fails.
	dataJSON, _ := json.Marshal(data)
	renderID, auditErr := insertRender(ctx.AppDB(), &Render{
		TemplateID:   t.ID,
		TemplateSlug: t.Slug,
		OutputFileID: strconv.FormatInt(uploaded.ID, 10),
		OutputName:   name,
		OutputFolder: folder,
		DataSnapshot: dataJSON,
		RenderedBy:   actor,
		Bytes:        int64(len(body)),
	})
	if auditErr != nil {
		ctx.Logger().Error("docs audit insert failed", "file_id", uploaded.ID, "err", auditErr)
		return nil, fmt.Errorf("PDF uploaded as storage file %d but audit recording failed: %w", uploaded.ID, auditErr)
	}

	return map[string]any{
		"file_id":   uploaded.ID,
		"url":       uploaded.URL,
		"name":      uploaded.Name,
		"folder":    uploaded.Folder,
		"sha256":    uploaded.SHA256,
		"render_id": renderID,
		"warnings":  warnings,
		"page_size": pageSize,
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
func (a *App) toolPreviewCtx(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	body := strArg(args, "body")
	if body != "" {
		if err := authorizeBroad(callCtx, "docs.render"); err != nil {
			return nil, err
		}
	} else {
		t, err := lookupTemplateForRender(ctx.AppDB(), args)
		if err != nil {
			return nil, err
		}
		if err := authorizeTemplate(callCtx, "docs.render", t.ID); err != nil {
			return nil, err
		}
	}
	return a.previewDocument(ctx, args)
}

func (a *App) previewDocument(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	body := strArg(args, "body")
	if body == "" {
		t, err := lookupTemplateForRender(ctx.AppDB(), args)
		if err != nil {
			return nil, err
		}
		body = t.Body
	}
	data, _ := args["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	if err := validateTemplateSize(ctx, body); err != nil {
		return nil, err
	}
	if err := validateRenderData(ctx, data); err != nil {
		return nil, err
	}
	pageSize, err := resolvePageSize(strArg(args, "page_size"), ctx.Config().Get("page_size"))
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	pdf, err := renderPDF(body, data, RenderOptions{PageSize: pageSize, ImageResolver: imageResolverFor(ctx), OnWarning: func(s string) { warnings = append(warnings, s) }})
	if err != nil {
		return nil, err
	}
	if max := configIntDefault(ctx.Config().Get("max_pdf_bytes"), 20<<20); max > 0 && len(pdf) > max {
		return nil, fmt.Errorf("generated PDF is %d bytes; maximum is %d", len(pdf), max)
	}
	return map[string]any{
		"content_type": "application/pdf",
		"size_bytes":   len(pdf),
		"base64":       base64.StdEncoding.EncodeToString(pdf),
		"warnings":     warnings,
		"page_size":    pageSize,
	}, nil
}

// renderTemplateToPDF wraps renderPDF with the install's page-size
// config. Pulled out for testability — preview uses the same path.
func renderTemplateToPDF(ctx *sdk.AppCtx, t *Template, data map[string]any, pageSize string, warnings *[]string) ([]byte, error) {
	return renderPDF(t.Body, data, RenderOptions{
		PageSize:      pageSize,
		ImageResolver: imageResolverFor(ctx),
		OnWarning: func(s string) {
			if warnings != nil {
				*warnings = append(*warnings, s)
			}
		},
	})
}

// imageResolverFor binds an imageResolver to this install's storage
// connection so docs_render / docs_preview can pull logo + inline
// images. Both paths share it, so the panel preview shows images too.
func imageResolverFor(ctx *sdk.AppCtx) imageResolver {
	type cached struct {
		data []byte
		ext  string
		err  error
	}
	cache := map[string]cached{}
	return func(src string) ([]byte, string, error) {
		if got, ok := cache[src]; ok {
			return got.data, got.ext, got.err
		}
		data, ext, err := resolveImageSrc(ctx, src)
		cache[src] = cached{data: data, ext: ext, err: err}
		return data, ext, err
	}
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

func (a *App) toolListRendersCtx(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	templateID, _ := int64Arg(args, "template_id")
	if templateID > 0 {
		if err := authorizeTemplate(callCtx, "docs.read", templateID); err != nil {
			return nil, err
		}
	}
	since := strArg(args, "since")
	limit := 50
	if v, ok := int64Arg(args, "limit"); ok && v > 0 && v <= 500 {
		limit = int(v)
	}
	offset := 0
	if v, ok := int64Arg(args, "offset"); ok && v > 0 {
		offset = int(v)
	}
	filters := RenderFilters{TemplateID: templateID, Since: since, Limit: limit, Offset: offset}
	var rows []Render
	var err error
	caller := sdk.CallerFrom(callCtx)
	if templateID == 0 && caller != nil {
		rows, err = listAuthorizedRenders(ctx.AppDB(), caller, filters)
	} else {
		rows, err = listRenders(ctx.AppDB(), filters)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"renders": rows, "count": len(rows)}, nil
}

// listAuthorizedRenders paginates over the raw audit stream and applies the
// caller's resource policy before limit/offset. Filtering only the first SQL
// page would hide older authorized rows whenever newer rows belonged to a
// different template.
func listAuthorizedRenders(db *sql.DB, caller *sdk.Caller, filters RenderFilters) ([]Render, error) {
	const batchSize = 500
	want := filters.Limit
	if want <= 0 || want > batchSize {
		want = 50
	}
	skip := filters.Offset
	if skip < 0 {
		skip = 0
	}
	out := make([]Render, 0, want)
	for rawOffset := 0; len(out) < want; rawOffset += batchSize {
		batch, err := listRenders(db, RenderFilters{Since: filters.Since, Limit: batchSize, Offset: rawOffset})
		if err != nil {
			return nil, err
		}
		for _, row := range batch {
			if !caller.Allows("docs.read", templateResource(row.TemplateID)) {
				continue
			}
			if skip > 0 {
				skip--
				continue
			}
			out = append(out, row)
			if len(out) == want {
				break
			}
		}
		if len(batch) < batchSize {
			break
		}
	}
	return out, nil
}

func (a *App) toolGetRenderCtx(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
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
	if err := authorizeTemplate(callCtx, "docs.read", r.TemplateID); err != nil {
		return nil, err
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
		if math.Trunc(v) == v {
			return int64(v), true
		}
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

func templateResource(id int64) string {
	return "template/" + strconv.FormatInt(id, 10)
}

func authorizeTemplate(callCtx context.Context, permission string, id int64) error {
	if id <= 0 {
		return errors.New("template id required for authorization")
	}
	resource := templateResource(id)
	if !sdk.CallerFrom(callCtx).Allows(permission, resource) {
		return sdk.Forbidden(permission, resource)
	}
	return nil
}

// authorizeBroad is used when no existing template can anchor a scoped
// grant (create and unsaved inline preview). Only a wildcard grant—or the
// SDK's backwards-compatible nil caller—may perform the operation.
func authorizeBroad(callCtx context.Context, permission string) error {
	if !sdk.CallerFrom(callCtx).Allows(permission, "*") {
		return sdk.Forbidden(permission, "*")
	}
	return nil
}

func renderedBy(callCtx context.Context) string {
	caller := sdk.CallerFrom(callCtx)
	if caller != nil && caller.AgentID > 0 {
		return "agent:" + strconv.FormatInt(caller.AgentID, 10)
	}
	return "mcp"
}

func validateTemplateSize(ctx *sdk.AppCtx, body string) error {
	max := configIntDefault(ctx.Config().Get("max_template_bytes"), 512<<10)
	if max > 0 && len(body) > max {
		return fmt.Errorf("template body is %d bytes; maximum is %d", len(body), max)
	}
	return nil
}

func validateRenderData(ctx *sdk.AppCtx, data map[string]any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("render data is not JSON-encodable: %w", err)
	}
	max := configIntDefault(ctx.Config().Get("max_render_data_bytes"), 256<<10)
	if max > 0 && len(encoded) > max {
		return fmt.Errorf("render data is %d bytes; maximum is %d", len(encoded), max)
	}
	return nil
}

func resolvePageSize(requested, configured string) (string, error) {
	value := strings.TrimSpace(requested)
	if value == "" {
		value = strings.TrimSpace(configured)
	}
	if value == "" {
		return "A4", nil
	}
	switch strings.ToLower(value) {
	case "a4":
		return "A4", nil
	case "letter":
		return "letter", nil
	case "legal":
		return "legal", nil
	default:
		return "", errors.New("page_size must be A4, letter, or legal")
	}
}
