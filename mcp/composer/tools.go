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
		{Pattern: "/validate", Handler: a.handleValidate},
		{Pattern: "/examples", Handler: a.handleExamples},
		{Pattern: "/assets/resolve", Handler: a.handleAssetResolve},
		{Pattern: "/assets/storage", Handler: a.handleStorageAssets},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "composition_create",
			Description: "Create a V1 timeline composition. Args: name?, tracks, soundtrack?, background?, output?. Clips support AI generation, source_images, audio tracks, overlays, and audio-only outputs. Returns {id, version, duration_seconds}.",
			InputSchema: schemaObject(map[string]any{
				"name":       map[string]any{"type": "string"},
				"tracks":     map[string]any{"type": "array"},
				"soundtrack": map[string]any{"type": "object"},
				"background": map[string]any{"type": "string"},
				"output":     map[string]any{"type": "object"},
			}, nil),
			Handler: a.toolCompositionCreate,
		},
		{
			Name:        "composition_update",
			Description: "Patch a V1 composition. Args: id, patch. Send subset of {name, tracks, soundtrack, background, output}.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolCompositionUpdate,
		},
		{
			Name:        "composition_validate",
			Description: "Validate a V1 composition before saving/rendering. Args: edit_json? (string) or V1 fields. Returns version, duration_seconds, renderer ('ffmpeg'), warnings, errors.",
			InputSchema: schemaObject(map[string]any{
				"spec":      map[string]any{"type": "object"},
				"edit_json": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolCompositionValidate,
		},
		{
			Name:        "composition_examples",
			Description: "Return composition examples agents can adapt. Experimental V2 examples are hidden in public installs.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolCompositionExamples,
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
			Description: "List compositions for this project. Args: limit? (default 50), summary? (true returns lightweight rows without full edit/output JSON).",
			InputSchema: schemaObject(map[string]any{
				"limit":   map[string]any{"type": "integer", "default": 50},
				"summary": map[string]any{"type": "boolean", "default": false},
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
			Description: "Submit a composition for rendering. Args: id, executor? ('local'|'remote' — overrides the auto ladder). If missing AI assets queue at Media Studio, returns {status:'waiting_ai', pending}. Local executors return {status:'complete'}.",
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
		{
			Name:        "asset_search",
			Description: "Search reusable Storage assets for composition. Args: q?, kind? ('audio'|'image'|'video'), tags? ([]), folder? (default '/'), recursive? (default true), limit? (default 50), inspect? (probe duration/codec), include_generated? (default false). Returns assets with composition-ready src values like storage:N.",
			InputSchema: schemaObject(map[string]any{
				"q":                 map[string]any{"type": "string"},
				"kind":              map[string]any{"type": "string", "enum": []string{"audio", "image", "video"}},
				"tags":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"folder":            map[string]any{"type": "string", "default": "/"},
				"recursive":         map[string]any{"type": "boolean", "default": true},
				"limit":             map[string]any{"type": "integer", "default": 50},
				"inspect":           map[string]any{"type": "boolean", "default": false},
				"include_generated": map[string]any{"type": "boolean", "default": false},
			}, nil),
			Handler: a.toolAssetSearch,
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
