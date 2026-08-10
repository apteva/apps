package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestBuildVeniceReferenceArgs_KlingDefaultsSourceImagesToIdentityElement(t *testing.T) {
	args := map[string]any{
		"source_images": []string{"FRONT", "SIDE", "ANGLE"},
	}
	out := map[string]any{}
	if err := buildVeniceReferenceArgs("kling-o3-pro-reference-to-video", args, out); err != nil {
		t.Fatalf("buildVeniceReferenceArgs: %v", err)
	}
	elements, ok := out["elements"].([]map[string]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("elements = %#v", out["elements"])
	}
	if elements[0]["frontal_image_url"] != "data:image/png;base64,FRONT" {
		t.Fatalf("frontal image = %#v", elements[0]["frontal_image_url"])
	}
	refs, ok := elements[0]["reference_image_urls"].([]string)
	if !ok || len(refs) != 2 ||
		refs[0] != "data:image/png;base64,SIDE" ||
		refs[1] != "data:image/png;base64,ANGLE" {
		t.Fatalf("element references = %#v", elements[0]["reference_image_urls"])
	}
	if _, exists := out["reference_image_urls"]; exists {
		t.Fatalf("Kling identity references must not be flattened: %#v", out)
	}
}

func TestBuildVeniceReferenceArgs_KlingMapsIdentityAndSceneGroups(t *testing.T) {
	args := map[string]any{
		"_resolved_reference_groups": []videoReferenceGroup{
			{Role: videoReferenceRoleIdentity, Images: []string{"FRONT", "SIDE"}},
			{Role: videoReferenceRoleScene, Images: []string{"https://example.test/studio.jpg"}},
		},
	}
	out := map[string]any{}
	if err := buildVeniceReferenceArgs("kling-v3-4k-reference-to-video", args, out); err != nil {
		t.Fatalf("buildVeniceReferenceArgs: %v", err)
	}
	elements, ok := out["elements"].([]map[string]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("elements = %#v", out["elements"])
	}
	scenes, ok := out["scene_image_urls"].([]string)
	if !ok || len(scenes) != 1 || scenes[0] != "https://example.test/studio.jpg" {
		t.Fatalf("scene_image_urls = %#v", out["scene_image_urls"])
	}
}

func TestBuildVeniceReferenceArgs_FlatFamiliesPreserveReferenceOrder(t *testing.T) {
	models := []string{
		"grok-imagine-reference-to-video-private",
		"wan-2-7-reference-to-video",
		"happyhorse-1-1-reference-to-video",
		"gemini-omni-flash-reference-to-video",
		"pixverse-c1-reference-to-video",
		"seedance-2-0-reference-to-video",
		"future-reference-to-video",
	}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			args := map[string]any{
				"_resolved_reference_groups": []videoReferenceGroup{
					{Role: videoReferenceRoleIdentity, Images: []string{"FRONT", "SIDE"}},
					{Role: videoReferenceRoleScene, Images: []string{"SCENE"}},
				},
			}
			out := map[string]any{}
			if err := buildVeniceReferenceArgs(model, args, out); err != nil {
				t.Fatalf("buildVeniceReferenceArgs: %v", err)
			}
			refs, ok := out["reference_image_urls"].([]string)
			if !ok || len(refs) != 3 {
				t.Fatalf("reference_image_urls = %#v", out["reference_image_urls"])
			}
			want := []string{
				"data:image/png;base64,FRONT",
				"data:image/png;base64,SIDE",
				"data:image/png;base64,SCENE",
			}
			for i := range want {
				if refs[i] != want[i] {
					t.Fatalf("reference_image_urls[%d] = %q, want %q", i, refs[i], want[i])
				}
			}
			if _, exists := out["elements"]; exists {
				t.Fatalf("flat model received elements: %#v", out)
			}
		})
	}
}

func TestValidateVeniceVideoReferences_UsesFamilyLimits(t *testing.T) {
	fiveIdentityImages := []videoReferenceGroup{{
		Role:   videoReferenceRoleIdentity,
		Images: []string{"1", "2", "3", "4", "5"},
	}}
	if err := validateVeniceVideoReferences(
		"kling-o3-pro-reference-to-video",
		fiveIdentityImages,
		nil,
	); err == nil || !strings.Contains(err.Error(), "at most 4 identity images") {
		t.Fatalf("Kling identity limit error = %v", err)
	}
	eightRefs := []string{"1", "2", "3", "4", "5", "6", "7", "8"}
	if err := validateVeniceVideoReferences(
		"grok-imagine-reference-to-video-private",
		nil,
		eightRefs,
	); err == nil || !strings.Contains(err.Error(), "at most 7 reference images") {
		t.Fatalf("Grok total limit error = %v", err)
	}
	if err := validateVeniceVideoReferences(
		"happyhorse-1-1-reference-to-video",
		nil,
		eightRefs,
	); err != nil {
		t.Fatalf("HappyHorse should accept 8 references: %v", err)
	}
	if err := buildVeniceReferenceArgs(
		"kling-o3-pro-reference-to-video",
		map[string]any{"source_images": []string{"1", "2", "3", "4", "5"}},
		map[string]any{},
	); err == nil || !strings.Contains(err.Error(), "at most 4 identity images") {
		t.Fatalf("legacy Kling source_images limit error = %v", err)
	}
}

