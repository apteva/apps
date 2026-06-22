package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// buildImageArgs assembles the request body for /v1/images/generations
// per-model. OpenAI rejects unknown / unsupported fields with 400, so
// we gate each parameter on the model's accepted set rather than always
// sending everything.
//
// buildImageArgs dispatches by (provider slug, capability). Each
// provider's request shape is distinct enough that a unified body
// would require gating every field on every model — clearer to fork.
func buildImageArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	switch capability {
	case "image.generate":
		switch providerSlug {
		case "openai-api":
			return buildOpenAIImageArgs(args), nil
		case "openai-codex":
			return buildOpenAICodexImageArgs(args), nil
		case "gemini":
			return buildGeminiImageArgs(args), nil
		case "venice-ai":
			return buildVeniceImageArgs(args), nil
		}
	case "image.edit":
		switch providerSlug {
		case "venice-ai":
			return buildVeniceImageEditArgs(args), nil
		case "openai-api":
			return buildOpenAIImageEditArgs(args), nil
		case "gemini":
			return buildGeminiImageEditArgs(args), nil
		case "openai-codex":
			return nil, fmt.Errorf("openai-codex image edit not wired (text-to-image only)")
		}
	}
	return nil, fmt.Errorf("unsupported (slug=%q, capability=%q)", providerSlug, capability)
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

func buildOpenAIImageEditArgs(args map[string]any) map[string]any {
	model := strArg(args, "model", "gpt-image-1.5")
	if model == "" || model == "gpt-image-2" || model == "dall-e-3" {
		model = "gpt-image-1.5"
	}
	out := map[string]any{
		"model":  model,
		"prompt": strArg(args, "prompt", ""),
		"images": openAIImageRefs(resolvedSourceImages(args)),
		"n":      intArg(args, "n", 1),
	}
	if size := strArg(args, "size", ""); size != "" {
		out["size"] = size
	} else {
		out["size"] = "auto"
	}
	if opts, ok := args["options"].(map[string]any); ok {
		for _, key := range []string{"quality", "output_format", "background", "input_fidelity"} {
			if v, exists := opts[key]; exists {
				out[key] = v
			}
		}
	}
	for _, key := range []string{"quality", "output_format", "background", "input_fidelity"} {
		if v, exists := args[key]; exists {
			out[key] = v
		}
	}
	return out
}

func openAIImageRefs(sources []string) []map[string]any {
	out := make([]map[string]any, 0, len(sources))
	for _, src := range sources {
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}
		if strings.HasPrefix(src, "file-") {
			out = append(out, map[string]any{"file_id": src})
			continue
		}
		out = append(out, map[string]any{"image_url": imageURLRef(src)})
	}
	return out
}

func imageURLRef(src string) string {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "data:") {
		return src
	}
	return "data:image/png;base64," + src
}

func buildGeminiImageArgs(args map[string]any) map[string]any {
	return buildGeminiImageRequest(args, resolvedSourceImages(args))
}

func buildGeminiImageEditArgs(args map[string]any) map[string]any {
	return buildGeminiImageRequest(args, resolvedSourceImages(args))
}

func buildGeminiImageRequest(args map[string]any, sources []string) map[string]any {
	model := strArg(args, "model", "gemini-2.5-flash-image")
	if model == "" {
		model = "gemini-2.5-flash-image"
	}
	parts := []map[string]any{{"text": strArg(args, "prompt", "")}}
	for _, src := range sources {
		if part := geminiImagePart(src); part != nil {
			parts = append(parts, part)
		}
	}
	out := map[string]any{
		"model":    model,
		"contents": []map[string]any{{"parts": parts}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"Image"},
		},
	}
	aspect := strArg(args, "aspect", "")
	if opts, ok := args["options"].(map[string]any); ok {
		if v := strArg(opts, "aspect_ratio", ""); v != "" {
			aspect = v
		}
		if v := strArg(opts, "resolution", ""); v != "" {
			ensureGeminiResponseFormat(out)["imageSize"] = v
		}
	}
	if aspect == "" {
		aspect = aspectFromSize(strArg(args, "size", ""))
	}
	if aspect != "" {
		ensureGeminiResponseFormat(out)["aspectRatio"] = aspect
	}
	return out
}

