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
// buildImageArgs dispatches by provider slug. Each provider's request
// shape is distinct enough (OpenAI: model/prompt/size/quality, Venice:
// model/prompt/width/height/format/cfg_scale/…) that a unified body
// would require gating every field on every model — clearer to fork.
func buildImageArgs(args map[string]any, providerSlug string) (map[string]any, error) {
	switch providerSlug {
	case "openai-api":
		return buildOpenAIImageArgs(args), nil
	case "venice-ai":
		return buildVeniceImageArgs(args), nil
	}
	return nil, fmt.Errorf("unsupported image provider slug: %q", providerSlug)
}

// buildOpenAIImageArgs unpacks the options bag and delegates to
// buildProviderArgs (the original openai-shape builder).
func buildOpenAIImageArgs(args map[string]any) map[string]any {
	model := strArg(args, "model", "gpt-image-2")
	prompt := strArg(args, "prompt", "")
	size := strArg(args, "size", "1024x1024")
	n := intArg(args, "n", 1)

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
	return buildProviderArgs(model, prompt, size, quality, outputFormat, background, n)
}

// buildVeniceImageArgs assembles Venice's POST /images/generations body.
// Venice requires both model + prompt; format defaults to webp; size
// translates to width/height when given as WxH (otherwise width/height
// fall back to 1024x1024). Per-Venice extras come through args["options"]:
// style_preset, negative_prompt, cfg_scale, steps, seed, safe_mode,
// hide_watermark, lora_strength, aspect_ratio, resolution.
func buildVeniceImageArgs(args map[string]any) map[string]any {
	model := strArg(args, "model", "grok-imagine-image")
	prompt := strArg(args, "prompt", "")
	n := intArg(args, "n", 1)

	out := map[string]any{
		"model":         model,
		"prompt":        prompt,
		"variants":      n,
		"return_binary": false, // we want JSON+base64 — saveToStorage handles bytes
	}

	// size "WxH" → width + height. Pixel-sized Venice models honour these;
	// aspect-ratio models (nano-banana, qwen-image-2) ignore them and use
	// aspect_ratio / resolution from options instead.
	w, h, ok := parseWxH(strArg(args, "size", ""))
	if ok {
		out["width"] = w
		out["height"] = h
	}

	// options.* — pass through everything the catalog supports.
	if opts, ok := args["options"].(map[string]any); ok {
		passThrough := []string{
			"negative_prompt", "format", "cfg_scale", "steps", "seed",
			"style_preset", "safe_mode", "hide_watermark", "lora_strength",
			"aspect_ratio", "resolution", "embed_exif_metadata",
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out
}

func parseWxH(s string) (int, int, bool) {
	if s == "" {
		return 0, 0, false
	}
	var w, h int
	if _, err := fmt.Sscanf(s, "%dx%d", &w, &h); err != nil {
		return 0, 0, false
	}
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
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
	case "venice-ai":
		// Venice native shape: { id, images:[<b64>,...], request, timing }.
		// Default format is webp; if the request asked for png/jpeg the
		// bytes differ but our metadata still tags webp — storage's
		// content-type sniffer sorts the rest out for downstream consumers.
		var body struct {
			ID     string   `json:"id"`
			Images []string `json:"images"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", "", err
		}
		media := make([]generatedMedia, 0, len(body.Images))
		for _, b64 := range body.Images {
			media = append(media, generatedMedia{
				B64:      b64,
				MimeType: "image/webp",
				Ext:      "webp",
			})
		}
		// Venice doesn't return a revised prompt or echo the model.
		return media, "", "", nil
	}
	return nil, "", "", fmt.Errorf("unsupported provider slug: %q", slug)
}