func TestBuildModelEntryFromVeniceSpec_ParsesVideoPromptLimit(t *testing.T) {
	entry := buildModelEntryFromVeniceSpec(
		"grok-imagine-reference-to-video-private",
		json.RawMessage(`{"model_spec":{"constraints":{"model_type":"image-to-video","prompt_character_limit":4096}}}`),
		"video",
	)
	if entry.PromptCharLimit != 4096 {
		t.Fatalf("PromptCharLimit = %d, want 4096", entry.PromptCharLimit)
	}
	if entry.MaxSourceImages != 7 {
		t.Fatalf("MaxSourceImages = %d, want 7", entry.MaxSourceImages)
	}
}

func TestNormalizeVeniceReferencePrompt_UsesFamilyIdentityTokens(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"kling-o3-pro-reference-to-video", "@Element1"},
		{"grok-imagine-reference-to-video-private", "@Image1"},
		{"happyhorse-1-0-reference-to-video", "character1"},
		{"happyhorse-1-1-reference-to-video", "[Image 1]"},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			args := map[string]any{
				"model":  tc.model,
				"prompt": "Walk toward the camera.",
				"reference_groups": []any{map[string]any{
					"role":   "identity",
					"images": []any{"front", "side"},
				}},
			}
			if err := normalizeVeniceReferencePrompt(args); err != nil {
				t.Fatalf("normalizeVeniceReferencePrompt: %v", err)
			}
			if prompt := strArg(args, "prompt", ""); !strings.Contains(prompt, tc.want) {
				t.Fatalf("prompt = %q, want token %q", prompt, tc.want)
			}
			before := strArg(args, "prompt", "")
			if err := normalizeVeniceReferencePrompt(args); err != nil {
				t.Fatalf("second normalizeVeniceReferencePrompt: %v", err)
			}
			if after := strArg(args, "prompt", ""); after != before {
				t.Fatalf("normalization is not idempotent: before=%q after=%q", before, after)
			}
		})
	}
}

func TestToolMediaGenerate_Video_VeniceKlingIdentityGroups(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.identity.Bindings["video_provider"] = float64(77)
	pf.perAppCallResults = map[string]json.RawMessage{
		"storage:files_get_content": json.RawMessage(
			`{"result":{"content":[{"type":"text","text":"{\"content_base64\":\"SU1BR0U=\"}"}]}}`,
		),
	}
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"list_models": {
			Success: true,
			Status:  200,
			Data: json.RawMessage(
				`{"data":[{"id":"kling-o3-pro-reference-to-video","model_spec":{"constraints":{"model_type":"image-to-video","aspect_ratios":["16:9"],"durations":["5s"]}}}]}`,
			),
		},
		"queue_video": {
			Success: true,
			Status:  200,
			Data:    json.RawMessage(`{"model":"kling-o3-pro-reference-to-video","queue_id":"q-kling-ref"}`),
		},
		"quote_video": {
			Success: true,
			Status:  200,
			Data:    json.RawMessage(`{"quote":0.56}`),
		},
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":     "video",
		"prompt":   "@Element1 walks through @Image1",
		"model":    "kling-o3-pro-reference-to-video",
		"duration": "5s",
		"reference_groups": []any{
			map[string]any{
				"role":   "identity",
				"images": []any{"storage:1001", "https://example.test/side.jpg"},
			},
			map[string]any{
				"role":   "scene",
				"images": []any{"https://example.test/studio.jpg"},
			},
		},
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	if result := out.(map[string]any); result["isError"] == true {
		t.Fatalf("unexpected error result: %+v", result)
	}
	var queue executeCall
	for _, call := range pf.executeCalls {
		if call.Tool == "queue_video" {
			queue = call
			break
		}
	}
	elements, ok := queue.Input["elements"].([]map[string]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("queue elements = %#v", queue.Input["elements"])
	}
	if !strings.HasPrefix(elements[0]["frontal_image_url"].(string), "data:image/png;base64,SU1BR0U=") {
		t.Fatalf("storage identity was not resolved: %#v", elements[0])
	}
	scenes, ok := queue.Input["scene_image_urls"].([]string)
	if !ok || len(scenes) != 1 || scenes[0] != "https://example.test/studio.jpg" {
		t.Fatalf("queue scene_image_urls = %#v", queue.Input["scene_image_urls"])
	}
	if _, exists := queue.Input["reference_image_urls"]; exists {
		t.Fatalf("Kling queue should not contain flat references: %+v", queue.Input)
	}
}