func ensureGeminiResponseFormat(out map[string]any) map[string]any {
	cfg, _ := out["generationConfig"].(map[string]any)
	rf, _ := cfg["responseFormat"].(map[string]any)
	if rf == nil {
		rf = map[string]any{}
		cfg["responseFormat"] = rf
	}
	img, _ := rf["image"].(map[string]any)
	if img == nil {
		img = map[string]any{}
		rf["image"] = img
	}
	return img
}

func aspectFromSize(size string) string {
	switch size {
	case "720x1280", "1024x1536", "1080x1920", "9:16":
		return "9:16"
	case "1280x720", "1536x1024", "1920x1080", "16:9":
		return "16:9"
	case "1024x1024", "1:1":
		return "1:1"
	}
	w, h, ok := parseWxH(size)
	if !ok {
		return ""
	}
	ratio := float64(w) / float64(h)
	candidates := []struct {
		aspect string
		ratio  float64
	}{
		{"1:1", 1},
		{"3:2", 3.0 / 2.0},
		{"16:9", 16.0 / 9.0},
		{"21:9", 21.0 / 9.0},
		{"9:16", 9.0 / 16.0},
		{"2:3", 2.0 / 3.0},
		{"3:4", 3.0 / 4.0},
		{"4:5", 4.0 / 5.0},
		{"4:3", 4.0 / 3.0},
	}
	best := ""
	bestDelta := math.MaxFloat64
	for _, c := range candidates {
		if d := math.Abs(ratio - c.ratio); d < bestDelta {
			best = c.aspect
			bestDelta = d
		}
	}
	if bestDelta <= 0.02 {
		return best
	}
	return ""
}

func geminiImagePart(src string) map[string]any {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil
	}
	if strings.HasPrefix(src, "data:") {
		mime := "image/png"
		data := src
		if semi := strings.Index(src, ";base64,"); semi > len("data:") {
			mime = src[len("data:"):semi]
			data = src[semi+len(";base64,"):]
		}
		return map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": data}}
	}
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return map[string]any{"fileData": map[string]any{"mimeType": "image/png", "fileUri": src}}
	}
	return map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": src}}
}

func buildOpenAICodexImageArgs(args map[string]any) map[string]any {
	model := strArg(args, "model", "gpt-5.5")
	if model == "" || strings.HasPrefix(model, "gpt-image") || strings.HasPrefix(model, "dall-e") {
		model = "gpt-5.5"
	}
	prompt := strArg(args, "prompt", "")
	instructions := strArg(args, "instructions", "")
	if opts, ok := args["options"].(map[string]any); ok && instructions == "" {
		instructions = strArg(opts, "instructions", "")
	}
	if strings.TrimSpace(instructions) == "" {
		instructions = "Generate the requested image using the hosted image_generation tool. Return the completed image result."
	}
	out := map[string]any{
		"model":        model,
		"prompt":       prompt,
		"instructions": instructions,
	}
	if size := strArg(args, "size", ""); size != "" {
		out["size"] = size
	}
	if n := intArg(args, "n", 1); n > 1 {
		// The Responses image_generation tool currently returns one final
		// image per call; keep n for metadata compatibility but the Codex
		// executor intentionally ignores it upstream.
		out["n"] = n
	}
	for _, key := range []string{"quality", "output_format", "background", "output_compression"} {
		if v := strArg(args, key, ""); v != "" {
			out[key] = v
		}
	}
	if opts, ok := args["options"].(map[string]any); ok {
		for _, key := range []string{"quality", "output_format", "background", "output_compression"} {
			if v, exists := opts[key]; exists {
				out[key] = v
			}
		}
	}
	return out
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
		// Default to PNG so the stdlib image decoder (used by
		// makeThumbnail) can read the bytes. Venice's own default
		// is webp which Go's image package doesn't understand —
		// thumbnails would silently fall back to no-preview.
		// User can override via options.format.
		"format": "png",
		// Quality defaults — SD / Flux / Qwen models honour these,
		// resolution-tier models (gpt-image-2, nano-banana) silently
		// ignore. Without these Venice falls back to ~8 steps which
		// produces visibly soft output for the SD family. Users can
		// override via options.steps / options.cfg_scale.
		"steps":     30,
		"cfg_scale": 7.5,
		// safe_mode=false by default — Venice's own default is true,
		// which blurs adult-classified content. We want the API to
		// return whatever the model produces; operator can override
		// per-call via options.safe_mode.
		"safe_mode": false,
	}

	// size "WxH" → width + height. Pixel-sized Venice models honour these;
	// aspect-ratio models (nano-banana, qwen-image-2) ignore them and use
	// aspect_ratio / resolution from options instead.
	size := strArg(args, "size", "")
	w, h, ok := parseWxH(size)
	if ok {
		out["width"] = w
		out["height"] = h
	}
	if aspect := strArg(args, "aspect", ""); aspect != "" {
		out["aspect_ratio"] = aspect
	} else if aspect := aspectFromSize(size); aspect != "" {
		out["aspect_ratio"] = aspect
	}
	if res := strArg(args, "resolution", ""); res != "" && validVeniceTierResolution(res) {
		out["resolution"] = res
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
				if k == "resolution" && !validVeniceTierResolution(v) {
					continue
				}
				out[k] = v
			}
		}
		if v, exists := opts["output_format"]; exists {
			out["format"] = v
		}
	}
	return out
}

