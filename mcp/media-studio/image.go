package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// buildImageArgs assembles the request body for /v1/images/generations
// per-model. OpenAI rejects unknown / unsupported fields with 400, so
// we gate each parameter on the model's accepted set rather than always
// sending everything.
//
// Thin shim over buildProviderArgs that also unpacks the options bag —
// the unified media_generate surface stashes provider-specific extras
// (quality, output_format, background) under args["options"].
func buildImageArgs(args map[string]any, providerSlug string) (map[string]any, error) {
	model := strArg(args, "model", "gpt-image-2")
	prompt := strArg(args, "prompt", "")
	size := strArg(args, "size", "1024x1024")
	n := intArg(args, "n", 1)

	// options.* takes precedence — agent-supplied quality/output_format/background
	// usually arrives via the options bag in the unified surface.
	quality := strArg(args, "quality", "")
	outputFormat := strArg(args, "output_format", "")
	background := strArg(args, "background", "")
	if opts, ok := args["options"].(map[string]any); ok {
		if v := strArg(opts, "quality", ""); v != "" {
			quality = v
		}
		if v := strArg(opts, "output_format", ""); v != "" {
			outputFormat = v
		}
		if v := strArg(opts, "background", ""); v != "" {
			background = v
		}
	}

	return buildProviderArgs(model, prompt, size, quality, outputFormat, background, n), nil
}

// buildProviderArgs is the original openai-shape builder. Kept as a
// pure function (no map access) so the image tests can hit it directly.
func buildProviderArgs(model, prompt, size, quality, outputFormat, background string, n int) map[string]any {
	args := map[string]any{
		"model":  model,
		"prompt": prompt,
		"n":      n,
	}
	if size != "" {
		args["size"] = size
	}
	switch {
	case strings.HasPrefix(model, "gpt-image"):
		// gpt-image-*: low | medium | high | auto. Default 'auto' is fine.
		if quality != "" {
			args["quality"] = quality
		}
		if outputFormat != "" {
			args["output_format"] = outputFormat
		}
		if background != "" {
			args["background"] = background
		}
	case model == "dall-e-3":
		// standard | hd
		if quality == "" || quality == "auto" {
			args["quality"] = "standard"
		} else {
			args["quality"] = quality
		}
	case model == "dall-e-2":
		// no quality/format/background — stripped by omission above.
	}
	return args
}

// normalizeImageResponse parses provider-specific shapes into the
// uniform generatedMedia list. Today only openai-api is supported;
// extend as new providers land.
//
// OpenAI returns the same envelope ({data:[…], created}) for every model
// in the family — only the per-item shape differs (url vs b64_json), and
// gpt-image-* never includes a URL. We surface both fields so the caller
// can pick the path that matches what was returned.
func normalizeImageResponse(slug string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	switch slug {
	case "openai-api":
		var body struct {
			Data []struct {
				URL           string `json:"url"`
				B64JSON       string `json:"b64_json"`
				RevisedPrompt string `json:"revised_prompt"`
			} `json:"data"`
			Created int64  `json:"created"`
			Model   string `json:"model"` // gpt-image-2 echoes this; DALL·E doesn't
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", "", err
		}
		media := make([]generatedMedia, 0, len(body.Data))
		var revised string
		for i, d := range body.Data {
			media = append(media, generatedMedia{
				UpstreamURL: d.URL,
				B64:         d.B64JSON,
				MimeType:    "image/png", // overridden by storage path when output_format requests jpeg/webp
				Ext:         "png",
			})
			if i == 0 {
				revised = d.RevisedPrompt
			}
		}
		return media, revised, body.Model, nil
	}
	return nil, "", "", fmt.Errorf("unsupported provider slug: %q", slug)
}
