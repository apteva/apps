package main

import sdk "github.com/apteva/app-sdk"

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/compositions", Handler: a.handleListCompositions},
		{Pattern: "/composition/", Handler: a.handleCompositionByID},
		{Pattern: "/render", Handler: a.handleRender},
		{Pattern: "/render-status/", Handler: a.handleRenderStatus},
		{Pattern: "/render-font", Handler: a.handleRenderFont},
		{Pattern: "/cards/composition/", Handler: a.handleCompositionCard},
		{Pattern: "/cards/render/", Handler: a.handleRenderCard},
		{Pattern: "/ai/generate", Handler: a.handleAIGenerate},
		{Pattern: "/cache/", Handler: a.handleCacheGet},
		{Pattern: "/bindings", Handler: a.handleBindings},
		{Pattern: "/validate", Handler: a.handleValidate},
		{Pattern: "/examples", Handler: a.handleExamples},
		{Pattern: "/assets/resolve", Handler: a.handleAssetResolve},
		{Pattern: "/assets/storage", Handler: a.handleStorageAssets},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return append(a.outputTools(), []sdk.Tool{
		{
			Name:        "composition_create",
			Description: "Create a V1 timeline composition, or pass spec with version composer/v2 for native shape/text scene graphs. V2 shapes support rectangles, ellipses, gradients, borders, radii, and shadows. V1 args: name?, tracks, markers?, soundtrack?, background?, output?. Video and audio clips can reuse source assets with source_start/source_end and playback_rate; retained audio stays synchronized. Visual clips can crop normalized source regions and animate source-space focus/zoom through transform.keyframes. Returns {id, version, duration_seconds}.",
			InputSchema: schemaObject(map[string]any{
				"name":       map[string]any{"type": "string"},
				"tracks":     map[string]any{"type": "array"},
				"soundtrack": map[string]any{"type": "object"},
				"background": map[string]any{"type": "string"},
				"markers":    map[string]any{"type": "array"},
				"output":     map[string]any{"type": "object"},
			}, nil),
			Handler: a.toolCompositionCreate,
		},
		{
			Name:        "composition_update",
			Description: "Patch a V1 composition. Args: id, patch. Send subset of {name, tracks, markers, soundtrack, background, output, expected_revision}. Video/audio clips support source ranges and playback_rate. Visual clips support normalized crop, source-space transform keyframes, Shotstack-style layout fields, or Composer's layout alias.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolCompositionUpdate,
		},
		{
			Name:        "composition_validate",
			Description: "Validate a Composer V1 or V2 composition before saving/rendering. Args: spec, edit_json, or V1 fields. Returns version, duration_seconds, renderer, warnings, and errors including safe-area diagnostics.",
			InputSchema: schemaObject(map[string]any{
				"spec":      map[string]any{"type": "object"},
				"edit_json": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolCompositionValidate,
		},
		{
			Name:        "composition_examples",
			Description: "Return Composer V2 examples with native shapes, text, motion, and browser scene graphs that agents can adapt.",
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
			Description: "Submit a composition for rendering. Args: id, executor? ('local'|'remote' — overrides the auto ladder), wait? (default true for compatibility). Set wait=false for a durable background render: the call returns {render_id,status:'queued'} immediately, AI assets generate automatically, and render_status or the Composer render-card follows the result.",
			InputSchema: schemaObject(map[string]any{
				"id":                map[string]any{"type": "integer"},
				"executor":          map[string]any{"type": "string", "enum": []string{"local", "remote"}},
				"wait":              map[string]any{"type": "boolean", "default": true},
				"expected_revision": map[string]any{"type": "integer", "description": "Optional revision returned by create/update/get; rejects rendering if it changed."},
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
			Name:        "render_cancel",
			Description: "Cancel a queued or running render. Args: render_id. Active encoding is interrupted; completed, failed, or already-cancelled renders are idempotent.",
			InputSchema: schemaObject(map[string]any{
				"render_id": map[string]any{"type": "integer"},
			}, []string{"render_id"}),
			Handler: a.toolRenderCancel,
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
	}...)
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

func (a *App) outputTools() []sdk.Tool {
	props := map[string]any{"storage_id": map[string]any{"type": "integer"}, "render_id": map[string]any{"type": "integer"}, "duration_ms": map[string]any{"type": "integer"}, "id": map[string]any{"type": "integer"}, "kind": map[string]any{"type": "string", "enum": []string{"song", "image_video", "full_clip"}}, "project_id": map[string]any{"type": "string"}, "expected_revision": map[string]any{"type": "integer"}, "idempotency_key": map[string]any{"type": "string"}, "settings": map[string]any{"type": "object"}, "plan": map[string]any{"type": "object"}, "master": map[string]any{"type": "object"}, "executor": map[string]any{"type": "string"}}
	return []sdk.Tool{
		{Name: "composition_output_estimate", Description: "Read the selected output dependency requirements without generating anything. Unknown costs are null.", InputSchema: schemaObject(props, []string{"id", "kind"}), Handler: a.toolOutputEstimate},
		{Name: "composition_output_adopt", Description: "Adopt an existing Storage artifact or legacy render into an output, preserving all original media/history. Requires expected_revision and idempotency_key. Imported input revisions are unknown until re-exported.", InputSchema: schemaObject(props, []string{"id", "kind", "expected_revision", "idempotency_key"}), Handler: a.toolOutputAdopt},
		{Name: "composition_outputs", Description: "List saved audio, image-video and full-video outputs with independent latest attempts and successful artifacts.", InputSchema: schemaObject(props, []string{"id"}), Handler: a.toolOutputList},
		{Name: "composition_output_update", Description: "Save output settings or a visual-only Edit plan. Requires expected_revision. Excerpts use excerpt_start/excerpt_end in seconds; audio comes from shared inputs.", InputSchema: schemaObject(props, []string{"id", "kind", "expected_revision"}), Handler: a.toolOutputUpdate},
		{Name: "composition_output_master", Description: "Select the shared audio master as a Clip {asset:{type:audio,src:storage:N},length:seconds,ai?}. Requires shared expected_revision. Use a new AI cache_key only to explicitly regenerate; existing outputs retain artifacts and become stale.", InputSchema: schemaObject(props, []string{"id", "master", "expected_revision"}), Handler: a.toolOutputMaster},
		{Name: "composition_output_render", Description: "Render one saved output using only its dependencies. Requires idempotency_key and expected_revision. Repeat the same request to resume waiting_ai; terminal attempts are immutable. New attempts reuse generated assets.", InputSchema: schemaObject(props, []string{"id", "kind", "expected_revision", "idempotency_key"}), Handler: a.toolOutputRender},
		{Name: "composition_output_history", Description: "List up to 100 attempts for one saved output.", InputSchema: schemaObject(props, []string{"id", "kind"}), Handler: a.toolOutputHistory},
	}
}