func validVeniceTierResolution(v any) bool {
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" {
		return false
	}
	_, _, isPixel := parseWxH(s)
	return !isPixel
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
func normalizeImageResponse(slug, capability string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	if capability == "image.edit" {
		return normalizeImageEditResponse(slug, raw)
	}
	switch slug {
	case "openai-api", "openai-codex":
		return normalizeOpenAIImageResponse(raw)
	case "gemini":
		return normalizeGeminiImageResponse(raw)
	case "venice-ai":
		// Venice native shape: { id, images:[<b64>,...], request:{success,data:{...}}, timing }.
		// request.data echoes back the canonical format + model so we
		// don't have to guess.
		var body struct {
			ID      string   `json:"id"`
			Images  []string `json:"images"`
			Request struct {
				Data struct {
					Format string `json:"format"`
					Model  string `json:"model"`
				} `json:"data"`
			} `json:"request"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", "", err
		}
		format := body.Request.Data.Format
		if format == "" {
			format = "png" // matches buildVeniceImageArgs default
		}
		mime, ext := imageFormatToMime(format)
		media := make([]generatedMedia, 0, len(body.Images))
		for _, b64 := range body.Images {
			media = append(media, generatedMedia{
				B64:      b64,
				MimeType: mime,
				Ext:      ext,
			})
		}
		// Venice doesn't return a revised prompt; model echoed under request.data.
		return media, "", body.Request.Data.Model, nil
	}
	return nil, "", "", fmt.Errorf("unsupported provider slug: %q", slug)
}

// buildVeniceImageEditArgs assembles Venice's edit request. With one source
// image it targets POST /image/edit via the edit_image tool. With multiple
// sources it targets POST /image/multi-edit via the multi_edit_image tool.
// The dispatcher has already resolved storage:N handles into base64.
func buildVeniceImageEditArgs(args map[string]any) map[string]any {
	model := strArg(args, "model", "firered-image-edit")
	prompt := strArg(args, "prompt", "")
	sources := resolvedSourceImages(args)
	if len(sources) > 1 {
		return buildVeniceImageMultiEditArgs(args, model, prompt, sources)
	}
	source := strArg(args, "source_image", "")
	if source == "" && len(sources) == 1 {
		source = sources[0]
	}

	out := map[string]any{
		"model":  model,
		"prompt": prompt,
		"image":  source,
		// safe_mode=false by default (Venice defaults true → blurs
		// adult-classified output). Operator can flip on via
		// options.safe_mode if they want filtered output.
		"safe_mode": false,
	}
	if opts, ok := args["options"].(map[string]any); ok {
		passThrough := []string{
			"aspect_ratio", "resolution", "output_format", "safe_mode",
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				if k == "resolution" && !validVeniceEditResolution(v) {
					continue
				}
				out[k] = v
			}
		}
	}
	return out
}

func buildVeniceImageMultiEditArgs(args map[string]any, model, prompt string, sources []string) map[string]any {
	out := map[string]any{
		"modelId":   model,
		"prompt":    prompt,
		"images":    sources,
		"safe_mode": false,
	}
	if opts, ok := args["options"].(map[string]any); ok {
		passThrough := []string{
			"aspect_ratio", "resolution", "output_format", "quality", "safe_mode",
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				if k == "quality" && !validVeniceEditQuality(v) {
					continue
				}
				if k == "resolution" && !validVeniceEditResolution(v) {
					continue
				}
				out[k] = v
			}
		}
	}
	return out
}

func validVeniceEditQuality(v any) bool {
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(v))) {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

func validVeniceEditResolution(v any) bool {
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" {
		return false
	}
	if _, _, ok := parseWxH(s); ok {
		return false
	}
	return true
}

func resolvedSourceImages(args map[string]any) []string {
	switch v := args["source_images"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, raw := range v {
			if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// normalizeImageEditResponse parses the binary-envelope shape the
// integrations http-executor produces for non-JSON responses:
//
//	{ "_binary": true, "base64": "<b64>", "mimeType": "image/png", "size": N }
//
// Venice's /image/edit always returns one binary image. The mimeType
// + ext fall through to whichever output_format was requested.
func normalizeImageEditResponse(slug string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	switch slug {
	case "openai-api":
		return normalizeOpenAIImageResponse(raw)
	case "gemini":
		return normalizeGeminiImageResponse(raw)
	case "venice-ai":
		var env struct {
			Binary   bool   `json:"_binary"`
			Base64   string `json:"base64"`
			MimeType string `json:"mimeType"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, "", "", err
		}
		if !env.Binary || env.Base64 == "" {
			return nil, "", "", fmt.Errorf("edit response missing binary payload (got: %s)", truncate(string(raw), 200))
		}
		mt := env.MimeType
		if mt == "" {
			mt = "image/png"
		}
		return []generatedMedia{{
			B64:      env.Base64,
			MimeType: mt,
			Ext:      extFromMime(mt),
		}}, "", "", nil
	}
	return nil, "", "", fmt.Errorf("unsupported edit provider slug: %q", slug)
}

