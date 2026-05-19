package main

import sdk "github.com/apteva/app-sdk"

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/compositions", Handler: a.handleListCompositions},
		{Pattern: "/composition/", Handler: a.handleCompositionByID},
		{Pattern: "/render", Handler: a.handleRender},
		{Pattern: "/render-status/", Handler: a.handleRenderStatus},
		{Pattern: "/cache/", Handler: a.handleCacheGet},
		{Pattern: "/bindings", Handler: a.handleBindings},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "composition_create",
			Description: "Create a composition. Args: name?, tracks (array of {clips:[{asset:{type,src},start,length,transition?,text?}]}), soundtrack? ({src,volume?}), output? ({format,resolution,aspect,fps}). Returns {id, duration_seconds}.",
			InputSchema: schemaObject(map[string]any{
				"name":       map[string]any{"type": "string"},
				"tracks":     map[string]any{"type": "array"},
				"soundtrack": map[string]any{"type": "object"},
				"background": map[string]any{"type": "string"},
				"output":     map[string]any{"type": "object"},
			}, []string{"tracks"}),
			Handler: a.toolCompositionCreate,
		},
		{
			Name:        "composition_update",
			Description: "Patch a composition. Args: id, patch (subset of {name, tracks, soundtrack, background, output}).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolCompositionUpdate,
		},
		{
			Name:        "composition_get",
			Description: "Fetch one composition with its latest render. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCompositionGet,
		},
		{
			Name:        "composition_list",
			Description: "List compositions for this project. Args: limit? (default 50).",
			InputSchema: schemaObject(map[string]any{
				"limit": map[string]any{"type": "integer", "default": 50},
			}, nil),
			Handler: a.toolCompositionList,
		},
		{
			Name:        "composition_delete",
			Description: "Delete a composition (cascades renders). Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCompositionDelete,
		},
		{
			Name:        "composition_render",
			Description: "Submit a composition for rendering. Args: id, executor? ('local'|'remote' — overrides the auto ladder). Local executors return {status:'complete'}; SaaS executors (v0.2+) return {status:'queued', render_id}.",
			InputSchema: schemaObject(map[string]any{
				"id":       map[string]any{"type": "integer"},
				"executor": map[string]any{"type": "string", "enum": []string{"local", "remote"}},
			}, []string{"id"}),
			Handler: a.toolCompositionRender,
		},
		{
			Name:        "render_status",
			Description: "Poll one render row. Args: render_id.",
			InputSchema: schemaObject(map[string]any{
				"render_id": map[string]any{"type": "integer"},
			}, []string{"render_id"}),
			Handler: a.toolRenderStatus,
		},
		{
			Name:        "asset_inspect",
			Description: "Probe an asset via ffprobe. Args: src (storage:N | https:// | mediastudio:N). Returns {kind, duration_seconds, width, height, codec, fps}.",
			InputSchema: schemaObject(map[string]any{
				"src": map[string]any{"type": "string"},
			}, []string{"src"}),
			Handler: a.toolAssetInspect,
		},
	}
}

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
