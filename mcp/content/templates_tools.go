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
	return map[string]any{"template": t}, nil
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
	mode := ApplyMode(asStringDefault(args["mode"], string(ApplyEmptyOnly)))
	summary, err := applyTemplate(ctx, pid, name, mode, true /* dryRun */)
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
	mode := ApplyMode(asStringDefault(args["mode"], string(ApplyEmptyOnly)))
	summary, err := applyTemplate(ctx, pid, name, mode, false)
	if err != nil {
		return nil, err
	}
	ctx.Emit("template.applied", map[string]any{"name": name, "mode": string(mode)})
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
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body == nil {
				body = map[string]any{}
			}
			body["_project_id"] = pid
			body["name"] = name
			out, err := a.toolTemplatesApply(ctx, body)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "preview":
			out, err := a.toolTemplatesPreview(ctx, map[string]any{
				"_project_id": pid,
				"name":        name,
				"mode":        r.URL.Query().Get("mode"),
			})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
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