func normalizeOpenAIImageResponse(raw json.RawMessage) ([]generatedMedia, string, string, error) {
	var body struct {
		Data []struct {
			URL           string `json:"url"`
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
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
			MimeType:    "image/png",
			Ext:         "png",
		})
		if i == 0 {
			revised = d.RevisedPrompt
		}
	}
	return media, revised, body.Model, nil
}

func normalizeGeminiImageResponse(raw json.RawMessage) ([]generatedMedia, string, string, error) {
	var body struct {
		ModelVersion string `json:"modelVersion"`
		Candidates   []struct {
			Content struct {
				Parts []struct {
					Text       string `json:"text"`
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
					InlineDataSnake *struct {
						MimeType string `json:"mime_type"`
						Data     string `json:"data"`
					} `json:"inline_data"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, "", "", err
	}
	media := []generatedMedia{}
	var revised string
	for _, c := range body.Candidates {
		for _, p := range c.Content.Parts {
			if revised == "" && strings.TrimSpace(p.Text) != "" {
				revised = p.Text
			}
			mime, data := "", ""
			if p.InlineData != nil {
				mime, data = p.InlineData.MimeType, p.InlineData.Data
			} else if p.InlineDataSnake != nil {
				mime, data = p.InlineDataSnake.MimeType, p.InlineDataSnake.Data
			}
			if data == "" {
				continue
			}
			if mime == "" {
				mime = "image/png"
			}
			media = append(media, generatedMedia{B64: data, MimeType: mime, Ext: extFromMime(mime)})
		}
	}
	return media, revised, body.ModelVersion, nil
}

// imageFormatToMime maps Venice's `format` string ("png" / "jpeg" / "webp")
// to (mimeType, extension). Used by the normalizer to tag stored bytes
// correctly so the storage app serves them with the right Content-Type.
func imageFormatToMime(format string) (string, string) {
	switch format {
	case "jpeg", "jpg":
		return "image/jpeg", "jpg"
	case "webp":
		return "image/webp", "webp"
	}
	return "image/png", "png"
}

func extFromMime(mt string) string {
	switch mt {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	}
	return "bin"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
