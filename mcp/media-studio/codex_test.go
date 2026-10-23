package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestCodexImageCatalogMatchesRequestDefault(t *testing.T) {
	models, err := loadModelsForCapabilityBound(nil, KindImage, "image.generate", &sdk.BoundIntegration{AppSlug: "openai-codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gpt-6.1-sol" || models[0].Label != "GPT-6.1 Sol (Codex image generation)" {
		t.Fatalf("unexpected Codex image catalog: %+v", models)
	}
	args, err := buildImageArgs(map[string]any{"prompt": "draw a door"}, "openai-codex", "image.generate")
	if err != nil {
		t.Fatal(err)
	}
	if args["model"] != models[0].ID {
		t.Fatalf("request model %v differs from catalog model %q", args["model"], models[0].ID)
	}
}

func TestCodexImageModelSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model any
		want  string
	}{
		{name: "omitted", want: "gpt-6.1-sol"},
		{name: "empty", model: "", want: "gpt-6.1-sol"},
		{name: "whitespace", model: " \t ", want: "gpt-6.1-sol"},
		{name: "image model", model: "gpt-image-2", want: "gpt-6.1-sol"},
		{name: "dalle model", model: "dall-e-3", want: "gpt-6.1-sol"},
		{name: "explicit current", model: "gpt-6.1-sol", want: "gpt-6.1-sol"},
		{name: "explicit alternative", model: "gpt-5.6-sol", want: "gpt-5.6-sol"},
		{name: "explicit legacy", model: "gpt-5.5", want: "gpt-5.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{"prompt": "draw a door"}
			if tc.model != nil {
				input["model"] = tc.model
			}
			args, err := buildImageArgs(input, "openai-codex", "image.generate")
			if err != nil {
				t.Fatal(err)
			}
			if args["model"] != tc.want {
				t.Fatalf("model = %v, want %q", args["model"], tc.want)
			}
		})
	}
}
